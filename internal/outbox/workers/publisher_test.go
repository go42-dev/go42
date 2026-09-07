package workers

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/mock/gomock"

	"github.com/go42-dev/go42/internal/events"
	"github.com/go42-dev/go42/internal/outbox/domain"
	"github.com/go42-dev/go42/internal/outbox/models"
	"github.com/go42-dev/go42/internal/outbox/workers/mocks"
	"github.com/go42-dev/go42/internal/tools"
)

type outboxWorkerContextKey struct{}

func TestOutboxPublisherRestoresContextFromMetadata(t *testing.T) {
	ctrl := gomock.NewController(t)
	repository := mocks.NewMockrepository(ctrl)
	publisher := mocks.NewMockpublisher(ctrl)
	worker := NewOutboxMessagePublisher(repository, publisher)
	first, second := newOutboxTestMessage(), newOutboxTestMessage()
	firstSpan := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}})
	secondSpan := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{3}, SpanID: trace.SpanID{4}})
	firstCtx := tools.SetRequestIDToContext(trace.ContextWithSpanContext(t.Context(), firstSpan), "first-request")
	secondCtx := tools.SetRequestIDToContext(trace.ContextWithSpanContext(t.Context(), secondSpan), "second-request")
	first.Metadata = events.PropagationFromContext(firstCtx)
	second.Metadata = events.PropagationFromContext(secondCtx)
	workerCtx := context.WithValue(t.Context(), outboxWorkerContextKey{}, "transaction")
	workerCtx, cancel := context.WithCancel(workerCtx)
	defer cancel()
	expectOutboxTransaction(repository)
	repository.EXPECT().GetUnprocessedMessages(gomock.Any(), 10).Return([]models.Message{first, second}, nil)
	var contexts []context.Context
	publisher.EXPECT().Publish(gomock.Any(), first.Topic, gomock.Any()).Times(2).
		DoAndReturn(func(ctx context.Context, _ string, payload []byte) error {
			var event domain.Event
			if err := json.Unmarshal(payload, &event); err != nil {
				t.Errorf("published event is invalid: %v", err)
				return err
			}
			assert.Equal(t, "transaction", ctx.Value(outboxWorkerContextKey{}))
			assert.NoError(t, ctx.Err())
			if event.ID == first.ID {
				assert.Equal(t, "first-request", tools.GetRequestIDFromContext(ctx))
				assert.Equal(t, firstSpan.TraceID(), trace.SpanContextFromContext(ctx).TraceID())
			} else {
				assert.Equal(t, second.ID, event.ID)
				assert.Equal(t, "second-request", tools.GetRequestIDFromContext(ctx))
				assert.Equal(t, secondSpan.TraceID(), trace.SpanContextFromContext(ctx).TraceID())
			}
			contexts = append(contexts, ctx)
			return nil
		})
	repository.EXPECT().SaveProcessedMessages(gomock.Any(), gomock.Any()).Return(nil)
	require.NoError(t, worker.run(workerCtx, 10))
	assert.Empty(t, tools.GetRequestIDFromContext(workerCtx))
	cancel()
	for _, ctx := range contexts {
		assert.ErrorIs(t, ctx.Err(), context.Canceled)
	}
}

func TestOutboxPublisherMarksPublishedMessageProcessed(t *testing.T) {
	ctrl := gomock.NewController(t)
	repository := mocks.NewMockrepository(ctrl)
	publisher := mocks.NewMockpublisher(ctrl)
	worker := NewOutboxMessagePublisher(repository, publisher)
	message := newOutboxTestMessage()
	expectOutboxTransaction(repository)
	repository.EXPECT().GetUnprocessedMessages(gomock.Any(), 10).
		Return([]models.Message{message}, nil)
	publisher.EXPECT().Publish(gomock.Any(), message.Topic, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, payload []byte) error {
			var event domain.Event
			if err := json.Unmarshal(payload, &event); err != nil {
				t.Errorf("published event is invalid: %v", err)
				return err
			}
			if event.ID != message.ID || event.AggregateID != message.AggregateID ||
				event.AggregateType != message.AggregateType {
				t.Errorf("published event = %#v, want message identity", event)
			}
			return nil
		})
	repository.EXPECT().SaveProcessedMessages(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, messages []models.Message) error {
			if len(messages) != 1 || messages[0].ID != message.ID {
				t.Errorf("processed messages = %#v, want message %s", messages, message.ID)
			}
			return nil
		})

	if err := worker.run(t.Context(), 10); err != nil {
		t.Fatalf("run() error = %v", err)
	}
}

func TestOutboxPublisherPersistsRetryState(t *testing.T) {
	assertOutboxPublishFailure(t, 0, models.MessageStatusPending, 1, errors.New("broker unavailable"))
}

func TestOutboxPublisherMarksMessageFailedAfterLastRetry(t *testing.T) {
	assertOutboxPublishFailure(t, domain.MaxRetries-1, models.MessageStatusFailed, domain.MaxRetries,
		errors.New("broker unavailable"))
}

func TestOutboxPublisherTimeoutExhaustsRetries(t *testing.T) {
	assertOutboxPublishFailure(t, domain.MaxRetries-1, models.MessageStatusFailed, domain.MaxRetries,
		context.DeadlineExceeded)
}

