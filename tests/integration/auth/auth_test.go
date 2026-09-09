package auth_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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
	"github.com/ogen-go/ogen/ogenerrors"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"gorm.io/gorm"

	pb "github.com/go42-dev/go42/api/gen/sdk/grpc/auth/v1"
	oapi "github.com/go42-dev/go42/api/gen/sdk/http/v1/auth/oapi-codegen"
	ogen "github.com/go42-dev/go42/api/gen/sdk/http/v1/auth/ogen"
	"github.com/go42-dev/go42/internal/auth"
	grpcAdapter "github.com/go42-dev/go42/internal/auth/adapters/grpc/v1"
	httpAdapter "github.com/go42-dev/go42/internal/auth/adapters/http/v1"
	"github.com/go42-dev/go42/internal/auth/domain"
	"github.com/go42-dev/go42/internal/auth/models"
	authRepository "github.com/go42-dev/go42/internal/auth/repository"
	"github.com/go42-dev/go42/internal/cache"
	"github.com/go42-dev/go42/internal/cache/local"
	"github.com/go42-dev/go42/internal/database"
	"github.com/go42-dev/go42/internal/outbox"
	outboxDomain "github.com/go42-dev/go42/internal/outbox/domain"
	outboxModels "github.com/go42-dev/go42/internal/outbox/models"
	outboxRepository "github.com/go42-dev/go42/internal/outbox/repository"
	"github.com/go42-dev/go42/tests/integration"
)

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

func TestCredentials_HTTPClientsPreserveOmittedUpdateFields(t *testing.T) {
	for _, sdk := range []string{"ogen", "oapi-codegen"} {
		for _, target := range []string{"user", "self"} {
			for name, fields := range map[string]map[string]string{
				"email only":    {"email": "updated@example.com"},
				"password only": {"password": testPassword + "!new"},
				"empty":         {},
			} {
				t.Run(sdk+"/"+target+"/"+name, func(t *testing.T) {
					h := newSessionHarness(t)
					require.NoError(t, h.repo.AssignRoleToUser(t.Context(), h.user.ID, "admin"))
					tokens := h.login(t)
					e := newTestEcho()
					httpAdapter.New(h.service).Register(e.Group("/api/v1"))
					server := httptest.NewServer(e)
					t.Cleanup(server.Close)

					input := maps.Clone(fields)
					if target == "self" && len(fields) > 0 {
						input["current_password"] = testPassword
					}
					body, err := json.Marshal(input)
					require.NoError(t, err)
					sendCredentialSDKUpdate(t, sdk, target, server.URL+"/api/v1", h.user.UUID, tokens.AccessToken, body)

					stored, err := h.repo.GetUserByUUID(t.Context(), h.user.UUID.String())
					require.NoError(t, err)
					if email, supplied := fields["email"]; supplied {
						assert.Equal(t, email, stored.Email)
					} else {
						assert.Equal(t, h.user.Email, stored.Email, "omitted email must remain unchanged")
					}
					if password, supplied := fields["password"]; supplied {
						assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(stored.Password.V), []byte(password)))
					} else {
						assert.Equal(t, h.user.Password, stored.Password, "omitted password must remain unchanged")
					}
					if len(fields) == 0 {
						assert.Equal(t, h.user.CredentialVersion, stored.CredentialVersion)
					}
				})
			}
		}
	}
}

type credentialSDKSecurity string

func (credentialSDKSecurity) ApiKey(context.Context, ogen.OperationName, *ogen.Client) (ogen.ApiKey, error) {
	return ogen.ApiKey{}, ogenerrors.ErrSkipClientSecurity
}

func (s credentialSDKSecurity) Jwt(context.Context, ogen.OperationName, *ogen.Client) (ogen.Jwt, error) {
	return ogen.Jwt{Token: string(s)}, nil
}

func decodeCredentialSDKUpdate[T any](t *testing.T, body []byte) *T {
	t.Helper()
	request := new(T)
	require.NoError(t, json.Unmarshal(body, request))
	encoded, err := json.Marshal(request)
	require.NoError(t, err)
	require.JSONEq(t, string(body), string(encoded), "SDK decoding must preserve omitted fields")
	return request
}

