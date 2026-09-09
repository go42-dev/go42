package grpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	grpcpkg "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	testpb "google.golang.org/grpc/interop/grpc_testing"
	"google.golang.org/grpc/metadata"
	reflectionpb "google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/go42-dev/go42/internal/cache/local"
	"github.com/go42-dev/go42/internal/tools"
)

type grpcRequestLoggingTest struct {
	name      string
	streaming bool
	requestID string
	limited   bool
	panic     bool
	code      codes.Code
}

type grpcContextTestLimiter func(context.Context, string) (bool, error)

func (fn grpcContextTestLimiter) Limit(ctx context.Context, key string) (bool, error) {
	return fn(ctx, key)
}

type grpcTestService struct {
	testpb.UnimplementedTestServiceServer
}

func (grpcTestService) EmptyCall(context.Context, *testpb.Empty) (*testpb.Empty, error) {
	return &testpb.Empty{}, nil
}

func (grpcTestService) StreamingOutputCall(
	_ *testpb.StreamingOutputCallRequest,
	stream grpcpkg.ServerStreamingServer[testpb.StreamingOutputCallResponse],
) error {
	return stream.Send(&testpb.StreamingOutputCallResponse{})
}

func TestGRPCLogsRequestIDsForUnaryAndStreamCalls(t *testing.T) {
	for _, test := range []grpcRequestLoggingTest{
		{name: "unary", requestID: "request-42"},
		{name: "unary empty ID"},
		{name: "stream", streaming: true, requestID: "request-42"},
		{name: "stream empty ID", streaming: true},
		{name: "unary rate limit", requestID: "request-42", limited: true, code: codes.ResourceExhausted},
		{name: "stream rate limit", requestID: "request-42", streaming: true, limited: true, code: codes.ResourceExhausted},
		{name: "unary panic", requestID: "request-42", panic: true, code: codes.Internal},
		{name: "stream panic", requestID: "request-42", streaming: true, panic: true, code: codes.Internal},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := slog.New(tools.SlogContextWrapper(slog.NewJSONHandler(&output, &slog.HandlerOptions{
				Level: slog.LevelDebug,
			})))
			server, err := New(WithLogger(logger),
				func(s *Server) {
					s.rateLimiter = grpcContextTestLimiter(func(ctx context.Context, _ string) (bool, error) {
						logger.InfoContext(ctx, "limiter")
						return !test.limited, nil
					})
				},
				WithUnaryInterceptor(
					InterceptorPriorityBusinessLogic,
					func(ctx context.Context, req any, info *grpcpkg.UnaryServerInfo, next grpcpkg.UnaryHandler) (any, error) {
						logger.InfoContext(ctx, "application")
						if test.panic {
							panic("test panic")
						}
						return next(ctx, req)
					},
				),
				WithStreamInterceptor(
					InterceptorPriorityBusinessLogic,
					func(srv any, stream grpcpkg.ServerStream, info *grpcpkg.StreamServerInfo, next grpcpkg.StreamHandler) error {
						logger.InfoContext(stream.Context(), "application")
						if test.panic {
							panic("test panic")
						}
						return next(srv, stream)
					},
				),
			)
			require.NoError(t, err)
			testpb.RegisterTestServiceServer(server.grpcServer, grpcTestService{})
			listener := bufconn.Listen(1024 * 1024)
			served := make(chan error, 1)
			go func() { served <- server.grpcServer.Serve(listener) }()
			t.Cleanup(server.grpcServer.Stop)
			conn, err := grpcpkg.NewClient("passthrough:///context-test",
				grpcpkg.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
					return listener.DialContext(ctx)
				}),
				grpcpkg.WithTransportCredentials(insecure.NewCredentials()),
			)
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("x-request-id", test.requestID))
			client := testpb.NewTestServiceClient(conn)
			if test.streaming {
				stream, streamErr := client.StreamingOutputCall(ctx, &testpb.StreamingOutputCallRequest{})
				if streamErr == nil {
					_, streamErr = stream.Recv()
				}
				assert.Equal(t, test.code, status.Code(streamErr))
			} else {
				_, err = client.EmptyCall(ctx, &testpb.Empty{})
				assert.Equal(t, test.code, status.Code(err))
			}
			cancel()
			require.NoError(t, conn.Close())
			shutdownCtx, stop := context.WithTimeout(t.Context(), time.Second)
			defer stop()
			require.NoError(t, server.Shutdown(shutdownCtx))
			require.NoError(t, <-served)

			raw := output.String()
			decoder := json.NewDecoder(&output)
			requestID := test.requestID
			started, finished, records := 0, 0, 0
			for {
				var entry map[string]any
				err := decoder.Decode(&entry)
				if errors.Is(err, io.EOF) {
					break
				}
				require.NoError(t, err)
				id, ok := entry["request_id"].(string)
				require.True(t, ok, entry["msg"])
				if len(requestID) == 0 {
					_, err := uuid.Parse(id)
					require.NoError(t, err)
					requestID = id
				}
				assert.Equal(t, requestID, id, entry["msg"])
				switch entry["msg"] {
				case "started call":
					started++
				case "finished call":
					finished++
				}
				records++
			}
			wantStarted := 1
			if test.streaming && test.code != codes.OK {
				// Streaming logs start only after the first message is sent or received.
				wantStarted = 0
			}
			assert.Equal(t, wantStarted, started)
			assert.Equal(t, 1, finished)
			assert.Equal(t, records, strings.Count(raw, `"request_id":`))
		})
	}
}

func TestHealthMonitorOptionsAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name        string
		withContext bool
		nilContext  bool
		withCheck   bool
	}{
		{name: "neither option"},
		{name: "context only", withContext: true},
		{name: "readiness only", withCheck: true},
		{name: "both options", withContext: true, withCheck: true},
		{name: "explicit nil context", nilContext: true},
		{name: "readiness with explicit nil context", nilContext: true, withCheck: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				healthCtx, cancelHealth := context.WithCancel(t.Context())
				defer cancelHealth()
				checkStarted, checkCanceled := false, false
				opts := []Option{
					WithReadinessCheckInterval(time.Second),
					WithReadinessCheckTimeout(time.Minute),
				}
				if test.withContext {
					opts = append(opts, WitHealthCheckCtx(healthCtx))
				}
				if test.nilContext {
					//nolint:staticcheck // SA1012: deliberately test the optional nil context.
					opts = append(opts, WitHealthCheckCtx(nil))
				}
				if test.withCheck {
					opts = append(opts, WithReadinessCheck(func(ctx context.Context) error {
						checkStarted = true
						<-ctx.Done()
						checkCanceled = errors.Is(ctx.Err(), context.Canceled)
						return ctx.Err()
					}))
				}

				server, err := New(opts...)
				require.NoError(t, err)
				defer server.grpcServer.Stop()
				if server.healthMonitorCancel != nil {
					defer server.healthMonitorCancel()
				}
				require.Equal(t, test.withContext || test.withCheck, server.healthMonitorCancel != nil)

				if test.withCheck {
					time.Sleep(time.Second)
					synctest.Wait()
					require.True(t, checkStarted, "readiness check must run")
				}
				if test.withContext {
					cancelHealth()
					synctest.Wait()
					waitForHealthStatus(t, server, healthpb.HealthCheckResponse_NOT_SERVING)
					if test.withCheck {
						require.True(t, checkCanceled, "health context cancellation must stop the running check")
					}
				}

				require.NoError(t, server.Shutdown(t.Context()))
				synctest.Wait()
				if test.withCheck {
					require.True(t, checkCanceled, "shutdown must stop the running check")
				}
			})
		})
	}
}

