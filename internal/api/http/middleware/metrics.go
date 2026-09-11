package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/go42-dev/go42/internal/metrics"
)

func NewMetricsCollector() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if DefaultSkipper(c) {
				return next(c)
			}

			start := time.Now()

			labels := map[string]interface{}{
				"method": normalizeHTTPMethod(c.Request().Method),
				"path":   c.Path(),
			}

			metrics.Counter("application_http_requests_count", labels).Inc()

			err := next(c)

			duration := time.Since(start).Seconds()

			_, status := echo.ResolveResponseStatus(c.Response(), err)
			labels["status"] = strconv.Itoa(status)
			labels["is_error"] = toStringBool(status >= http.StatusBadRequest)

			metrics.Counter("application_http_responses_count", labels).Inc()
			metrics.Histogram("application_http_latency_sec", labels).Update(duration)

			return err
		}
	}
}

// normalizeHTTPMethod keeps metric labels bounded for arbitrary request methods.
func normalizeHTTPMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace:
		return method
	default:
		return "_OTHER"
	}
}

func toStringBool(is bool) string {
	if is {
		return "yes"
	}
	return "no"
}
