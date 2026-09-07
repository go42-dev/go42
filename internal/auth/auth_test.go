package auth_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"go.uber.org/mock/gomock"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"

	oapi "github.com/go42-dev/go42/api/gen/sdk/http/v1/auth/oapi-codegen"
	ogen "github.com/go42-dev/go42/api/gen/sdk/http/v1/auth/ogen"
	httpAPI "github.com/go42-dev/go42/internal/api/http"
	"github.com/go42-dev/go42/internal/auth"
	httpAdapter "github.com/go42-dev/go42/internal/auth/adapters/http/v1"
	"github.com/go42-dev/go42/internal/auth/domain"
	authMocks "github.com/go42-dev/go42/internal/auth/mocks"
	"github.com/go42-dev/go42/internal/auth/models"
	outboxDomain "github.com/go42-dev/go42/internal/outbox/domain"
)

const testAPIKey = "api_kXqdf2uQ7hmOARp-pZrhA6_IsZSeKCmSEM4YFKBGIzA"

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