func TestHealthStatusTracksDependencyAvailability(t *testing.T) {
	healthCtx, cancelHealth := context.WithCancel(context.Background())
	defer cancelHealth()
	dependencyUnavailable := new(atomic.Bool)
	server, err := New(
		WithLogger(slog.New(slog.DiscardHandler)),
		WitHealthCheckCtx(healthCtx),
		WithReadinessCheck(func(context.Context) error {
			if dependencyUnavailable.Load() {
				return errors.New("dependency unavailable")
			}
			return nil
		}),
		WithReadinessCheckInterval(5*time.Millisecond),
	)
	require.NoError(t, err)

	waitForHealthStatus(t, server, healthpb.HealthCheckResponse_SERVING)

	dependencyUnavailable.Store(true)
	waitForHealthStatus(t, server, healthpb.HealthCheckResponse_NOT_SERVING)

	dependencyUnavailable.Store(false)
	waitForHealthStatus(t, server, healthpb.HealthCheckResponse_SERVING)

	cancelHealth()
	waitForHealthStatus(t, server, healthpb.HealthCheckResponse_NOT_SERVING)
}

func TestPanicRecoveryDoesNotExposePanicDetails(t *testing.T) {
	server, err := New(WithLogger(slog.New(slog.DiscardHandler)))
	require.NoError(t, err)
	err = server.handlePanic(t.Context(), "database password: secret")

	if got := status.Code(err); got != codes.Internal {
		t.Errorf("panic status code = %s, want %s", got, codes.Internal)
	}
	if got := status.Convert(err).Message(); got != "internal server error" {
		t.Errorf("panic status message = %q, want %q", got, "internal server error")
	}
}

func TestShutdownForcesGRPCServerAfterDeadline(t *testing.T) {
	server, err := New(WithLogger(slog.New(slog.DiscardHandler)))
	require.NoError(t, err)
	listener := bufconn.Listen(1024 * 1024)
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.grpcServer.Serve(listener)
	}()

	clientConn, err := grpcpkg.NewClient(
		"passthrough:///bufconn",
		grpcpkg.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpcpkg.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("create gRPC client: %v", err)
	}
	t.Cleanup(func() { _ = clientConn.Close() })

	watchCtx, cancelWatch := context.WithCancel(context.Background())
	defer cancelWatch()
	stream, err := healthpb.NewHealthClient(clientConn).Watch(
		watchCtx,
		&healthpb.HealthCheckRequest{},
	)
	if err != nil {
		t.Fatalf("start health watch: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("receive initial health status: %v", err)
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelShutdown()
	err = server.Shutdown(shutdownCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v, want %v", err, context.DeadlineExceeded)
	}

	select {
	case err := <-serveResult:
		if err != nil && !errors.Is(err, grpcpkg.ErrServerStopped) {
			t.Errorf("Serve() error = %v, want nil or %v", err, grpcpkg.ErrServerStopped)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for forced gRPC shutdown")
	}
}

func TestShutdownGracefullyStopsIdleGRPCServer(t *testing.T) {
	server, err := New(WithLogger(slog.New(slog.DiscardHandler)))
	require.NoError(t, err)
	listener := bufconn.Listen(1024 * 1024)
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.grpcServer.Serve(listener)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	select {
	case err := <-serveResult:
		if err != nil && !errors.Is(err, grpcpkg.ErrServerStopped) {
			t.Errorf("Serve() error = %v, want nil or %v", err, grpcpkg.ErrServerStopped)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for graceful gRPC shutdown")
	}
}

func waitForHealthStatus(
	t *testing.T,
	server *Server,
	want healthpb.HealthCheckResponse_ServingStatus,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		response, err := server.healthServer.Check(
			context.Background(),
			&healthpb.HealthCheckRequest{},
		)
		if err == nil && response.Status == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("health status did not become %s", want)
}

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
			server, err := New(WithReflection(true), func(s *Server) {
				// Keep the single-token budget exhausted for the duration of the test.
				s.rateLimiter = tools.NewRateLimiter(cache, "grpc", 1, 1, time.Hour,
					tools.WithRateLimitWindow(time.Hour))
				if test.unavailable {
					s.rateLimiter = grpcContextTestLimiter(func(context.Context, string) (bool, error) {
						return false, errors.New("cache unavailable")
					})
				}
			})
			require.NoError(t, err)
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
			_, err = client.EmptyCall(ctx, &testpb.Empty{})
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

func TestHealthWaitsForInitialReadinessCheck(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want healthpb.HealthCheckResponse_ServingStatus
	}{
		{name: "success", want: healthpb.HealthCheckResponse_SERVING},
		{name: "failure", err: errors.New("dependency unavailable"), want: healthpb.HealthCheckResponse_NOT_SERVING},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				result := make(chan error)
				var checks atomic.Int32
				started := time.Now()
				server, err := New(WitHealthCheckCtx(ctx), WithReadinessCheck(func(ctx context.Context) error {
					checks.Add(1)
					select {
					case err := <-result:
						return err
					case <-ctx.Done():
						return ctx.Err()
					}
				}))
				require.NoError(t, err)
				defer server.grpcServer.Stop()

				synctest.Wait()
				requireReadinessStatus(t, server, healthpb.HealthCheckResponse_NOT_SERVING)
				require.Equal(t, int32(1), checks.Load(), "the initial check must run before the first poll")
				require.Equal(t, started, time.Now(), "construction must not wait for readiness")

				result <- test.err
				synctest.Wait()
				requireReadinessStatus(t, server, test.want)
				require.Equal(t, started, time.Now(), "the initial result must not wait for the first poll")
			})
		})
	}
}

func TestHealthRecoversFromInitialReadinessFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var unavailable atomic.Bool
		unavailable.Store(true)
		var checks atomic.Int32
		const interval = time.Minute
		server, err := New(
			WitHealthCheckCtx(ctx),
			WithReadinessCheckInterval(interval),
			WithReadinessCheck(func(context.Context) error {
				checks.Add(1)
				if unavailable.Load() {
					return errors.New("dependency unavailable")
				}
				return nil
			}),
		)
		require.NoError(t, err)
		defer server.grpcServer.Stop()

		synctest.Wait()
		requireReadinessStatus(t, server, healthpb.HealthCheckResponse_NOT_SERVING)
		require.Equal(t, int32(1), checks.Load())

		unavailable.Store(false)
		time.Sleep(interval - time.Nanosecond)
		synctest.Wait()
		requireReadinessStatus(t, server, healthpb.HealthCheckResponse_NOT_SERVING)
		require.Equal(t, int32(1), checks.Load(), "recovery must wait for the next check")

		time.Sleep(time.Nanosecond)
		synctest.Wait()
		requireReadinessStatus(t, server, healthpb.HealthCheckResponse_SERVING)
		require.Equal(t, int32(2), checks.Load())
	})
}

func TestHealthRemainsNotServingWhenInitialCheckIsCanceled(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		name := "context cancellation"
		if shutdown {
			name = "server shutdown"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var checks atomic.Int32
				server, err := New(WitHealthCheckCtx(ctx), WithReadinessCheck(func(ctx context.Context) error {
					checks.Add(1)
					<-ctx.Done()
					// A dependency may finish successfully as shutdown starts.
					return nil
				}))
				require.NoError(t, err)
				defer server.grpcServer.Stop()

				synctest.Wait()
				requireReadinessStatus(t, server, healthpb.HealthCheckResponse_NOT_SERVING)
				require.Equal(t, int32(1), checks.Load())

				if shutdown {
					require.NoError(t, server.Shutdown(t.Context()))
				} else {
					cancel()
				}
				synctest.Wait()
				requireReadinessStatus(t, server, healthpb.HealthCheckResponse_NOT_SERVING)
				time.Sleep(defaultReadinessCheckInterval)
				synctest.Wait()
				require.Equal(t, int32(1), checks.Load(), "cancellation must stop further checks")
			})
		})
	}
}

func requireReadinessStatus(t *testing.T, server *Server, want healthpb.HealthCheckResponse_ServingStatus) {
	t.Helper()
	response, err := server.healthServer.Check(t.Context(), &healthpb.HealthCheckRequest{})
	require.NoError(t, err)
	require.Equal(t, want, response.GetStatus())
}