func sendCredentialSDKUpdate(
	t *testing.T,
	sdk, target, serverURL string,
	userUUID uuid.UUID,
	bearer string,
	body []byte,
) {
	t.Helper()
	if sdk == "ogen" {
		client, err := ogen.NewClient(serverURL, credentialSDKSecurity(bearer))
		require.NoError(t, err)
		if target == "self" {
			request := decodeCredentialSDKUpdate[ogen.UpdateSelfRequest](t, body)
			response, err := client.UsersMeUpdate(t.Context(), request)
			require.NoError(t, err)
			require.IsType(t, &ogen.UsersMeUpdateOK{}, response)
		} else {
			request := decodeCredentialSDKUpdate[ogen.UpdateUserRequest](t, body)
			response, err := client.UsersUpdate(t.Context(), request, ogen.UsersUpdateParams{UUID: userUUID})
			require.NoError(t, err)
			require.IsType(t, &ogen.UsersUpdateOK{}, response)
		}
		return
	}

	client, err := oapi.NewClientWithResponses(serverURL, oapi.WithRequestEditorFn(
		func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+bearer)
			return nil
		},
	))
	require.NoError(t, err)
	if target == "self" {
		request := decodeCredentialSDKUpdate[oapi.UpdateSelfRequest](t, body)
		response, err := client.UsersMeUpdateWithResponse(t.Context(), *request)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode(), "response: %s", response.Body)
	} else {
		request := decodeCredentialSDKUpdate[oapi.UpdateUserRequest](t, body)
		response, err := client.UsersUpdateWithResponse(t.Context(), userUUID, *request)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode(), "response: %s", response.Body)
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
	db, _ := integration.NewDatabase(t)
	engine, dialect := "sqlite", goose.DialectSQLite3
	switch db.Master().Name() {
	case "postgres":
		engine, dialect = "pgsql", goose.DialectPostgres
	case "mysql":
		engine, dialect = "mysql", goose.DialectMySQL
	}
	sqlDB, err := db.Master().DB()
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(dialect, sqlDB, os.DirFS(filepath.Join("..", "..", "..", "migrate", engine)))
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
	for _, step := range []struct {
		name string
		when time.Time
		want time.Time
	}{
		{"first use", when, when},
		{"newer use", when.Add(time.Hour), when.Add(time.Hour)},
		{"delayed older flush", when, when.Add(time.Hour)},
		{"repeated timestamp", when.Add(time.Hour), when.Add(time.Hour)},
		{"later use", when.Add(2 * time.Hour), when.Add(2 * time.Hour)},
	} {
		t.Run(step.name, func(t *testing.T) {
			err := h.repo.WithTransaction(t.Context(), func(ctx context.Context) error {
				return h.repo.UpdateTokenLastUsed(ctx, token.ID, step.when)
			})
			if err != nil {
				t.Fatalf("update existing token: %v", err)
			}
			var stored models.Token
			if err := h.db.Master().WithContext(t.Context()).First(&stored, token.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !stored.LastUsedAt.Valid || !stored.LastUsedAt.V.Equal(step.want) {
				t.Fatalf("stored last-used time = %v, want %s", stored.LastUsedAt, step.want)
			}
		})
	}
	if err := h.db.Master().WithContext(t.Context()).Delete(token).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.repo.UpdateTokenLastUsed(t.Context(), token.ID, when); !errors.Is(err, domain.ErrEntityNotFound) {
		t.Fatalf("update deleted token = %v, want not found", err)
	}
}

