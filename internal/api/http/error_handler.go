package http

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"

	"github.com/go42-dev/go42/internal/metrics"
)

// NewErrorHandler renders HTTP errors, including validation details, as problem JSON.
func NewErrorHandler(logger *slog.Logger) echo.HTTPErrorHandler {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return func(c *echo.Context, err error) {
		var (
			httpStatus  = http.StatusInternalServerError
			httpMessage = "Internal HTTPServer Error"
		)

		var (
			logMessage      = "http api error"
			metricErrorType = "http_api_error"
			panicStack      []byte
		)

		if panicErr := new(middleware.PanicStackError); errors.As(err, &panicErr) {
			logMessage = "http api panic"
			metricErrorType = "http_api_panic"
			panicStack = panicErr.Stack
		} else if code := echo.StatusCode(err); code != 0 {
			httpStatus = code
			httpMessage = http.StatusText(code)
		}

		if httpStatus >= 500 {
			metrics.Counter("errors", map[string]any{
				"type": metricErrorType,
			}).Inc()
			slogAttrs := []any{
				slog.String("error", err.Error()),
				slog.Int("status", httpStatus),
				slog.String("method", c.Request().Method),
				slog.String("uri", c.Request().RequestURI),
				slog.String("who", "echo.HTTPErrorHandler"),
			}
			if len(panicStack) > 0 {
				slogAttrs = append(slogAttrs, slog.String("stack", string(panicStack)))
			}
			logger.ErrorContext(c.Request().Context(), logMessage, slogAttrs...)
		}

		if r, _ := echo.UnwrapResponse(c.Response()); r != nil && r.Committed {
			return
		}

		var opts []ErrorOption
		var validationErr *validationError
		if httpStatus == http.StatusBadRequest && errors.As(err, &validationErr) {
			opts = append(opts, WithValidationErrors(validationErr.fields...))
		}

		if err := SendJSONError(c, httpStatus, httpMessage, opts...); err != nil {
			logger.ErrorContext(
				c.Request().Context(),
				"failed to send json error response", slog.Any("error", err))
		}
	}
}
