package middleware_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"go.uber.org/mock/gomock"

	"github.com/go42-dev/go42/internal/auth"
	"github.com/go42-dev/go42/internal/auth/domain"
	"github.com/go42-dev/go42/internal/auth/middleware"
	authMocks "github.com/go42-dev/go42/internal/auth/mocks"
	"github.com/go42-dev/go42/internal/auth/models"
	"github.com/go42-dev/go42/internal/cache"
)

func TestAuthMiddleware_RequiresAccessToken(t *testing.T) {
	const password = "correct horse battery staple"
	user := &models.User{
		ID:     42,
		UUID:   uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
		Email:  "alice@example.com",
		Status: domain.UserStatusActive,
	}
	if err := user.SetPassword(password); err != nil {
		t.Fatalf("set test password: %v", err)
	}

	ctrl := gomock.NewController(t)
	repository := authMocks.NewMockrepository(ctrl)
	repository.EXPECT().
		GetUserByEmail(gomock.Any(), user.Email).
		Return(user, nil)
	repository.EXPECT().
		GetUserByUUID(gomock.Any(), user.UUID.String()).
		Return(user, nil).
		AnyTimes()

	session := new(models.Session)
	repository.EXPECT().
		CreateSession(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, generated *models.Session) error {
			*session = *generated
			return nil
		})
	repository.EXPECT().GetActiveSession(gomock.Any(), gomock.Any(), user.UUID.String()).Return(session, nil)

	outbox := authMocks.NewMockoutboxService(ctrl)
	outbox.EXPECT().
		NewOutboxMessage(gomock.Any(), domain.TopicNameAuthEvents, gomock.Any()).
		Return(nil)

	service := auth.NewService(
		repository,
		outbox,
		cache.NewNoop(),
		auth.WithJWTSecrets([]string{"token-purpose-test-secret"}),
		auth.WithJWTAccessTokenTTL(15*time.Minute),
		auth.WithJWTRefreshTokenTTL(24*time.Hour),
		auth.WithJWTIssuer("go42-test"),
		auth.WithJWTAudience([]string{"go42-test"}),
	)
	tokens, err := service.Login(context.Background(), user.Email, password)
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	var handlerCalls atomic.Int32
	e := echo.New()
	e.GET("/protected", func(c *echo.Context) error {
		handlerCalls.Add(1)
		return c.NoContent(http.StatusNoContent)
	}, middleware.NewAuthMiddleware(service))

	accessRecorder := performAuthenticatedRequest(e, tokens.AccessToken)
	if accessRecorder.Code != http.StatusNoContent {
		t.Fatalf("access token status = %d, want %d", accessRecorder.Code, http.StatusNoContent)
	}

	refreshRecorder := performAuthenticatedRequest(e, tokens.RefreshToken)
	if refreshRecorder.Code != http.StatusUnauthorized {
		t.Errorf("refresh token status = %d, want %d", refreshRecorder.Code, http.StatusUnauthorized)
	}
	if got := handlerCalls.Load(); got != 1 {
		t.Errorf("protected handler calls = %d, want 1", got)
	}
}

func TestAuthMiddleware_APITokenUsesOwnerIdentityAndTokenPermissions(t *testing.T) {
	const rawToken = "api_kXqdf2uQ7hmOARp-pZrhA6_IsZSeKCmSEM4YFKBGIzA"
	user := &models.User{
		ID:     42,
		UUID:   uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
		Status: domain.UserStatusActive,
		Roles: []models.Role{{
			Permissions: []models.Permission{{Resource: "users", Action: "delete"}},
		}},
	}
	apiToken := &models.Token{
		ID:     7,
		UUID:   uuid.MustParse("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"),
		UserID: user.ID,
		Permissions: []models.Permission{{
			Resource: "users",
			Action:   "list",
		}},
	}

	ctrl := gomock.NewController(t)
	repository := authMocks.NewMockrepository(ctrl)
	repository.EXPECT().GetToken(gomock.Any(), sha256String(rawToken)).Return(apiToken, nil)
	repository.EXPECT().GetUserByID(gomock.Any(), user.ID).Return(user, nil)
	service := auth.NewService(
		repository,
		authMocks.NewMockoutboxService(ctrl),
		cache.NewNoop(),
	)

	e := echo.New()
	e.GET("/protected", func(c *echo.Context) error {
		authInfo := auth.RetrieveAuthFromContext(c.Request().Context())
		if authInfo == nil {
			t.Fatal("authentication context is nil")
		}
		if authInfo.ID != user.ID || authInfo.UUID != user.UUID.String() {
			t.Errorf(
				"authentication identity = (%d, %q), want (%d, %q)",
				authInfo.ID, authInfo.UUID, user.ID, user.UUID.String(),
			)
		}
		if authInfo.Type != domain.AuthenticationTypeApiToken {
			t.Errorf("authentication type = %q, want %q", authInfo.Type, domain.AuthenticationTypeApiToken)
		}
		if !authInfo.HasPermission(domain.RBACPermissionUsersList) {
			t.Error("API-token permission is missing")
		}
		if authInfo.HasPermission(domain.RBACPermissionUsersDelete) {
			t.Error("owner role permission leaked into API-token permissions")
		}
		return c.NoContent(http.StatusNoContent)
	}, middleware.NewAuthMiddleware(service))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("x-api-key", rawToken)
	e.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Errorf("API-token status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func performAuthenticatedRequest(e *echo.Echo, token string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	e.ServeHTTP(recorder, request)
	return recorder
}

func sha256String(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}
