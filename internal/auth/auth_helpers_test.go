package auth_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	httpAPI "github.com/go42-dev/go42/internal/api/http"
	"github.com/go42-dev/go42/internal/auth"
	"github.com/go42-dev/go42/internal/auth/domain"
	authInterceptors "github.com/go42-dev/go42/internal/auth/interceptors"
	authMiddleware "github.com/go42-dev/go42/internal/auth/middleware"
	"github.com/go42-dev/go42/internal/auth/models"
)

const (
	testJWTSecret  = "auth-service-test-secret"
	testJWTIssuer  = "go42-test"
	testPassword   = "correct horse battery staple"
	testUserEmail  = "alice@example.com"
	testAccessTTL  = 15 * time.Minute
	testRefreshTTL = 24 * time.Hour
)

var testJWTAudience = []string{"go42-test"}

func newTestUser(t *testing.T, status string) *models.User {
	t.Helper()

	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash test password: %v", err)
	}

	return &models.User{
		ID:       42,
		UUID:     uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
		Email:    testUserEmail,
		Password: sql.Null[string]{V: string(hash), Valid: true},
		Status:   status,
	}
}

func signTestJWTWithClaims(
	t *testing.T,
	method jwt.SigningMethod,
	secret string,
	claims domain.JWTClaims,
) string {
	t.Helper()

	token, err := jwt.NewWithClaims(method, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign test JWT: %v", err)
	}
	return token
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func assertErrorIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want errors.Is(_, %v)", err, target)
	}
}

type invalidCredentialTestCase struct {
	name     string
	email    string
	password string
	want     error
}

func invalidCredentialTestCases() []invalidCredentialTestCase {
	return []invalidCredentialTestCase{
		{name: "empty email", password: testPassword, want: domain.ErrInvalidEmail},
		{name: "blank email", email: " \t ", password: testPassword, want: domain.ErrInvalidEmail},
		{name: "malformed email", email: "not-an-email", password: testPassword, want: domain.ErrInvalidEmail},
		{
			name:     "display name",
			email:    "Alice <alice@example.com>",
			password: testPassword,
			want:     domain.ErrInvalidEmail,
		},
		{
			name:     "space in email",
			email:    "alice smith@example.com",
			password: testPassword,
			want:     domain.ErrInvalidEmail,
		},
		{name: "empty password", email: testUserEmail, want: domain.ErrPasswordWeak},
		{name: "short password", email: testUserEmail, password: "Ab1!cdE", want: domain.ErrPasswordWeak},
		{
			name:     "short Unicode password",
			email:    testUserEmail,
			password: strings.Repeat("界", 7),
			want:     domain.ErrPasswordWeak,
		},
		{
			name:     "73 ASCII bytes",
			email:    testUserEmail,
			password: strings.Repeat("Ab1!cdE2", 9) + "x",
			want:     domain.ErrPasswordWeak,
		},
		{
			name:     "73 Unicode bytes",
			email:    testUserEmail,
			password: strings.Repeat("界", 24) + "x",
			want:     domain.ErrPasswordWeak,
		},
		{name: "weak password", email: testUserEmail, password: "password", want: domain.ErrPasswordWeak},
	}
}

func assertAPIKeyAuthenticationFailure(
	t *testing.T, service *auth.Service, key string, wantHTTPStatus int, wantGRPCCode codes.Code,
) {
	t.Helper()
	t.Run("http", func(t *testing.T) {
		e := newTestEcho()
		e.GET("/protected", func(*echo.Context) error {
			t.Error("rejected API key reached the HTTP handler")
			return nil
		}, authMiddleware.NewAuthMiddleware(service))
		request := httptest.NewRequest(http.MethodGet, "/protected", nil)
		request.Header.Set("X-API-Key", key)
		response := httptest.NewRecorder()
		e.ServeHTTP(response, request)
		if response.Code != wantHTTPStatus {
			t.Fatalf("HTTP status = %d, want %d", response.Code, wantHTTPStatus)
		}
		if got := response.Header().Get(echo.HeaderContentType); got != httpAPI.MIMEApplicationProblemJSON {
			t.Fatalf("HTTP content type = %q, want problem+json", got)
		}
		var problem httpAPI.Error
		if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
			t.Fatalf("decode HTTP problem: %v", err)
		}
		if problem.Status != wantHTTPStatus || problem.Title != http.StatusText(wantHTTPStatus) ||
			problem.Detail != "" {
			t.Fatalf("HTTP problem = %+v, want generic HTTP %d error", problem, wantHTTPStatus)
		}
	})

	const method = "/auth.v1.AuthService/ListUsers"
	t.Run("grpc unary", func(t *testing.T) {
		ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("x-api-key", key))
		_, err := authInterceptors.NewUnaryAuthInterceptor(service)(
			ctx, nil, &grpc.UnaryServerInfo{FullMethod: method},
			func(context.Context, any) (any, error) {
				t.Error("rejected API key reached the unary handler")
				return nil, nil
			},
		)
		if got := status.Code(err); got != wantGRPCCode {
			t.Fatalf("gRPC status = %s, want %s", got, wantGRPCCode)
		}
		if wantGRPCCode == codes.Unavailable && status.Convert(err).Message() != "authentication unavailable" {
			t.Fatalf("gRPC exposed storage details: %v", err)
		}
	})
	t.Run("grpc stream", func(t *testing.T) {
		ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("x-api-key", key))
		err := authInterceptors.NewStreamAuthInterceptor(service)(
			nil, &authTestStream{ctx: ctx}, &grpc.StreamServerInfo{FullMethod: method},
			func(any, grpc.ServerStream) error {
				t.Error("rejected API key reached the streaming handler")
				return nil
			},
		)
		if got := status.Code(err); got != wantGRPCCode {
			t.Fatalf("gRPC status = %s, want %s", got, wantGRPCCode)
		}
		if wantGRPCCode == codes.Unavailable && status.Convert(err).Message() != "authentication unavailable" {
			t.Fatalf("gRPC exposed storage details: %v", err)
		}
	})
}

type authTestStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authTestStream) Context() context.Context { return s.ctx }

func newTestEcho() *echo.Echo {
	e := echo.New()
	e.Validator = httpAPI.NewValidator()
	e.HTTPErrorHandler = httpAPI.NewErrorHandler(nil)
	return e
}

func sessionHTTPRequest(t *testing.T, e *echo.Echo, method, path, bearer string, data any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)
	return response
}
