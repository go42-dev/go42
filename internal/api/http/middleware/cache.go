package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
)

const (
	cacheHeaderName      = "X-Cache"
	cacheHeaderValueHit  = "HIT"
	cacheHeaderValueMISS = "MISS"
)

//go:generate go tool mockgen -source $GOFILE -package mocks -destination mocks/mocks.go

type cacheAccessor interface {
	Get(ctx context.Context, key string) (value string, found bool, err error)
	Set(ctx context.Context, key string, value string, ttl time.Duration) error
	Invalidate(ctx context.Context, key string) error
}

func CacheMiddleware(cache cacheAccessor, ttl time.Duration) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if ttl == 0 {
				return next(c)
			}

			key := fmt.Sprintf(
				"%s+%s?%s",
				c.Request().Method, c.Request().URL.Path, c.Request().URL.RawQuery,
			)

			cached, found, err := cache.Get(c.Request().Context(), key)
			if err != nil {
				slog.Default().
					With(slog.String("component", "echo-middleware-cache")).
					ErrorContext(
						c.Request().Context(),
						"failed to fetch cached data", slog.Any("error", err))
				return next(c)
			}

			if found {
				c.Response().Header().Add(cacheHeaderName, cacheHeaderValueHit)
				return c.JSONBlob(http.StatusOK, []byte(cached))
			}
			c.Response().Header().Add(cacheHeaderName, cacheHeaderValueMISS)

			resRecorder := newResponseRecorder(c.Response(), true)
			c.SetResponse(resRecorder)

			if err := next(c); err != nil {
				return err
			}
			if resRecorder.status != http.StatusOK {
				return nil
			}

			err = cache.Set(c.Request().Context(), key, resRecorder.body.String(), ttl)
			if err != nil {
				slog.Default().
					With(slog.String("component", "echo-middleware-cache")).
					ErrorContext(
						c.Request().Context(),
						"failed to cache response", slog.Any("error", err))
			}

			return nil
		}
	}
}
