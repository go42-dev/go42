package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go42-dev/go42/internal/metrics"
)

type errorHandlerTestCase struct {
	name       string
	err        error
	wantStatus int
	wantTitle  string
	wantLog    bool
}

type errorHandlerLog struct {
	Level   string `json:"level"`
	Message string `json:"msg"`
	Error   string `json:"error"`
	Status  int    `json:"status"`
	Method  string `json:"method"`
	URI     string `json:"uri"`
	Stack   string `json:"stack"`
}

func TestNewErrorHandlerSanitizesErrorsAndLogsServerFailures(t *testing.T) {
	for _, test := range []errorHandlerTestCase{
		{
			name: "plain error", err: errors.New("private storage failure"),
			wantStatus: http.StatusInternalServerError, wantTitle: "Internal HTTPServer Error", wantLog: true,
		},
		{
			name: "wrapped HTTP error",
			err: fmt.Errorf("request failed: %w",
				echo.NewHTTPError(http.StatusBadRequest, "private request detail").Wrap(errors.New("private cause"))),
			wantStatus: http.StatusBadRequest, wantTitle: "Bad Request",
		},
		{
			name:       "validation failure wrapped as an internal error",
			err:        echo.ErrInternalServerError.Wrap(NewValidator().Validate(&validationRequest{})),
			wantStatus: http.StatusInternalServerError, wantTitle: "Internal Server Error", wantLog: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			e := echo.New()
			e.HTTPErrorHandler = NewErrorHandler(slog.New(slog.NewJSONHandler(&logs, nil)))
			e.POST("/error", func(*echo.Context) error { return test.err })
			request := httptest.NewRequest(http.MethodPost, "/error?source=test", nil)
			response := httptest.NewRecorder()
			e.ServeHTTP(response, request)

			assert.Equal(t, test.wantStatus, response.Code)
			assert.Equal(t, MIMEApplicationProblemJSON, response.Header().Get(echo.HeaderContentType))
			assert.JSONEq(t, fmt.Sprintf(
				`{"type":"/error?source=test","title":%q,"status":%d}`, test.wantTitle, test.wantStatus,
			), response.Body.String())
			if !test.wantLog {
				assert.Empty(t, logs.String(), "client errors should not be logged as server failures")
				return
			}

			var entry errorHandlerLog
			require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
			assert.Equal(t, "ERROR", entry.Level)
			assert.Equal(t, "http api error", entry.Message)
			assert.Equal(t, test.err.Error(), entry.Error)
			assert.Equal(t, test.wantStatus, entry.Status)
			assert.Equal(t, http.MethodPost, entry.Method)
			assert.Equal(t, request.RequestURI, entry.URI)
			assert.Empty(t, entry.Stack)
		})
	}
}

func TestNewErrorHandlerTreatsRecoveredHTTPErrorAsPanic(t *testing.T) {
	var logs bytes.Buffer
	e := echo.New()
	e.HTTPErrorHandler = NewErrorHandler(slog.New(slog.NewJSONHandler(&logs, nil)))
	e.Use(middleware.RecoverWithConfig(middleware.RecoverConfig{DisableStackAll: true}))
	e.GET("/panic", func(*echo.Context) error {
		panic(echo.ErrBadRequest.Wrap(errors.New("private panic detail")))
	})
	panics := metrics.Counter("errors", map[string]any{"type": "http_api_panic"})
	before := panics.Get()
	request := httptest.NewRequest(http.MethodGet, "/panic", nil)
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)

	assert.Equal(t, http.StatusInternalServerError, response.Code)
	assert.JSONEq(t, `{"type":"/panic","title":"Internal HTTPServer Error","status":500}`, response.Body.String())
	assert.Equal(t, before+1, panics.Get())
	var entry errorHandlerLog
	require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
	assert.Equal(t, "ERROR", entry.Level)
	assert.Equal(t, "http api panic", entry.Message)
	assert.Equal(t, http.StatusInternalServerError, entry.Status)
	assert.Contains(t, entry.Error, "private panic detail")
	assert.NotEmpty(t, entry.Stack)
}

func TestNewErrorHandlerPreservesCommittedResponse(t *testing.T) {
	var logs bytes.Buffer
	e := echo.New()
	e.HTTPErrorHandler = NewErrorHandler(slog.New(slog.NewJSONHandler(&logs, nil)))
	e.GET("/committed", func(c *echo.Context) error {
		if err := c.String(http.StatusAccepted, "already sent"); err != nil {
			return err
		}
		return errors.New("failure after response")
	})
	request := httptest.NewRequest(http.MethodGet, "/committed", nil)
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)

	assert.Equal(t, http.StatusAccepted, response.Code)
	assert.Equal(t, "already sent", response.Body.String())
	assert.Contains(t, logs.String(), "failure after response")
}

type failingErrorResponseWriter struct {
	http.ResponseWriter
}

func (failingErrorResponseWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

func TestNewErrorHandlerLogsResponseWriteFailure(t *testing.T) {
	var logs bytes.Buffer
	e := echo.New()
	e.HTTPErrorHandler = NewErrorHandler(slog.New(slog.NewJSONHandler(&logs, nil)))
	e.GET("/error", func(*echo.Context) error { return echo.ErrBadRequest })
	request := httptest.NewRequest(http.MethodGet, "/error", nil)
	e.ServeHTTP(failingErrorResponseWriter{httptest.NewRecorder()}, request)

	var entry errorHandlerLog
	require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
	assert.Equal(t, "ERROR", entry.Level)
	assert.Equal(t, "failed to send json error response", entry.Message)
	assert.Equal(t, io.ErrClosedPipe.Error(), entry.Error)
}
