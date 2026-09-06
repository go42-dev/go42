package auth_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"buf.build/go/protovalidate"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	protovalidateInterceptor "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/protovalidate"
	"github.com/labstack/echo/v5"
	"github.com/pressly/goose/v3"
	"go.uber.org/mock/gomock"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"gorm.io/gorm"

	pb "github.com/go42-dev/go42/api/gen/sdk/grpc/auth/v1"
	oapi "github.com/go42-dev/go42/api/gen/sdk/http/v1/auth/oapi-codegen"
	ogen "github.com/go42-dev/go42/api/gen/sdk/http/v1/auth/ogen"
	httpAPI "github.com/go42-dev/go42/internal/api/http"
	"github.com/go42-dev/go42/internal/auth"
	grpcAdapter "github.com/go42-dev/go42/internal/auth/adapters/grpc/v1"
	httpAdapter "github.com/go42-dev/go42/internal/auth/adapters/http/v1"
	"github.com/go42-dev/go42/internal/auth/domain"
	authInterceptors "github.com/go42-dev/go42/internal/auth/interceptors"
	authMiddleware "github.com/go42-dev/go42/internal/auth/middleware"
	authMocks "github.com/go42-dev/go42/internal/auth/mocks"
	"github.com/go42-dev/go42/internal/auth/models"
	authRepository "github.com/go42-dev/go42/internal/auth/repository"
	"github.com/go42-dev/go42/internal/cache/local"
	"github.com/go42-dev/go42/internal/database"
	"github.com/go42-dev/go42/internal/database/mysql"
	"github.com/go42-dev/go42/internal/database/pgsql"
	"github.com/go42-dev/go42/internal/database/sqlite"
	"github.com/go42-dev/go42/internal/outbox"
	outboxDomain "github.com/go42-dev/go42/internal/outbox/domain"
	outboxModels "github.com/go42-dev/go42/internal/outbox/models"
	outboxRepository "github.com/go42-dev/go42/internal/outbox/repository"
)

const (
	testJWTSecret  = "auth-service-test-secret"
	testJWTIssuer  = "go42-test"
	testPassword   = "correct horse battery staple"
	testUserEmail  = "alice@example.com"
	testAPIKey     = "api_kXqdf2uQ7hmOARp-pZrhA6_IsZSeKCmSEM4YFKBGIzA"
	testAccessTTL  = 15 * time.Minute
	testRefreshTTL = 24 * time.Hour
)

var testJWTAudience = []string{"go42-test"}

type serviceHarness struct {
	repository *authMocks.Mockrepository
	cache      *authMocks.Mockcache
	outbox     *authMocks.MockoutboxService
	service    *auth.Service
}

type serviceCache interface {
	AllowRateLimit(context.Context, string, time.Duration, int, time.Duration) (bool, error)
}

func newServiceHarness(t *testing.T, extraOptions ...auth.Option) *serviceHarness {
	t.Helper()

	ctrl := gomock.NewController(t)
	h := &serviceHarness{
		repository: authMocks.NewMockrepository(ctrl),
		cache:      authMocks.NewMockcache(ctrl),
		outbox:     authMocks.NewMockoutboxService(ctrl),
	}

	h.service = newTestService(h.repository, h.outbox, h.cache, extraOptions...)

	return h
}

func newTestService(
	repository *authMocks.Mockrepository,
	outbox *authMocks.MockoutboxService,
	cache serviceCache,
	extraOptions ...auth.Option,
) *auth.Service {
	options := []auth.Option{
		auth.WithJWTSecrets([]string{testJWTSecret}),
		auth.WithJWTAccessTokenTTL(testAccessTTL),
		auth.WithJWTRefreshTokenTTL(testRefreshTTL),
		auth.WithJWTIssuer(testJWTIssuer),
		auth.WithJWTAudience(testJWTAudience),
		auth.WithMinPasswordEntropyBits(60),
		auth.WithRateLimiterEnabled(false),
	}
	options = append(options, extraOptions...)
	return auth.NewService(repository, outbox, cache, options...)
}

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

func expectTransaction(repository *authMocks.Mockrepository) *gomock.Call {
	return repository.EXPECT().
		WithTransaction(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error {
			return fn(ctx)
		})
}

type outboxEventMatcher struct {
	aggregateID   int
	aggregateType string
}

func (m outboxEventMatcher) Matches(value any) bool {
	message, ok := value.(*outboxDomain.Message)
	return ok &&
		message.AggregateID == m.aggregateID &&
		message.AggregateType == m.aggregateType
}

func (m outboxEventMatcher) String() string {
	return fmt.Sprintf(
		"outbox message with aggregate ID %d and type %q",
		m.aggregateID,
		m.aggregateType,
	)
}

func expectOutboxEvent(
	h *serviceHarness,
	aggregateID int,
	aggregateType string,
	err error,
) *gomock.Call {
	return h.outbox.EXPECT().NewOutboxMessage(
		gomock.Any(),
		domain.TopicNameAuthEvents,
		outboxEventMatcher{aggregateID: aggregateID, aggregateType: aggregateType},
	).Return(err)
}

func signTestJWT(
	t *testing.T,
	secret string,
	purpose domain.JWTTokenPurpose,
	subject string,
	expiresAt time.Time,
) string {
	t.Helper()

	claims := domain.JWTClaims{
		ID:        uuid.NewString(),
		Audience:  testJWTAudience,
		Issuer:    testJWTIssuer,
		Subject:   subject,
		IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Minute)),
		ExpiresAt: jwt.NewNumericDate(expiresAt),
		KID:       sha256Hex(secret),
		TokenUse:  purpose,
		SessionID: uuid.NewString(),
	}

	return signTestJWTWithClaims(t, jwt.SigningMethodHS256, secret, claims)
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

