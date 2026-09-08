package adapter_test

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
	userTestToken    = "user-test-token"
	userTestUUID     = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	userTestPassword = "new user password"
	userCreateBody   = `{"email":"Alice@Example.com","password":"new user password"}`
	userTestJSON     = `{
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
			server, service := newUserTestServer(t)
			expectUserAuthentication(service, "list")
			service.EXPECT().ListUsers(gomock.Any(), test.wantLimit, test.wantOffset).Return(nil, nil)

			response := performUserRequest(t, server, http.MethodGet, "/api/v1/users"+test.query, userTestToken, "")

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
			server, service := newUserTestServer(t)
			expectUserAuthentication(service, "list")
			target := "/api/v1/users" + test.query

			response := performUserRequest(t, server, http.MethodGet, target, userTestToken, "")

			assertUserProblem(t, response, target, http.StatusBadRequest)
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
			users: []*models.User{userFixture(), secondUser},
			want: `[` + userTestJSON + `, {
				"uuid": "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
				"email": "bob@example.com",
				"created_at": "2026-09-06 08:15:00",
				"roles": [],
				"permissions": []
			}]`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, service := newUserTestServer(t)
			expectUserAuthentication(service, "list")
			service.EXPECT().ListUsers(gomock.Any(), domain.UserListDefaultLimit, 0).Return(test.users, nil)

			response := performUserRequest(t, server, http.MethodGet, "/api/v1/users", userTestToken, "")

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
			server, service := newUserTestServer(t)
			expectUserAuthentication(service, "list")
			service.EXPECT().ListUsers(gomock.Any(), domain.UserListDefaultLimit, 0).Return(nil, test.err)

			response := performUserRequest(t, server, http.MethodGet, "/api/v1/users", userTestToken, "")

			assertUserProblem(t, response, "/api/v1/users", test.wantStatus)
		})
	}
}

func TestUserByUUIDReturnsUser(t *testing.T) {
	server, service := newUserTestServer(t)
	expectUserAuthentication(service, "read_others")
	service.EXPECT().GetUserByUUID(gomock.Any(), userTestUUID).Return(userFixture(), nil)

	response := performUserRequest(t, server, http.MethodGet, "/api/v1/users/"+userTestUUID, userTestToken, "")

	require.Equal(t, http.StatusOK, response.Code, "response body: %s", response.Body.String())
	assert.JSONEq(t, userTestJSON, response.Body.String())
}

func TestUserByUUIDRejectsInvalidUUID(t *testing.T) {
	for _, invalidUUID := range []string{"not-a-uuid", "xxxxxxxx-xxxx-4xxx-8xxx-xxxxxxxxxxxx"} {
		t.Run(invalidUUID, func(t *testing.T) {
			server, service := newUserTestServer(t)
			expectUserAuthentication(service, "read_others")
			target := "/api/v1/users/" + invalidUUID

			response := performUserRequest(t, server, http.MethodGet, target, userTestToken, "")

			assertUserProblem(t, response, target, http.StatusBadRequest)
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
			server, service := newUserTestServer(t)
			expectUserAuthentication(service, "read_others")
			service.EXPECT().GetUserByUUID(gomock.Any(), userTestUUID).Return(nil, test.err)
			target := "/api/v1/users/" + userTestUUID

			response := performUserRequest(t, server, http.MethodGet, target, userTestToken, "")

			assertUserProblem(t, response, target, test.wantStatus)
		})
	}
}

func TestCreateUserReturnsCreatedUser(t *testing.T) {
	server, service := newUserTestServer(t)
	expectUserAuthentication(service, "create")
	service.EXPECT().CreateUser(gomock.Any(), &domain.CreateUserData{
		Email: "Alice@Example.com", Password: userTestPassword,
	}).Return(userFixture(), nil)

	response := performUserRequest(t, server, http.MethodPost, "/api/v1/users", userTestToken, userCreateBody)

	require.Equal(t, http.StatusCreated, response.Code, "response body: %s", response.Body.String())
	assert.JSONEq(t, userTestJSON, response.Body.String())
}

func TestCreateUserRejectsInvalidJSON(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "malformed JSON", body: `{"email":`},
		{name: "array instead of object", body: `[]`},
		{name: "wrong email type", body: `{"email":42,"password":"secret"}`},
		{name: "wrong password type", body: `{"email":"alice@example.com","password":42}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, service := newUserTestServer(t)
			expectUserAuthentication(service, "create")

			response := performUserRequest(t, server, http.MethodPost, "/api/v1/users", userTestToken, test.body)

			assertUserProblem(t, response, "/api/v1/users", http.StatusBadRequest)
		})
	}
}

