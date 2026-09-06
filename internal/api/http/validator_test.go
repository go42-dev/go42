package http

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
)

type validationRequest struct {
	Email string `json:"email,omitempty" v:"required"`
}

type bindAndValidateTestCase struct {
	name        string
	body        string
	contentType string
	wantStatus  int
	wantJSON    string
}

func TestBindAndValidate(t *testing.T) {
	for _, test := range []bindAndValidateTestCase{
		{
			name: "valid request", body: `{"email":"alice@example.com"}`,
			contentType: echo.MIMEApplicationJSON, wantStatus: http.StatusNoContent,
		},
		{
			name: "missing field", body: `{}`, contentType: echo.MIMEApplicationJSON,
			wantStatus: http.StatusBadRequest,
			wantJSON: `{"type":"/validate","title":"Bad Request","status":400,"errors":[{
				"pointer":"#/validationRequest/email","detail":"email is a required field","code":"INVALID_VALUE"
			}]}`,
		},
		{
			name: "empty body", contentType: echo.MIMEApplicationJSON,
			wantStatus: http.StatusBadRequest,
			wantJSON: `{"type":"/validate","title":"Bad Request","status":400,"errors":[{
				"pointer":"#/validationRequest/email","detail":"email is a required field","code":"INVALID_VALUE"
			}]}`,
		},
		{
			name: "malformed JSON", body: `{"email":`, contentType: echo.MIMEApplicationJSON,
			wantStatus: http.StatusBadRequest,
			wantJSON:   `{"type":"/validate","title":"Bad Request","status":400}`,
		},
		{
			name: "wrong field type", body: `{"email":42}`, contentType: echo.MIMEApplicationJSON,
			wantStatus: http.StatusBadRequest,
			wantJSON:   `{"type":"/validate","title":"Bad Request","status":400}`,
		},
		{
			name: "unsupported content type", body: `{"email":"alice@example.com"}`, contentType: echo.MIMETextPlain,
			wantStatus: http.StatusBadRequest,
			wantJSON:   `{"type":"/validate","title":"Bad Request","status":400}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServer(t, WithBodyLimit(1024))
			t.Cleanup(server.shutdownCancel)
			accepted := false
			server.root.POST("/validate", func(c *echo.Context) error {
				var request validationRequest
				if err := BindAndValidate(c, &request); err != nil {
					// The error handler must also find field details through wrapped errors.
					return fmt.Errorf("validate request: %w", err)
				}
				accepted = true
				if request.Email != "alice@example.com" {
					t.Errorf("bound email = %q, want alice@example.com", request.Email)
				}
				return c.NoContent(http.StatusNoContent)
			})

			request := httptest.NewRequest(http.MethodPost, "/validate", strings.NewReader(test.body))
			request.Header.Set(echo.HeaderContentType, test.contentType)
			response := httptest.NewRecorder()
			server.e.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.wantStatus, response.Body.String())
			}
			if accepted != (test.wantStatus == http.StatusNoContent) {
				t.Errorf("request reached service code = %t", accepted)
			}
			if len(test.wantJSON) > 0 {
				if got := response.Header().Get(echo.HeaderContentType); got != MIMEApplicationProblemJSON {
					t.Errorf("content type = %q, want %s", got, MIMEApplicationProblemJSON)
				}
				assert.JSONEq(t, test.wantJSON, response.Body.String())
			} else if response.Body.Len() != 0 {
				t.Errorf("unexpected response body: %s", response.Body.String())
			}
		})
	}
}

func TestBindAndValidateRequiresRegisteredValidator(t *testing.T) {
	server := newTestServer(t, WithBodyLimit(1024))
	t.Cleanup(server.shutdownCancel)
	server.e.Validator = nil
	server.root.POST("/validate", func(c *echo.Context) error {
		var request validationRequest
		err := BindAndValidate(c, &request)
		if !errors.Is(err, echo.ErrValidatorNotRegistered) {
			t.Errorf("BindAndValidate() error = %v, want ErrValidatorNotRegistered", err)
		}
		return err
	})
	request := httptest.NewRequest(http.MethodPost, "/validate", strings.NewReader(`{"email":"alice@example.com"}`))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	response := httptest.NewRecorder()
	server.e.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	assert.JSONEq(t, `{"type":"/validate","title":"Internal HTTPServer Error","status":500}`, response.Body.String())
}