func TestService_CheckPasswordStrength(t *testing.T) {
	tests := []struct {
		name     string
		password string
		wantErr  bool
	}{
		{name: "strong password", password: testPassword},
		{name: "weak password", password: "password", wantErr: true},
		{name: "empty password", password: "", wantErr: true},
		{name: "bcrypt byte limit", password: strings.Repeat("Strong!password7", 5), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newServiceHarness(t)
			err := h.service.CheckPasswordStrength(tt.password)
			if (err != nil) != tt.wantErr {
				t.Fatalf("CheckPasswordStrength() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
func TestCredentials_PasswordLength(t *testing.T) {
	h := newServiceHarness(t, auth.WithMinPasswordEntropyBits(0))
	for _, test := range []struct {
		name     string
		password string
		valid    bool
	}{
		{name: "eight ASCII characters", password: "Ab1!cdE2", valid: true},
		{name: "eight Unicode characters", password: strings.Repeat("界", 8), valid: true},
		{name: "72 ASCII bytes", password: strings.Repeat("Ab1!cdE2", 9), valid: true},
		{name: "72 Unicode bytes", password: strings.Repeat("界", 24), valid: true},
		{name: "empty"},
		{name: "seven ASCII characters", password: "Ab1!cdE"},
		{name: "seven Unicode characters", password: strings.Repeat("界", 7)},
		{name: "73 ASCII bytes", password: strings.Repeat("Ab1!cdE2", 9) + "x"},
		{name: "73 Unicode bytes", password: strings.Repeat("界", 24) + "x"},
		{name: "invalid UTF-8", password: "Ab1!cdE2\xff"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := h.service.CheckPasswordStrength(test.password)
			if test.valid {
				if err != nil {
					t.Fatalf("valid password rejected: %v", err)
				}
			} else {
				assertErrorIs(t, err, domain.ErrPasswordWeak)
			}
		})
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

func TestCredentials_ServiceRejectsInvalidInputBeforePersistence(t *testing.T) {
	tests := append(
		invalidCredentialTestCases(),
		invalidCredentialTestCase{
			name:     "invalid UTF-8 email",
			email:    "alice\xff@example.com",
			password: testPassword,
			want:     domain.ErrInvalidEmail,
		},
		invalidCredentialTestCase{
			name:     "invalid UTF-8 password",
			email:    testUserEmail,
			password: testPassword + "\xff",
			want:     domain.ErrPasswordWeak,
		},
	)
	for _, test := range tests {
		for _, entry := range []string{"signup", "create", "update", "update self"} {
			t.Run(test.name+"/"+entry, func(t *testing.T) {
				h := newServiceHarness(t)
				data := domain.UpdateUserData{Email: &test.email, Password: &test.password}
				var err error
				switch entry {
				case "signup":
					_, err = h.service.SignUp(t.Context(), test.email, test.password)
				case "create":
					_, err = h.service.CreateUser(
						t.Context(),
						&domain.CreateUserData{Email: test.email, Password: test.password},
					)
				case "update":
					err = h.service.UpdateUser(t.Context(), uuid.NewString(), &data)
				case "update self":
					err = h.service.UpdateSelf(t.Context(), uuid.NewString(), &domain.UpdateSelfData{
						UpdateUserData: data, CurrentPassword: testPassword,
					})
				}
				assertErrorIs(t, err, test.want)
			})
		}
	}
}

func TestCredentials_TransportsRejectInvalidInputWithoutChangingCredentials(t *testing.T) {
	h := newSessionHarness(t)
	if err := h.repo.AssignRoleToUser(t.Context(), h.user.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	tokens := h.login(t)
	e := newTestEcho()
	httpAdapter.New(h.service).Register(e.Group("/api/v1"))
	client := newCredentialGRPCClient(t, h.service)

	for _, test := range invalidCredentialTestCases() {
		t.Run(test.name, func(t *testing.T) {
			body := map[string]string{"email": test.email, "password": test.password, "current_password": testPassword}
			for _, endpoint := range []struct{ method, path string }{
				{http.MethodPost, "/api/v1/auth/signup"},
				{http.MethodPost, "/api/v1/users"},
				{http.MethodPut, "/api/v1/users/me"},
				{http.MethodPut, "/api/v1/users/" + h.user.UUID.String()},
			} {
				t.Run(endpoint.method+" "+endpoint.path, func(t *testing.T) {
					credentialHTTPRequest(
						t,
						e,
						endpoint.method,
						endpoint.path,
						tokens.AccessToken,
						body,
						http.StatusBadRequest,
					)
				})
			}
			_, err := client.CreateUser(t.Context(), &pb.CreateUserRequest{Email: test.email, Password: test.password})
			if status.Code(err) != codes.InvalidArgument {
				t.Errorf("gRPC create = %v, want InvalidArgument", err)
			}
			_, err = client.UpdateUser(t.Context(), &pb.UpdateUserRequest{
				Uuid: h.user.UUID.String(), Email: &test.email, Password: &test.password,
			})
			if status.Code(err) != codes.InvalidArgument {
				t.Errorf("gRPC update = %v, want InvalidArgument", err)
			}
		})
	}

	// Omitting fields and supplying the same normalized email are both no-ops.
	credentialHTTPRequest(
		t,
		e,
		http.MethodPut,
		"/api/v1/users/me",
		tokens.AccessToken,
		map[string]string{},
		http.StatusOK,
	)
	credentialHTTPRequest(
		t,
		e,
		http.MethodPut,
		"/api/v1/users/"+h.user.UUID.String(),
		tokens.AccessToken,
		map[string]string{},
		http.StatusOK,
	)
	credentialHTTPRequest(t, e, http.MethodPut, "/api/v1/users/me", tokens.AccessToken, map[string]string{
		"email": "  " + strings.ToUpper(h.user.Email) + "  ", "current_password": testPassword,
	}, http.StatusOK)
	if _, err := client.UpdateUser(t.Context(), &pb.UpdateUserRequest{Uuid: h.user.UUID.String()}); err != nil {
		t.Fatalf("gRPC update without fields: %v", err)
	}
	stored, err := h.repo.GetUserByUUID(t.Context(), h.user.UUID.String())
	if err != nil {
		t.Fatal(err)
	}
	if stored.Email != h.user.Email || stored.Password != h.user.Password ||
		stored.CredentialVersion != h.user.CredentialVersion {
		t.Fatal("rejected or omitted credentials changed the stored user")
	}
	if _, err := h.service.ValidateJWTToken(t.Context(), tokens.AccessToken, domain.JWTTokenPurposeAccess); err != nil {
		t.Fatalf("rejected or omitted credentials invalidated the session: %v", err)
	}
}

func TestCredentials_AllCreationPathsCanLoginOverHTTP(t *testing.T) {
	h := newSessionHarness(t, auth.WithMinPasswordEntropyBits(0))
	if err := h.repo.AssignRoleToUser(t.Context(), h.user.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	admin := h.login(t)
	e := newTestEcho()
	httpAdapter.New(h.service).Register(e.Group("/api/v1"))
	client := newCredentialGRPCClient(t, h.service)
	for _, entry := range []string{"service signup", "service create", "HTTP signup", "HTTP create", "gRPC create"} {
		for _, test := range []struct{ name, password string }{
			{"eight Unicode characters", strings.Repeat("界", 8)},
			{"long ASCII password", testPassword},
			{"72 UTF-8 bytes", strings.Repeat("界", 22) + "Ab3!z9"},
			{"surrounding password spaces", "  long password with spaces  "},
		} {
			t.Run(entry+"/"+test.name, func(t *testing.T) {
				email := " \tCREDENTIALS-" + uuid.NewString() + "@EXAMPLE.COM \n"
				body := map[string]string{"email": email, "password": test.password}
				var err error
				switch entry {
				case "service signup":
					_, err = h.service.SignUp(t.Context(), email, test.password)
				case "service create":
					_, err = h.service.CreateUser(
						t.Context(),
						&domain.CreateUserData{Email: email, Password: test.password},
					)
				case "HTTP signup":
					credentialHTTPRequest(t, e, http.MethodPost, "/api/v1/auth/signup", "", body, http.StatusCreated)
				case "HTTP create":
					credentialHTTPRequest(
						t,
						e,
						http.MethodPost,
						"/api/v1/users",
						admin.AccessToken,
						body,
						http.StatusCreated,
					)
				case "gRPC create":
					_, err = client.CreateUser(
						t.Context(),
						&pb.CreateUserRequest{Email: email, Password: test.password},
					)
				}
				if err != nil {
					t.Fatalf("create: %v", err)
				}
				stored, err := h.repo.GetUserByEmail(t.Context(), strings.ToLower(strings.TrimSpace(email)))
				if err != nil || stored == nil {
					t.Fatalf("lookup normalized email: %v", err)
				}
				response := credentialHTTPRequest(t, e, http.MethodPost, "/api/v1/auth/login", "", body, http.StatusOK)
				var tokens domain.Tokens
				if err := json.Unmarshal(response.Body.Bytes(), &tokens); err != nil {
					t.Fatal(err)
				}
				if tokens.AccessToken == "" || tokens.RefreshToken == "" {
					t.Fatal("HTTP login returned empty tokens")
				}
				if trimmed := strings.TrimSpace(test.password); trimmed != test.password {
					body["password"] = trimmed
					credentialHTTPRequest(t, e, http.MethodPost, "/api/v1/auth/login", "", body, http.StatusBadRequest)
				}
			})
		}
	}
}

func TestCredentials_AllUpdatePathsCanLoginOverHTTP(t *testing.T) {
	h := newSessionHarness(t)
	if err := h.repo.AssignRoleToUser(t.Context(), h.user.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	admin := h.login(t)
	e := newTestEcho()
	httpAdapter.New(h.service).Register(e.Group("/api/v1"))
	client := newCredentialGRPCClient(t, h.service)
	for _, entry := range []string{"service update", "service self update", "HTTP update", "HTTP self update", "gRPC update"} {
		t.Run(entry, func(t *testing.T) {
			user, err := h.service.CreateUser(t.Context(), &domain.CreateUserData{
				Email: "target-" + uuid.NewString() + "@example.com", Password: testPassword,
			})
			if err != nil {
				t.Fatal(err)
			}
			oldTokens, err := h.service.Login(t.Context(), user.Email, testPassword)
			if err != nil {
				t.Fatal(err)
			}
			email, password := " \tUPDATED-"+user.Email+" \n", strings.Repeat("界", 22)+"Ab3!z9"
			data := domain.UpdateUserData{Email: &email, Password: &password}
			body := map[string]string{"email": email, "password": password, "current_password": testPassword}
			switch entry {
			case "service update":
				err = h.service.UpdateUser(t.Context(), user.UUID.String(), &data)
			case "service self update":
				err = h.service.UpdateSelf(t.Context(), user.UUID.String(), &domain.UpdateSelfData{
					UpdateUserData: data, CurrentPassword: testPassword,
				})
			case "HTTP update":
				credentialHTTPRequest(
					t,
					e,
					http.MethodPut,
					"/api/v1/users/"+user.UUID.String(),
					admin.AccessToken,
					body,
					http.StatusOK,
				)
			case "HTTP self update":
				credentialHTTPRequest(
					t,
					e,
					http.MethodPut,
					"/api/v1/users/me",
					oldTokens.AccessToken,
					body,
					http.StatusOK,
				)
			case "gRPC update":
				_, err = client.UpdateUser(t.Context(), &pb.UpdateUserRequest{
					Uuid: user.UUID.String(), Email: &email, Password: &password,
				})
			}
			if err != nil {
				t.Fatalf("update: %v", err)
			}
			if _, err := h.service.ValidateJWTToken(
				t.Context(),
				oldTokens.AccessToken,
				domain.JWTTokenPurposeAccess,
			); !errors.Is(
				err,
				domain.ErrInvalidToken,
			) {
				t.Fatalf("updated credentials left old session valid: %v", err)
			}
			credentialHTTPRequest(t, e, http.MethodPost, "/api/v1/auth/login", "", body, http.StatusOK)
		})
	}
}

func TestCredentials_LoginAndProofDoNotRecheckPasswordStrength(t *testing.T) {
	h := newSessionHarness(t, auth.WithMinPasswordEntropyBits(1000))
	if err := h.service.CheckPasswordStrength(testPassword); err == nil {
		t.Fatal("fixture password unexpectedly meets the new strength policy")
	}
	e := newTestEcho()
	httpAdapter.New(h.service).Register(e.Group("/api/v1"))
	credentialHTTPRequest(t, e, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email": h.user.Email, "password": testPassword,
	}, http.StatusOK)
	tokens := h.login(t)
	credentialHTTPRequest(t, e, http.MethodPut, "/api/v1/users/me", tokens.AccessToken, map[string]string{
		"email": "changed-" + h.user.Email, "current_password": testPassword,
	}, http.StatusOK)
}

func TestCredentials_LoginAndProofRejectPasswordsBeyondByteLimit(t *testing.T) {
	h := newSessionHarness(t)
	password := strings.Repeat("Ab1!cdE2", 9)
	if err := h.service.UpdateUser(
		t.Context(),
		h.user.UUID.String(),
		&domain.UpdateUserData{Password: &password},
	); err != nil {
		t.Fatal(err)
	}
	tokens, err := h.service.Login(t.Context(), h.user.Email, password)
	if err != nil {
		t.Fatal(err)
	}
	e := newTestEcho()
	httpAdapter.New(h.service).Register(e.Group("/api/v1"))
	credentialHTTPRequest(t, e, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email": h.user.Email, "password": password + "x",
	}, http.StatusBadRequest)
	credentialHTTPRequest(t, e, http.MethodPut, "/api/v1/users/me", tokens.AccessToken, map[string]string{
		"email": "changed-" + h.user.Email, "current_password": password + "x",
	}, http.StatusBadRequest)
	if _, err := h.service.ValidateJWTToken(t.Context(), tokens.AccessToken, domain.JWTTokenPurposeAccess); err != nil {
		t.Fatalf("rejected proof invalidated the session: %v", err)
	}
}

func TestCredentials_HTTPClientsAcceptLongPasswords(t *testing.T) {
	h := newSessionHarness(t)
	e := newTestEcho()
	httpAdapter.New(h.service).Register(e.Group("/api/v1"))
	server := httptest.NewServer(e)
	t.Cleanup(server.Close)
	ogenClient, err := ogen.NewClient(server.URL+"/api/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	oapiClient, err := oapi.NewClientWithResponses(server.URL + "/api/v1")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, password string }{
		{"28 characters", testPassword},
		{"72 ASCII bytes", strings.Repeat("Ab1!cdE2", 9)},
		{"72 Unicode bytes", strings.Repeat("界", 22) + "Ab3!z9"},
	} {
		t.Run(test.name, func(t *testing.T) {
			email := " \tSDK-" + uuid.NewString() + "@EXAMPLE.COM \n"
			if _, err := h.service.SignUp(t.Context(), email, test.password); err != nil {
				t.Fatal(err)
			}
			response, err := ogenClient.Login(t.Context(), &ogen.LoginRequest{Email: email, Password: test.password})
			if err != nil {
				t.Fatalf("ogen login: %v", err)
			}
			if tokens, ok := response.(*ogen.Tokens); !ok || tokens.AccessToken.Value == "" {
				t.Fatalf("ogen login response = %T, want tokens", response)
			}
			oapiResponse, err := oapiClient.LoginWithResponse(
				t.Context(),
				oapi.LoginJSONRequestBody{Email: email, Password: test.password},
			)
			if err != nil {
				t.Fatalf("oapi-codegen login: %v", err)
			}
			if oapiResponse.StatusCode() != http.StatusOK || oapiResponse.JSON200 == nil {
				t.Fatalf("oapi-codegen login status = %d, want tokens", oapiResponse.StatusCode())
			}
		})
	}
	// OpenAPI maxLength counts characters; the server must still enforce bytes.
	overlong := strings.Repeat("界", 24) + "x"
	response, err := ogenClient.Login(t.Context(), &ogen.LoginRequest{Email: h.user.Email, Password: overlong})
	if err != nil {
		t.Fatalf("ogen byte-limit response: %v", err)
	}
	if _, ok := response.(*ogen.LoginBadRequest); !ok {
		t.Fatalf("ogen byte-limit response = %T, want BadRequest", response)
	}
	oapiResponse, err := oapiClient.LoginWithResponse(
		t.Context(),
		oapi.LoginJSONRequestBody{Email: h.user.Email, Password: overlong},
	)
	if err != nil {
		t.Fatalf("oapi-codegen byte-limit response: %v", err)
	}
	if oapiResponse.StatusCode() != http.StatusBadRequest || oapiResponse.ApplicationproblemJSON400 == nil {
		t.Fatalf("oapi-codegen byte-limit status = %d, want BadRequest", oapiResponse.StatusCode())
	}
}

func credentialHTTPRequest(
	t *testing.T,
	e *echo.Echo,
	method, path, bearer string,
	data any,
	want int,
) *httptest.ResponseRecorder {
	t.Helper()
	response := sessionHTTPRequest(t, e, method, path, bearer, data)
	if response.Code != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, response.Code, want, response.Body.String())
	}
	return response
}

func newCredentialGRPCClient(t *testing.T, service *auth.Service) pb.AuthServiceClient {
	t.Helper()
	server := grpc.NewServer(grpc.UnaryInterceptor(
		protovalidateInterceptor.UnaryServerInterceptor(protovalidate.GlobalValidator),
	))
	grpcAdapter.New(service).Register(server)
	listener := bufconn.Listen(1024 * 1024)
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		if err := <-result; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Errorf("stop gRPC test server: %v", err)
		}
	})
	connection, err := grpc.NewClient(
		"passthrough:///credentials",
		grpc.WithContextDialer(
			func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) },
		),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := connection.Close(); err != nil {
			t.Errorf("close gRPC test client: %v", err)
		}
	})
	return pb.NewAuthServiceClient(connection)
}

func TestService_SignUp(t *testing.T) {
	h := newServiceHarness(t)
	expectTransaction(h.repository)
	h.repository.EXPECT().CreateUser(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, user *models.User) error {
			user.ID = 42
			return nil
		})
	h.repository.EXPECT().AssignRoleToUser(gomock.Any(), 42, domain.RBACRoleUser).Return(nil)
	expectOutboxEvent(h, 42, domain.EventTypeAuthSignUp, nil)

	user, err := h.service.SignUp(context.Background(), testUserEmail, testPassword)
	if err != nil {
		t.Fatalf("SignUp() error = %v", err)
	}
	if user.ID != 42 {
		t.Errorf("user ID = %d, want 42", user.ID)
	}
	if user.UUID.String() == "" {
		t.Error("user UUID is empty")
	}
	if user.Email != testUserEmail {
		t.Errorf("user email = %q, want %q", user.Email, testUserEmail)
	}
	if user.Status != domain.UserStatusActive {
		t.Errorf("user status = %q, want %q", user.Status, domain.UserStatusActive)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password.V), []byte(testPassword)); err != nil {
		t.Errorf("stored password does not match input: %v", err)
	}
}

func TestService_SignUpRejectsWeakPasswordBeforePersistence(t *testing.T) {
	h := newServiceHarness(t)

	user, err := h.service.SignUp(context.Background(), testUserEmail, "password")
	assertErrorIs(t, err, domain.ErrPasswordWeak)
	if user != nil {
		t.Errorf("SignUp() user = %#v, want nil", user)
	}
}

func TestService_SignUpRepositoryFailures(t *testing.T) {
	repositoryError := errors.New("repository unavailable")

	tests := []struct {
		name  string
		setup func(*serviceHarness)
	}{
		{
			name: "transaction",
			setup: func(h *serviceHarness) {
				h.repository.EXPECT().WithTransaction(gomock.Any(), gomock.Any()).Return(repositoryError)
			},
		},
		{
			name: "create user",
			setup: func(h *serviceHarness) {
				expectTransaction(h.repository)
				h.repository.EXPECT().CreateUser(gomock.Any(), gomock.Any()).Return(repositoryError)
			},
		},
		{
			name: "assign role",
			setup: func(h *serviceHarness) {
				expectTransaction(h.repository)
				h.repository.EXPECT().CreateUser(gomock.Any(), gomock.Any()).Return(nil)
				h.repository.EXPECT().AssignRoleToUser(gomock.Any(), 0, domain.RBACRoleUser).
					Return(repositoryError)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newServiceHarness(t)
			tt.setup(h)

			user, err := h.service.SignUp(context.Background(), testUserEmail, testPassword)
			assertErrorIs(t, err, repositoryError)
			if user != nil {
				t.Errorf("SignUp() user = %#v, want nil", user)
			}
		})
	}
}

func TestService_SignUpReportsOutboxFailure(t *testing.T) {
	h := newServiceHarness(t)
	outboxError := errors.New("outbox unavailable")
	expectTransaction(h.repository)
	h.repository.EXPECT().CreateUser(gomock.Any(), gomock.Any()).Return(nil)
	h.repository.EXPECT().AssignRoleToUser(gomock.Any(), 0, domain.RBACRoleUser).Return(nil)
	expectOutboxEvent(h, 0, domain.EventTypeAuthSignUp, outboxError)

	user, err := h.service.SignUp(context.Background(), testUserEmail, testPassword)
	assertErrorIs(t, err, outboxError)
	if user != nil {
		t.Errorf("SignUp() user = %#v, want nil", user)
	}
}

func TestService_Login(t *testing.T) {
	h := newServiceHarness(t)
	user := newTestUser(t, domain.UserStatusActive)
	h.repository.EXPECT().GetUserByEmail(gomock.Any(), testUserEmail).Return(user, nil)
	expectOutboxEvent(h, user.ID, domain.EventTypeAuthLogin, nil)
	session := expectSessionCreation(h)
	h.repository.EXPECT().GetActiveSession(gomock.Any(), gomock.Any(), user.UUID.String()).Return(session, nil).Times(2)

	tokens, err := h.service.Login(context.Background(), "  ALICE@EXAMPLE.COM ", testPassword)
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" {
		t.Fatalf("Login() returned empty token: %#v", tokens)
	}
	if tokens.AccessToken == tokens.RefreshToken {
		t.Error("access and refresh tokens are identical")
	}
	if tokens.ExpiresIn != int(testAccessTTL.Seconds()) {
		t.Errorf("ExpiresIn = %d, want %d", tokens.ExpiresIn, int(testAccessTTL.Seconds()))
	}

	accessClaims, err := h.service.ValidateJWTToken(
		context.Background(), tokens.AccessToken, domain.JWTTokenPurposeAccess,
	)
	if err != nil {
		t.Fatalf("validate access token: %v", err)
	}
	refreshClaims, err := h.service.ValidateJWTToken(
		context.Background(), tokens.RefreshToken, domain.JWTTokenPurposeRefresh,
	)
	if err != nil {
		t.Fatalf("validate refresh token: %v", err)
	}
	if accessClaims.Subject != user.UUID.String() || refreshClaims.Subject != user.UUID.String() {
		t.Errorf("token subjects = (%q, %q), want %q", accessClaims.Subject, refreshClaims.Subject, user.UUID)
	}
	if accessClaims.ID == refreshClaims.ID {
		t.Error("access and refresh tokens have the same JWT ID")
	}
}

