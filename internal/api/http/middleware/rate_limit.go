package middleware

import (
	"context"
	"fmt"

	"github.com/labstack/echo/v5"
)

type rateLimiterAcessor interface {
	Limit(ctx context.Context, key string) (bool, error)
}

func NewRateLimiter(limiter rateLimiterAcessor) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if DefaultSkipper(c) {
				return next(c)
			}
			if limiter == nil {
				return next(c)
			}
			allowed, err := limiter.Limit(c.Request().Context(), extractRateLimitKeyFromCtx(c))
			if err != nil {
				return fmt.Errorf("rate limiter unavailable: %w", err)
			}
			if !allowed {
				return echo.ErrTooManyRequests
			}
			return next(c)
		}
	}
}

func extractRateLimitKeyFromCtx(c *echo.Context) string {
	return c.RealIP()
}