func TestRepository_UpdateTokenLastUsedInCurrentTransaction(t *testing.T) {
	h := newSessionHarness(t)
	when := time.Now().UTC().Truncate(time.Second)
	token := &models.Token{
		UUID: uuid.New(), UserID: h.user.ID, Token: sha256Hex(uuid.NewString()), Name: "uncommitted last use",
		LastUsedAt: sql.Null[time.Time]{V: when, Valid: true},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	err := h.repo.WithTransaction(ctx, func(txCtx context.Context) error {
		if err := h.repo.GetTx(txCtx).Create(token).Error; err != nil {
			return err
		}
		// The existence check for an ignored update must see this uncommitted token.
		if err := h.repo.UpdateTokenLastUsed(txCtx, token.ID, when.Add(-time.Hour)); err != nil {
			return err
		}
		return h.repo.UpdateTokenLastUsed(txCtx, token.ID, when)
	})
	if err != nil {
		t.Fatalf("update within current transaction: %v", err)
	}
	var stored models.Token
	if err := h.db.Master().WithContext(ctx).First(&stored, token.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !stored.LastUsedAt.Valid || !stored.LastUsedAt.V.Equal(when) {
		t.Fatalf("stored last-used time = %v, want %s", stored.LastUsedAt, when)
	}
}

func TestRepository_UpdateTokenLastUsedConcurrent(t *testing.T) {
	h := newSessionHarness(t)
	token := &models.Token{
		UUID: uuid.New(), UserID: h.user.ID, Token: sha256Hex(uuid.NewString()), Name: "concurrent last use",
	}
	if err := h.db.Master().WithContext(t.Context()).Create(token).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	base := time.Now().UTC().Truncate(time.Second)
	const writers = 16
	want := base.Add(7 * time.Minute)
	for _, delayed := range []bool{false, true} {
		start := make(chan struct{})
		results := make(chan error, writers)
		for i := range writers {
			when := base.Add(time.Duration(i%8) * time.Minute)
			if delayed {
				when = base.Add(-time.Duration(i+1) * time.Minute)
			}
			go func() {
				<-start
				results <- h.repo.WithTransaction(ctx, func(txCtx context.Context) error {
					return h.repo.UpdateTokenLastUsed(txCtx, token.ID, when)
				})
			}()
		}
		close(start)
		for range writers {
			if err := <-results; err != nil {
				t.Errorf("concurrent update (delayed=%t): %v", delayed, err)
			}
		}
		var stored models.Token
		if err := h.db.Master().WithContext(ctx).First(&stored, token.ID).Error; err != nil {
			t.Fatal(err)
		}
		if !stored.LastUsedAt.Valid || !stored.LastUsedAt.V.Equal(want) {
			t.Errorf("stored last-used time (delayed=%t) = %v, want %s", delayed, stored.LastUsedAt, want)
		}
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
	if winner == nil {
		t.Fatal("successful refresh returned no tokens")
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

func TestRepository_ListUsersPagination(t *testing.T) {
	h := newSessionHarness(t)
	var existing int64
	require.NoError(t, h.db.Master().WithContext(t.Context()).Model(&models.User{}).Count(&existing).Error)
	users := make([]*models.User, 5)
	for index := range users {
		users[index] = createRepositoryUser(t, h)
	}
	require.NoError(t, h.db.Master().WithContext(t.Context()).Delete(users[1]).Error)

	for _, test := range []struct {
		name   string
		limit  int
		offset int
		want   []int
	}{
		{name: "first page", limit: 2, want: []int{users[0].ID, users[2].ID}},
		{name: "middle page", limit: 2, offset: 1, want: []int{users[2].ID, users[3].ID}},
		{name: "last page", limit: 2, offset: 3, want: []int{users[4].ID}},
		{name: "large page", limit: 100, want: []int{users[0].ID, users[2].ID, users[3].ID, users[4].ID}},
		{name: "at end", limit: 2, offset: 4, want: []int{}},
		{name: "past end", limit: 2, offset: 10, want: []int{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			page, err := h.repo.ListUsers(t.Context(), test.limit, int(existing)+test.offset)
			require.NoError(t, err)
			ids := make([]int, len(page))
			for index, user := range page {
				ids[index] = user.ID
				assert.False(t, user.DeletedAt.Valid)
				assert.Empty(t, user.Roles, "unassigned users must not inherit another user's roles")
			}
			assert.Equal(t, test.want, ids)
		})
	}
}

func TestRepository_UserRoleQueries(t *testing.T) {
	h := newSessionHarness(t)
	other := createRepositoryUser(t, h)
	unassigned := createRepositoryUser(t, h)
	db := h.db.Master().WithContext(t.Context())
	require.NoError(t, db.Where("user_id = ?", h.user.ID).Delete(&models.UserRole{}).Error)
	suffix := uuid.NewString()
	resource := "repository-" + suffix
	permissions := make(map[string]models.Permission)
	for _, action := range []string{"read", "write", "hidden", "other"} {
		permission := models.Permission{Resource: resource, Action: action}
		require.NoError(t, db.Create(&permission).Error)
		permissions[action] = permission
	}
	roles := make(map[string]models.Role)
	for _, fixture := range []struct {
		name    string
		users   []int
		actions []string
		expires time.Time
		deleted bool
	}{
		{name: "shared", users: []int{h.user.ID, other.ID}, actions: []string{"read"}},
		{name: "future", users: []int{h.user.ID}, actions: []string{"read", "write"}, expires: time.Now().Add(time.Hour)},
		{name: "empty", users: []int{h.user.ID}},
		{name: "expired", users: []int{h.user.ID}, actions: []string{"hidden"}, expires: time.Now().Add(-time.Hour)},
		{name: "deleted", users: []int{h.user.ID}, actions: []string{"hidden"}, deleted: true},
		{name: "other", users: []int{other.ID}, actions: []string{"other"}},
	} {
		role := models.Role{
			Name: fixture.name + "-" + suffix, Description: "description for " + fixture.name,
			IsSystem: fixture.name == "shared",
		}
		require.NoError(t, db.Create(&role).Error)
		for _, action := range fixture.actions {
			require.NoError(t, db.Table("auth_role_permissions").Create(&map[string]any{
				"role_id": role.ID, "permission_id": permissions[action].ID,
			}).Error)
		}
		for _, userID := range fixture.users {
			assignment := models.UserRole{UserID: userID, RoleID: role.ID}
			if !fixture.expires.IsZero() {
				assignment.ExpiresAt = sql.Null[time.Time]{V: fixture.expires, Valid: true}
			}
			require.NoError(t, db.Create(&assignment).Error)
		}
		if fixture.deleted {
			require.NoError(t, db.Delete(&role).Error)
		}
		roles[role.Name] = role
	}
	wantRoles := map[int]map[string][]string{
		h.user.ID: {
			"shared-" + suffix: {resource + ":read"},
			"future-" + suffix: {resource + ":read", resource + ":write"},
			"empty-" + suffix:  {},
		},
		other.ID: {
			"shared-" + suffix: {resource + ":read"},
			"other-" + suffix:  {resource + ":other"},
		},
		unassigned.ID: {},
	}
	assertUser := func(t *testing.T, got, want *models.User) {
		t.Helper()
		require.NotNil(t, got)
		assert.Equal(t, want.ID, got.ID)
		assert.Equal(t, want.UUID, got.UUID)
		assert.Equal(t, want.Email, got.Email)
		require.Len(t, got.Roles, len(wantRoles[want.ID]))
		seen := make(map[string]struct{}, len(got.Roles))
		for _, role := range got.Roles {
			require.NotContains(t, seen, role.Name, "duplicate role for user %d", want.ID)
			seen[role.Name] = struct{}{}
			expectedPermissions, exists := wantRoles[want.ID][role.Name]
			require.True(t, exists, "unexpected role %q for user %d", role.Name, want.ID)
			assert.Equal(t, roles[role.Name].ID, role.ID)
			assert.Equal(t, roles[role.Name].Description, role.Description)
			assert.Equal(t, roles[role.Name].IsSystem, role.IsSystem)
			assert.ElementsMatch(t, expectedPermissions, repositoryPermissionNames(role.Permissions))
		}
	}
	users := []*models.User{h.user, other, unassigned}
	t.Run("list", func(t *testing.T) {
		var count int64
		require.NoError(t, db.Model(&models.User{}).Count(&count).Error)
		page, err := h.repo.ListUsers(t.Context(), int(count)+1, 0)
		require.NoError(t, err)
		byID := make(map[int]*models.User)
		for _, user := range page {
			byID[user.ID] = user
		}
		for _, user := range users {
			assertUser(t, byID[user.ID], user)
		}
	})
	for _, query := range []struct {
		name string
		get  func(context.Context, *models.User) (*models.User, error)
	}{
		{name: "id", get: func(ctx context.Context, user *models.User) (*models.User, error) {
			return h.repo.GetUserByID(ctx, user.ID)
		}},
		{name: "uuid", get: func(ctx context.Context, user *models.User) (*models.User, error) {
			return h.repo.GetUserByUUID(ctx, user.UUID.String())
		}},
		{name: "email", get: func(ctx context.Context, user *models.User) (*models.User, error) {
			return h.repo.GetUserByEmail(ctx, user.Email)
		}},
	} {
		t.Run(query.name, func(t *testing.T) {
			for _, user := range users {
				got, err := query.get(t.Context(), user)
				require.NoError(t, err)
				assertUser(t, got, user)
			}
		})
	}
}

func TestRepository_GetTokenCache(t *testing.T) {
	cacheFailure := errors.New("cache unavailable")
	for _, test := range []struct {
		name     string
		warm     bool
		corrupt  bool
		readErr  error
		writeErr error
	}{
		{name: "miss populates cache"},
		{name: "hit works without database", warm: true},
		{name: "corrupt entry is replaced", corrupt: true},
		{name: "read failure falls back to database", readErr: cacheFailure},
		{name: "write failure retains database result", writeErr: cacheFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newSessionHarness(t)
			token := createRepositoryToken(t, h, true)
			backend := &repositoryCacheStub{Wrapper: h.cache, readErr: test.readErr, writeErr: test.writeErr}
			repo := authRepository.New(h.repo.BaseRepository, backend, time.Minute)
			key := "cache:token:" + token.Token
			if test.warm {
				_, err := repo.GetToken(t.Context(), token.Token)
				require.NoError(t, err)
				backend.writes = 0
				sqlDB, err := h.db.Master().DB()
				require.NoError(t, err)
				require.NoError(t, sqlDB.Close())
			}
			if test.corrupt {
				require.NoError(t, h.cache.Set(t.Context(), key, "not a gob token", time.Minute))
			}
			got, err := repo.GetToken(t.Context(), token.Token)
			require.NoError(t, err)
			assertRepositoryToken(t, token, got)
			if test.warm {
				assert.Zero(t, backend.writes, "a cache hit must not refresh its TTL")
			} else {
				assert.Equal(t, 1, backend.writes)
			}
			cached, err := cache.GetDecode[*models.Token](t.Context(), h.cache, key)
			require.NoError(t, err)
			if test.writeErr != nil {
				assert.Nil(t, cached)
			} else {
				assertRepositoryToken(t, token, cached)
			}
		})
	}
}

func TestRepository_GetTokenFailures(t *testing.T) {
	for _, name := range []string{"missing", "deleted", "canceled", "token query", "permission query"} {
		t.Run(name, func(t *testing.T) {
			h := newSessionHarness(t)
			token := createRepositoryToken(t, h, true)
			ctx := t.Context()
			lookup := token.Token
			var want error
			var prefix string
			switch name {
			case "missing":
				lookup = sha256Hex(uuid.NewString())
				want = domain.ErrEntityNotFound
			case "deleted":
				require.NoError(t, h.db.Master().WithContext(ctx).Delete(token).Error)
				want = domain.ErrEntityNotFound
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			case "token query", "permission query":
				sqlDB, err := h.db.Master().DB()
				require.NoError(t, err)
				prefix = "error fetching api token:"
				if name == "token query" {
					require.NoError(t, sqlDB.Close())
				} else {
					prefix = "error fetching api token permissions:"
					const callback = "test:close_after_token_query"
					require.NoError(t, h.db.Master().Callback().Query().After("gorm:query").Register(
						callback, func(tx *gorm.DB) {
							if tx.Statement.Table == "auth_api_tokens" {
								assert.NoError(t, sqlDB.Close())
							}
						},
					))
					t.Cleanup(func() { assert.NoError(t, h.db.Master().Callback().Query().Remove(callback)) })
				}
			}
			got, err := h.repo.GetToken(ctx, lookup)
			require.Error(t, err)
			assert.Nil(t, got, "failed reads must not return a partially loaded token")
			if want != nil {
				assert.ErrorIs(t, err, want)
			} else {
				assert.ErrorContains(t, err, prefix)
				assert.ErrorContains(t, errors.Unwrap(err), "sql: database is closed")
			}
			cached, cacheErr := cache.GetDecode[*models.Token](t.Context(), h.cache, "cache:token:"+lookup)
			require.NoError(t, cacheErr)
			assert.Nil(t, cached, "failed reads must not populate the cache")
		})
	}
}

func TestRepository_GetTokenWithoutPermissions(t *testing.T) {
	h := newSessionHarness(t)
	token := createRepositoryToken(t, h, false)
	got, err := h.repo.GetToken(t.Context(), token.Token)
	require.NoError(t, err)
	assertRepositoryToken(t, token, got)
	assert.Empty(t, got.Permissions)
	cached, err := cache.GetDecode[*models.Token](t.Context(), h.cache, "cache:token:"+token.Token)
	require.NoError(t, err)
	assertRepositoryToken(t, token, cached)
	assert.Empty(t, cached.Permissions)
}

func createRepositoryUser(t *testing.T, h *sessionHarness) *models.User {
	t.Helper()
	id := uuid.New()
	user := &models.User{
		UUID: id, Email: "repository-" + id.String() + "@example.com", Status: domain.UserStatusActive,
		CredentialVersion: 1,
	}
	require.NoError(t, h.db.Master().WithContext(t.Context()).Create(user).Error)
	return user
}

func createRepositoryToken(t *testing.T, h *sessionHarness, withPermissions bool) *models.Token {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	token := &models.Token{
		UUID: uuid.New(), UserID: h.user.ID, Token: sha256Hex(uuid.NewString()), Name: "repository token",
		LastUsedAt: sql.Null[time.Time]{V: now, Valid: true},
		ExpiresAt:  sql.Null[time.Time]{V: now.Add(time.Hour), Valid: true},
	}
	db := h.db.Master().WithContext(t.Context())
	require.NoError(t, db.Create(token).Error)
	if withPermissions {
		for _, action := range []string{"read", "write"} {
			permission := models.Permission{Resource: "token-" + token.UUID.String(), Action: action}
			require.NoError(t, db.Create(&permission).Error)
			require.NoError(t, db.Table("auth_api_tokens_permissions").Create(&map[string]any{
				"token_id": token.ID, "permission_id": permission.ID,
			}).Error)
			token.Permissions = append(token.Permissions, permission)
		}
	}
	return token
}

func assertRepositoryToken(t *testing.T, want, got *models.Token) {
	t.Helper()
	require.NotNil(t, got)
	assert.Equal(t, want.ID, got.ID)
	assert.Equal(t, want.UUID, got.UUID)
	assert.Equal(t, want.UserID, got.UserID)
	assert.Equal(t, want.Token, got.Token)
	assert.Equal(t, want.Name, got.Name)
	assert.Equal(t, want.LastUsedAt.Valid, got.LastUsedAt.Valid)
	assert.True(t, want.LastUsedAt.V.Equal(got.LastUsedAt.V))
	assert.Equal(t, want.ExpiresAt.Valid, got.ExpiresAt.Valid)
	assert.True(t, want.ExpiresAt.V.Equal(got.ExpiresAt.V))
	assert.ElementsMatch(t, repositoryPermissionNames(want.Permissions), repositoryPermissionNames(got.Permissions))
}

func repositoryPermissionNames(permissions []models.Permission) []string {
	names := make([]string, len(permissions))
	for index, permission := range permissions {
		names[index] = permission.Resource + ":" + permission.Action
	}
	return names
}

type repositoryCacheStub struct {
	*local.Wrapper
	readErr  error
	writeErr error
	writes   int
}

func (c *repositoryCacheStub) Get(ctx context.Context, key string) (string, bool, error) {
	if c.readErr != nil {
		return "", false, c.readErr
	}
	return c.Wrapper.Get(ctx, key)
}

func (c *repositoryCacheStub) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	c.writes++
	if c.writeErr != nil {
		return c.writeErr
	}
	return c.Wrapper.Set(ctx, key, value, ttl)
}

func TestRepository_CreateUserRejectsInvalidWrites(t *testing.T) {
	for _, name := range []string{"duplicate email", "duplicate uuid", "canceled"} {
		t.Run(name, func(t *testing.T) {
			h := newSessionHarness(t)
			before := loadStoredRepositoryUser(t, h, h.user.ID)
			var countBefore int64
			require.NoError(
				t,
				h.db.Master().WithContext(t.Context()).Unscoped().Model(&models.User{}).Count(&countBefore).Error,
			)
			candidate := &models.User{
				UUID: uuid.New(), Email: "create-" + uuid.NewString() + "@example.com",
				Status: domain.UserStatusActive, CredentialVersion: 1,
			}
			ctx := t.Context()
			want := domain.ErrUserAlreadyExists
			switch name {
			case "duplicate email":
				candidate.Email = h.user.Email
			case "duplicate uuid":
				candidate.UUID = h.user.UUID
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			}
			require.ErrorIs(t, h.repo.CreateUser(ctx, candidate), want)
			assert.Equal(t, before, loadStoredRepositoryUser(t, h, h.user.ID))
			var countAfter int64
			require.NoError(
				t,
				h.db.Master().WithContext(t.Context()).Unscoped().Model(&models.User{}).Count(&countAfter).Error,
			)
			assert.Equal(t, countBefore, countAfter, "a failed insert must not create another user")
		})
	}
}

func TestRepository_UpdateUserRejectsStaleOrInvalidWrites(t *testing.T) {
	for _, name := range []string{"duplicate email", "stale version", "deleted user", "canceled"} {
		t.Run(name, func(t *testing.T) {
			h := newSessionHarness(t)
			candidate := *h.user
			candidate.Email = "attempt-" + uuid.NewString() + "@example.com"
			ctx := t.Context()
			want := domain.ErrInvalidCredentials
			switch name {
			case "duplicate email":
				other := createRepositoryUser(t, h)
				candidate.Email = other.Email
				want = domain.ErrUserAlreadyExists
			case "stale version":
				winner := *h.user
				winner.Email = "winner-" + uuid.NewString() + "@example.com"
				require.NoError(t, h.repo.UpdateUser(ctx, &winner))
				require.Equal(t, candidate.CredentialVersion+1, winner.CredentialVersion)
			case "deleted user":
				require.NoError(t, h.db.Master().WithContext(ctx).Delete(h.user).Error)
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			}
			before := loadStoredRepositoryUser(t, h, h.user.ID)
			require.ErrorIs(t, h.repo.UpdateUser(ctx, &candidate), want)
			assert.Equal(t, before, loadStoredRepositoryUser(t, h, h.user.ID),
				"rejected updates must preserve stored credentials and their version")
		})
	}
}

func TestRepository_AssignRoleRejectsInvalidAssignments(t *testing.T) {
	for _, name := range []string{"missing role", "deleted role", "missing user", "duplicate assignment"} {
		t.Run(name, func(t *testing.T) {
			h := newSessionHarness(t)
			userID, roleName := h.user.ID, domain.RBACRoleUser
			var before []models.UserRole
			require.NoError(
				t,
				h.db.Master().WithContext(t.Context()).Where("user_id = ?", h.user.ID).Find(&before).Error,
			)
			var want error
			prefix := "error assigning role to user"
			switch name {
			case "missing role":
				roleName = "missing-" + uuid.NewString()
				want, prefix = gorm.ErrRecordNotFound, "error retrieving role"
			case "deleted role":
				role := models.Role{Name: "deleted-" + uuid.NewString()}
				require.NoError(t, h.db.Master().WithContext(t.Context()).Create(&role).Error)
				require.NoError(t, h.db.Master().WithContext(t.Context()).Delete(&role).Error)
				roleName = role.Name
				want, prefix = gorm.ErrRecordNotFound, "error retrieving role"
			case "missing user":
				userID = -1
			}
			err := h.repo.AssignRoleToUser(t.Context(), userID, roleName)
			require.ErrorContains(t, err, prefix)
			if want != nil {
				assert.ErrorIs(t, err, want)
			} else {
				assert.NotNil(t, errors.Unwrap(err), "assignment errors must preserve the database cause")
			}
			var after []models.UserRole
			require.NoError(
				t,
				h.db.Master().WithContext(t.Context()).Where("user_id IN ?", []int{h.user.ID, -1}).Find(&after).Error,
			)
			assert.ElementsMatch(t, before, after)
		})
	}
}

func TestRepository_UserHistoryIsIdempotent(t *testing.T) {
	for _, changed := range []bool{false, true} {
		name := "identical replay"
		if changed {
			name = "conflicting replay"
		}
		t.Run(name, func(t *testing.T) {
			h := newSessionHarness(t)
			original := models.UserHistoryRecord{
				ID: uuid.New(), UserID: h.user.ID, OccurredAt: time.Now().UTC().Truncate(time.Second),
				EventType: "user.created", Data: []byte(`{"email":"original@example.com"}`),
				Metadata: `{"request_id":"original-request"}`,
			}
			require.NoError(t, h.repo.SaveUserHistoryRecord(t.Context(), &original))
			var before models.UserHistoryRecord
			require.NoError(t, h.db.Master().WithContext(t.Context()).First(&before, "id = ?", original.ID).Error)
			assert.Equal(t, original.UserID, before.UserID)
			assert.Equal(t, original.EventType, before.EventType)
			assert.Equal(t, original.Data, before.Data)
			assert.Equal(t, original.Metadata, before.Metadata)
			assert.True(t, original.OccurredAt.Equal(before.OccurredAt))
			replay := original
			if changed {
				replay.UserID = createRepositoryUser(t, h).ID
				replay.OccurredAt = original.OccurredAt.Add(time.Hour)
				replay.EventType = "user.updated"
				replay.Data = []byte(`{"email":"replacement@example.com"}`)
				replay.Metadata = `{"request_id":"replacement-request"}`
			}
			require.NoError(t, h.repo.SaveUserHistoryRecord(t.Context(), &replay))
			var after models.UserHistoryRecord
			require.NoError(t, h.db.Master().WithContext(t.Context()).First(&after, "id = ?", original.ID).Error)
			assert.Equal(t, before, after, "replaying an event must preserve its original history")
			next := original
			next.ID = uuid.New()
			next.EventType = "user.updated"
			require.NoError(t, h.repo.SaveUserHistoryRecord(t.Context(), &next))
			var records []models.UserHistoryRecord
			require.NoError(
				t,
				h.db.Master().
					WithContext(t.Context()).
					Where("id IN ?", []uuid.UUID{original.ID, next.ID}).
					Find(&records).
					Error,
			)
			require.Len(t, records, 2, "deduplication must not suppress a different event")
		})
	}
}

func loadStoredRepositoryUser(t *testing.T, h *sessionHarness, id int) models.User {
	t.Helper()
	var user models.User
	require.NoError(t, h.db.Master().WithContext(t.Context()).Unscoped().First(&user, id).Error)
	return user
}

func TestRepository_SessionCleanupBatchesOldestFirst(t *testing.T) {
	for _, count := range []int{0, 999, 1000, 1001} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			h := newSessionCleanupHarness(t)
			now := time.Now().UTC().Truncate(time.Second)
			active := repositoryCleanupSession(h, now.Add(time.Hour))
			revoked := repositoryCleanupSession(h, now.Add(time.Hour))
			revoked.RevokedAt = sql.Null[time.Time]{V: now.Add(-time.Minute), Valid: true}
			sessions := []models.Session{active, revoked}
			oldest := time.Date(2001, time.January, 1, 0, 0, 0, 0, time.UTC)
			for index := range count {
				// Insert newest first so insertion order cannot determine which batch is deleted.
				session := repositoryCleanupSession(h, oldest.Add(time.Duration(count-index)*time.Second))
				if index%2 == 0 {
					session.RevokedAt = sql.Null[time.Time]{V: session.ExpiresAt.Add(-time.Minute), Valid: true}
				}
				sessions = append(sessions, session)
			}
			require.NoError(t, h.db.Master().WithContext(t.Context()).CreateInBatches(&sessions, 100).Error)
			deleted, err := h.repo.DeleteExpiredSessions(t.Context())
			require.NoError(t, err)
			assert.EqualValues(t, min(count, 1000), deleted)
			wantIDs := []uuid.UUID{active.ID, revoked.ID}
			if count > 1000 {
				wantIDs = append(wantIDs, sessions[2].ID)
			}
			var retained []models.Session
			require.NoError(
				t,
				h.db.Master().WithContext(t.Context()).Where("user_id = ?", h.user.ID).Find(&retained).Error,
			)
			ids := make([]uuid.UUID, len(retained))
			for index, session := range retained {
				ids[index] = session.ID
			}
			assert.ElementsMatch(t, wantIDs, ids, "only the oldest expired sessions belong in the first batch")
			deleted, err = h.repo.DeleteExpiredSessions(t.Context())
			require.NoError(t, err)
			assert.EqualValues(t, max(count-1000, 0), deleted)
			deleted, err = h.repo.DeleteExpiredSessions(t.Context())
			require.NoError(t, err)
			assert.Zero(t, deleted)
			var remaining int64
			require.NoError(t, h.db.Master().WithContext(t.Context()).Model(&models.Session{}).
				Where("id IN ?", []uuid.UUID{active.ID, revoked.ID}).Count(&remaining).Error)
			assert.EqualValues(t, 2, remaining, "unexpired sessions must survive every cleanup batch")
		})
	}
}

