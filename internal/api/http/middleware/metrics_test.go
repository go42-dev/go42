package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/go42-dev/go42/internal/metrics"
)

type metricsStatusTestCase struct {
	name       string
	handler    echo.HandlerFunc
	wantStatus int
	wantError  string
}

func TestMetricsCollectorRecordsResponseStatus(t *testing.T) {
	for _, test := range []metricsStatusTestCase{
		{
			name: "success", wantStatus: http.StatusCreated, wantError: "no",
			handler: func(c *echo.Context) error { return c.NoContent(http.StatusCreated) },
		},
		{
			name: "implicit-success", wantStatus: http.StatusOK, wantError: "no",
			handler: func(*echo.Context) error { return nil },
		},
		{
			name: "deferred-validation-error", wantStatus: http.StatusBadRequest, wantError: "yes",
			handler: func(*echo.Context) error { return echo.ErrBadRequest.Wrap(errors.New("invalid request")) },
		},
		{
			name: "deferred-internal-error", wantStatus: http.StatusInternalServerError, wantError: "yes",
			handler: func(*echo.Context) error { return errors.New("private storage failure") },
		},
		{
			name: "committed-response", wantStatus: http.StatusAccepted, wantError: "yes",
			handler: func(c *echo.Context) error {
				if err := c.NoContent(http.StatusAccepted); err != nil {
					return err
				}
				return echo.ErrBadRequest
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := echo.New()
			e.Use(NewMetricsCollector())
			path := "/metrics-test/" + test.name
			e.GET(path, test.handler)
			counter := metrics.Counter("application_http_responses_count", map[string]any{
				"method": http.MethodGet, "path": path,
				"status": strconv.Itoa(test.wantStatus), "is_error": test.wantError,
			})
			before := counter.Get()
			request := httptest.NewRequest(http.MethodGet, path, nil)
			response := httptest.NewRecorder()
			e.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			if got := counter.Get(); got != before+1 {
				t.Errorf("response counter for HTTP %d = %d, want %d", test.wantStatus, got, before+1)
			}
		})
	}
}
