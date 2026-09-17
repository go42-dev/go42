package http

import (
	"github.com/labstack/echo/v5"
)

const MIMEApplicationProblemJSON = "application/problem+json"

// Error is JSON error described by RFC9457.
// @see https://datatracker.ietf.org/doc/html/rfc9457/
// @note subtle deviation from rfc - title and status are required.
type Error struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
	Errors   []any  `json:"errors,omitempty"`
}

func SendJSONError(ctx *echo.Context, status int, title string, opts ...ErrorOption) error {
	errorResponse := Error{
		Type:   ctx.Request().RequestURI,
		Title:  title,
		Status: status,
	}
	for _, opt := range opts {
		opt(&errorResponse)
	}
	ctx.Response().Header().Set(echo.HeaderContentType, MIMEApplicationProblemJSON)
	return ctx.JSON(errorResponse.Status, errorResponse)
}

// ---

type ErrorOption func(error *Error)

func WithDetail(detail string) ErrorOption {
	return func(error *Error) {
		error.Detail = detail
	}
}

func WithInstance(instance string) ErrorOption {
	return func(error *Error) {
		error.Instance = instance
	}
}

// ---

// responseValidationError renders validation errors as json.
// @see https://www.rfc-editor.org/rfc/rfc6901.html
type responseValidationError struct {
	Pointer string `json:"pointer,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Code    string `json:"code,omitempty"`
}

// validationErrorAccessor is an interface for accessing validation errors
// from validation providers, like go-playground/validator.
type validationErrorAccessor interface {
	Pointer() string
	Detail() string
	Code() string
}

// WithValidationErrors translates various interfaces to json errors.
func WithValidationErrors[T validationErrorAccessor](errs ...T) ErrorOption {
	return func(error *Error) {
		for _, err := range errs {
			ve := &responseValidationError{
				Pointer: err.Pointer(),
				Detail:  err.Detail(),
				Code:    err.Code(),
			}
			error.Errors = append(error.Errors, ve)
		}
	}
}