func TestOutboxPublisherTimeoutBoundsBlockedCallsAndAllowsRecovery(t *testing.T) {
	entered := make(chan struct{})
	released := make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	var calls atomic.Int32
	router := newOutboxTestRouter(t, func(string, ...*message.Message) error {
		if calls.Add(1) == 1 {
			close(entered)
			<-released // Simulate a broker client that ignores context cancellation.
		}
		return nil
	})
	worker := NewOutboxMessagePublisher(mocks.NewMockrepository(gomock.NewController(t)), router)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- worker.publish(ctx, "blocked", []byte("event")) }()
	waitForOutboxPublishSignal(t, entered)
	require.ErrorIs(t, waitForOutboxRun(t, done), context.DeadlineExceeded)

	for range 3 {
		attemptCtx, attemptCancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
		err := worker.publish(attemptCtx, "waiting", []byte("expired"))
		attemptCancel()
		require.ErrorIs(t, err, context.DeadlineExceeded)
	}
	canceledCtx, canceledCancel := context.WithCancel(t.Context())
	canceledCancel()
	require.ErrorIs(t, worker.publish(canceledCtx, "canceled", nil), context.Canceled)
	require.EqualValues(t, 1, calls.Load(), "timeouts must not create more blocked broker calls")

	release()
	recoveryCtx, recoveryCancel := context.WithTimeout(t.Context(), time.Second)
	defer recoveryCancel()
	require.NoError(t, worker.publish(recoveryCtx, "recovered", []byte("new")))
	require.EqualValues(t, 2, calls.Load(), "expired waiting calls must not publish later")
}

func TestOutboxPublisherReturnsRepositoryReadError(t *testing.T) {
	ctrl := gomock.NewController(t)
	repository := mocks.NewMockrepository(ctrl)
	worker := NewOutboxMessagePublisher(repository, mocks.NewMockpublisher(ctrl))
	wantErr := errors.New("repository unavailable")
	expectOutboxTransaction(repository)
	repository.EXPECT().GetUnprocessedMessages(gomock.Any(), 10).Return(nil, wantErr)

	err := worker.run(t.Context(), 10)
	if !errors.Is(err, wantErr) {
		t.Errorf("run() error = %v, want %v", err, wantErr)
	}
}

func assertOutboxPublishFailure(
	t *testing.T,
	retryCount int,
	wantStatus string,
	wantRetryCount int,
	publishErr error,
) {
	t.Helper()
	ctrl := gomock.NewController(t)
	repository := mocks.NewMockrepository(ctrl)
	publisher := mocks.NewMockpublisher(ctrl)
	worker := NewOutboxMessagePublisher(repository, publisher)
	message := newOutboxTestMessage()
	message.RetryCount = retryCount
	message.Metadata = map[string]string{"request_id": "request-42"}

	expectOutboxTransaction(repository)
	repository.EXPECT().GetUnprocessedMessages(gomock.Any(), 10).
		Return([]models.Message{message}, nil)
	publisher.EXPECT().Publish(gomock.Any(), message.Topic, gomock.Any()).Return(publishErr)
	repository.EXPECT().SaveFailedMessages(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, messages []models.Message) error {
			if len(messages) != 1 {
				t.Fatalf("failed messages count = %d, want 1", len(messages))
			}
			stored := messages[0]
			if stored.Status != wantStatus {
				t.Errorf("stored status = %q, want %q", stored.Status, wantStatus)
			}
			if stored.RetryCount != wantRetryCount {
				t.Errorf("stored retry count = %d, want %d", stored.RetryCount, wantRetryCount)
			}
			if stored.LastError != publishErr.Error() {
				t.Errorf("stored last error = %q, want %q", stored.LastError, publishErr)
			}
			assert.Equal(t, message.Metadata, stored.Metadata)
			return nil
		})

	if err := worker.run(t.Context(), 10); err != nil {
		t.Fatalf("run() error = %v", err)
	}
}

func expectOutboxTransaction(repository *mocks.Mockrepository) {
	repository.EXPECT().WithTransaction(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error {
			return fn(ctx)
		})
}

func newOutboxTestMessage() models.Message {
	return models.Message{
		ID:            uuid.New(),
		AggregateID:   42,
		AggregateType: "user.created",
		Topic:         "auth",
		Payload:       []byte(`{"uuid":"test"}`),
		CreatedAt:     time.Now().Add(-time.Second),
		Status:        models.MessageStatusPending,
		MaxRetries:    domain.MaxRetries,
	}
}

type outboxTestBackend struct {
	*events.NoopEngine
	publish func(string, ...*message.Message) error
}

func (b *outboxTestBackend) Publisher() message.Publisher { return b }

func (b *outboxTestBackend) Publish(topic string, messages ...*message.Message) error {
	return b.publish(topic, messages...)
}

func newOutboxTestRouter(t *testing.T, publish func(string, ...*message.Message) error) *events.Router {
	t.Helper()
	backend := &outboxTestBackend{NoopEngine: events.NewNoop(), publish: publish}
	router, err := events.NewRouter(backend, events.DeliveryPolicy{CloseTimeout: time.Second})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, router.Shutdown(ctx))
	})
	return router
}

func waitForOutboxPublishSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal("publisher did not reach the expected state")
	}
}

func waitForOutboxRun(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("outbox worker did not return after its publish context expired")
		return nil
	}
}
