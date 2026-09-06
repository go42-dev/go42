package workers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

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
			require.NoError(t, json.Unmarshal(payload, &event))
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
				t.Fatalf("published event is invalid: %v", err)
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
	assertOutboxPublishFailure(t, 0, models.MessageStatusPending, 1)
}

func TestOutboxPublisherMarksMessageFailedAfterLastRetry(t *testing.T) {
	assertOutboxPublishFailure(t, domain.MaxRetries-1, models.MessageStatusFailed, domain.MaxRetries)
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
) {
	t.Helper()
	ctrl := gomock.NewController(t)
	repository := mocks.NewMockrepository(ctrl)
	publisher := mocks.NewMockpublisher(ctrl)
	worker := NewOutboxMessagePublisher(repository, publisher)
	message := newOutboxTestMessage()
	message.RetryCount = retryCount
	message.Metadata = map[string]string{"request_id": "request-42"}
	publishErr := errors.New("broker unavailable")

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