func TestCreateUserRequiresEmailAndPassword(t *testing.T) {
	const emailError = `{
		"pointer":"#/CreateUserRequest/email","detail":"email is a required field","code":"INVALID_VALUE"
	}`
	const passwordError = `{
		"pointer":"#/CreateUserRequest/password","detail":"password is a required field","code":"INVALID_VALUE"
	}`
	for _, test := range []struct {
		name   string
		body   string
		errors []string
	}{
		{name: "empty body", errors: []string{emailError, passwordError}},
		{name: "empty object", body: `{}`, errors: []string{emailError, passwordError}},
		{name: "missing email", body: `{"password":"secret"}`, errors: []string{emailError}},
		{name: "missing password", body: `{"email":"alice@example.com"}`, errors: []string{passwordError}},
		{name: "empty email", body: `{"email":"","password":"secret"}`, errors: []string{emailError}},
		{name: "empty password", body: `{"email":"alice@example.com","password":""}`, errors: []string{passwordError}},
		{name: "null email", body: `{"email":null,"password":"secret"}`, errors: []string{emailError}},
		{name: "null password", body: `{"email":"alice@example.com","password":null}`, errors: []string{passwordError}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, service := newUserTestServer(t)
			expectUserAuthentication(service, "create")

			response := performUserRequest(t, server, http.MethodPost, "/api/v1/users", userTestToken, test.body)

			assertUserProblem(t, response, "/api/v1/users", http.StatusBadRequest, test.errors...)
		})
	}
}

func TestCreateUserServiceErrors(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "invalid email", err: domain.ErrInvalidEmail, wantStatus: http.StatusBadRequest},
		{name: "weak password", err: domain.ErrPasswordWeak, wantStatus: http.StatusBadRequest},
		{name: "duplicate user", err: domain.ErrUserAlreadyExists, wantStatus: http.StatusConflict},
		{
			name: "wrapped duplicate user", err: fmt.Errorf("create user: %w", domain.ErrUserAlreadyExists),
			wantStatus: http.StatusConflict,
		},
		{
			name: "unexpected failure", err: errors.New("private database failure"),
			wantStatus: http.StatusInternalServerError,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, service := newUserTestServer(t)
			expectUserAuthentication(service, "create")
			service.EXPECT().CreateUser(gomock.Any(), &domain.CreateUserData{
				Email: "Alice@Example.com", Password: userTestPassword,
			}).Return(nil, test.err)

			response := performUserRequest(t, server, http.MethodPost, "/api/v1/users", userTestToken, userCreateBody)

			assertUserProblem(t, response, "/api/v1/users", test.wantStatus)
		})
	}
}

func TestUpdateUserPreservesOptionalFields(t *testing.T) {
	email, password, empty := "new@example.com", "replacement password", ""
	for _, test := range []struct {
		name string
		body string
		want domain.UpdateUserData
	}{
		{name: "omitted fields", body: `{}`},
		{name: "null fields", body: `{"email":null,"password":null}`},
		{name: "email only", body: `{"email":"new@example.com"}`, want: domain.UpdateUserData{Email: &email}},
		{
			name: "password only", body: `{"password":"replacement password"}`,
			want: domain.UpdateUserData{Password: &password},
		},
		{
			name: "both fields", body: `{"email":"new@example.com","password":"replacement password"}`,
			want: domain.UpdateUserData{Email: &email, Password: &password},
		},
		{name: "empty email", body: `{"email":""}`, want: domain.UpdateUserData{Email: &empty}},
		{name: "empty password", body: `{"password":""}`, want: domain.UpdateUserData{Password: &empty}},
		{
			name: "both fields empty", body: `{"email":"","password":""}`,
			want: domain.UpdateUserData{Email: &empty, Password: &empty},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, service := newUserTestServer(t)
			expectUserAuthentication(service, "update")
			service.EXPECT().UpdateUser(gomock.Any(), userTestUUID, &test.want).Return(nil)
			target := "/api/v1/users/" + userTestUUID

			response := performUserRequest(t, server, http.MethodPut, target, userTestToken, test.body)

			require.Equal(t, http.StatusOK, response.Code, "response body: %s", response.Body.String())
			assert.Empty(t, response.Body.String())
		})
	}
}

