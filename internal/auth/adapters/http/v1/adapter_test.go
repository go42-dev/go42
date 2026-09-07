package adapter_test

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	httpAPI "github.com/go42-dev/go42/internal/api/http"
	adapter "github.com/go42-dev/go42/internal/auth/adapters/http/v1"
	"github.com/go42-dev/go42/internal/auth/adapters/http/v1/mocks"
	"github.com/go42-dev/go42/internal/auth/domain"
	"github.com/go42-dev/go42/internal/auth/models"
)

const (
	userReadToken = "user-read-token"
	userReadUUID  = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	userReadJSON  = `{
		"uuid": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		"email": "alice@example.com",
		"created_at": "2026-09-07 12:30:00",
		"roles": ["auditor"],
		"permissions": ["users:list", "users:read_others"]
	}`
)

func TestListUsersPagination(t *testing.T) {
	for _, test := range []struct {
		name       string
		query      string
		wantLimit  int
		wantOffset int
	}{
		{name: "defaults", wantLimit: domain.UserListDefaultLimit},
		{name: "offset only", query: "?offset=7", wantLimit: domain.UserListDefaultLimit, wantOffset: 7},
		{name: "zero limit", query: "?limit=0&offset=3", wantLimit: domain.UserListDefaultLimit, wantOffset: 3},
		{name: "minimum limit", query: "?limit=1&offset=0", wantLimit: 1},
		{
			name: "maximum limit", query: fmt.Sprintf("?limit=%d&offset=11", domain.UserListMaximumLimit),
			wantLimit: domain.UserListMaximumLimit, wantOffset: 11,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, service := newUserReadTestServer(t)
			expectUserReadAuthentication(service, "list")
			service.EXPECT().ListUsers(gomock.Any(), test.wantLimit, test.wantOffset).Return(nil, nil)

			response := performUserReadRequest(t, server, "/api/v1/users"+test.query, userReadToken)

			require.Equal(t, http.StatusOK, response.Code, "response body: %s", response.Body.String())
			assert.JSONEq(t, `[]`, response.Body.String())
		})
	}
}

func TestListUsersRejectsInvalidPagination(t *testing.T) {
	for _, test := range []struct {
		name  string
		query string
	}{
		{name: "nonnumeric limit", query: "?limit=invalid"},
		{name: "overflowing limit", query: "?limit=999999999999999999999999999999"},
		{name: "negative limit", query: "?limit=-1"},
		{name: "limit above maximum", query: fmt.Sprintf("?limit=%d", domain.UserListMaximumLimit+1)},
		{name: "nonnumeric offset", query: "?offset=invalid"},
		{name: "overflowing offset", query: "?offset=999999999999999999999999999999"},
		{name: "negative offset", query: "?offset=-1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, service := newUserReadTestServer(t)
			expectUserReadAuthentication(service, "list")
			target := "/api/v1/users" + test.query

			response := performUserReadRequest(t, server, target, userReadToken)

			assertUserReadProblem(t, response, target, http.StatusBadRequest)
		})
	}
}

func TestListUsersResponses(t *testing.T) {
	secondUser := &models.User{
		UUID:      uuid.MustParse("cccccccc-cccc-4ccc-8ccc-cccccccccccc"),
		Email:     "bob@example.com",
		CreatedAt: time.Date(2026, time.September, 6, 8, 15, 0, 0, time.UTC),
	}
	for _, test := range []struct {
		name  string
		users []*models.User
		want  string
	}{
		{name: "nil result is an empty array", want: `[]`},
		{name: "empty result is an empty array", users: []*models.User{}, want: `[]`},
		{
			name:  "users retain their order and expose public fields",
			users: []*models.User{userReadFixture(), secondUser},
			want: `[` + userReadJSON + `, {
				"uuid": "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
				"email": "bob@example.com",
				"created_at": "2026-09-06 08:15:00",
				"roles": [],
				"permissions": []
			}]`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, service := newUserReadTestServer(t)
			expectUserReadAuthentication(service, "list")
			service.EXPECT().ListUsers(gomock.Any(), domain.UserListDefaultLimit, 0).Return(test.users, nil)

			response := performUserReadRequest(t, server, "/api/v1/users", userReadToken)

			require.Equal(t, http.StatusOK, response.Code, "response body: %s", response.Body.String())
			assert.JSONEq(t, test.want, response.Body.String())
		})
	}
}

func TestListUsersServiceErrors(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "invalid pagination", err: domain.ErrInvalidPagination, wantStatus: http.StatusBadRequest},
		{
			name: "wrapped invalid pagination", err: fmt.Errorf("list users: %w", domain.ErrInvalidPagination),
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "unexpected failure", err: errors.New("private database failure"),
			wantStatus: http.StatusInternalServerError,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, service := newUserReadTestServer(t)
			expectUserReadAuthentication(service, "list")
			service.EXPECT().ListUsers(gomock.Any(), domain.UserListDefaultLimit, 0).Return(nil, test.err)

			response := performUserReadRequest(t, server, "/api/v1/users", userReadToken)

			assertUserReadProblem(t, response, "/api/v1/users", test.wantStatus)
		})
	}
}

func TestUserByUUIDReturnsUser(t *testing.T) {
	server, service := newUserReadTestServer(t)
	expectUserReadAuthentication(service, "read_others")
	service.EXPECT().GetUserByUUID(gomock.Any(), userReadUUID).Return(userReadFixture(), nil)

	response := performUserReadRequest(t, server, "/api/v1/users/"+userReadUUID, userReadToken)

	require.Equal(t, http.StatusOK, response.Code, "response body: %s", response.Body.String())
	assert.JSONEq(t, userReadJSON, response.Body.String())
}