func TestService_LoginRejectsInvalidCredentials(t *testing.T) {
	repositoryError := domain.ErrEntityNotFound

	tests := []struct {
		name      string
		user      *models.User
		lookupErr error
		password  string
	}{
		{name: "unknown email", lookupErr: repositoryError, password: testPassword},
		{name: "inactive user", user: newTestUser(t, domain.UserStatusInactive), password: testPassword},
		{name: "wrong password", user: newTestUser(t, domain.UserStatusActive), password: "wrong password"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newServiceHarness(t)
			h.repository.EXPECT().GetUserByEmail(gomock.Any(), testUserEmail).
				Return(tt.user, tt.lookupErr)

			tokens, err := h.service.Login(context.Background(), testUserEmail, tt.password)
			assertErrorIs(t, err, domain.ErrInvalidCredentials)
			if tokens != nil {
				t.Errorf("Login() tokens = %#v, want nil", tokens)
			}
		})
	}
}

func TestService_LoginIgnoresOutboxFailure(t *testing.T) {
	h := newServiceHarness(t)
	user := newTestUser(t, domain.UserStatusActive)
	h.repository.EXPECT().GetUserByEmail(gomock.Any(), testUserEmail).Return(user, nil)
	expectOutboxEvent(h, user.ID, domain.EventTypeAuthLogin, errors.New("outbox unavailable"))
	expectSessionCreation(h)

	tokens, err := h.service.Login(context.Background(), testUserEmail, testPassword)
	if err != nil {
		t.Fatalf("Login() error = %v, want nil", err)
	}
	if tokens == nil {
		t.Fatal("Login() tokens = nil")
	}
}
func TestService_CreateUser(t *testing.T) {
	h := newServiceHarness(t)
	expectTransaction(h.repository)
	h.repository.EXPECT().CreateUser(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, user *models.User) error {
			user.ID = 84
			return nil
		})
	h.repository.EXPECT().AssignRoleToUser(gomock.Any(), 84, domain.RBACRoleUser).Return(nil)
	expectOutboxEvent(h, 84, domain.EventTypeUserCreate, nil)

	user, err := h.service.CreateUser(context.Background(), &domain.CreateUserData{
		Email: testUserEmail, Password: testPassword,
	})
	if err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	if user.ID != 84 || user.Email != testUserEmail || !user.IsActive() {
		t.Errorf("CreateUser() user = %#v", user)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password.V), []byte(testPassword)); err != nil {
		t.Errorf("stored password does not match input: %v", err)
	}
}

func TestService_CreateUserRejectsWeakPassword(t *testing.T) {
	h := newServiceHarness(t)

	user, err := h.service.CreateUser(context.Background(), &domain.CreateUserData{
		Email: testUserEmail, Password: "password",
	})
	assertErrorIs(t, err, domain.ErrPasswordWeak)
	if user != nil {
		t.Errorf("CreateUser() user = %#v, want nil", user)
	}
}

func TestService_CreateUserTransactionFailures(t *testing.T) {
	operationError := errors.New("operation failed")

	tests := []struct {
		name  string
		setup func(*serviceHarness)
	}{
		{
			name: "create",
			setup: func(h *serviceHarness) {
				expectTransaction(h.repository)
				h.repository.EXPECT().CreateUser(gomock.Any(), gomock.Any()).Return(operationError)
			},
		},
		{
			name: "assign role",
			setup: func(h *serviceHarness) {
				expectTransaction(h.repository)
				h.repository.EXPECT().CreateUser(gomock.Any(), gomock.Any()).Return(nil)
				h.repository.EXPECT().AssignRoleToUser(gomock.Any(), 0, domain.RBACRoleUser).
					Return(operationError)
			},
		},
		{
			name: "outbox",
			setup: func(h *serviceHarness) {
				expectTransaction(h.repository)
				h.repository.EXPECT().CreateUser(gomock.Any(), gomock.Any()).Return(nil)
				h.repository.EXPECT().AssignRoleToUser(gomock.Any(), 0, domain.RBACRoleUser).Return(nil)
				expectOutboxEvent(h, 0, domain.EventTypeUserCreate, operationError)
			},
		},
		{
			name: "transaction",
			setup: func(h *serviceHarness) {
				h.repository.EXPECT().WithTransaction(gomock.Any(), gomock.Any()).Return(operationError)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newServiceHarness(t)
			tt.setup(h)

			user, err := h.service.CreateUser(context.Background(), &domain.CreateUserData{
				Email: testUserEmail, Password: testPassword,
			})
			assertErrorIs(t, err, operationError)
			if user != nil {
				t.Errorf("CreateUser() user = %#v, want nil", user)
			}
		})
	}
}

func TestService_UpdateUserSkipsUnchangedData(t *testing.T) {
	h := newServiceHarness(t)
	user := newTestUser(t, domain.UserStatusActive)
	expectTransaction(h.repository)
	h.repository.EXPECT().GetUserByUUID(gomock.Any(), user.UUID.String()).Return(user, nil)

	err := h.service.UpdateUser(context.Background(), user.UUID.String(), &domain.UpdateUserData{
		Email: &user.Email,
	})
	if err != nil {
		t.Fatalf("UpdateUser() error = %v", err)
	}
}

func TestService_UpdateUserEmail(t *testing.T) {
	h := newServiceHarness(t)
	user := newTestUser(t, domain.UserStatusActive)
	newEmail := "new@example.com"
	expectTransaction(h.repository)
	h.repository.EXPECT().GetUserByUUID(gomock.Any(), user.UUID.String()).Return(user, nil)
	h.repository.EXPECT().UpdateUser(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, updated *models.User) error {
			if updated.Email != newEmail {
				t.Errorf("updated email = %q, want %q", updated.Email, newEmail)
			}
			return nil
		})
	expectOutboxEvent(h, user.ID, domain.EventTypeUserUpdate, nil)
	if err := h.service.UpdateUser(context.Background(), user.UUID.String(), &domain.UpdateUserData{
		Email: &newEmail,
	}); err != nil {
		t.Fatalf("UpdateUser() error = %v", err)
	}
}

func TestService_UpdateUserPassword(t *testing.T) {
	h := newServiceHarness(t)
	user := newTestUser(t, domain.UserStatusActive)
	newPassword := "another correct horse battery staple"
	expectTransaction(h.repository)
	h.repository.EXPECT().GetUserByUUID(gomock.Any(), user.UUID.String()).Return(user, nil)
	h.repository.EXPECT().UpdateUser(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, updated *models.User) error {
			if err := bcrypt.CompareHashAndPassword(
				[]byte(updated.Password.V), []byte(newPassword),
			); err != nil {
				t.Errorf("updated password does not match input: %v", err)
			}
			return nil
		})
	expectOutboxEvent(h, user.ID, domain.EventTypeUserUpdate, nil)
	if err := h.service.UpdateUser(context.Background(), user.UUID.String(), &domain.UpdateUserData{
		Password: &newPassword,
	}); err != nil {
		t.Fatalf("UpdateUser() error = %v", err)
	}
}

func TestService_UpdateUserRejectsWeakPassword(t *testing.T) {
	h := newServiceHarness(t)
	user := newTestUser(t, domain.UserStatusActive)
	weakPassword := "password"
	err := h.service.UpdateUser(context.Background(), user.UUID.String(), &domain.UpdateUserData{
		Password: &weakPassword,
	})
	assertErrorIs(t, err, domain.ErrPasswordWeak)
}

func TestService_UpdateUserFailures(t *testing.T) {
	operationError := errors.New("operation failed")
	newEmail := "new@example.com"

	tests := []struct {
		name  string
		setup func(*serviceHarness, *models.User)
	}{
		{
			name: "get user",
			setup: func(h *serviceHarness, user *models.User) {
				expectTransaction(h.repository)
				h.repository.EXPECT().GetUserByUUID(gomock.Any(), user.UUID.String()).
					Return(nil, operationError)
			},
		},
		{
			name: "update user",
			setup: func(h *serviceHarness, user *models.User) {
				expectTransaction(h.repository)
				h.repository.EXPECT().GetUserByUUID(gomock.Any(), user.UUID.String()).Return(user, nil)
				h.repository.EXPECT().UpdateUser(gomock.Any(), user).Return(operationError)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newServiceHarness(t)
			user := newTestUser(t, domain.UserStatusActive)
			tt.setup(h, user)

			err := h.service.UpdateUser(context.Background(), user.UUID.String(), &domain.UpdateUserData{
				Email: &newEmail,
			})
			assertErrorIs(t, err, operationError)
		})
	}
}

func TestService_UpdateUserReportsOutboxFailure(t *testing.T) {
	h := newServiceHarness(t)
	outboxError := errors.New("outbox unavailable")
	user := newTestUser(t, domain.UserStatusActive)
	newEmail := "new@example.com"
	expectTransaction(h.repository)
	h.repository.EXPECT().GetUserByUUID(gomock.Any(), user.UUID.String()).Return(user, nil)
	h.repository.EXPECT().UpdateUser(gomock.Any(), user).Return(nil)
	expectOutboxEvent(h, user.ID, domain.EventTypeUserUpdate, outboxError)
	err := h.service.UpdateUser(context.Background(), user.UUID.String(), &domain.UpdateUserData{
		Email: &newEmail,
	})
	assertErrorIs(t, err, outboxError)
}

func TestService_DeleteUser(t *testing.T) {
	h := newServiceHarness(t)
	user := newTestUser(t, domain.UserStatusActive)
	expectTransaction(h.repository)
	h.repository.EXPECT().GetUserByUUID(gomock.Any(), user.UUID.String()).Return(user, nil)
	h.repository.EXPECT().DeleteUser(gomock.Any(), user).Return(nil)
	expectOutboxEvent(h, user.ID, domain.EventTypeUserDelete, nil)
	if err := h.service.DeleteUser(context.Background(), user.UUID.String()); err != nil {
		t.Fatalf("DeleteUser() error = %v", err)
	}
}

func TestService_UpdateUserReportsCommitFailure(t *testing.T) {
	h := newServiceHarness(t)
	user := newTestUser(t, domain.UserStatusActive)
	newEmail := "new@example.com"
	commitError := errors.New("commit failed")
	h.repository.EXPECT().WithTransaction(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error {
			if err := fn(ctx); err != nil {
				return err
			}
			return commitError
		})
	h.repository.EXPECT().GetUserByUUID(gomock.Any(), user.UUID.String()).Return(user, nil)
	h.repository.EXPECT().UpdateUser(gomock.Any(), user).Return(nil)
	expectOutboxEvent(h, user.ID, domain.EventTypeUserUpdate, nil)

	err := h.service.UpdateUser(context.Background(), user.UUID.String(), &domain.UpdateUserData{
		Email: &newEmail,
	})
	assertErrorIs(t, err, commitError)
}

func TestService_DeleteUserFailures(t *testing.T) {
	operationError := errors.New("operation failed")

	tests := []struct {
		name  string
		setup func(*serviceHarness, *models.User)
	}{
		{
			name: "get user",
			setup: func(h *serviceHarness, user *models.User) {
				expectTransaction(h.repository)
				h.repository.EXPECT().GetUserByUUID(gomock.Any(), user.UUID.String()).
					Return(nil, operationError)
			},
		},
		{
			name: "delete user",
			setup: func(h *serviceHarness, user *models.User) {
				expectTransaction(h.repository)
				h.repository.EXPECT().GetUserByUUID(gomock.Any(), user.UUID.String()).Return(user, nil)
				h.repository.EXPECT().DeleteUser(gomock.Any(), user).Return(operationError)
			},
		},
		{
			name: "outbox",
			setup: func(h *serviceHarness, user *models.User) {
				expectTransaction(h.repository)
				h.repository.EXPECT().GetUserByUUID(gomock.Any(), user.UUID.String()).Return(user, nil)
				h.repository.EXPECT().DeleteUser(gomock.Any(), user).Return(nil)
				expectOutboxEvent(h, user.ID, domain.EventTypeUserDelete, operationError)
			},
		},
		{
			name: "transaction",
			setup: func(h *serviceHarness, _ *models.User) {
				h.repository.EXPECT().WithTransaction(gomock.Any(), gomock.Any()).Return(operationError)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newServiceHarness(t)
			user := newTestUser(t, domain.UserStatusActive)
			tt.setup(h, user)

			err := h.service.DeleteUser(context.Background(), user.UUID.String())
			assertErrorIs(t, err, operationError)
		})
	}
}