func TestUpdateUserRejectsInvalidJSON(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "malformed JSON", body: `{"email":`},
		{name: "array instead of object", body: `[]`},
		{name: "wrong email type", body: `{"email":42}`},
		{name: "wrong password type", body: `{"password":42}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, service := newUserTestServer(t)
			expectUserAuthentication(service, "update")
			target := "/api/v1/users/" + userTestUUID

			response := performUserRequest(t, server, http.MethodPut, target, userTestToken, test.body)

			assertUserProblem(t, response, target, http.StatusBadRequest)
		})
	}
}

func TestUserWritesRejectInvalidUUID(t *testing.T) {
	for _, endpoint := range []struct {
		method string
		action string
	}{
		{method: http.MethodPut, action: "update"},
		{method: http.MethodDelete, action: "delete"},
	} {
		t.Run(endpoint.method, func(t *testing.T) {
			for _, invalidUUID := range []string{"not-a-uuid", "xxxxxxxx-xxxx-4xxx-8xxx-xxxxxxxxxxxx"} {
				t.Run(invalidUUID, func(t *testing.T) {
					server, service := newUserTestServer(t)
					expectUserAuthentication(service, endpoint.action)
					target := "/api/v1/users/" + invalidUUID

					response := performUserRequest(t, server, endpoint.method, target, userTestToken, `{}`)

					assertUserProblem(t, response, target, http.StatusBadRequest)
				})
			}
		})
	}
}

func TestUpdateUserServiceErrors(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "not found", err: domain.ErrEntityNotFound, wantStatus: http.StatusNotFound},
		{name: "invalid email", err: domain.ErrInvalidEmail, wantStatus: http.StatusBadRequest},
		{name: "weak password", err: domain.ErrPasswordWeak, wantStatus: http.StatusBadRequest},
		{name: "duplicate email", err: domain.ErrUserAlreadyExists, wantStatus: http.StatusConflict},
		{
			name: "wrapped not found", err: fmt.Errorf("update user: %w", domain.ErrEntityNotFound),
			wantStatus: http.StatusNotFound,
		},
		{
			name: "wrapped duplicate email", err: fmt.Errorf("update user: %w", domain.ErrUserAlreadyExists),
			wantStatus: http.StatusConflict,
		},
		{
			name: "unexpected failure", err: errors.New("private database failure"),
			wantStatus: http.StatusInternalServerError,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, service := newUserTestServer(t)
			expectUserAuthentication(service, "update")
			email, password := "Alice@Example.com", userTestPassword
			service.EXPECT().UpdateUser(gomock.Any(), userTestUUID, &domain.UpdateUserData{
				Email: &email, Password: &password,
			}).Return(test.err)
			target := "/api/v1/users/" + userTestUUID

			response := performUserRequest(t, server, http.MethodPut, target, userTestToken, userCreateBody)

			assertUserProblem(t, response, target, test.wantStatus)
		})
	}
}

func TestDeleteUserReturnsEmptyResponse(t *testing.T) {
	server, service := newUserTestServer(t)
	expectUserAuthentication(service, "delete")
	service.EXPECT().DeleteUser(gomock.Any(), userTestUUID).Return(nil)
	target := "/api/v1/users/" + userTestUUID

	response := performUserRequest(t, server, http.MethodDelete, target, userTestToken, "")

	require.Equal(t, http.StatusOK, response.Code, "response body: %s", response.Body.String())
	assert.Empty(t, response.Body.String())
}

func TestDeleteUserServiceErrors(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "not found", err: domain.ErrEntityNotFound, wantStatus: http.StatusNotFound},
		{
			name: "wrapped not found", err: fmt.Errorf("delete user: %w", domain.ErrEntityNotFound),
			wantStatus: http.StatusNotFound,
		},
		{
			name: "unexpected failure", err: errors.New("private database failure"),
			wantStatus: http.StatusInternalServerError,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, service := newUserTestServer(t)
			expectUserAuthentication(service, "delete")
			service.EXPECT().DeleteUser(gomock.Any(), userTestUUID).Return(test.err)
			target := "/api/v1/users/" + userTestUUID

			response := performUserRequest(t, server, http.MethodDelete, target, userTestToken, "")

			assertUserProblem(t, response, target, test.wantStatus)
		})
	}
}

func TestUserAuthorization(t *testing.T) {
	for _, endpoint := range []struct {
		name        string
		method      string
		target      string
		body        string
		otherAction string
	}{
		{name: "list", method: http.MethodGet, target: "/api/v1/users", otherAction: "read_others"},
		{name: "lookup", method: http.MethodGet, target: "/api/v1/users/" + userTestUUID, otherAction: "list"},
		{
			name: "create", method: http.MethodPost, target: "/api/v1/users",
			body: userCreateBody, otherAction: "list",
		},
		{
			name: "update", method: http.MethodPut, target: "/api/v1/users/" + userTestUUID,
			body: userCreateBody, otherAction: "list",
		},
		{name: "delete", method: http.MethodDelete, target: "/api/v1/users/" + userTestUUID, otherAction: "list"},
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
					name: "invalid token", token: userTestToken, wantStatus: http.StatusUnauthorized,
					setup: func(service *mocks.MockserviceAccessor) {
						service.EXPECT().ValidateJWTToken(gomock.Any(), userTestToken, domain.JWTTokenPurposeAccess).
							Return(nil, domain.ErrInvalidToken)
					},
				},
				{
					name: "missing permission", token: userTestToken, wantStatus: http.StatusForbidden,
					setup: func(service *mocks.MockserviceAccessor) {
						expectUserAuthentication(service, "")
					},
				},
				{
					name: "other endpoint permission", token: userTestToken, wantStatus: http.StatusForbidden,
					setup: func(service *mocks.MockserviceAccessor) {
						expectUserAuthentication(service, endpoint.otherAction)
					},
				},
			} {
				t.Run(test.name, func(t *testing.T) {
					server, service := newUserTestServer(t)
					if test.setup != nil {
						test.setup(service)
					}

					response := performUserRequest(
						t,
						server,
						endpoint.method,
						endpoint.target,
						test.token,
						endpoint.body,
					)

					assertUserProblem(t, response, endpoint.target, test.wantStatus)
				})
			}
		})
	}
}

func newUserTestServer(t *testing.T) (*echo.Echo, *mocks.MockserviceAccessor) {
	t.Helper()
	service := mocks.NewMockserviceAccessor(gomock.NewController(t))
	server := echo.New()
	server.Validator = httpAPI.NewValidator()
	server.HTTPErrorHandler = httpAPI.NewErrorHandler(nil)
	adapter.New(service).Register(server.Group("/api/v1"))
	return server, service
}

func expectUserAuthentication(service *mocks.MockserviceAccessor, action string) {
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
	service.EXPECT().ValidateJWTToken(gomock.Any(), userTestToken, domain.JWTTokenPurposeAccess).
		Return(claimsFor(user), nil)
	service.EXPECT().GetUserByUUID(gomock.Any(), user.UUID.String()).Return(user, nil)
}

func performUserRequest(
	t *testing.T, server http.Handler, method, target, token, body string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	if token != "" {
		request.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func assertUserProblem(
	t *testing.T, response *httptest.ResponseRecorder, target string, status int, validationErrors ...string,
) {
	t.Helper()
	require.Equal(t, status, response.Code, "response body: %s", response.Body.String())
	assert.Equal(t, httpAPI.MIMEApplicationProblemJSON, response.Header().Get(echo.HeaderContentType))
	want := fmt.Sprintf(`{"type":%q,"title":%q,"status":%d`, target, http.StatusText(status), status)
	if len(validationErrors) > 0 {
		want += `,"errors":[` + strings.Join(validationErrors, ",") + `]`
	}
	assert.JSONEq(t, want+`}`, response.Body.String())
}

func userFixture() *models.User {
	return &models.User{
		ID:        202,
		UUID:      uuid.MustParse(userTestUUID),
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
