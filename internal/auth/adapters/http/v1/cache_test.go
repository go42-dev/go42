package adapter_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"go.uber.org/mock/gomock"

	adapter "github.com/go42-dev/go42/internal/auth/adapters/http/v1"
	"github.com/go42-dev/go42/internal/auth/adapters/http/v1/mocks"
	"github.com/go42-dev/go42/internal/auth/domain"
	"github.com/go42-dev/go42/internal/auth/models"
)

func TestReadSelf_CacheIsIsolatedByAuthenticatedUser(t *testing.T) {
	userA := activeUserWithReadSelfPermission(
		101,
		uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
		"alice@example.com",
	)
	userB := activeUserWithReadSelfPermission(
		202,
		uuid.MustParse("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"),
		"bob@example.com",
	)

	ctrl := gomock.NewController(t)
	service := mocks.NewMockserviceAccessor(ctrl)
	gomock.InOrder(
		service.EXPECT().
			ValidateJWTToken(gomock.Any(), "token-a", domain.JWTTokenPurposeAccess).
			Return(claimsFor(userA), nil),
		service.EXPECT().
			GetUserByUUID(gomock.Any(), userA.UUID.String()).
			Return(userA, nil),
		service.EXPECT().
			GetUserByID(gomock.Any(), userA.ID).
			Return(userA, nil),
		service.EXPECT().
			ValidateJWTToken(gomock.Any(), "token-b", domain.JWTTokenPurposeAccess).
			Return(claimsFor(userB), nil),
		service.EXPECT().
			GetUserByUUID(gomock.Any(), userB.UUID.String()).
			Return(userB, nil),
		service.EXPECT().
			GetUserByID(gomock.Any(), userB.ID).
			Return(userB, nil),
	)

	e := echo.New()
	a := adapter.New(service)
	a.Register(e.Group("/api/v1"))

	first := performReadSelfRequest(t, e, "token-a")
	if first.UUID != userA.UUID.String() {
		t.Fatalf("first response UUID = %q, want %q", first.UUID, userA.UUID)
	}

	second := performReadSelfRequest(t, e, "token-b")
	if second.UUID != userB.UUID.String() {
		t.Errorf(
			"second response UUID = %q, want %q; /users/me cache crossed authenticated users",
			second.UUID,
			userB.UUID,
		)
	}
}

func activeUserWithReadSelfPermission(id int, userUUID uuid.UUID, email string) *models.User {
	return &models.User{
		ID:        id,
		UUID:      userUUID,
		Email:     email,
		Status:    domain.UserStatusActive,
		CreatedAt: time.Unix(1_700_000_000, 0).UTC(),
		Roles: []models.Role{
			{
				Name: domain.RBACRoleUser,
				Permissions: []models.Permission{
					{Resource: "users", Action: "read_self"},
				},
			},
		},
	}
}

func claimsFor(user *models.User) *domain.JWTClaims {
	return &domain.JWTClaims{
		Subject: user.UUID.String(),
	}
}

func performReadSelfRequest(t *testing.T, e *echo.Echo, token string) adapter.UserResponse {
	t.Helper()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/users/me", nil)
	request.Header.Set("Authorization", "Bearer "+token)

	e.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/users/me status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var response adapter.UserResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode GET /api/v1/users/me response: %v", err)
	}
	return response
}
