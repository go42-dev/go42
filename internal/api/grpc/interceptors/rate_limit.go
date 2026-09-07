package interceptors

import (
	"context"
	"net"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type rateLimiterAcessor interface {
	Limit(ctx context.Context, key string) (bool, error)
}

const clientRateLimitNamespace = "grpc-client"

type ClientRateLimitKeyFunc func(ctx context.Context, target string, method string) string

type ClientRateLimiterOption func(*clientRateLimiterConfig)

type clientRateLimiterConfig struct {
	keyFunc ClientRateLimitKeyFunc
}

func WithClientRateLimitKeyFunc(keyFunc ClientRateLimitKeyFunc) ClientRateLimiterOption {
	return func(config *clientRateLimiterConfig) {
		if keyFunc != nil {
			config.keyFunc = keyFunc
		}
	}
}

// WithClientRateLimitScope replaces the transport target with a stable logical
// upstream name while retaining per-method buckets.
func WithClientRateLimitScope(scope string) ClientRateLimiterOption {
	return WithClientRateLimitKeyFunc(func(_ context.Context, _ string, method string) string {
		return buildClientRateLimitKey(scope, method)
	})
}

func UnaryServerRateLimiterInterceptor(limiter rateLimiterAcessor) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		if limiter == nil || DefaultSkipper(info.FullMethod) {
			return handler(ctx, req)
		}
		allowed, err := limiter.Limit(ctx, extractRateLimitKeyFromCtx(ctx))
		if err != nil {
			return nil, status.Error(codes.Unavailable, "rate limiter unavailable")
		}
		if !allowed {
			return nil, status.Errorf(codes.ResourceExhausted, "rate limit exceeded")
		}
		return handler(ctx, req)
	}
}

func StreamServerRateLimiterInterceptor(limiter rateLimiterAcessor) grpc.StreamServerInterceptor {
	return func(
		srv interface{},
		stream grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		if limiter == nil || DefaultSkipper(info.FullMethod) {
			return handler(srv, stream)
		}
		allowed, err := limiter.Limit(stream.Context(), extractRateLimitKeyFromCtx(stream.Context()))
		if err != nil {
			return status.Error(codes.Unavailable, "rate limiter unavailable")
		}
		if !allowed {
			return status.Errorf(codes.ResourceExhausted, "rate limit exceeded")
		}
		return handler(srv, stream)
	}
}

func UnaryClientRateLimiterInterceptor(
	limiter rateLimiterAcessor,
	opts ...ClientRateLimiterOption,
) grpc.UnaryClientInterceptor {
	config := newClientRateLimiterConfig(opts...)

	return func(
		ctx context.Context,
		method string,
		req, reply interface{},
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		if limiter == nil {
			return invoker(ctx, method, req, reply, cc, opts...)
		}
		key := config.keyFunc(ctx, cc.CanonicalTarget(), method)
		allowed, err := limiter.Limit(ctx, key)
		if err != nil {
			return status.Error(codes.Unavailable, "rate limiter unavailable")
		}
		if !allowed {
			return status.Errorf(codes.ResourceExhausted, "rate limit exceeded")
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func StreamClientRateLimiterInterceptor(
	limiter rateLimiterAcessor,
	opts ...ClientRateLimiterOption,
) grpc.StreamClientInterceptor {
	config := newClientRateLimiterConfig(opts...)

	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		if limiter == nil {
			return streamer(ctx, desc, cc, method, opts...)
		}
		key := config.keyFunc(ctx, cc.CanonicalTarget(), method)
		allowed, err := limiter.Limit(ctx, key)
		if err != nil {
			return nil, status.Error(codes.Unavailable, "rate limiter unavailable")
		}
		if !allowed {
			return nil, status.Errorf(codes.ResourceExhausted, "rate limit exceeded")
		}
		return streamer(ctx, desc, cc, method, opts...)
	}
}

func newClientRateLimiterConfig(opts ...ClientRateLimiterOption) clientRateLimiterConfig {
	config := clientRateLimiterConfig{keyFunc: defaultClientRateLimitKey}
	for _, opt := range opts {
		if opt != nil {
			opt(&config)
		}
	}
	return config
}

func defaultClientRateLimitKey(_ context.Context, target string, method string) string {
	return buildClientRateLimitKey(target, method)
}

func buildClientRateLimitKey(scope string, method string) string {
	return strings.Join([]string{clientRateLimitNamespace, scope, method}, "|")
}

func extractRateLimitKeyFromCtx(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return ""
	}

	if tcpAddr, ok := p.Addr.(*net.TCPAddr); ok {
		return tcpAddr.IP.String()
	}

	host, _, err := net.SplitHostPort(p.Addr.String())
	if err == nil {
		return host
	}

	return p.Addr.String()
}
