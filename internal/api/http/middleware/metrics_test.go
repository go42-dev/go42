package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	vmetrics "github.com/VictoriaMetrics/metrics"
	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go42-dev/go42/internal/metrics"
)

type metricsStatusTestCase struct {
	name       string
	handler    echo.HandlerFunc
	wantStatus int
	wantError  string
}

func TestMetricsCollectorPreservesKnownMethods(t *testing.T) {
	const path = "/metrics-test/known-methods"
	methods := []string{
		http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace,
	}
	e := echo.New()
	e.Use(NewMetricsCollector())
	var receivedMethod string
	e.Match(methods, path, func(c *echo.Context) error {
		receivedMethod = c.Request().Method
		return c.NoContent(http.StatusNoContent)
	})

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			before := readHTTPMetricCounts(method, path, http.StatusNoContent)
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Method = method
			response := httptest.NewRecorder()
			e.ServeHTTP(response, request)

			require.Equal(t, http.StatusNoContent, response.Code)
			assert.Equal(t, method, receivedMethod)
			assertHTTPMetricIncrease(t, before, readHTTPMetricCounts(method, path, http.StatusNoContent), 1)
		})
	}
}

func TestMetricsCollectorBoundsUnknownMethods(t *testing.T) {
	const path = "/metrics-test/unknown-methods"
	e := echo.New()
	e.Use(NewMetricsCollector())
	var receivedMethod string
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			receivedMethod = c.Request().Method
			return next(c)
		}
	})
	e.GET(path, func(c *echo.Context) error { return c.NoContent(http.StatusNoContent) })

	methods := []string{"get", "Post", "PROPFIND", "QUERY"}
	for i := range 100 {
		methods = append(methods, fmt.Sprintf("CUSTOM_%d", i))
	}
	before := readHTTPMetricCounts("_OTHER", path, http.StatusMethodNotAllowed)
	registeredBefore := registeredHTTPMetricCount()
	for _, method := range methods {
		request := httptest.NewRequest(method, path, nil)
		response := httptest.NewRecorder()
		e.ServeHTTP(response, request)

		require.Equal(t, http.StatusMethodNotAllowed, response.Code, method)
		assert.Equal(t, method, receivedMethod, "downstream middleware must receive the original method")
	}

	assertHTTPMetricIncrease(t, before,
		readHTTPMetricCounts("_OTHER", path, http.StatusMethodNotAllowed), uint64(len(methods)))
	assert.Equal(t, registeredBefore, registeredHTTPMetricCount(), "custom methods must reuse registered metrics")
}

func TestMetricsCollectorBoundsPaths(t *testing.T) {
	for _, test := range []struct {
		name   string
		path   string
		target string
		status int
	}{
		{
			name: "parameters", path: "/metrics-test/parameters/:id",
			target: "/metrics-test/parameters/%d", status: http.StatusNoContent,
		},
		{
			name: "query strings", path: "/metrics-test/query",
			target: "/metrics-test/query?value=%d", status: http.StatusNoContent,
		},
		{
			name: "wildcard", path: "/metrics-test/wildcard/*",
			target: "/metrics-test/wildcard/subdir/file-%d.txt", status: http.StatusNoContent,
		},
		{
			name: "unmatched", path: "",
			target: "/metrics-test/unmatched/%d", status: http.StatusNotFound,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := echo.New()
			e.Use(NewMetricsCollector())
			if test.path != "" {
				e.GET(test.path, func(c *echo.Context) error { return c.NoContent(http.StatusNoContent) })
			}
			before := readHTTPMetricCounts(http.MethodGet, test.path, test.status)
			registeredBefore := registeredHTTPMetricCount()
			for i := range 100 {
				request := httptest.NewRequest(http.MethodGet, fmt.Sprintf(test.target, i), nil)
				response := httptest.NewRecorder()
				e.ServeHTTP(response, request)
				require.Equal(t, test.status, response.Code)
			}

			assertHTTPMetricIncrease(t, before, readHTTPMetricCounts(http.MethodGet, test.path, test.status), 100)
			assert.Equal(t, registeredBefore, registeredHTTPMetricCount(), "URLs must reuse route metrics")
		})
	}
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
			name: "implicit-write", wantStatus: http.StatusOK, wantError: "no",
			handler: func(c *echo.Context) error {
				_, err := c.Response().Write([]byte("ok"))
				return err
			},
		},
		{
			name: "explicit-client-error", wantStatus: http.StatusBadRequest, wantError: "yes",
			handler: func(c *echo.Context) error { return c.NoContent(http.StatusBadRequest) },
		},
		{
			name: "explicit-server-error", wantStatus: http.StatusInternalServerError, wantError: "yes",
			handler: func(c *echo.Context) error { return c.NoContent(http.StatusInternalServerError) },
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
			name: "committed-response", wantStatus: http.StatusAccepted, wantError: "no",
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

func TestMetricsCollectorPassesThroughErrors(t *testing.T) {
	const path = "/metrics-test/pass-through"
	e := echo.New()
	e.HTTPErrorHandler = func(*echo.Context, error) {
		t.Error("metrics must not invoke the error handler")
	}
	response := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, path, nil), response)
	c.SetPath(path)
	wantErr := errors.New("request failure")
	handler := NewMetricsCollector()(func(*echo.Context) error { return wantErr })
	if err := handler(c); !errors.Is(err, wantErr) {
		t.Errorf("returned error = %v, want the original error %v", err, wantErr)
	}
}

type httpMetricCounts struct {
	requests     uint64
	responses    uint64
	observations uint64
}

func readHTTPMetricCounts(method, path string, status int) httpMetricCounts {
	labels := map[string]any{"method": method, "path": path}
	counts := httpMetricCounts{requests: metrics.Counter("application_http_requests_count", labels).Get()}
	labels["status"] = strconv.Itoa(status)
	labels["is_error"] = "no"
	if status >= http.StatusBadRequest {
		labels["is_error"] = "yes"
	}
	counts.responses = metrics.Counter("application_http_responses_count", labels).Get()
	metrics.Histogram("application_http_latency_sec", labels).VisitNonZeroBuckets(func(_ string, count uint64) {
		counts.observations += count
	})
	return counts
}

func assertHTTPMetricIncrease(t *testing.T, before, after httpMetricCounts, count uint64) {
	t.Helper()
	assert.Equal(t, before.requests+count, after.requests, "request counter")
	assert.Equal(t, before.responses+count, after.responses, "response counter")
	assert.Equal(t, before.observations+count, after.observations, "latency observations")
}

func registeredHTTPMetricCount() int {
	var count int
	for _, name := range vmetrics.ListMetricNames() {
		if strings.HasPrefix(name, "application_http_") {
			count++
		}
	}
	return count
}