func TestService_UserQueriesDelegateToRepository(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		h := newServiceHarness(t)
		users := []*models.User{newTestUser(t, domain.UserStatusActive)}
		h.repository.EXPECT().ListUsers(gomock.Any(), 20, 40).Return(users, nil)

		got, err := h.service.ListUsers(context.Background(), 20, 40)
		if err != nil || len(got) != 1 || got[0] != users[0] {
			t.Fatalf("ListUsers() = (%#v, %v), want (%#v, nil)", got, err, users)
		}
	})

	t.Run("by ID", func(t *testing.T) {
		h := newServiceHarness(t)
		user := newTestUser(t, domain.UserStatusActive)
		h.repository.EXPECT().GetUserByID(gomock.Any(), user.ID).Return(user, nil)

		got, err := h.service.GetUserByID(context.Background(), user.ID)
		if err != nil || got != user {
			t.Fatalf("GetUserByID() = (%#v, %v), want (%#v, nil)", got, err, user)
		}
	})

	t.Run("by UUID", func(t *testing.T) {
		h := newServiceHarness(t)
		user := newTestUser(t, domain.UserStatusActive)
		h.repository.EXPECT().GetUserByUUID(gomock.Any(), user.UUID.String()).Return(user, nil)

		got, err := h.service.GetUserByUUID(context.Background(), user.UUID.String())
		if err != nil || got != user {
			t.Fatalf("GetUserByUUID() = (%#v, %v), want (%#v, nil)", got, err, user)
		}
	})
}
func TestService_ValidateJWTToken(t *testing.T) {
	h := newServiceHarness(t)
	user := newTestUser(t, domain.UserStatusActive)
	token := signTestJWT(
		t,
		testJWTSecret,
		domain.JWTTokenPurposeAccess,
		user.UUID.String(),
		time.Now().Add(time.Hour),
	)
	h.repository.EXPECT().GetActiveSession(gomock.Any(), gomock.Any(), gomock.Any()).Return(&models.Session{}, nil)

	claims, err := h.service.ValidateJWTToken(
		context.Background(), token, domain.JWTTokenPurposeAccess,
	)
	if err != nil {
		t.Fatalf("ValidateJWTToken() error = %v", err)
	}
	if claims.Subject != user.UUID.String() {
		t.Errorf("subject = %q, want %q", claims.Subject, user.UUID)
	}
	if claims.TokenUse != domain.JWTTokenPurposeAccess {
		t.Errorf("token_use = %q, want %q", claims.TokenUse, domain.JWTTokenPurposeAccess)
	}
	if claims.Issuer != testJWTIssuer {
		t.Errorf("issuer = %q, want %q", claims.Issuer, testJWTIssuer)
	}
	if claims.KID != sha256Hex(testJWTSecret) {
		t.Errorf("kid = %q, want %q", claims.KID, sha256Hex(testJWTSecret))
	}
}

func TestService_ValidateJWTTokenEnforcesSecurityContract(t *testing.T) {
	validClaims := func() domain.JWTClaims {
		return domain.JWTClaims{
			ID:        uuid.NewString(),
			Audience:  testJWTAudience,
			Issuer:    testJWTIssuer,
			Subject:   "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Minute)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			KID:       sha256Hex(testJWTSecret),
			TokenUse:  domain.JWTTokenPurposeAccess,
			SessionID: uuid.NewString(),
		}
	}

	tests := []struct {
		name   string
		method jwt.SigningMethod
		mutate func(*domain.JWTClaims)
	}{
		{
			name:   "HS512 algorithm",
			method: jwt.SigningMethodHS512,
			mutate: func(*domain.JWTClaims) {},
		},
		{
			name:   "missing issuer",
			method: jwt.SigningMethodHS256,
			mutate: func(claims *domain.JWTClaims) { claims.Issuer = "" },
		},
		{
			name:   "wrong issuer",
			method: jwt.SigningMethodHS256,
			mutate: func(claims *domain.JWTClaims) { claims.Issuer = "other-service" },
		},
		{
			name:   "missing audience",
			method: jwt.SigningMethodHS256,
			mutate: func(claims *domain.JWTClaims) { claims.Audience = nil },
		},
		{
			name:   "wrong audience",
			method: jwt.SigningMethodHS256,
			mutate: func(claims *domain.JWTClaims) {
				claims.Audience = jwt.ClaimStrings{"other-service"}
			},
		},
		{
			name:   "missing expiration",
			method: jwt.SigningMethodHS256,
			mutate: func(claims *domain.JWTClaims) { claims.ExpiresAt = nil },
		},
		{
			name: "missing issued at", method: jwt.SigningMethodHS256,
			mutate: func(claims *domain.JWTClaims) { claims.IssuedAt = nil },
		},
		{
			name: "future issued at", method: jwt.SigningMethodHS256,
			mutate: func(claims *domain.JWTClaims) { claims.IssuedAt = jwt.NewNumericDate(time.Now().Add(time.Hour)) },
		},
		{
			name: "missing session ID", method: jwt.SigningMethodHS256,
			mutate: func(claims *domain.JWTClaims) { claims.SessionID = "" },
		},
		{
			name: "invalid session ID", method: jwt.SigningMethodHS256,
			mutate: func(claims *domain.JWTClaims) { claims.SessionID = "invalid" },
		},
		{
			name: "missing token ID", method: jwt.SigningMethodHS256,
			mutate: func(claims *domain.JWTClaims) { claims.ID = "" },
		},
		{
			name: "invalid token ID", method: jwt.SigningMethodHS256,
			mutate: func(claims *domain.JWTClaims) { claims.ID = "invalid" },
		},
		{
			name: "missing subject", method: jwt.SigningMethodHS256,
			mutate: func(claims *domain.JWTClaims) { claims.Subject = "" },
		},
		{
			name: "invalid subject", method: jwt.SigningMethodHS256,
			mutate: func(claims *domain.JWTClaims) { claims.Subject = "invalid" },
		},
		{
			name: "unknown key ID", method: jwt.SigningMethodHS256,
			mutate: func(claims *domain.JWTClaims) { claims.KID = "unknown" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newServiceHarness(t)
			claims := validClaims()
			tt.mutate(&claims)
			token := signTestJWTWithClaims(t, tt.method, testJWTSecret, claims)

			var (
				got       *domain.JWTClaims
				err       error
				recovered any
			)
			func() {
				defer func() {
					recovered = recover()
				}()
				got, err = h.service.ValidateJWTToken(
					context.Background(),
					token,
					domain.JWTTokenPurposeAccess,
				)
			}()

			if recovered != nil {
				t.Fatalf("ValidateJWTToken() panicked: %v", recovered)
			}
			if err == nil {
				t.Fatal("ValidateJWTToken() error = nil, want validation error")
			}
			if got != nil {
				t.Errorf("ValidateJWTToken() claims = %#v, want nil", got)
			}
		})
	}
}

func TestService_ValidateJWTTokenRejectsInvalidPurposeArgument(t *testing.T) {
	h := newServiceHarness(t)
	token := signTestJWT(
		t,
		testJWTSecret,
		domain.JWTTokenPurposeAccess,
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		time.Now().Add(time.Hour),
	)

	claims, err := h.service.ValidateJWTToken(context.Background(), token, "unknown")
	assertErrorIs(t, err, domain.ErrInvalidToken)
	if claims != nil {
		t.Errorf("ValidateJWTToken() claims = %#v, want nil", claims)
	}
}

func TestService_ValidateJWTTokenRejectsWrongTokenPurpose(t *testing.T) {
	h := newServiceHarness(t)
	token := signTestJWT(
		t,
		testJWTSecret,
		domain.JWTTokenPurposeRefresh,
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		time.Now().Add(time.Hour),
	)

	claims, err := h.service.ValidateJWTToken(
		context.Background(), token, domain.JWTTokenPurposeAccess,
	)
	assertErrorIs(t, err, domain.ErrInvalidToken)
	if claims != nil {
		t.Errorf("ValidateJWTToken() claims = %#v, want nil", claims)
	}
}

func TestService_ValidateJWTTokenRejectsMalformedExpiredAndInvalidSignature(t *testing.T) {
	tests := []struct {
		name  string
		token func(*testing.T) string
	}{
		{
			name: "malformed",
			token: func(*testing.T) string {
				return "not-a-jwt"
			},
		},
		{
			name: "expired",
			token: func(t *testing.T) string {
				return signTestJWT(
					t,
					testJWTSecret,
					domain.JWTTokenPurposeAccess,
					"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
					time.Now().Add(-time.Minute),
				)
			},
		},
		{
			name: "invalid signature",
			token: func(t *testing.T) string {
				return signTestJWT(
					t,
					"different-secret",
					domain.JWTTokenPurposeAccess,
					"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
					time.Now().Add(time.Hour),
				)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newServiceHarness(t)
			claims, err := h.service.ValidateJWTToken(
				context.Background(), tt.token(t), domain.JWTTokenPurposeAccess,
			)
			assertErrorIs(t, err, domain.ErrInvalidToken)
			if claims != nil {
				t.Errorf("ValidateJWTToken() claims = %#v, want nil", claims)
			}
		})
	}
}

func TestService_ValidateJWTTokenRejectsNonHMACAlgorithm(t *testing.T) {
	h := newServiceHarness(t)
	claims := domain.JWTClaims{
		Subject:   "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		TokenUse:  domain.JWTTokenPurposeAccess,
		SessionID: uuid.NewString(),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).
		SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign unsigned JWT: %v", err)
	}

	got, err := h.service.ValidateJWTToken(
		context.Background(), token, domain.JWTTokenPurposeAccess,
	)
	if err == nil {
		t.Fatal("ValidateJWTToken() error = nil, want signing-method error")
	}
	if got != nil {
		t.Errorf("ValidateJWTToken() claims = %#v, want nil", got)
	}
}

func TestService_ValidateJWTTokenRejectsRevokedToken(t *testing.T) {
	h := newServiceHarness(t)
	token := signTestJWT(
		t,
		testJWTSecret,
		domain.JWTTokenPurposeAccess,
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		time.Now().Add(time.Hour),
	)
	h.repository.EXPECT().GetActiveSession(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, domain.ErrInvalidToken)

	claims, err := h.service.ValidateJWTToken(
		context.Background(), token, domain.JWTTokenPurposeAccess,
	)
	assertErrorIs(t, err, domain.ErrInvalidToken)
	if claims != nil {
		t.Errorf("ValidateJWTToken() claims = %#v, want nil", claims)
	}
}

func TestService_ValidateJWTTokenFailsClosedWhenRevocationLookupFails(t *testing.T) {
	h := newServiceHarness(t)
	token := signTestJWT(
		t,
		testJWTSecret,
		domain.JWTTokenPurposeAccess,
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		time.Now().Add(time.Hour),
	)
	cacheErr := errors.New("database unavailable")
	h.repository.EXPECT().GetActiveSession(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, cacheErr)

	claims, err := h.service.ValidateJWTToken(
		context.Background(), token, domain.JWTTokenPurposeAccess,
	)
	assertErrorIs(t, err, domain.ErrAuthenticationUnavailable)
	assertErrorIs(t, err, cacheErr)
	if claims != nil {
		t.Errorf("ValidateJWTToken() claims = %#v, want nil", claims)
	}
}

func TestService_ConfiguredJWTKeyRing(t *testing.T) {
	const (
		previousSecret = "previous-secret"
		currentSecret  = "current-secret"
	)
	h := newServiceHarness(t, auth.WithJWTSecrets([]string{previousSecret, currentSecret}))
	h.repository.EXPECT().
		GetActiveSession(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&models.Session{}, nil).
		AnyTimes()

	for _, secret := range []string{testJWTSecret, previousSecret, currentSecret} {
		token := signTestJWT(
			t,
			secret,
			domain.JWTTokenPurposeAccess,
			"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			time.Now().Add(time.Hour),
		)
		if _, err := h.service.ValidateJWTToken(
			context.Background(), token, domain.JWTTokenPurposeAccess,
		); err != nil {
			t.Fatalf("token signed by configured secret %q was rejected: %v", secret, err)
		}
	}

	user := newTestUser(t, domain.UserStatusActive)
	h.repository.EXPECT().GetUserByEmail(gomock.Any(), testUserEmail).Return(user, nil)
	expectOutboxEvent(h, user.ID, domain.EventTypeAuthLogin, nil)
	expectSessionCreation(h)
	tokens, err := h.service.Login(context.Background(), testUserEmail, testPassword)
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	claims, err := h.service.ValidateJWTToken(
		context.Background(), tokens.AccessToken, domain.JWTTokenPurposeAccess,
	)
	if err != nil {
		t.Fatalf("validate generated access token: %v", err)
	}
	if claims.KID != sha256Hex(currentSecret) {
		t.Errorf("generated token KID = %q, want current configured secret %q", claims.KID, sha256Hex(currentSecret))
	}
}
func TestService_ValidateAPIToken(t *testing.T) {
	h := newServiceHarness(t)
	rawToken := testAPIKey
	apiToken := &models.Token{
		ID:     17,
		UserID: 42,
		Name:   "automation",
		ExpiresAt: sql.Null[time.Time]{
			V:     time.Now().Add(time.Hour),
			Valid: true,
		},
	}
	h.repository.EXPECT().GetToken(gomock.Any(), sha256Hex(rawToken)).Return(apiToken, nil)
	before := time.Now()

	got, err := h.service.ValidateAPIToken(context.Background(), rawToken)
	if err != nil {
		t.Fatalf("ValidateAPIToken() error = %v", err)
	}
	if got != apiToken {
		t.Errorf("ValidateAPIToken() token = %#v, want %#v", got, apiToken)
	}

	select {
	case usage := <-h.service.RecentlyUsedTokensChan():
		if usage.ID != apiToken.ID {
			t.Errorf("usage token ID = %d, want %d", usage.ID, apiToken.ID)
		}
		if usage.When.Before(before) || usage.When.After(time.Now()) {
			t.Errorf("usage time = %v, want between call start and now", usage.When)
		}
	default:
		t.Fatal("ValidateAPIToken() did not publish token usage")
	}
}

func TestService_ValidateAPITokenAcceptsTokenWithoutExpiration(t *testing.T) {
	h := newServiceHarness(t)
	rawToken := testAPIKey
	apiToken := &models.Token{ID: 18, UserID: 42}
	h.repository.EXPECT().GetToken(gomock.Any(), sha256Hex(rawToken)).Return(apiToken, nil)

	got, err := h.service.ValidateAPIToken(context.Background(), rawToken)
	if err != nil {
		t.Fatalf("ValidateAPIToken() error = %v", err)
	}
	if got != apiToken {
		t.Errorf("ValidateAPIToken() token = %#v, want %#v", got, apiToken)
	}
}

