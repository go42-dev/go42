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
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	grpcpkg "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

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
			server := New(WithLogger(logger),
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
			client := healthpb.NewHealthClient(conn)
			if test.streaming {
				stream, streamErr := client.Watch(ctx, &healthpb.HealthCheckRequest{})
				if streamErr == nil {
					_, streamErr = stream.Recv()
				}
				assert.Equal(t, test.code, status.Code(streamErr))
			} else {
				_, err = client.Check(ctx, &healthpb.HealthCheckRequest{})
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

func TestHealthStatusTracksDependencyAvailability(t *testing.T) {
	healthCtx, cancelHealth := context.WithCancel(context.Background())
	defer cancelHealth()
	dependencyUnavailable := new(atomic.Bool)
	server := New(
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

	waitForHealthStatus(t, server, healthpb.HealthCheckResponse_SERVING)

	dependencyUnavailable.Store(true)
	waitForHealthStatus(t, server, healthpb.HealthCheckResponse_NOT_SERVING)

	dependencyUnavailable.Store(false)
	waitForHealthStatus(t, server, healthpb.HealthCheckResponse_SERVING)

	cancelHealth()
	waitForHealthStatus(t, server, healthpb.HealthCheckResponse_NOT_SERVING)
}

func TestPanicRecoveryDoesNotExposePanicDetails(t *testing.T) {
	server := New(WithLogger(slog.New(slog.DiscardHandler)))
	err := server.handlePanic(t.Context(), "database password: secret")

	if got := status.Code(err); got != codes.Internal {
		t.Errorf("panic status code = %s, want %s", got, codes.Internal)
	}
	if got := status.Convert(err).Message(); got != "internal server error" {
		t.Errorf("panic status message = %q, want %q", got, "internal server error")
	}
}

func TestShutdownForcesGRPCServerAfterDeadline(t *testing.T) {
	server := New(WithLogger(slog.New(slog.DiscardHandler)))
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
	server := New(WithLogger(slog.New(slog.DiscardHandler)))
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
