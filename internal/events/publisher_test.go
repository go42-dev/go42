package events_test

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go42-dev/go42/internal/events"
	"github.com/go42-dev/go42/internal/metrics"
)

func TestPublisherTimeoutBoundsBlockedCallsAndAllowsRecovery(t *testing.T) {
	for _, limit := range []int{1, 4} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				entered := make(chan struct{}, limit)
				released := make(chan struct{})
				release := sync.OnceFunc(func() { close(released) })
				defer release()
				var calls atomic.Int32
				backend := &routerBackendStub{
					Backend: events.NewNoop(),
					publish: func(string, ...*message.Message) error {
						if calls.Add(1) <= int32(limit) {
							entered <- struct{}{}
							<-released // Simulate a broker that ignores cancellation.
						}
						return nil
					},
				}
				router, err := events.NewRouter(backend,
					events.WithCloseTimeout(time.Second),
					events.WithPublishMaxInflight(limit),
				)
				require.NoError(t, err)
				ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
				registerRouterCleanup(t, router, cancel)
				topic := t.Name()
				successes := metrics.Counter("application_event_publish_total", map[string]any{
					"result": "success", "topic": topic,
				})
				failures := metrics.Counter("application_event_publish_total", map[string]any{
					"result": "error", "topic": topic,
				})
				successesBefore, failuresBefore := successes.Get(), failures.Get()
				done := make(chan error, limit)
				for i := range limit {
					go func() { done <- router.Publish(ctx, topic, strconv.Itoa(i), []byte("event")) }()
				}
				synctest.Wait()
				require.Len(t, entered, limit, "independent calls may use all configured capacity")
				time.Sleep(time.Hour)
				synctest.Wait()
				require.Len(t, done, limit)
				for range limit {
					require.ErrorIs(t, <-done, context.DeadlineExceeded)
				}

				for range 3 {
					attemptCtx, attemptCancel := context.WithTimeout(t.Context(), time.Minute)
					err = router.Publish(attemptCtx, topic, "expired-event", []byte("expired"))
					attemptCancel()
					require.ErrorIs(t, err, context.DeadlineExceeded)
				}
				canceledCtx, canceledCancel := context.WithCancel(t.Context())
				canceledCancel()
				require.ErrorIs(t, router.Publish(canceledCtx, topic, "canceled-event", nil), context.Canceled)
				require.EqualValues(t, limit, calls.Load(), "timeouts must not start more blocked broker calls")
				assert.Equal(t, successesBefore, successes.Get())
				assert.Equal(t, uint64(limit+4), failures.Get()-failuresBefore)

				release()
				synctest.Wait()
				recoveryCtx, recoveryCancel := context.WithTimeout(t.Context(), time.Minute)
				defer recoveryCancel()
				require.NoError(t, router.Publish(recoveryCtx, topic, "new-event", []byte("new")))
				require.EqualValues(t, limit+1, calls.Load(), "expired waiting calls must not publish later")
				assert.Equal(t, successesBefore+1, successes.Get(), "late completions must not report success")
				assert.Equal(t, uint64(limit+4), failures.Get()-failuresBefore)
			})
		})
	}
}

func TestPublisherOwnsPayloadAfterCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		released := make(chan struct{})
		release := sync.OnceFunc(func() { close(released) })
		defer release()
		observed := make(chan []byte, 1)
		backend := &routerBackendStub{
			Backend: events.NewNoop(),
			publish: func(_ string, messages ...*message.Message) error {
				close(entered)
				<-released
				observed <- messages[0].Payload
				return nil
			},
		}
		router, err := events.NewRouter(backend, events.WithCloseTimeout(time.Second))
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(t.Context())
		registerRouterCleanup(t, router, cancel)
		payload := []byte("event")
		done := make(chan error, 1)
		go func() { done <- router.Publish(ctx, t.Name(), "event-id", payload) }()
		<-entered
		cancel()
		synctest.Wait()
		require.Len(t, done, 1)
		require.ErrorIs(t, <-done, context.Canceled)
		copy(payload, "other")
		release()
		synctest.Wait()
		require.Equal(t, []byte("event"), <-observed)
	})
}