func TestRepository_SessionCleanupPreservesRefreshedSessions(t *testing.T) {
	h := newSessionCleanupHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	refreshed := repositoryCleanupSession(h, now.Add(-time.Hour))
	expired := repositoryCleanupSession(h, now.Add(-2*time.Hour))
	require.NoError(t, h.db.Master().WithContext(t.Context()).Create(&[]models.Session{refreshed, expired}).Error)
	future, nextToken := now.Add(time.Hour), uuid.New()
	updated := false
	callback := h.db.Master().Callback().Delete()
	const name = "test:refresh_session_before_cleanup_delete"
	require.NoError(t, callback.Before("gorm:delete").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table != "auth_sessions" {
			return
		}
		result := tx.Session(&gorm.Session{NewDB: true}).Model(&models.Session{}).
			Where("id = ?", refreshed.ID).Updates(map[string]any{"expires_at": future, "refresh_token_id": nextToken})
		if result.Error != nil {
			_ = tx.AddError(result.Error)
			return
		}
		updated = result.RowsAffected == 1
	}))
	t.Cleanup(func() { require.NoError(t, callback.Remove(name)) })
	deleted, err := h.repo.DeleteExpiredSessions(t.Context())
	require.NoError(t, err)
	require.True(t, updated, "the refresh must happen after selection and before deletion")
	assert.EqualValues(t, 1, deleted)
	stored, err := h.repo.GetActiveSession(t.Context(), refreshed.ID.String(), h.user.UUID.String())
	require.NoError(t, err, "the refreshed session must remain usable")
	assert.Equal(t, nextToken, stored.RefreshTokenID)
	assert.True(t, future.Equal(stored.ExpiresAt))
	var count int64
	require.NoError(
		t,
		h.db.Master().WithContext(t.Context()).Model(&models.Session{}).Where("id = ?", expired.ID).Count(&count).Error,
	)
	assert.Zero(t, count)
}

