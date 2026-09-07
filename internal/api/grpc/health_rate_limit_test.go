package grpc

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	grpcpkg "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	testpb "google.golang.org/grpc/interop/grpc_testing"
	reflectionpb "google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/go42-dev/go42/internal/cache/local"
	"github.com/go42-dev/go42/internal/tools"
)

func TestHealthAndReflectionBypassApplicationRateLimiter(t *testing.T) {
	for _, test := range []struct {
		name        string
		unavailable bool
		code        codes.Code
	}{
		{name: "exhausted_quota", code: codes.ResourceExhausted},
		{name: "unavailable_limiter", unavailable: true, code: codes.Unavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			cache := local.New()
			t.Cleanup(func() { require.NoError(t, cache.Shutdown(context.Background())) })
			server := New(WithReflection(true), func(s *Server) {
				// Keep the single-token budget exhausted for the duration of the test.
				s.rateLimiter = tools.NewRateLimiter(cache, "grpc", 1, 1, time.Hour,
					tools.WithRateLimitWindow(time.Hour))
				if test.unavailable {
					s.rateLimiter = grpcContextTestLimiter(func(context.Context, string) (bool, error) {
						return false, errors.New("cache unavailable")
					})
				}
			})
			testpb.RegisterTestServiceServer(server.grpcServer, grpcTestService{})
			conn := newHealthRateLimitTestClient(t, server)
			client := testpb.NewTestServiceClient(conn)
			healthClient := healthpb.NewHealthClient(conn)
			reflectionClient := reflectionpb.NewServerReflectionClient(conn)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			for range 2 {
				requireHealthProbes(t, ctx, healthClient, healthpb.HealthCheckResponse_SERVING)
				requireReflectionServices(t, ctx, reflectionClient)
			}
			if !test.unavailable {
				_, err := client.EmptyCall(ctx, &testpb.Empty{})
				require.NoError(t, err, "health and reflection requests must not consume the application budget")
			}
			_, err := client.EmptyCall(ctx, &testpb.Empty{})
			require.Equal(t, test.code, status.Code(err))
			stream, err := client.StreamingOutputCall(ctx, &testpb.StreamingOutputCallRequest{})
			if err == nil {
				_, err = stream.Recv()
			}
			require.Equal(t, test.code, status.Code(err))

			requireHealthProbes(t, ctx, healthClient, healthpb.HealthCheckResponse_SERVING)
			requireReflectionServices(t, ctx, reflectionClient)
			server.healthServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
			requireHealthProbes(t, ctx, healthClient, healthpb.HealthCheckResponse_NOT_SERVING)
			_, err = client.EmptyCall(ctx, &testpb.Empty{})
			require.Equal(t, test.code, status.Code(err))
		})
	}
}

func requireReflectionServices(t *testing.T, ctx context.Context, client reflectionpb.ServerReflectionClient) {
	t.Helper()
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := client.ServerReflectionInfo(streamCtx)
	require.NoError(t, err)
	require.NoError(t, stream.Send(&reflectionpb.ServerReflectionRequest{
		MessageRequest: &reflectionpb.ServerReflectionRequest_ListServices{ListServices: ""},
	}))
	require.NoError(t, stream.CloseSend())
	response, err := stream.Recv()
	require.NoError(t, err)
	services := response.GetListServicesResponse()
	require.NotNil(t, services)
	var names []string
	for _, service := range services.GetService() {
		names = append(names, service.GetName())
	}
	require.Contains(t, names, testpb.TestService_ServiceDesc.ServiceName)
	require.Contains(t, names, healthpb.Health_ServiceDesc.ServiceName)
}

func requireHealthProbes(
	t *testing.T,
	ctx context.Context,
	client healthpb.HealthClient,
	want healthpb.HealthCheckResponse_ServingStatus,
) {
	t.Helper()
	check, err := client.Check(ctx, &healthpb.HealthCheckRequest{})
	require.NoError(t, err, "health Check")
	require.Equal(t, want, check.GetStatus())
	list, err := client.List(ctx, &healthpb.HealthListRequest{})
	require.NoError(t, err, "health List")
	require.Contains(t, list.GetStatuses(), "")
	require.Equal(t, want, list.GetStatuses()[""].GetStatus())
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	watch, err := client.Watch(watchCtx, &healthpb.HealthCheckRequest{})
	require.NoError(t, err, "health Watch")
	update, err := watch.Recv()
	require.NoError(t, err, "health Watch response")
	require.Equal(t, want, update.GetStatus())
}

func newHealthRateLimitTestClient(t *testing.T, server *Server) *grpcpkg.ClientConn {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	served := make(chan error, 1)
	go func() { served <- server.grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		server.grpcServer.Stop()
		err := <-served
		if !errors.Is(err, grpcpkg.ErrServerStopped) {
			require.NoError(t, err)
		}
	})
	conn, err := grpcpkg.NewClient("passthrough:///health-rate-limit-test",
		grpcpkg.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpcpkg.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	return conn
}
