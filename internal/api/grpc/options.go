package grpc

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"

	"github.com/go42-dev/go42/internal/cache"
	"github.com/go42-dev/go42/internal/tools"
)

type Option func(*Server)

// WithLogger sets the logger.
func WithLogger(logger *slog.Logger) Option {
	return func(s *Server) {
		s.logger = logger
	}
}

// WithTLSEnabled enables or disables TLS.
func WithTLSEnabled(enabled bool) Option {
	return func(s *Server) {
		s.tlsEnabled = enabled
	}
}

// WithTLSConfig sets certificate files, loaded by New when TLS is enabled.
// Providing a client CA file requires verified client certificates for mutual TLS.
func WithTLSConfig(clientCAFile, certFile, keyFile string) Option {
	return func(s *Server) {
		s.tlsOpts.CAFile = clientCAFile
		s.tlsOpts.CertFile = certFile
		s.tlsOpts.KeyFile = keyFile
	}
}

// WithTracing enables/disables tracing.
func WithTracing(enabled bool) Option {
	return func(s *Server) {
		s.tracingEnabled = enabled
	}
}

// WithMaxRecvMsgSize sets the maximum receive message size.
func WithMaxRecvMsgSize(size int) Option {
	return func(s *Server) {
		s.maxRecvMsgSize = size
	}
}

// WithMaxSendMsgSize sets the maximum send message size.
func WithMaxSendMsgSize(size int) Option {
	return func(s *Server) {
		s.maxSendMsgSize = size
	}
}

// WithReadinessContext sets the parent context for the readiness monitor.
// Cancellation stops the monitor and sets the gRPC health status to NOT_SERVING.
func WithReadinessContext(ctx context.Context) Option {
	return func(s *Server) {
		s.readinessCtx = ctx
	}
}

func WithReadinessCheck(check func(context.Context) error) Option {
	return func(s *Server) {
		s.readyCheck = check
	}
}

func WithReadinessCheckTimeout(timeout time.Duration) Option {
	return func(s *Server) {
		s.readyCheckTimeout = timeout
	}
}

func WithReadinessCheckInterval(interval time.Duration) Option {
	return func(s *Server) {
		s.readyCheckInterval = interval
	}
}

// WithReflection enables/disables reflection.
func WithReflection(enabled bool) Option {
	return func(s *Server) {
		s.withReflection = enabled
	}
}

// WithRateLimiter enables/disables rate limiting.
func WithRateLimiter(cacheEngine cache.Engine, rate int, burst int, ttl time.Duration) Option {
	return func(s *Server) {
		s.rateLimiter = tools.NewRateLimiter(cacheEngine, "grpc", rate, burst, ttl)
	}
}

// WithUnaryInterceptor adds a unary interceptor with a specific priority.
func WithUnaryInterceptor(priority int, interceptor grpc.UnaryServerInterceptor) Option {
	return func(s *Server) {
		s.extraUnaryInterceptors[priority] = append(s.extraUnaryInterceptors[priority], interceptor)
	}
}

// WithStreamInterceptor adds a stream interceptor with a specific priority.
func WithStreamInterceptor(priority int, interceptor grpc.StreamServerInterceptor) Option {
	return func(s *Server) {
		s.extraStreamInterceptors[priority] = append(s.extraStreamInterceptors[priority], interceptor)
	}
}