func TestUserByUUIDRejectsInvalidUUID(t *testing.T) {
	for _, invalidUUID := range []string{"not-a-uuid", "xxxxxxxx-xxxx-4xxx-8xxx-xxxxxxxxxxxx"} {
		t.Run(invalidUUID, func(t *testing.T) {
			server, service := newUserReadTestServer(t)
			expectUserReadAuthentication(service, "read_others")
			target := "/api/v1/users/" + invalidUUID

			response := performUserReadRequest(t, server, target, userReadToken)

			assertUserReadProblem(t, response, target, http.StatusBadRequest)
		})
	}
}

func TestUserByUUIDServiceErrors(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "not found", err: domain.ErrEntityNotFound, wantStatus: http.StatusNotFound},
		{
			name: "wrapped not found", err: fmt.Errorf("read user: %w", domain.ErrEntityNotFound),
			wantStatus: http.StatusNotFound,
		},
		{
			name: "unexpected failure", err: errors.New("private database failure"),
			wantStatus: http.StatusInternalServerError,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, service := newUserReadTestServer(t)
			expectUserReadAuthentication(service, "read_others")
			service.EXPECT().GetUserByUUID(gomock.Any(), userReadUUID).Return(nil, test.err)
			target := "/api/v1/users/" + userReadUUID

			response := performUserReadRequest(t, server, target, userReadToken)

			assertUserReadProblem(t, response, target, test.wantStatus)
		})
	}
}

func TestUserReadAuthorization(t *testing.T) {
	for _, endpoint := range []struct {
		name        string
		target      string
		otherAction string
	}{
		{name: "list", target: "/api/v1/users", otherAction: "read_others"},
		{name: "lookup", target: "/api/v1/users/" + userReadUUID, otherAction: "list"},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			for _, test := range []struct {
				name       string
				token      string
				setup      func(*mocks.MockserviceAccessor)
				wantStatus int
			}{
				{name: "missing credentials", wantStatus: http.StatusUnauthorized},
				{
					name: "invalid token", token: userReadToken, wantStatus: http.StatusUnauthorized,
					setup: func(service *mocks.MockserviceAccessor) {
						service.EXPECT().ValidateJWTToken(gomock.Any(), userReadToken, domain.JWTTokenPurposeAccess).
							Return(nil, domain.ErrInvalidToken)
					},
				},
				{
					name: "missing permission", token: userReadToken, wantStatus: http.StatusForbidden,
					setup: func(service *mocks.MockserviceAccessor) {
						expectUserReadAuthentication(service, "")
					},
				},
				{
					name: "other endpoint permission", token: userReadToken, wantStatus: http.StatusForbidden,
					setup: func(service *mocks.MockserviceAccessor) {
						expectUserReadAuthentication(service, endpoint.otherAction)
					},
				},
			} {
				t.Run(test.name, func(t *testing.T) {
					server, service := newUserReadTestServer(t)
					if test.setup != nil {
						test.setup(service)
					}

					response := performUserReadRequest(t, server, endpoint.target, test.token)

					assertUserReadProblem(t, response, endpoint.target, test.wantStatus)
				})
			}
		})
	}
}

func newUserReadTestServer(t *testing.T) (*echo.Echo, *mocks.MockserviceAccessor) {
	t.Helper()
	service := mocks.NewMockserviceAccessor(gomock.NewController(t))
	server := echo.New()
	server.HTTPErrorHandler = httpAPI.NewErrorHandler(nil)
	adapter.New(service).Register(server.Group("/api/v1"))
	return server, service
}

func expectUserReadAuthentication(service *mocks.MockserviceAccessor, action string) {
	user := &models.User{
		ID:     101,
		UUID:   uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
		Status: domain.UserStatusActive,
	}
	if action != "" {
		user.Roles = []models.Role{{
			Permissions: []models.Permission{{Resource: "users", Action: action}},
		}}
	}
	service.EXPECT().ValidateJWTToken(gomock.Any(), userReadToken, domain.JWTTokenPurposeAccess).
		Return(claimsFor(user), nil)
	service.EXPECT().GetUserByUUID(gomock.Any(), user.UUID.String()).Return(user, nil)
}

func performUserReadRequest(t *testing.T, server http.Handler, target, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	if token != "" {
		request.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func assertUserReadProblem(t *testing.T, response *httptest.ResponseRecorder, target string, status int) {
	t.Helper()
	require.Equal(t, status, response.Code, "response body: %s", response.Body.String())
	assert.Equal(t, httpAPI.MIMEApplicationProblemJSON, response.Header().Get(echo.HeaderContentType))
	assert.JSONEq(t, fmt.Sprintf(
		`{"type":%q,"title":%q,"status":%d}`, target, http.StatusText(status), status,
	), response.Body.String())
}

func userReadFixture() *models.User {
	return &models.User{
		ID:        202,
		UUID:      uuid.MustParse(userReadUUID),
		Email:     "alice@example.com",
		Password:  sql.Null[string]{V: "private-password-hash", Valid: true},
		Status:    domain.UserStatusActive,
		CreatedAt: time.Date(2026, time.September, 7, 12, 30, 0, 0, time.UTC),
		Roles: []models.Role{{
			Name: "auditor",
			Permissions: []models.Permission{
				{Resource: "users", Action: "list"},
				{Resource: "users", Action: "read_others"},
			},
		}},
	}
}
