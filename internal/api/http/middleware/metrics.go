package middleware

import (
	"strconv"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/go42-dev/go42/internal/metrics"
)

func NewMetricsCollector() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) (returnErr error) {
			if DefaultSkipper(c) {
				return next(c)
			}

			start := time.Now()

			labels := map[string]interface{}{
				"method": c.Request().Method,
				"path":   c.Path(),
			}

			metrics.Counter("application_http_requests_count", labels).Inc()

			err := next(c)

			duration := time.Since(start).Seconds()

			_, status := echo.ResolveResponseStatus(c.Response(), err)
			labels["status"] = strconv.Itoa(status)
			labels["is_error"] = toStringBool(err != nil)

			metrics.Counter("application_http_responses_count", labels).Inc()
			metrics.Histogram("application_http_latency_sec", labels).Update(duration)

			return err
		}
	}
}

func toStringBool(is bool) string {
	if is {
		return "yes"
	}
	return "no"
}
