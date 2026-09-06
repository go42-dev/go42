package events_test

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/message/router/middleware"

	"github.com/go42-dev/go42/internal/events"
	"github.com/go42-dev/go42/internal/events/gochan"
)

const (
	routerTestTimeout = 3 * time.Second
	routerDLQSuffix   = "_dlq"
)

func TestRouterRetriesTransientFailure(t *testing.T) {
	router, ctx := newTestRouter(t, events.DeliveryPolicy{
		MaxRetries:            3,
		InitialBackoff:        time.Millisecond,
		MaxBackoff:            2 * time.Millisecond,
		DeadLetterTopicSuffix: routerDLQSuffix,
		CloseTimeout:          time.Second,
	})

	var attempts atomic.Int32
	processed := make(chan struct{})
	if err := router.Subscribe("transient", func(context.Context, []byte) error {
		if attempts.Add(1) < 3 {
			return errors.New("temporary failure")
		}
		close(processed)
		return nil
	}); err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	startRouter(t, router, ctx)

	if err := router.Publish("transient", []byte("event")); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	waitForRouterSignal(t, processed)
	if got := attempts.Load(); got != 3 {
		t.Errorf("handler attempts = %d, want 3", got)
	}
}

func TestRouterSkipsRetriesForPermanentFailure(t *testing.T) {
	assertDeadLetterDelivery(t, 5, true, 1)
}

func TestRouterMovesMessageToDeadLetterTopicAfterRetries(t *testing.T) {
	assertDeadLetterDelivery(t, 2, false, 3)
}

func TestRouterReportsUnexpectedStop(t *testing.T) {
	router, backend, _ := newRunningTestRouter(t)
	if err := backend.Shutdown(t.Context()); err != nil {
		t.Fatalf("stop event backend: %v", err)
	}

	err, open := waitForRouterError(t, router.Errors())
	if !open || err == nil {
		t.Fatalf("router termination = (%v, %t), want an unexpected termination error", err, open)
	}
	if err, open := waitForRouterError(t, router.Errors()); open {
		t.Fatalf("router reported more than one failure: %v", err)
	}
}

func TestRouterShutdownDoesNotReportFailure(t *testing.T) {
	router, _, _ := newRunningTestRouter(t)
	if err := router.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err, open := waitForRouterError(t, router.Errors()); open {
		t.Fatalf("explicit shutdown reported a failure: %v", err)
	}
}

func TestRouterCancellationDoesNotReportFailure(t *testing.T) {
	router, _, cancel := newRunningTestRouter(t)
	cancel()
	if err, open := waitForRouterError(t, router.Errors()); open {
		t.Fatalf("context cancellation reported a failure: %v", err)
	}
}

func newRunningTestRouter(t *testing.T) (*events.Router, *gochan.GoChan, context.CancelFunc) {
	t.Helper()
	backend := gochan.New(gochan.WithLogger(slog.New(slog.DiscardHandler)))
	router, err := events.NewRouter(backend, events.DeliveryPolicy{
		DeadLetterTopicSuffix: routerDLQSuffix,
		CloseTimeout:          time.Second,
	})
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	registerRouterCleanup(t, router, cancel)
	if err := router.Subscribe("lifecycle", func(context.Context, []byte) error { return nil }); err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	startRouter(t, router, ctx)
	return router, backend, cancel
}

func waitForRouterError(t *testing.T, failures <-chan error) (error, bool) {
	t.Helper()
	select {
	case err, open := <-failures:
		return err, open
	case <-time.After(routerTestTimeout):
		t.Fatal("timed out waiting for router termination")
		return nil, false
	}
}

func assertDeadLetterDelivery(t *testing.T, maxRetries int, permanent bool, wantAttempts int32) {
	t.Helper()
	backend := gochan.New(gochan.WithLogger(slog.New(slog.DiscardHandler)))
	ctx, cancel := context.WithCancel(t.Context())
	router, err := events.NewRouter(
		backend,
		events.DeliveryPolicy{
			MaxRetries:            maxRetries,
			InitialBackoff:        time.Millisecond,
			MaxBackoff:            2 * time.Millisecond,
			DeadLetterTopicSuffix: routerDLQSuffix,
			CloseTimeout:          time.Second,
		},
		events.WithLogger(slog.New(slog.DiscardHandler)),
	)
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	registerRouterCleanup(t, router, cancel)

	const topic = "failing"
	deadLetters, err := backend.Subscriber().Subscribe(ctx, topic+routerDLQSuffix)
	if err != nil {
		t.Fatalf("subscribe to dead-letter topic: %v", err)
	}
	var attempts atomic.Int32
	if err := router.Subscribe(topic, func(context.Context, []byte) error {
		attempts.Add(1)
		err := errors.New("handler failure")
		if permanent {
			return events.Permanent(err)
		}
		return err
	}); err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	startRouter(t, router, ctx)

	payload := []byte("event")
	if err := router.Publish(topic, payload); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	deadLetter := waitForRouterMessage(t, deadLetters)
	deadLetter.Ack()

	if string(deadLetter.Payload) != string(payload) {
		t.Errorf("dead-letter payload = %q, want %q", deadLetter.Payload, payload)
	}
	if got := deadLetter.Metadata.Get(middleware.PoisonedTopicKey); got != topic {
		t.Errorf("dead-letter source topic = %q, want %q", got, topic)
	}
	if got := attempts.Load(); got != wantAttempts {
		t.Errorf("handler attempts = %d, want %d", got, wantAttempts)
	}
}

func newTestRouter(t *testing.T, policy events.DeliveryPolicy) (*events.Router, context.Context) {
	t.Helper()
	backend := gochan.New(gochan.WithLogger(slog.New(slog.DiscardHandler)))
	router, err := events.NewRouter(
		backend,
		policy,
		events.WithLogger(slog.New(slog.DiscardHandler)),
	)
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	registerRouterCleanup(t, router, cancel)
	return router, ctx
}

func startRouter(t *testing.T, router *events.Router, ctx context.Context) {
	t.Helper()
	if err := router.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
}

func registerRouterCleanup(t *testing.T, router *events.Router, cancel context.CancelFunc) {
	t.Helper()
	t.Cleanup(func() {
		cancel()
		ctx, shutdownCancel := context.WithTimeout(context.Background(), routerTestTimeout)
		defer shutdownCancel()
		if err := router.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})
}

func waitForRouterSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(routerTestTimeout):
		t.Fatal("timed out waiting for routed event")
	}
}

func waitForRouterMessage(t *testing.T, messages <-chan *message.Message) *message.Message {
	t.Helper()
	select {
	case msg, open := <-messages:
		if !open {
			t.Fatal("message channel closed")
		}
		return msg
	case <-time.After(routerTestTimeout):
		t.Fatal("timed out waiting for routed message")
		return nil
	}
}
