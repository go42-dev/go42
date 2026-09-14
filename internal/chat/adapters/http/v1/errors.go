package adapter

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v5"

	httpAPI "github.com/go42-dev/go42/internal/api/http"
	"github.com/go42-dev/go42/internal/chat/domain"
)

func (a *Adapter) processError(ctx *echo.Context, err error) error {
	switch {
	case errors.Is(err, domain.ErrEntityNotFound):
		return httpAPI.SendJSONError(ctx, http.StatusNotFound, http.StatusText(http.StatusNotFound))
	case errors.Is(err, domain.ErrInvalidInput):
		return httpAPI.SendJSONError(ctx, http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
	default:
		return echo.ErrInternalServerError.Wrap(err)
	}
}
