package http

import (
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/go42-dev/go42/internal/tools"
)

type validator struct{}

// NewValidator adapts the shared struct validator to Echo.
func NewValidator() echo.Validator {
	return validator{}
}

func (validator) Validate(target any) error {
	if fields := tools.ValidateStruct(target); len(fields) > 0 {
		return &validationError{fields: fields}
	}
	return nil
}

type validationError struct {
	fields []tools.ValidationError
}

func (*validationError) Error() string {
	return "request validation failed"
}

func (*validationError) StatusCode() int {
	return http.StatusBadRequest
}

// BindAndValidate binds request data and validates it with the registered Echo validator.
// Errors are returned for the HTTP error handler to render.
func BindAndValidate(c *echo.Context, target any) error {
	if err := c.Bind(target); err != nil {
		// Preserve the adapters' HTTP 400 response for all binding failures.
		return echo.ErrBadRequest.Wrap(err)
	}
	return c.Validate(target)
}
