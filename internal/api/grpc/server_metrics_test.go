package grpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	grpcpkg "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	testpb "google.golang.org/grpc/interop/grpc_testing"
	"google.golang.org/grpc/status"

	"github.com/go42-dev/go42/internal/metrics"
)

type grpcMetricsTestCase struct {
	name         string
	err          error
	panic        bool
	denied       bool
	limiterErr   error
	limiterPanic bool
	priority     int
	code         codes.Code
}

func TestGRPCMetricsMatchRPCOutcome(t *testing.T) {
	for _, test := range []grpcMetricsTestCase{
		{name: "success"},
		{name: "ordinary error", err: errors.New("application error"), code: codes.Unknown},
		{name: "wrapped ordinary error", err: fmt.Errorf("wrapped: %w", errors.New("failure")), code: codes.Unknown},
		{name: "canceled", err: context.Canceled, code: codes.Canceled},
		{name: "wrapped cancellation", err: fmt.Errorf("wrapped: %w", context.Canceled), code: codes.Canceled},
		{name: "deadline", err: context.DeadlineExceeded, code: codes.DeadlineExceeded},
		{
			name: "wrapped deadline", err: fmt.Errorf("wrapped: %w", context.DeadlineExceeded),
			code: codes.DeadlineExceeded,
		},
		{name: "status", err: status.Error(codes.PermissionDenied, "denied"), code: codes.PermissionDenied},
		{
			name: "wrapped status", err: fmt.Errorf("wrapped: %w", status.Error(codes.PermissionDenied, "denied")),
			code: codes.PermissionDenied,
		},
		{
			name: "status takes precedence over context",
			err:  errors.Join(status.Error(codes.PermissionDenied, "denied"), context.Canceled),
			code: codes.PermissionDenied,
		},
		{name: "application panic", panic: true, code: codes.Internal},
		{name: "limiter rejection", denied: true, code: codes.ResourceExhausted},
		{name: "limiter failure", limiterErr: errors.New("cache unavailable"), code: codes.Unavailable},
		{name: "limiter panic", limiterPanic: true, code: codes.Internal},
		{
			name: "preprocess rejection", priority: InterceptorPriorityPreprocess,
			err: status.Error(codes.Aborted, "preprocess rejection"), code: codes.Aborted,
		},
		{
			name: "preprocess panic", priority: InterceptorPriorityPreprocess,
			panic: true, code: codes.Internal,
		},
		{
			name: "authentication rejection", priority: InterceptorPriorityAuthentication,
			err: status.Error(codes.Unauthenticated, "authentication required"), code: codes.Unauthenticated,
		},
	} {
		for _, grpcType := range []string{"unary", "stream"} {
			t.Run(test.name+"/"+grpcType, func(t *testing.T) {
				method := testpb.TestService_EmptyCall_FullMethodName
				if grpcType == "stream" {
					method = testpb.TestService_StreamingOutputCall_FullMethodName
				}
				labels := map[string]any{"grpc_type": grpcType, "method": method}
				requests := metrics.Counter("application_grpc_requests_count", labels)
				labels["code"] = int(test.code)
				labels["status"] = test.code.String()
				labels["is_error"] = "yes"
				if test.code == codes.OK {
					labels["is_error"] = "no"
				}
				responses := metrics.Counter("application_grpc_responses_count", labels)
				invalidStatus := metrics.Counter("application_grpc_responses_count", map[string]any{
					"grpc_type": grpcType, "method": method, "code": 0, "status": "", "is_error": "yes",
				})
				panics := metrics.Counter("application_errors", map[string]any{"type": "grpc_panic"})
				beforeRequests, beforeResponses := requests.Get(), responses.Get()
				beforeLatency := grpcLatencyObservationCount(labels)
				beforeInvalidStatus, beforePanics := invalidStatus.Get(), panics.Get()

				var applicationCalls atomic.Int32
				outcome := func() error {
					applicationCalls.Add(1)
					if test.panic {
						panic("application panic")
					}
					return test.err
				}
				priority := test.priority
				if priority == 0 {
					priority = InterceptorPriorityBusinessLogic
				}
				server := New(func(s *Server) {
					s.rateLimiter = grpcContextTestLimiter(func(context.Context, string) (bool, error) {
						if test.limiterPanic {
							panic("limiter panic")
						}
						return !test.denied, test.limiterErr
					})
				}, WithUnaryInterceptor(
					priority,
					func(ctx context.Context, req any, _ *grpcpkg.UnaryServerInfo, next grpcpkg.UnaryHandler) (any, error) {
						if err := outcome(); err != nil {
							return nil, err
						}
						return next(ctx, req)
					},
				), WithStreamInterceptor(
					priority,
					func(srv any, stream grpcpkg.ServerStream, _ *grpcpkg.StreamServerInfo, next grpcpkg.StreamHandler) error {
						if err := outcome(); err != nil {
							return err
						}
						return next(srv, stream)
					},
				))
				testpb.RegisterTestServiceServer(server.grpcServer, grpcTestService{})
				client := testpb.NewTestServiceClient(newHealthRateLimitTestClient(t, server))
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				defer cancel()

				// Drain streaming calls to their terminal status, including successful EOF.
				// Context cases return application errors while the client context is live.
				var err error
				if grpcType == "stream" {
					var stream grpcpkg.ServerStreamingClient[testpb.StreamingOutputCallResponse]
					stream, err = client.StreamingOutputCall(ctx, &testpb.StreamingOutputCallRequest{})
					if err == nil {
						for err == nil {
							_, err = stream.Recv()
						}
						if errors.Is(err, io.EOF) {
							err = nil
						}
					}
				} else {
					_, err = client.EmptyCall(ctx, &testpb.Empty{})
				}
				require.Equal(t, test.code, status.Code(err), "client status: %v", err)

				requestDelta, responseDelta := requests.Get()-beforeRequests, responses.Get()-beforeResponses
				latencyDelta := grpcLatencyObservationCount(labels) - beforeLatency
				invalidStatusDelta := invalidStatus.Get() - beforeInvalidStatus
				t.Logf("wire=%s requests=%d responses=%d latency=%d invalid_status=%d",
					test.code, requestDelta, responseDelta, latencyDelta, invalidStatusDelta)
				assert.Equal(t, uint64(1), requestDelta, "one request per RPC")
				assert.Equal(t, uint64(1), responseDelta, "one response with the client status")
				assert.Equal(t, uint64(1), latencyDelta, "one latency observation with the client status")
				assert.Zero(t, invalidStatusDelta, "no empty status labels")
				wantPanics := uint64(0)
				if test.panic || test.limiterPanic {
					wantPanics = 1
				}
				assert.Equal(t, wantPanics, panics.Get()-beforePanics, "recovery owns panic accounting")
				wantApplicationCalls := int32(1)
				if test.denied || test.limiterErr != nil || test.limiterPanic {
					wantApplicationCalls = 0
				}
				assert.Equal(t, wantApplicationCalls, applicationCalls.Load(), "limiter controls admission")
			})
		}
	}
}

func grpcLatencyObservationCount(labels map[string]any) uint64 {
	var total uint64
	metrics.Histogram("application_grpc_latency_sec", labels).VisitNonZeroBuckets(func(_ string, count uint64) {
		total += count
	})
	return total
}
