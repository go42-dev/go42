package http

import (
	"context"
	"log/slog"
	"net"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/go42-dev/go42/internal/cache"
	"github.com/go42-dev/go42/internal/tools"
)

type Option func(s *Server)

// WithLogger sets the logger. A nil logger uses a discard logger.
func WithLogger(logger *slog.Logger) Option {
	return func(s *Server) {
		s.l = logger
	}
}

// WithTracing enables/disables tracing.
func WithTracing(enabled bool) Option {
	return func(s *Server) {
		s.tracingEnabled = enabled
	}
}

// WithReadTimeout sets the read timeout.
func WithReadTimeout(d time.Duration) Option {
	return func(s *Server) {
		s.readTimeout = d
	}
}

// WithWriteTimeout sets the write timeout.
func WithWriteTimeout(d time.Duration) Option {
	return func(s *Server) {
		s.writeTimeout = d
	}
}

// WithGracefulTimeout sets how long Echo waits for active requests during shutdown.
func WithGracefulTimeout(d time.Duration) Option {
	return func(s *Server) {
		s.gracefulTimeout = d
	}
}

// WithStaticRoot sets the root directory for static files.
// An empty root disables static file serving, which is the default.
func WithStaticRoot(root string) Option {
	return func(s *Server) {
		s.staticRoot = root
	}
}

// WithSwaggerRoot sets the root for swagger files.
// An empty root disables Swagger, which is the default.
// @note given directory should contain folders: v1, v2, etc.
func WithSwaggerRoot(root string) Option {
	return func(s *Server) {
		s.swaggerRoot = root
	}
}

// WitHealthCheckCtx sets the health-check context.
// Once context is canceled, health-check will return error.
func WitHealthCheckCtx(ctx context.Context) Option {
	return func(s *Server) {
		go func() {
			<-ctx.Done()
			s.readyStatus.Store(ReadyStatusShuttingDown)
		}()
	}
}

// WithReadinessCheck sets the readiness check function.
func WithReadinessCheck(check func(context.Context) error) Option {
	return func(s *Server) {
		s.readyCheck = check
	}
}

// WithReadinessCheckTimeout sets the timeout for the readiness check function.
func WithReadinessCheckTimeout(timeout time.Duration) Option {
	return func(s *Server) {
		s.readyCheckTimeout = timeout
	}
}

// WithRateLimiter enables/disables rate limiting.
func WithRateLimiter(cacheEngine cache.Engine, rate int, burst int, ttl time.Duration) Option {
	return func(s *Server) {
		s.rateLimiter = tools.NewRateLimiter(cacheEngine, "http", rate, burst, ttl)
	}
}

// WithBodyLimit sets the maximum allowed size of the request body in bytes.
// The default is 1 MiB. Zero allows only empty request bodies.
func WithBodyLimit(limit int64) Option {
	return func(s *Server) {
		s.bodyLimit = limit
	}
}

// WithSwaggerDarkStyle enabled dark theme for swagger UI page
func WithSwaggerDarkStyle(enabled bool) Option {
	return func(s *Server) {
		s.swaggerDarkStyle = enabled
	}
}

// WithCORSAllowOrigins sets the allowed origins for CORS requests.
// Nil or empty origins disable CORS, which is the default.
// Any list containing "*" allows all origins without credentials.
func WithCORSAllowOrigins(allowedOrigins []string) Option {
	return func(s *Server) {
		s.allowOrigins = allowedOrigins
	}
}

// WithTrustedProxyCIDRs trusts `X-Forwarded-For` values received through the given proxy networks.
func WithTrustedProxyCIDRs(cidrs []string) Option {
	return func(s *Server) {
		if len(cidrs) == 0 {
			return
		}

		trustOptions := []echo.TrustOption{
			echo.TrustLoopback(false),
			echo.TrustLinkLocal(false),
			echo.TrustPrivateNet(false),
		}

		for _, cidr := range cidrs {
			_, network, err := net.ParseCIDR(cidr)
			if err != nil {
				continue
			}
			trustOptions = append(trustOptions, echo.TrustIPRange(network))
		}

		s.e.IPExtractor = echo.ExtractIPFromXFFHeader(trustOptions...)
	}
}
