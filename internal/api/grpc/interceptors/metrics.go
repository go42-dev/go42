package interceptors

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/go42-dev/go42/internal/metrics"
)

func UnaryMetricsInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		labels := map[string]interface{}{
			"grpc_type": "unary",
			"method":    info.FullMethod,
		}

		metrics.Counter("application_grpc_requests_count", labels).Inc()

		start := time.Now()
		resp, err := handler(ctx, req)
		reqDuration := time.Since(start).Seconds()

		code := grpcStatusCode(err)
		labels["code"] = int(code)
		labels["status"] = code.String()
		labels["is_error"] = toStringBool(err != nil)

		metrics.Counter("application_grpc_responses_count", labels).Inc()
		metrics.Histogram("application_grpc_latency_sec", labels).Update(reqDuration)

		return resp, err
	}
}

func StreamMetricsInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		labels := map[string]interface{}{
			"grpc_type": "stream",
			"method":    info.FullMethod,
		}

		metrics.Counter("application_grpc_requests_count", labels).Inc()

		start := time.Now()
		err := handler(srv, ss)
		reqDuration := time.Since(start).Seconds()

		code := grpcStatusCode(err)
		labels["code"] = int(code)
		labels["status"] = code.String()
		labels["is_error"] = toStringBool(err != nil)

		metrics.Counter("application_grpc_responses_count", labels).Inc()
		metrics.Histogram("application_grpc_latency_sec", labels).Update(reqDuration)

		return err
	}
}

// grpcStatusCode matches grpc-go's conversion of application errors.
func grpcStatusCode(err error) codes.Code {
	s, ok := status.FromError(err)
	if !ok {
		s = status.FromContextError(err)
	}
	return s.Code()
}

func toStringBool(is bool) string {
	if is {
		return "yes"
	}
	return "no"
}