func TestService_ValidateAPITokenRejectsExpiredToken(t *testing.T) {
	h := newServiceHarness(t)
	rawToken := testAPIKey
	apiToken := &models.Token{
		ID: 19,
		ExpiresAt: sql.Null[time.Time]{
			V:     time.Now().Add(-time.Minute),
			Valid: true,
		},
	}
	h.repository.EXPECT().GetToken(gomock.Any(), sha256Hex(rawToken)).Return(apiToken, nil)

	got, err := h.service.ValidateAPIToken(context.Background(), rawToken)
	assertErrorIs(t, err, domain.ErrInvalidToken)
	if got != nil {
		t.Errorf("ValidateAPIToken() token = %#v, want nil", got)
	}
	select {
	case usage := <-h.service.RecentlyUsedTokensChan():
		t.Fatalf("expired token published usage: %#v", usage)
	default:
	}
}

func TestService_ValidateAPITokenClassifiesRepositoryErrors(t *testing.T) {
	for _, test := range []struct {
		name  string
		cause error
		want  error
	}{
		{name: "unknown token", cause: domain.ErrEntityNotFound, want: domain.ErrInvalidToken},
		{
			name: "storage unavailable", cause: errors.New("private database connection failure"),
			want: domain.ErrAuthenticationUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newServiceHarness(t)
			h.repository.EXPECT().GetToken(gomock.Any(), sha256Hex(testAPIKey)).
				Return(nil, fmt.Errorf("token lookup failed: %w", test.cause))

			got, err := h.service.ValidateAPIToken(t.Context(), testAPIKey)
			assertErrorIs(t, err, test.want)
			assertErrorIs(t, err, test.cause)
			if got != nil {
				t.Errorf("ValidateAPIToken() token = %#v, want nil", got)
			}
		})
	}
}

func TestService_ValidateAPITokenRejectsInvalidFormat(t *testing.T) {
	tests := []struct {
		name  string
		token string
	}{
		{name: "empty", token: ""},
		{name: "missing prefix", token: strings.TrimPrefix(testAPIKey, "api_")},
		{name: "empty secret", token: "api_"},
		{name: "short secret", token: "api_" + strings.Repeat("a", 42)},
		{name: "long secret", token: "api_" + strings.Repeat("a", 44)},
		{name: "invalid base64url", token: "api_" + strings.Repeat("a", 42) + "*"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newServiceHarness(t)

			got, err := h.service.ValidateAPIToken(context.Background(), tt.token)
			assertErrorIs(t, err, domain.ErrInvalidToken)
			if got != nil {
				t.Errorf("ValidateAPIToken() token = %#v, want nil", got)
			}
		})
	}
}

func TestAPIKeyAuthentication_ClassifiesFailures(t *testing.T) {
	validToken := &models.Token{ID: 17, UserID: 42}
	expiredToken := &models.Token{
		ID: 18, UserID: 42,
		ExpiresAt: sql.Null[time.Time]{V: time.Now().Add(-time.Minute), Valid: true},
	}
	storageErr := errors.New("private database connection failure")
	notFoundErr := fmt.Errorf("record lookup: %w", domain.ErrEntityNotFound)

	for _, test := range []struct {
		name       string
		key        string
		token      *models.Token
		tokenErr   error
		owner      *models.User
		ownerErr   error
		httpStatus int
		grpcCode   codes.Code
	}{
		{name: "missing key", httpStatus: http.StatusUnauthorized, grpcCode: codes.Unauthenticated},
		{
			name: "malformed key", key: "api_invalid",
			httpStatus: http.StatusUnauthorized, grpcCode: codes.Unauthenticated,
		},
		{
			name: "unknown key", key: testAPIKey, tokenErr: notFoundErr,
			httpStatus: http.StatusUnauthorized, grpcCode: codes.Unauthenticated,
		},
		{
			name: "expired key", key: testAPIKey, token: expiredToken,
			httpStatus: http.StatusUnauthorized, grpcCode: codes.Unauthenticated,
		},
		{
			name: "token storage unavailable", key: testAPIKey, tokenErr: storageErr,
			httpStatus: http.StatusServiceUnavailable, grpcCode: codes.Unavailable,
		},
		{
			name: "owner missing", key: testAPIKey, token: validToken, ownerErr: notFoundErr,
			httpStatus: http.StatusUnauthorized, grpcCode: codes.Unauthenticated,
		},
		{
			name: "owner inactive", key: testAPIKey, token: validToken,
			owner:      &models.User{ID: validToken.UserID, Status: domain.UserStatusInactive},
			httpStatus: http.StatusUnauthorized, grpcCode: codes.Unauthenticated,
		},
		{
			name: "owner storage unavailable", key: testAPIKey, token: validToken, ownerErr: storageErr,
			httpStatus: http.StatusServiceUnavailable, grpcCode: codes.Unavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newServiceHarness(t)
			if test.token != nil || test.tokenErr != nil {
				h.repository.EXPECT().GetToken(gomock.Any(), sha256Hex(test.key)).
					Return(test.token, test.tokenErr).Times(3)
			}
			if test.owner != nil || test.ownerErr != nil {
				h.repository.EXPECT().GetUserByID(gomock.Any(), test.token.UserID).
					Return(test.owner, test.ownerErr).Times(3)
			}
			assertAPIKeyAuthenticationFailure(t, h.service, test.key, test.httpStatus, test.grpcCode)
		})
	}
}

