package events_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/message/router/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go42-dev/go42/internal/events"
	"github.com/go42-dev/go42/internal/metrics"
)

func TestDeadLetterPublisherSharesCapacityAndRespectsCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		incoming := make(chan *message.Message, 1)
		defer close(incoming)
		entered := make(chan struct{})
		released := make(chan struct{})
		release := sync.OnceFunc(func() { close(released) })
		defer release()
		observed := make(chan *message.Message, 1)
		var calls atomic.Int32
		topic := t.Name()
		dlqTopic := topic + routerDLQSuffix
		backend := &routerBackendStub{
			Backend: events.NewNoop(),
			subscriber: func() message.Subscriber {
				return &routerSubscribeStub{
					Subscriber: events.NewNoop(),
					subscribe: func(context.Context, string) (<-chan *message.Message, error) {
						return incoming, nil
					},
				}
			},
			publish: func(gotTopic string, messages ...*message.Message) error {
				calls.Add(1)
				if gotTopic == dlqTopic {
					close(entered)
					<-released
					observed <- messages[0]
				}
				return nil
			},
		}
		router, err := events.NewRouter(backend,
			events.WithDeadLetterTopicSuffix(routerDLQSuffix),
			events.WithCloseTimeout(time.Second),
			events.WithPublishMaxInflight(1),
		)
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(t.Context())
		registerRouterCleanup(t, router, cancel)
		require.NoError(t, router.Subscribe(topic, func(context.Context, []byte) error {
			return events.Permanent(errors.New("invalid event"))
		}))
		startRouter(t, router, ctx)
		successes := metrics.Counter("application_event_consumer_dead_letters_total", map[string]any{
			"result": "published", "topic": dlqTopic,
		})
		failures := metrics.Counter("application_event_consumer_dead_letters_total", map[string]any{
			"result": "failed", "topic": dlqTopic,
		})
		successesBefore, failuresBefore := successes.Get(), failures.Get()
		messageCtx, cancelMessage := context.WithCancel(ctx)
		defer cancelMessage()
		msg := message.NewMessageWithContext(messageCtx, "event-id", []byte("event"))
		msg.Metadata.Set("custom", "original")
		incoming <- msg
		<-entered

		waitingCtx, cancelWaiting := context.WithTimeout(ctx, time.Minute)
		err = router.Publish(waitingCtx, "waiting", []byte("expired"))
		cancelWaiting()
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.EqualValues(t, 1, calls.Load(), "normal publishing shares the dead-letter capacity limit")

		cancelMessage()
		synctest.Wait()
		select {
		case <-msg.Nacked():
		default:
			t.Fatal("failed dead-letter publishing must nack the source message")
		}
		assert.Equal(t, failuresBefore+1, failures.Get())
		assert.Equal(t, successesBefore, successes.Get())
		msg.UUID = "reused-id"
		copy(msg.Payload, "other")
		msg.Metadata.Set("custom", "changed")
		release()
		synctest.Wait()
		require.Len(t, observed, 1)
		published := <-observed
		assert.Equal(t, "event-id", published.UUID)
		assert.Equal(t, []byte("event"), []byte(published.Payload))
		assert.Equal(t, "original", published.Metadata.Get("custom"))
		assert.Equal(t, "invalid event", published.Metadata.Get(middleware.ReasonForPoisonedKey))
		assert.Equal(t, topic, published.Metadata.Get(middleware.PoisonedTopicKey))
		assert.ErrorIs(t, published.Context().Err(), context.Canceled)
		assert.Equal(t, successesBefore, successes.Get(), "late completion must not report DLQ success")
		select {
		case <-msg.Acked():
			t.Fatal("late completion must not ack the source message")
		default:
		}
		require.NoError(t, router.Publish(ctx, "recovered", []byte("new")))
		require.EqualValues(t, 2, calls.Load(), "capacity is released when the backend returns")
	})
}
