package grpc

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

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
				server := New(WitHealthCheckCtx(ctx), WithReadinessCheck(func(ctx context.Context) error {
					checks.Add(1)
					select {
					case err := <-result:
						return err
					case <-ctx.Done():
						return ctx.Err()
					}
				}))
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
		server := New(
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
				server := New(WitHealthCheckCtx(ctx), WithReadinessCheck(func(ctx context.Context) error {
					checks.Add(1)
					<-ctx.Done()
					// A dependency may finish successfully as shutdown starts.
					return nil
				}))
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
