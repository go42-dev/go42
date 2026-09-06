package adapter

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v5"

	httpAPI "github.com/go42-dev/go42/internal/api/http"
	"github.com/go42-dev/go42/internal/auth/domain"
)

func (a *Adapter) processError(ctx *echo.Context, err error) error {
	switch {
	case errors.Is(err, domain.ErrEntityNotFound):
		return httpAPI.SendJSONError(ctx,
			http.StatusNotFound, http.StatusText(http.StatusNotFound))
	case errors.Is(err, domain.ErrUserAlreadyExists):
		return httpAPI.SendJSONError(ctx,
			http.StatusConflict, http.StatusText(http.StatusConflict))
	case errors.Is(err, domain.ErrInvalidCredentials):
		return httpAPI.SendJSONError(ctx,
			http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
	case errors.Is(err, domain.ErrPasswordWeak):
		return httpAPI.SendJSONError(ctx,
			http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
	case errors.Is(err, domain.ErrInvalidPagination):
		return httpAPI.SendJSONError(ctx,
			http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
	case errors.Is(err, domain.ErrRateLimited):
		return httpAPI.SendJSONError(ctx,
			http.StatusTooManyRequests, http.StatusText(http.StatusTooManyRequests))
	case errors.Is(err, domain.ErrAuthenticationUnavailable):
		return httpAPI.SendJSONError(ctx,
			http.StatusServiceUnavailable, http.StatusText(http.StatusServiceUnavailable))
	case errors.Is(err, domain.ErrInvalidToken):
		return httpAPI.SendJSONError(ctx,
			http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized))
	default:
		return echo.ErrInternalServerError.Wrap(err)
	}
}