func TestRepository_SessionCleanupPropagatesFailures(t *testing.T) {
	for _, stage := range []string{"canceled", "query", "delete"} {
		t.Run(stage, func(t *testing.T) {
			h := newSessionCleanupHarness(t)
			session := repositoryCleanupSession(h, time.Now().UTC().Add(-time.Hour).Truncate(time.Second))
			require.NoError(t, h.db.Master().WithContext(t.Context()).Create(&session).Error)
			ctx := t.Context()
			cause := errors.New("session cleanup " + stage + " failed")
			blocked := true
			if stage == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				cause = context.Canceled
			} else {
				callback := h.db.Master().Callback().Query()
				if stage == "delete" {
					callback = h.db.Master().Callback().Delete()
				}
				name := "test:fail_session_cleanup_" + stage
				require.NoError(t, callback.Before("gorm:"+stage).Register(name, func(tx *gorm.DB) {
					if blocked && tx.Statement.Table == "auth_sessions" {
						_ = tx.AddError(cause)
					}
				}))
				t.Cleanup(func() { require.NoError(t, callback.Remove(name)) })
			}
			deleted, err := h.repo.DeleteExpiredSessions(ctx)
			require.ErrorIs(t, err, cause)
			assert.Zero(t, deleted)
			blocked = false
			var stored models.Session
			require.NoError(t, h.db.Master().WithContext(t.Context()).First(&stored, "id = ?", session.ID).Error)
			assert.Equal(t, session.RefreshTokenID, stored.RefreshTokenID)
			assert.True(t, session.ExpiresAt.Equal(stored.ExpiresAt))
			deleted, err = h.repo.DeleteExpiredSessions(t.Context())
			require.NoError(t, err)
			assert.EqualValues(t, 1, deleted, "cleanup must recover once the failure is removed")
		})
	}
}

func newSessionCleanupHarness(t *testing.T) *sessionHarness {
	t.Helper()
	h := newSessionHarness(t)
	// Optional shared test databases can contain expired sessions from earlier tests.
	require.NoError(t, h.db.Master().WithContext(t.Context()).Where("expires_at <= ?", time.Now().UTC()).
		Delete(&models.Session{}).Error)
	return h
}

func repositoryCleanupSession(h *sessionHarness, expiresAt time.Time) models.Session {
	return models.Session{
		ID: uuid.New(), UserID: h.user.ID, CredentialVersion: h.user.CredentialVersion,
		RefreshTokenID: uuid.New(), ExpiresAt: expiresAt,
	}
}