func TestAPIKeyAuthentication_DatabaseOutage(t *testing.T) {
	for _, warmCache := range []bool{false, true} {
		t.Run("warm cache="+strconv.FormatBool(warmCache), func(t *testing.T) {
			h := newSessionHarness(t)
			secret := make([]byte, 32)
			if _, err := rand.Read(secret); err != nil {
				t.Fatal(err)
			}
			key := "api_" + base64.RawURLEncoding.EncodeToString(secret)
			token := &models.Token{
				UUID: uuid.New(), UserID: h.user.ID, Token: sha256Hex(key), Name: "outage-test",
			}
			if err := h.db.Master().Create(token).Error; err != nil {
				t.Fatal(err)
			}
			if warmCache {
				if _, err := h.service.ValidateAPIToken(t.Context(), key); err != nil {
					t.Fatalf("warm token cache: %v", err)
				}
			}
			db, err := h.db.Master().DB()
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			assertAPIKeyAuthenticationFailure(t, h.service, key, http.StatusServiceUnavailable, codes.Unavailable)
		})
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

func expectSessionCreation(h *serviceHarness) *models.Session {
	session := new(models.Session)
	h.repository.EXPECT().
		CreateSession(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, generated *models.Session) error {
			*session = *generated
			return nil
		})
	return session
}

type discardAuthEvents struct{}

func (discardAuthEvents) NewOutboxMessage(context.Context, string, *outboxDomain.Message) error {
	return nil
}

type sessionHarness struct {
	db      database.Database
	repo    *authRepository.Repository
	cache   *local.Wrapper
	service *auth.Service
	user    *models.User
}

func sessionOptions(extra ...auth.Option) []auth.Option {
	return append([]auth.Option{
		auth.WithJWTSecrets([]string{testJWTSecret}),
		auth.WithJWTIssuer(testJWTIssuer), auth.WithJWTAudience(testJWTAudience),
		auth.WithJWTAccessTokenTTL(testAccessTTL), auth.WithJWTRefreshTokenTTL(testRefreshTTL),
		auth.WithMinPasswordEntropyBits(60), auth.WithRateLimiterEnabled(false),
	}, extra...)
}

func newSessionHarness(t *testing.T, extra ...auth.Option) *sessionHarness {
	t.Helper()
	var db database.Database
	var err error
	engine, dialect := "sqlite", goose.DialectSQLite3
	// Optional DSNs must point to disposable test databases. Normal unit tests
	// use a separate SQLite database per test and require no external services.
	switch {
	case os.Getenv("GO42_AUTH_TEST_PGSQL_DSN") != "":
		engine, dialect = "pgsql", goose.DialectPostgres
		db, err = pgsql.Open(t.Context(), os.Getenv("GO42_AUTH_TEST_PGSQL_DSN"), "")
	case os.Getenv("GO42_AUTH_TEST_MYSQL_DSN") != "":
		engine, dialect = "mysql", goose.DialectMySQL
		db, err = mysql.Open(t.Context(), os.Getenv("GO42_AUTH_TEST_MYSQL_DSN"), "")
	default:
		db, err = sqlite.Open(filepath.Join(t.TempDir(), "auth.db"))
	}
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := db.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	sqlDB, err := db.Master().DB()
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(dialect, sqlDB, os.DirFS(filepath.Join("..", "..", "migrate", engine)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("migrate %s: %v", engine, err)
	}

	cache := local.New(local.WithCapacity(100))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := cache.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	repo := authRepository.New(database.NewBaseRepository(db), cache, time.Minute)
	user := newTestUser(t, domain.UserStatusActive)
	user.ID, user.UUID, user.CredentialVersion = 0, uuid.New(), 1
	user.Email = "session-" + user.UUID.String() + "@example.com"
	if err := repo.CreateUser(t.Context(), user); err != nil {
		t.Fatal(err)
	}
	if err := repo.AssignRoleToUser(t.Context(), user.ID, domain.RBACRoleUser); err != nil {
		t.Fatal(err)
	}
	return &sessionHarness{db: db, repo: repo, cache: cache, user: user,
		service: auth.NewService(repo, discardAuthEvents{}, cache, sessionOptions(extra...)...)}
}

func (h *sessionHarness) login(t *testing.T) *domain.Tokens {
	t.Helper()
	tokens, err := h.service.Login(t.Context(), h.user.Email, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	return tokens
}

func (h *sessionHarness) assertRevoked(t *testing.T, tokens *domain.Tokens) {
	t.Helper()
	if _, err := h.service.ValidateJWTToken(
		t.Context(),
		tokens.AccessToken,
		domain.JWTTokenPurposeAccess,
	); !errors.Is(
		err,
		domain.ErrInvalidToken,
	) {
		t.Errorf("access validation = %v, want revoked", err)
	}
	if _, err := h.service.Refresh(t.Context(), tokens.RefreshToken); !errors.Is(err, domain.ErrInvalidToken) {
		t.Errorf("refresh = %v, want revoked", err)
	}
}

type failingAuthOutboxRepository struct {
	*outboxRepository.Repository
	duplicateID uuid.UUID
	message     outboxModels.Message
	err         error
}

func (r *failingAuthOutboxRepository) NewOutboxMessage(ctx context.Context, msg *outboxModels.Message) error {
	// Force a real SQL insert failure inside the credential transaction on every
	// supported database. Clearing duplicateID restores normal outbox writes.
	if r.duplicateID != uuid.Nil {
		msg.ID = r.duplicateID
	}
	r.message = *msg
	r.err = r.Repository.NewOutboxMessage(ctx, msg)
	return r.err
}

func failAuthOutboxInserts(t *testing.T, h *sessionHarness) *failingAuthOutboxRepository {
	t.Helper()
	repo := outboxRepository.New(database.NewBaseRepository(h.db))
	existing := outboxModels.Message{
		ID: uuid.New(), AggregateID: h.user.ID, AggregateType: "test.existing",
		Topic: domain.TopicNameAuthEvents, Status: outboxModels.MessageStatusPending,
		MaxRetries: outboxDomain.MaxRetries,
	}
	if err := repo.NewOutboxMessage(t.Context(), &existing); err != nil {
		t.Fatal(err)
	}
	failing := &failingAuthOutboxRepository{Repository: repo, duplicateID: existing.ID}
	h.service = auth.NewService(h.repo, outbox.NewService(failing), h.cache, sessionOptions()...)
	return failing
}

func assertAuthOutboxCount(t *testing.T, h *sessionHarness, message outboxModels.Message, want int64) {
	t.Helper()
	var count int64
	err := h.db.Master().Model(&outboxModels.Message{}).
		Where("aggregate_id = ? AND aggregate_type = ?", message.AggregateID, message.AggregateType).
		Count(&count).Error
	if err != nil || count != want {
		t.Fatalf("outbox events = %d, want %d; error = %v", count, want, err)
	}
}

func TestService_SignUpRollsBackOnOutboxFailure(t *testing.T) {
	h := newSessionHarness(t)
	failing := failAuthOutboxInserts(t, h)
	email := "signup-" + uuid.NewString() + "@example.com"
	user, err := h.service.SignUp(t.Context(), email, testPassword)
	if failing.err == nil || !errors.Is(err, failing.err) {
		t.Errorf("signup error = %v, want original outbox insert error %v", err, failing.err)
	}
	if user != nil {
		t.Error("failed signup returned a user")
	}
	if _, err := h.repo.GetUserByEmail(t.Context(), email); !errors.Is(err, domain.ErrEntityNotFound) {
		t.Errorf("failed signup persisted a user: %v", err)
	}
	var roleCount int64
	if err := h.db.Master().Model(&models.UserRole{}).
		Where("user_id = ?", failing.message.AggregateID).Count(&roleCount).Error; err != nil || roleCount != 0 {
		t.Errorf("failed signup persisted role assignments: count = %d, error = %v", roleCount, err)
	}
	assertAuthOutboxCount(t, h, failing.message, 0)

	failing.duplicateID = uuid.Nil
	user, err = h.service.SignUp(t.Context(), email, testPassword)
	if err != nil || user == nil {
		t.Fatalf("retry signup after outbox recovery: %v", err)
	}
	stored, err := h.repo.GetUserByEmail(t.Context(), email)
	if err != nil || stored.ID != user.ID || stored.CredentialVersion != 1 ||
		len(stored.Roles) != 1 || stored.Roles[0].Name != domain.RBACRoleUser {
		t.Fatalf("successful signup did not persist its user and role: %v", err)
	}
	assertAuthOutboxCount(t, h, failing.message, 1)
}

func TestService_CredentialChangesRollBackOnOutboxFailure(t *testing.T) {
	for _, method := range []string{"admin", "self"} {
		for _, field := range []string{"email", "password"} {
			t.Run(method+"/"+field, func(t *testing.T) {
				h := newSessionHarness(t)
				initial := h.login(t)
				before := *h.user
				failing := failAuthOutboxInserts(t, h)
				email, password := "updated-"+h.user.Email, testPassword+"!new"
				data := domain.UpdateUserData{}
				if field == "email" {
					data.Email = &email
				} else {
					data.Password = &password
				}
				update := func() error {
					if method == "self" {
						return h.service.UpdateSelf(t.Context(), h.user.UUID.String(), &domain.UpdateSelfData{
							UpdateUserData: data, CurrentPassword: testPassword,
						})
					}
					return h.service.UpdateUser(t.Context(), h.user.UUID.String(), &data)
				}
				err := update()
				if failing.err == nil || !errors.Is(err, failing.err) {
					t.Errorf("credential update error = %v, want original outbox insert error %v", err, failing.err)
				}
				stored, err := h.repo.GetUserByID(t.Context(), before.ID)
				if err != nil {
					t.Fatal(err)
				}
				if stored.Email != before.Email || stored.Password != before.Password ||
					stored.CredentialVersion != before.CredentialVersion {
					t.Error("failed credential change persisted the email, password, or credential version")
				}
				for purpose, token := range map[domain.JWTTokenPurpose]string{
					domain.JWTTokenPurposeAccess: initial.AccessToken, domain.JWTTokenPurposeRefresh: initial.RefreshToken,
				} {
					if _, err := h.service.ValidateJWTToken(t.Context(), token, purpose); err != nil {
						t.Errorf("failed credential change invalidated the %s token: %v", purpose, err)
					}
				}
				assertAuthOutboxCount(t, h, failing.message, 0)

				failing.duplicateID = uuid.Nil
				if err := update(); err != nil {
					t.Fatalf("retry credential change after outbox recovery: %v", err)
				}
				stored, err = h.repo.GetUserByID(t.Context(), before.ID)
				if err != nil {
					t.Fatal(err)
				}
				if stored.CredentialVersion != before.CredentialVersion+1 ||
					(field == "email" && stored.Email != email) ||
					(field == "password" && bcrypt.CompareHashAndPassword([]byte(stored.Password.V), []byte(password)) != nil) {
					t.Error("successful credential change did not persist the credentials and credential version")
				}
				assertAuthOutboxCount(t, h, failing.message, 1)
				h.assertRevoked(t, initial)
			})
		}
	}
}

func TestRepository_DeleteUser(t *testing.T) {
	h := newSessionHarness(t)
	if err := h.repo.DeleteUser(t.Context(), &models.User{ID: -1}); !errors.Is(err, domain.ErrEntityNotFound) {
		t.Fatalf("delete missing user = %v, want not found", err)
	}

	user := *h.user
	if err := h.repo.DeleteUser(t.Context(), &user); err != nil {
		t.Fatalf("delete existing user: %v", err)
	}
	var stored models.User
	if err := h.db.Master().Unscoped().First(&stored, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !stored.DeletedAt.Valid {
		t.Fatal("successful delete did not persist the deletion timestamp")
	}
	if err := h.repo.DeleteUser(t.Context(), &user); !errors.Is(err, domain.ErrEntityNotFound) {
		t.Fatalf("delete already deleted user = %v, want not found", err)
	}
}

func TestRepository_DeleteUserRejectsFailedWrites(t *testing.T) {
	h := newSessionHarness(t)
	if err := h.repo.AssignRoleToUser(t.Context(), h.user.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	tokens := h.login(t)
	rejectAuthUpdates(t, h, "auth_users")

	user := *h.user
	assertAuthWriteFailure(t, h.repo.DeleteUser(t.Context(), &user))

	e := newTestEcho()
	httpAdapter.New(h.service).Register(e.Group("/api/v1"))
	client := newCredentialGRPCClient(t, h.service)
	for _, test := range []struct {
		name       string
		userUUID   string
		httpStatus int
		grpcStatus codes.Code
	}{
		{"failed write", user.UUID.String(), http.StatusInternalServerError, codes.Internal},
		{"missing user", uuid.NewString(), http.StatusNotFound, codes.NotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := sessionHTTPRequest(t, e, http.MethodDelete,
				"/api/v1/users/"+test.userUUID, tokens.AccessToken, nil)
			if response.Code != test.httpStatus {
				t.Fatalf("HTTP delete = %d, want %d: %s", response.Code, test.httpStatus, response.Body.String())
			}
			if strings.Contains(response.Body.String(), authWriteFailureMessage) {
				t.Fatal("HTTP response exposed the database error")
			}
			_, err := client.DeleteUser(t.Context(), &pb.DeleteUserRequest{Uuid: test.userUUID})
			if status.Code(err) != test.grpcStatus {
				t.Fatalf("gRPC delete = %v, want %s", err, test.grpcStatus)
			}
			if strings.Contains(status.Convert(err).Message(), authWriteFailureMessage) {
				t.Fatal("gRPC response exposed the database error")
			}
		})
	}
	var stored models.User
	if err := h.db.Master().Unscoped().First(&stored, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.DeletedAt.Valid {
		t.Fatal("failed delete changed the stored user")
	}
}

func TestRepository_UpdateTokenLastUsed(t *testing.T) {
	h := newSessionHarness(t)
	token := &models.Token{
		UUID: uuid.New(), UserID: h.user.ID, Token: sha256Hex(uuid.NewString()), Name: "last used test",
	}
	if err := h.db.Master().Create(token).Error; err != nil {
		t.Fatal(err)
	}
	when := time.Now().UTC().Truncate(time.Second)
	if err := h.repo.UpdateTokenLastUsed(t.Context(), -1, when); !errors.Is(err, domain.ErrEntityNotFound) {
		t.Fatalf("update missing token = %v, want not found", err)
	}
	if err := h.repo.UpdateTokenLastUsed(t.Context(), token.ID, when); err != nil {
		t.Fatalf("update existing token: %v", err)
	}
	var stored models.Token
	if err := h.db.Master().First(&stored, token.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !stored.LastUsedAt.Valid || !stored.LastUsedAt.V.Equal(when) {
		t.Fatalf("stored last-used time = %v, want %s", stored.LastUsedAt, when)
	}
}

func TestRepository_UpdateTokenLastUsedRejectsFailedWrites(t *testing.T) {
	h := newSessionHarness(t)
	token := &models.Token{
		UUID: uuid.New(), UserID: h.user.ID, Token: sha256Hex(uuid.NewString()), Name: "last used test",
	}
	if err := h.db.Master().Create(token).Error; err != nil {
		t.Fatal(err)
	}
	rejectAuthUpdates(t, h, "auth_api_tokens")
	assertAuthWriteFailure(t, h.repo.UpdateTokenLastUsed(t.Context(), token.ID, time.Now().UTC()))

	var stored models.Token
	if err := h.db.Master().First(&stored, token.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.LastUsedAt.Valid {
		t.Fatalf("failed update changed the token's last-used time: %v", stored.LastUsedAt)
	}
}

const authWriteFailureMessage = "auth_test_rejected_write"

func rejectAuthUpdates(t *testing.T, h *sessionHarness, table string) {
	t.Helper()
	sqlDB, err := h.db.Master().DB()
	if err != nil {
		t.Fatal(err)
	}
	name := authWriteFailureMessage + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	var setup, cleanup string
	switch dialect := h.db.Master().Name(); dialect {
	case "sqlite":
		setup = fmt.Sprintf(
			"CREATE TRIGGER %s BEFORE UPDATE ON %s BEGIN SELECT RAISE(ABORT, '%s'); END",
			name, table, authWriteFailureMessage,
		)
		cleanup = "DROP TRIGGER " + name
	case "postgres", "mysql":
		// Restrict only this fixture's rows so existing test data stays valid.
		condition := fmt.Sprintf("uuid <> '%s' OR deleted_at IS NULL", h.user.UUID.String())
		if table == "auth_api_tokens" {
			condition = fmt.Sprintf("user_id <> %d OR last_used_at IS NULL", h.user.ID)
		}
		setup = fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s CHECK (%s)", table, name, condition)
		kind := "CONSTRAINT"
		if dialect == "mysql" {
			kind = "CHECK"
		}
		cleanup = fmt.Sprintf("ALTER TABLE %s DROP %s %s", table, kind, name)
	default:
		t.Fatalf("unsupported test database: %s", dialect)
	}
	if _, err := sqlDB.ExecContext(t.Context(), setup); err != nil {
		t.Fatalf("reject database updates: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := sqlDB.ExecContext(ctx, cleanup); err != nil {
			t.Errorf("remove write rejection: %v", err)
		}
	})
}

func assertAuthWriteFailure(t *testing.T, err error) {
	t.Helper()
	if err == nil || errors.Is(err, domain.ErrEntityNotFound) {
		t.Fatalf("write error = %v, want the underlying database failure", err)
	}
	if cause := errors.Unwrap(err); cause == nil || !strings.Contains(cause.Error(), authWriteFailureMessage) {
		t.Fatalf("write error does not wrap the database cause: %v", err)
	}
}

func TestSessions_RotationAndReplayRevokeTheFamily(t *testing.T) {
	h := newSessionHarness(t)
	initial := h.login(t)
	unrelated := h.login(t)
	next, err := h.service.Refresh(t.Context(), initial.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if next.RefreshToken == initial.RefreshToken {
		t.Fatal("refresh token was not rotated")
	}
	if _, err := h.service.ValidateJWTToken(t.Context(), next.AccessToken, domain.JWTTokenPurposeAccess); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Refresh(t.Context(), initial.RefreshToken); !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("replay = %v", err)
	}
	h.assertRevoked(t, next)
	if _, err := h.service.Refresh(t.Context(), unrelated.RefreshToken); err != nil {
		t.Errorf("unrelated session revoked: %v", err)
	}
}

func TestSessions_ConcurrentRefreshHasOneWinnerAndRevokesOnReuse(t *testing.T) {
	h := newSessionHarness(t)
	initial := h.login(t)
	const requests = 8
	type result struct {
		tokens *domain.Tokens
		err    error
	}
	results := make(chan result, requests)
	start := make(chan struct{})
	for range requests {
		go func() {
			<-start
			tokens, err := h.service.Refresh(t.Context(), initial.RefreshToken)
			results <- result{tokens, err}
		}()
	}
	close(start)
	var winner *domain.Tokens
	winners := 0
	for range requests {
		got := <-results
		if got.err == nil {
			winners++
			winner = got.tokens
		} else if !errors.Is(got.err, domain.ErrInvalidToken) {
			t.Errorf("refresh: %v", got.err)
		}
	}
	if winners != 1 {
		t.Fatalf("successful refreshes = %d, want 1", winners)
	}
	h.assertRevoked(t, winner)
}

func TestSessions_LogoutIsIdempotentAndRevokesAcrossInstances(t *testing.T) {
	h := newSessionHarness(t)
	initial := h.login(t)
	next, err := h.service.Refresh(t.Context(), initial.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := h.service.Logout(t.Context(), initial.RefreshToken); err != nil {
			t.Fatal(err)
		}
	}
	h.assertRevoked(t, next)
	other := auth.NewService(h.repo, discardAuthEvents{}, nil, sessionOptions()...)
	if _, err := other.ValidateJWTToken(
		t.Context(),
		initial.AccessToken,
		domain.JWTTokenPurposeAccess,
	); !errors.Is(
		err,
		domain.ErrInvalidToken,
	) {
		t.Errorf("separate instance with empty cache accepted revoked session: %v", err)
	}
}

func TestSessions_ConcurrentLogoutAndRefreshLeaveNoUsableSession(t *testing.T) {
	h := newSessionHarness(t)
	initial := h.login(t)
	start := make(chan struct{})
	var wg sync.WaitGroup
	var refreshed *domain.Tokens
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		var err error
		refreshed, err = h.service.Refresh(t.Context(), initial.RefreshToken)
		if err != nil && !errors.Is(err, domain.ErrInvalidToken) {
			t.Error(err)
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		if err := h.service.Logout(t.Context(), initial.RefreshToken); err != nil {
			t.Error(err)
		}
	}()
	close(start)
	wg.Wait()
	h.assertRevoked(t, initial)
	if refreshed != nil {
		h.assertRevoked(t, refreshed)
	}
}

func TestSessions_CredentialChangesRequireProofAndInvalidateAllSessions(t *testing.T) {
	for _, field := range []string{"password", "email"} {
		t.Run(field, func(t *testing.T) {
			h := newSessionHarness(t)
			first, second := h.login(t), h.login(t)
			data := &domain.UpdateSelfData{}
			value := "N3w!correctPassword#"
			if field == "password" {
				data.Password = &value
			} else {
				value = "updated-" + h.user.Email
				data.Email = &value
			}
			for _, proof := range []string{"", "wrong password"} {
				data.CurrentPassword = proof
				if err := h.service.UpdateSelf(
					t.Context(),
					h.user.UUID.String(),
					data,
				); !errors.Is(
					err,
					domain.ErrInvalidCredentials,
				) {
					t.Errorf("proof %q: %v", proof, err)
				}
			}
			data.CurrentPassword = testPassword
			if err := h.service.UpdateSelf(t.Context(), h.user.UUID.String(), data); err != nil {
				t.Fatal(err)
			}
			h.assertRevoked(t, first)
			h.assertRevoked(t, second)
			email, password := h.user.Email, testPassword
			if field == "password" {
				password = value
			} else {
				email = value
			}
			if _, err := h.service.Login(t.Context(), email, password); err != nil {
				t.Fatalf("login with updated credentials: %v", err)
			}
		})
	}
}

type staleLoginRepository struct {
	*authRepository.Repository
	user *models.User
}

func (r staleLoginRepository) GetUserByEmail(context.Context, string) (*models.User, error) {
	return r.user, nil
}

func TestSessions_AdminResetRejectsConcurrentLoginWithOldCredentials(t *testing.T) {
	h := newSessionHarness(t)
	initial := h.login(t)
	stale, err := h.repo.GetUserByEmail(t.Context(), h.user.Email)
	if err != nil {
		t.Fatal(err)
	}
	password := "N3w!correctPassword#"
	if err := h.service.UpdateUser(
		t.Context(),
		h.user.UUID.String(),
		&domain.UpdateUserData{Password: &password},
	); err != nil {
		t.Fatal(err)
	}
	h.assertRevoked(t, initial)
	// Complete a login whose password lookup happened before the reset commit.
	concurrent := auth.NewService(
		staleLoginRepository{h.repo, stale},
		discardAuthEvents{},
		h.cache,
		sessionOptions()...)
	tokens, err := concurrent.Login(t.Context(), stale.Email, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	h.assertRevoked(t, tokens)
}

func TestSessions_InactiveDeletedExpiredAndMissingSessionsAreRejected(t *testing.T) {
	for _, state := range []string{"inactive_user", "deleted_user", "expired_session", "missing_session"} {
		t.Run(state, func(t *testing.T) {
			h := newSessionHarness(t)
			tokens := h.login(t)
			claims, err := h.service.ValidateJWTToken(t.Context(), tokens.AccessToken, domain.JWTTokenPurposeAccess)
			if err != nil {
				t.Fatal(err)
			}
			switch state {
			case "inactive_user":
				err = h.db.Master().Model(h.user).Update("status", domain.UserStatusInactive).Error
			case "deleted_user":
				err = h.repo.DeleteUser(t.Context(), h.user)
			case "expired_session":
				err = h.db.Master().
					Model(&models.Session{}).
					Where("id = ?", claims.SessionID).
					Update("expires_at", time.Now().UTC().Add(-time.Hour)).
					Error
			case "missing_session":
				err = h.db.Master().Where("id = ?", claims.SessionID).Delete(&models.Session{}).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			h.assertRevoked(t, tokens)
		})
	}
}

func tokenEncodingVariants(t *testing.T, token string) []string {
	t.Helper()
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alphabet, token[len(token)-1])
	if last < 0 || last%4 != 0 {
		t.Fatal("expected canonical HS256 token")
	}
	return []string{token[:len(token)-1] + string(alphabet[last^1]), token + "\n", token + "\r\n", token + "="}
}

func TestSessions_RejectNoncanonicalTokensAtHTTPBoundary(t *testing.T) {
	h := newSessionHarness(t)
	tokens := h.login(t)
	e := newTestEcho()
	httpAdapter.New(h.service).Register(e.Group("/api/v1"))
	for _, token := range tokenEncodingVariants(t, tokens.RefreshToken) {
		response := sessionHTTPRequest(
			t,
			e,
			http.MethodPost,
			"/api/v1/auth/refresh",
			"",
			map[string]string{"token": token},
		)
		if response.Code != http.StatusUnauthorized {
			t.Errorf("noncanonical refresh status = %d", response.Code)
		}
	}
	if err := h.service.Logout(t.Context(), tokens.RefreshToken); err != nil {
		t.Fatal(err)
	}
	for _, token := range tokenEncodingVariants(t, tokens.AccessToken) {
		if strings.ContainsAny(token, "\r\n") {
			continue
		} // HTTP headers prohibit line breaks.
		response := sessionHTTPRequest(t, e, http.MethodGet, "/api/v1/users/me", token, nil)
		if response.Code != http.StatusUnauthorized {
			t.Errorf("noncanonical access status = %d", response.Code)
		}
	}
}

func TestSessions_HTTPLogoutAcceptsExpiredOrOmittedAccessToken(t *testing.T) {
	for _, includeAccess := range []bool{false, true} {
		t.Run(map[bool]string{false: "omitted", true: "expired"}[includeAccess], func(t *testing.T) {
			h := newSessionHarness(t, auth.WithJWTAccessTokenTTL(time.Nanosecond))
			tokens := h.login(t)
			e := newTestEcho()
			httpAdapter.New(h.service).Register(e.Group("/api/v1"))
			body := map[string]string{"refresh_token": tokens.RefreshToken}
			if includeAccess {
				body["access_token"] = tokens.AccessToken
			}
			for range 2 {
				response := sessionHTTPRequest(t, e, http.MethodPost, "/api/v1/auth/logout", "", body)
				if response.Code != http.StatusOK {
					t.Fatalf("logout status = %d", response.Code)
				}
			}
			h.assertRevoked(t, tokens)
		})
	}
}

func TestSessions_HTTPUpdateRequiresCurrentPassword(t *testing.T) {
	h := newSessionHarness(t)
	tokens := h.login(t)
	e := newTestEcho()
	httpAdapter.New(h.service).Register(e.Group("/api/v1"))
	body := map[string]string{"password": "N3w!correctPassword#"}
	if response := sessionHTTPRequest(
		t,
		e,
		http.MethodPut,
		"/api/v1/users/me",
		tokens.AccessToken,
		body,
	); response.Code != http.StatusBadRequest {
		t.Fatalf("missing proof status = %d", response.Code)
	}
	body["current_password"] = testPassword
	if response := sessionHTTPRequest(
		t,
		e,
		http.MethodPut,
		"/api/v1/users/me",
		tokens.AccessToken,
		body,
	); response.Code != http.StatusOK {
		t.Fatalf("valid proof status = %d: %s", response.Code, response.Body.String())
	}
	h.assertRevoked(t, tokens)
}

func TestSessions_EnforceTokenPurposeAndSessionOwner(t *testing.T) {
	h := newSessionHarness(t)
	tokens := h.login(t)
	if _, err := h.service.Refresh(t.Context(), tokens.AccessToken); !errors.Is(err, domain.ErrInvalidToken) {
		t.Errorf("access used as refresh: %v", err)
	}
	if err := h.service.Logout(t.Context(), tokens.AccessToken); !errors.Is(err, domain.ErrInvalidToken) {
		t.Errorf("access used for logout: %v", err)
	}
	claims, err := h.service.ValidateJWTToken(t.Context(), tokens.RefreshToken, domain.JWTTokenPurposeRefresh)
	if err != nil {
		t.Fatal(err)
	}
	claims.Subject = uuid.NewString()
	wrongOwner := signTestJWTWithClaims(t, jwt.SigningMethodHS256, testJWTSecret, *claims)
	if _, err := h.service.Refresh(t.Context(), wrongOwner); !errors.Is(err, domain.ErrInvalidToken) {
		t.Errorf("wrong session owner: %v", err)
	}
	if _, err := h.service.Refresh(t.Context(), tokens.RefreshToken); err != nil {
		t.Errorf("wrong owner revoked the real session: %v", err)
	}
}

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

func TestSessions_FailedWritesDoNotIssueTokensOrReportLogoutSuccess(t *testing.T) {
	for _, operation := range []string{"create", "rotate", "revoke reuse", "logout"} {
		t.Run(operation, func(t *testing.T) {
			h := newServiceHarness(t)
			storageErr := errors.New("database write failed")
			token := signTestJWT(
				t,
				testJWTSecret,
				domain.JWTTokenPurposeRefresh,
				"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
				time.Now().Add(time.Hour),
			)
			var tokens *domain.Tokens
			var err error
			switch operation {
			case "create":
				user := newTestUser(t, domain.UserStatusActive)
				h.repository.EXPECT().GetUserByEmail(gomock.Any(), testUserEmail).Return(user, nil)
				h.repository.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(storageErr)
				tokens, err = h.service.Login(t.Context(), testUserEmail, testPassword)
			case "rotate":
				h.repository.EXPECT().
					RotateSession(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Return(false, storageErr)
				tokens, err = h.service.Refresh(t.Context(), token)
			case "revoke reuse":
				h.repository.EXPECT().
					RotateSession(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Return(false, nil)
				h.repository.EXPECT().RevokeSession(gomock.Any(), gomock.Any(), gomock.Any()).Return(storageErr)
				tokens, err = h.service.Refresh(t.Context(), token)
			case "logout":
				h.repository.EXPECT().RevokeSession(gomock.Any(), gomock.Any(), gomock.Any()).Return(storageErr)
				err = h.service.Logout(t.Context(), token)
			}
			assertErrorIs(t, err, domain.ErrAuthenticationUnavailable)
			assertErrorIs(t, err, storageErr)
			if tokens != nil {
				t.Fatal("tokens returned despite a failed session write")
			}
		})
	}
}

func TestSessions_HTTPDatabaseOutageFailsClosed(t *testing.T) {
	h := newSessionHarness(t)
	tokens := h.login(t)
	e := newTestEcho()
	httpAdapter.New(h.service).Register(e.Group("/api/v1"))
	sqlDB, err := h.db.Master().DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method, path, bearer string
		body                 any
	}{
		{http.MethodPost, "/api/v1/auth/login", "", map[string]string{"email": h.user.Email, "password": "TestPassword123!"}},
		{http.MethodPost, "/api/v1/auth/refresh", "", map[string]string{"token": tokens.RefreshToken}},
		{http.MethodPost, "/api/v1/auth/logout", "", map[string]string{"refresh_token": tokens.RefreshToken}},
		{http.MethodGet, "/api/v1/users/me", tokens.AccessToken, nil},
	} {
		t.Run(test.path, func(t *testing.T) {
			response := sessionHTTPRequest(t, e, test.method, test.path, test.bearer, test.body)
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("database outage status=%d: %s", response.Code, response.Body.String())
			}
		})
	}
}

type primaryOnlyDatabase struct{ database.Database }

func (primaryOnlyDatabase) Slave() *gorm.DB {
	panic("authentication must not use a potentially stale read replica")
}

func TestSessions_AuthenticationReadsThePrimaryDatabase(t *testing.T) {
	h := newSessionHarness(t)
	repo := authRepository.New(database.NewBaseRepository(primaryOnlyDatabase{h.db}), h.cache, time.Minute)
	service := auth.NewService(repo, discardAuthEvents{}, h.cache, sessionOptions()...)
	tokens, err := service.Login(t.Context(), h.user.Email, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	e := newTestEcho()
	httpAdapter.New(service).Register(e.Group("/api/v1"))
	response := sessionHTTPRequest(t, e, http.MethodGet, "/api/v1/users/me", tokens.AccessToken, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("user authentication status=%d: %s", response.Code, response.Body.String())
	}
	next, err := service.Refresh(t.Context(), tokens.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Logout(t.Context(), next.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ValidateJWTToken(
		t.Context(),
		next.AccessToken,
		domain.JWTTokenPurposeAccess,
	); !errors.Is(
		err,
		domain.ErrInvalidToken,
	) {
		t.Fatalf("revocation was not visible immediately: %v", err)
	}
}

func TestSessions_InvalidJWTConfigurationFailsClosed(t *testing.T) {
	for _, option := range []auth.Option{
		auth.WithJWTSecrets([]string{""}), auth.WithJWTIssuer(""), auth.WithJWTAudience(nil),
		auth.WithJWTAudience([]string{""}), auth.WithJWTAccessTokenTTL(0), auth.WithJWTRefreshTokenTTL(0),
	} {
		h := newServiceHarness(t, option)
		token := signTestJWT(
			t,
			testJWTSecret,
			domain.JWTTokenPurposeRefresh,
			"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			time.Now().Add(time.Hour),
		)
		_, err := h.service.Refresh(t.Context(), token)
		assertErrorIs(t, err, domain.ErrAuthenticationUnavailable)
	}
	service := auth.NewService(nil, nil, nil)
	token := signTestJWT(
		t,
		testJWTSecret,
		domain.JWTTokenPurposeRefresh,
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		time.Now().Add(time.Hour),
	)
	_, err := service.Refresh(t.Context(), token)
	assertErrorIs(t, err, domain.ErrAuthenticationUnavailable)
}

func TestSessions_RepositoryNormalizesExpiryTimezones(t *testing.T) {
	h := newSessionHarness(t)
	expired := time.Now().Add(-time.Minute).In(time.FixedZone("test", 14*60*60))
	session := &models.Session{
		ID: uuid.New(), UserID: h.user.ID, CredentialVersion: h.user.CredentialVersion,
		RefreshTokenID: uuid.New(), ExpiresAt: expired,
	}
	if err := h.repo.CreateSession(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	_, err := h.repo.GetActiveSession(t.Context(), session.ID.String(), h.user.UUID.String())
	assertErrorIs(t, err, domain.ErrInvalidToken)
	tokens := h.login(t)
	claims, err := h.service.ValidateJWTToken(t.Context(), tokens.RefreshToken, domain.JWTTokenPurposeRefresh)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := h.repo.RotateSession(
		t.Context(),
		claims.SessionID,
		claims.Subject,
		claims.ID,
		uuid.NewString(),
		expired,
	)
	if err != nil || !rotated {
		t.Fatalf("rotate session: rotated=%v, error=%v", rotated, err)
	}
	_, err = h.repo.GetActiveSession(t.Context(), claims.SessionID, claims.Subject)
	assertErrorIs(t, err, domain.ErrInvalidToken)
}

func TestSessions_CleanupRetainsUnexpiredSessions(t *testing.T) {
	h := newSessionHarness(t)
	active, expired, revoked := h.login(t), h.login(t), h.login(t)
	expiredClaims, err := h.service.ValidateJWTToken(t.Context(), expired.RefreshToken, domain.JWTTokenPurposeRefresh)
	if err != nil {
		t.Fatal(err)
	}
	revokedClaims, err := h.service.ValidateJWTToken(t.Context(), revoked.RefreshToken, domain.JWTTokenPurposeRefresh)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.service.Logout(t.Context(), revoked.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if err := h.db.Master().Model(&models.Session{}).Where("id = ?", expiredClaims.SessionID).
		Update("expires_at", time.Now().UTC().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	deleted, err := h.repo.DeleteExpiredSessions(t.Context())
	if err != nil || deleted < 1 {
		t.Fatalf("cleanup deleted=%d, error=%v", deleted, err)
	}
	for _, test := range []struct {
		id   string
		want int64
	}{{expiredClaims.SessionID, 0}, {revokedClaims.SessionID, 1}} {
		var count int64
		if err := h.db.Master().
			Model(&models.Session{}).
			Where("id = ?", test.id).
			Count(&count).
			Error; err != nil ||
			count != test.want {
			t.Fatalf("retained session count=%d, want=%d, error=%v", count, test.want, err)
		}
	}
	if _, err := h.service.ValidateJWTToken(t.Context(), active.AccessToken, domain.JWTTokenPurposeAccess); err != nil {
		t.Fatalf("cleanup removed an active session: %v", err)
	}
}

func TestAuthRateLimits_NormalizesKnownAndUnknownAccounts(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(strconv.FormatBool(known), func(t *testing.T) {
			h := newSessionHarness(t,
				auth.WithRateLimiterEnabled(true),
				auth.WithLoginAccountRequests(3),
				auth.WithLoginWindow(time.Hour),
			)
			email := h.user.Email
			if !known {
				email = "unknown-" + email
			}
			for _, spelling := range []string{email, strings.ToUpper(email), " " + email + " "} {
				_, err := h.service.Login(t.Context(), spelling, "wrong password")
				assertErrorIs(t, err, domain.ErrInvalidCredentials)
			}
			_, err := h.service.Login(t.Context(), email, testPassword)
			assertErrorIs(t, err, domain.ErrRateLimited)
			_, err = h.service.Login(t.Context(), "different-"+email, "wrong password")
			assertErrorIs(t, err, domain.ErrInvalidCredentials)
		})
	}
}

func TestAuthRateLimits_ReauthenticationSharesAccountBudget(t *testing.T) {
	h := newSessionHarness(t,
		auth.WithRateLimiterEnabled(true),
		auth.WithLoginAccountRequests(2),
		auth.WithLoginWindow(time.Hour),
	)
	tokens := h.login(t)
	newEmail := "updated-" + h.user.Email
	data := &domain.UpdateSelfData{
		UpdateUserData: domain.UpdateUserData{Email: &newEmail}, CurrentPassword: "wrong password",
	}
	err := h.service.UpdateSelf(t.Context(), h.user.UUID.String(), data)
	assertErrorIs(t, err, domain.ErrInvalidCredentials)
	data.CurrentPassword = testPassword
	err = h.service.UpdateSelf(t.Context(), h.user.UUID.String(), data)
	assertErrorIs(t, err, domain.ErrRateLimited)
	if _, err := h.service.ValidateJWTToken(t.Context(), tokens.AccessToken, domain.JWTTokenPurposeAccess); err != nil {
		t.Fatalf("rejected credential changes revoked the session: %v", err)
	}
	user, err := h.repo.GetUserByUUID(t.Context(), h.user.UUID.String())
	if err != nil || user.Email != h.user.Email {
		t.Fatalf("rejected credential change altered the account: user=%+v, error=%v", user, err)
	}
}

func TestAuthRateLimits_DefaultIPLimitsRunBeforeRequestValidation(t *testing.T) {
	h := newSessionHarness(t, auth.WithRateLimiterEnabled(true))
	e := newTestEcho()
	httpAdapter.New(h.service).Register(e.Group("/api/v1"))
	for _, test := range []struct {
		path  string
		burst int
	}{{"/api/v1/auth/login", 20}, {"/api/v1/auth/signup", 5}} {
		t.Run(test.path, func(t *testing.T) {
			for range test.burst {
				response := sessionHTTPRequest(t, e, http.MethodPost, test.path, "", map[string]string{})
				if response.Code != http.StatusBadRequest {
					t.Fatalf("within IP budget: status=%d, body=%s", response.Code, response.Body.String())
				}
			}
			response := sessionHTTPRequest(t, e, http.MethodPost, test.path, "", map[string]string{})
			if response.Code != http.StatusTooManyRequests {
				t.Fatalf("exceeded IP budget: status=%d", response.Code)
			}
		})
	}
	if err := h.service.CheckIPLimit(t.Context(), domain.AuthenticationActionLogin, "192.0.2.2"); err != nil {
		t.Fatalf("different IP shares an exhausted bucket: %v", err)
	}
}

func TestAuthRateLimits_RefreshBudgetFollowsSessionAcrossRotation(t *testing.T) {
	h := newSessionHarness(t,
		auth.WithRateLimiterEnabled(true),
		auth.WithRefreshSessionRequests(2),
		auth.WithRefreshWindow(time.Hour),
	)
	tokens, unrelated := h.login(t), h.login(t)
	for range 2 {
		var err error
		tokens, err = h.service.Refresh(t.Context(), tokens.RefreshToken)
		if err != nil {
			t.Fatal(err)
		}
	}
	e := newTestEcho()
	httpAdapter.New(h.service).Register(e.Group("/api/v1"))
	response := sessionHTTPRequest(
		t,
		e,
		http.MethodPost,
		"/api/v1/auth/refresh",
		"",
		map[string]string{"token": tokens.RefreshToken},
	)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("refresh over budget: status=%d", response.Code)
	}
	if _, err := h.service.ValidateJWTToken(
		t.Context(),
		tokens.RefreshToken,
		domain.JWTTokenPurposeRefresh,
	); err != nil {
		t.Fatalf("throttling consumed or revoked the current refresh token: %v", err)
	}
	if _, err := h.service.Refresh(t.Context(), unrelated.RefreshToken); err != nil {
		t.Fatalf("different session shares exhausted refresh budget: %v", err)
	}
	if err := h.service.Logout(t.Context(), tokens.RefreshToken); err != nil {
		t.Fatalf("rate limit prevented logout: %v", err)
	}
}

func TestAuthRateLimits_BackendErrorsFailClosed(t *testing.T) {
	backendErr := errors.New("limiter unavailable")
	for _, action := range []string{"login IP", "signup IP", "account", "refresh"} {
		t.Run(action, func(t *testing.T) {
			h := newServiceHarness(t, auth.WithRateLimiterEnabled(true))
			h.cache.EXPECT().
				AllowRateLimit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				Return(false, backendErr)
			var err error
			switch action {
			case "login IP":
				err = h.service.CheckIPLimit(t.Context(), domain.AuthenticationActionLogin, "192.0.2.1")
			case "signup IP":
				err = h.service.CheckIPLimit(t.Context(), domain.AuthenticationActionSignup, "192.0.2.1")
			case "account":
				_, err = h.service.Login(t.Context(), testUserEmail, testPassword)
			case "refresh":
				token := signTestJWT(
					t,
					testJWTSecret,
					domain.JWTTokenPurposeRefresh,
					"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
					time.Now().Add(time.Hour),
				)
				_, err = h.service.Refresh(t.Context(), token)
			}
			assertErrorIs(t, err, domain.ErrAuthenticationUnavailable)
			assertErrorIs(t, err, backendErr)
		})
	}
}

func TestAuthRateLimits_HTTPFailureAndDenial(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
	}{
		{name: "backend unavailable", err: errors.New("cache offline"), status: http.StatusServiceUnavailable},
		{name: "budget exhausted", status: http.StatusTooManyRequests},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newServiceHarness(t, auth.WithRateLimiterEnabled(true))
			h.cache.EXPECT().
				AllowRateLimit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				Return(false, test.err)
			e := newTestEcho()
			httpAdapter.New(h.service).Register(e.Group("/api/v1"))
			response := sessionHTTPRequest(t, e, http.MethodPost, "/api/v1/auth/login", "", nil)
			if response.Code != test.status || response.Header().Get("Retry-After") != "" {
				t.Fatalf("status=%d, Retry-After=%q", response.Code, response.Header().Get("Retry-After"))
			}
		})
	}
}

func TestAuthRateLimits_MissingConfigurationFailsClosed(t *testing.T) {
	service := auth.NewService(nil, nil, nil)
	err := service.CheckIPLimit(t.Context(), domain.AuthenticationActionLogin, "192.0.2.1")
	assertErrorIs(t, err, domain.ErrAuthenticationUnavailable)
	h := newServiceHarness(t, auth.WithRateLimiterEnabled(true), auth.WithLoginIPRequests(0))
	err = h.service.CheckIPLimit(t.Context(), domain.AuthenticationActionLogin, "192.0.2.1")
	assertErrorIs(t, err, domain.ErrAuthenticationUnavailable)
}

func TestAuthHTTPClientsDecodeLoginErrors(t *testing.T) {
	const password = "TestPassword123!"
	for _, test := range []struct {
		name      string
		email     string
		limited   bool
		configure func(*serviceHarness)
		status    int
	}{
		{
			name: "invalid request", email: "not-an-email", status: http.StatusBadRequest,
		},
		{
			name: "invalid credentials", email: testUserEmail, status: http.StatusBadRequest,
			configure: func(h *serviceHarness) {
				h.repository.EXPECT().GetUserByEmail(gomock.Any(), testUserEmail).
					Return(nil, domain.ErrEntityNotFound).Times(2)
			},
		},
		{
			name: "auth rate limited", email: testUserEmail, limited: true, status: http.StatusTooManyRequests,
			configure: func(h *serviceHarness) {
				h.cache.EXPECT().
					AllowRateLimit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Return(false, nil).Times(2)
			},
		},
		{
			name: "auth limiter unavailable", email: testUserEmail, limited: true, status: http.StatusServiceUnavailable,
			configure: func(h *serviceHarness) {
				h.cache.EXPECT().
					AllowRateLimit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Return(false, errors.New("cache offline")).Times(2)
			},
		},
		{
			name: "database unavailable", email: testUserEmail, status: http.StatusServiceUnavailable,
			configure: func(h *serviceHarness) {
				h.repository.EXPECT().GetUserByEmail(gomock.Any(), testUserEmail).
					Return(nil, errors.New("database offline")).Times(2)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newServiceHarness(t, auth.WithRateLimiterEnabled(test.limited))
			if test.configure != nil {
				test.configure(h)
			}
			e := newTestEcho()
			httpAdapter.New(h.service).Register(e.Group("/api/v1"))
			server := httptest.NewServer(e)
			t.Cleanup(server.Close)

			t.Run("ogen", func(t *testing.T) {
				client, err := ogen.NewClient(server.URL+"/api/v1", nil)
				if err != nil {
					t.Fatal(err)
				}
				response, err := client.Login(t.Context(), &ogen.LoginRequest{Email: test.email, Password: password})
				if err != nil {
					t.Fatalf("decode login response: %v", err)
				}
				var problem *ogen.Error
				var status int
				switch response := response.(type) {
				case *ogen.LoginBadRequest:
					problem, status = (*ogen.Error)(response), http.StatusBadRequest
				case *ogen.LoginTooManyRequests:
					problem, status = (*ogen.Error)(response), http.StatusTooManyRequests
				case *ogen.LoginServiceUnavailable:
					problem, status = (*ogen.Error)(response), http.StatusServiceUnavailable
				default:
					t.Fatalf("unexpected login response: %T", response)
				}
				if status != test.status || int(problem.Status) != test.status ||
					problem.Title != http.StatusText(test.status) || problem.Type != "/api/v1/auth/login" {
					t.Fatalf(
						"login response = %+v (HTTP %d), want problem details for HTTP %d",
						problem,
						status,
						test.status,
					)
				}
			})

			t.Run("oapi-codegen", func(t *testing.T) {
				client, err := oapi.NewClientWithResponses(server.URL + "/api/v1")
				if err != nil {
					t.Fatal(err)
				}
				response, err := client.LoginWithResponse(t.Context(), oapi.LoginJSONRequestBody{
					Email: test.email, Password: password,
				})
				if err != nil {
					t.Fatalf("decode login response: %v", err)
				}
				var problem *oapi.Error
				switch test.status {
				case http.StatusBadRequest:
					problem = response.ApplicationproblemJSON400
				case http.StatusTooManyRequests:
					problem = response.ApplicationproblemJSON429
				case http.StatusServiceUnavailable:
					problem = response.ApplicationproblemJSON503
				}
				if response.StatusCode() != test.status {
					t.Fatalf("login status = %d, want %d", response.StatusCode(), test.status)
				}
				if response.ContentType() != httpAPI.MIMEApplicationProblemJSON {
					t.Fatalf("login content type = %q, want problem+json", response.ContentType())
				}
				if problem == nil || int(problem.Status) != test.status ||
					problem.Title != http.StatusText(test.status) || problem.Type != "/api/v1/auth/login" {
					t.Fatalf("login problem = %+v, want problem details for HTTP %d", problem, test.status)
				}
			})
		})
	}
}

func TestAuthHTTPClientsDecodeInvalidRefresh(t *testing.T) {
	h := newServiceHarness(t)
	e := newTestEcho()
	httpAdapter.New(h.service).Register(e.Group("/api/v1"))
	server := httptest.NewServer(e)
	t.Cleanup(server.Close)

	t.Run("ogen", func(t *testing.T) {
		client, err := ogen.NewClient(server.URL+"/api/v1", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Refresh(t.Context(), &ogen.RefreshRequest{Token: "invalid-token"})
		if err != nil {
			t.Fatalf("decode refresh response: %v", err)
		}
		problem, ok := response.(*ogen.RefreshUnauthorized)
		if !ok || problem.Status != http.StatusUnauthorized ||
			problem.Title != http.StatusText(http.StatusUnauthorized) || problem.Type != "/api/v1/auth/refresh" {
			t.Fatalf("refresh response = %+v, want typed 401 problem details", response)
		}
	})

	t.Run("oapi-codegen", func(t *testing.T) {
		client, err := oapi.NewClientWithResponses(server.URL + "/api/v1")
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.RefreshWithResponse(t.Context(), oapi.RefreshJSONRequestBody{Token: "invalid-token"})
		if err != nil {
			t.Fatalf("decode refresh response: %v", err)
		}
		problem := response.ApplicationproblemJSON401
		if response.StatusCode() != http.StatusUnauthorized {
			t.Fatalf("refresh status = %d, want 401", response.StatusCode())
		}
		if response.ContentType() != httpAPI.MIMEApplicationProblemJSON {
			t.Fatalf("refresh content type = %q, want problem+json", response.ContentType())
		}
		if problem == nil || problem.Status != http.StatusUnauthorized ||
			problem.Title != http.StatusText(http.StatusUnauthorized) || problem.Type != "/api/v1/auth/refresh" {
			t.Fatalf("refresh problem = %+v, want typed 401 problem details", problem)
		}
	})
}
