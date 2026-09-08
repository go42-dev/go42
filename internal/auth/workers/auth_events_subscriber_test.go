package workers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/go42-dev/go42/internal/auth/domain"
	"github.com/go42-dev/go42/internal/auth/models"
	"github.com/go42-dev/go42/internal/auth/workers/mocks"
	"github.com/go42-dev/go42/internal/events"
	"github.com/go42-dev/go42/internal/metrics"
	outboxDomain "github.com/go42-dev/go42/internal/outbox/domain"
	"github.com/go42-dev/go42/internal/tools"
)

func TestAuthEventSubscriberPassesCorrelationToRepositoryAndErrorLogs(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(tools.SlogContextWrapper(slog.NewJSONHandler(&output, nil)))
	repository := mocks.NewMockrepository(gomock.NewController(t))
	subscriber := NewAuthEventSubscriber(repository, AuthEventSubscriberWithLogger(logger))
	event := outboxDomain.Event{
		ID: uuid.New(), CreatedAt: time.Now(), AggregateID: 42, AggregateType: "user.created",
		Payload: []byte(`{"user":42}`),
	}
	payload, err := json.Marshal(event)
	require.NoError(t, err)
	wantErr := errors.New("storage unavailable")
	repository.EXPECT().WithTransaction(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) })
	repository.EXPECT().SaveUserHistoryRecord(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, record *models.UserHistoryRecord) error {
			assert.Equal(t, "request-42", tools.GetRequestIDFromContext(ctx))
			assert.Contains(t, tools.LogAttrsFromContext(ctx), slog.String("event_id", event.ID.String()))
			assert.Equal(t, event.ID, record.ID)
			assert.Equal(t, event.Payload, record.Data)
			return wantErr
		})
	ctx := tools.SetRequestIDToContext(t.Context(), "request-42")
	ctx = tools.WithLogAttrs(ctx, slog.String("message_uuid", "message-42"))
	assert.ErrorIs(t, subscriber.handleEvent(ctx, payload), wantErr)
	decoder := json.NewDecoder(&output)
	var entry map[string]any
	require.NoError(t, decoder.Decode(&entry))
	assert.Equal(t, "failed to save event", entry["msg"])
	assert.Equal(t, "request-42", entry["request_id"])
	assert.Equal(t, "message-42", entry["message_uuid"])
	assert.Equal(t, event.ID.String(), entry["event_id"])
	assert.Equal(t, float64(42), entry["aggregate_id"])
	assert.ErrorIs(t, decoder.Decode(&entry), io.EOF)
}

func TestAuthEventSubscriberRejectsInvalidEnvelopes(t *testing.T) {
	for _, test := range []struct {
		name        string
		body        string
		wantMessage string
	}{
		{name: "empty input", wantMessage: "failed to unmarshal event"},
		{name: "malformed JSON", body: `{"id":`, wantMessage: "failed to unmarshal event"},
		{name: "array", body: `[]`, wantMessage: "failed to unmarshal event"},
		{name: "invalid UUID", body: `{"id":"invalid"}`, wantMessage: "failed to unmarshal event"},
		{name: "invalid timestamp", body: `{"created_at":"invalid"}`, wantMessage: "failed to unmarshal event"},
		{name: "invalid aggregate ID type", body: `{"aggregate_id":"42"}`, wantMessage: "failed to unmarshal event"},
		{name: "invalid payload encoding", body: `{"payload":"!!!"}`, wantMessage: "failed to unmarshal event"},
		{name: "empty object", body: `{}`, wantMessage: "event is missing required fields"},
		{name: "null event", body: `null`, wantMessage: "event is missing required fields"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := mocks.NewMockrepository(gomock.NewController(t))
			subscriber := NewAuthEventSubscriber(repository)
			failures := metrics.Counter("application_errors", map[string]any{"type": "auth_event_subscriber_error"})
			processed := metrics.Counter("application_auth_event_subscriber_processed", nil)
			failuresBefore, processedBefore := failures.Get(), processed.Get()

			err := subscriber.handleEvent(t.Context(), []byte(test.body))

			assertPermanentAuthEventError(t, err)
			assert.ErrorContains(t, err, test.wantMessage)
			assert.Equal(t, uint64(1), failures.Get()-failuresBefore)
			assert.Equal(t, processedBefore, processed.Get())
		})
	}
}

func TestAuthEventSubscriberRequiresEventFields(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*outboxDomain.Event)
	}{
		{name: "nil event ID", change: func(event *outboxDomain.Event) { event.ID = uuid.Nil }},
		{name: "zero creation time", change: func(event *outboxDomain.Event) { event.CreatedAt = time.Time{} }},
		{name: "zero aggregate ID", change: func(event *outboxDomain.Event) { event.AggregateID = 0 }},
		{name: "negative aggregate ID", change: func(event *outboxDomain.Event) { event.AggregateID = -1 }},
		{name: "empty aggregate type", change: func(event *outboxDomain.Event) { event.AggregateType = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := mocks.NewMockrepository(gomock.NewController(t))
			subscriber := NewAuthEventSubscriber(repository)
			event := authEventFixture()
			test.change(&event)
			payload, err := json.Marshal(event)
			require.NoError(t, err)
			failures := metrics.Counter("application_errors", map[string]any{"type": "auth_event_subscriber_error"})
			processed := metrics.Counter("application_auth_event_subscriber_processed", nil)
			failuresBefore, processedBefore := failures.Get(), processed.Get()

			err = subscriber.handleEvent(t.Context(), payload)

			assertPermanentAuthEventError(t, err)
			assert.ErrorContains(t, err, "event is missing required fields")
			assert.Equal(t, uint64(1), failures.Get()-failuresBefore)
			assert.Equal(t, processedBefore, processed.Get())
		})
	}
}

func TestAuthEventSubscriberStoresDeliveredHistoryWithTransactionContext(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "JSON payload", data: []byte(`{"user":42}`)},
		{name: "nil payload"},
		{name: "empty payload", data: []byte{}},
		{name: "binary payload", data: []byte{0, 0xff, 0x80, '\n'}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			repository := mocks.NewMockrepository(ctrl)
			transport := mocks.NewMocksubscriber(ctrl)
			subscriber := NewAuthEventSubscriber(repository)
			var handler func(context.Context, []byte) error
			transport.EXPECT().Subscribe(domain.TopicNameAuthEvents, gomock.Any()).
				DoAndReturn(func(_ string, registered func(context.Context, []byte) error) error {
					handler = registered
					return nil
				})
			require.NoError(t, subscriber.Subscribe(transport))
			require.NotNil(t, handler)
			event := authEventFixture()
			event.Payload = test.data
			payload, err := json.Marshal(event)
			require.NoError(t, err)
			repository.EXPECT().WithTransaction(gomock.Any(), gomock.Any()).
				DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error {
					assert.Equal(t, "delivery-42", tools.GetRequestIDFromContext(ctx))
					txCtx, cancel := context.WithCancel(ctx)
					defer cancel()
					repository.EXPECT().SaveUserHistoryRecord(gomock.Any(), &models.UserHistoryRecord{
						ID: event.ID, OccurredAt: event.CreatedAt, UserID: event.AggregateID,
						EventType: event.AggregateType, Data: test.data,
					}).DoAndReturn(func(recordCtx context.Context, _ *models.UserHistoryRecord) error {
						assert.Same(t, txCtx, recordCtx)
						return nil
					})
					return fn(txCtx)
				})
			failures := metrics.Counter("application_errors", map[string]any{"type": "auth_event_subscriber_error"})
			processed := metrics.Counter("application_auth_event_subscriber_processed", nil)
			failuresBefore, processedBefore := failures.Get(), processed.Get()
			ctx := tools.SetRequestIDToContext(t.Context(), "delivery-42")

			err = handler(ctx, payload)

			require.NoError(t, err)
			assert.Equal(t, uint64(1), processed.Get()-processedBefore)
			assert.Equal(t, failuresBefore, failures.Get())
		})
	}
}

func TestAuthEventSubscriberKeepsRepositoryFailuresRetryable(t *testing.T) {
	storageError := errors.New("storage unavailable")
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "direct", err: storageError},
		{name: "wrapped", err: fmt.Errorf("repository: %w", storageError)},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := mocks.NewMockrepository(gomock.NewController(t))
			subscriber := NewAuthEventSubscriber(repository)
			payload, err := json.Marshal(authEventFixture())
			require.NoError(t, err)
			repository.EXPECT().WithTransaction(gomock.Any(), gomock.Any()).
				DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) })
			repository.EXPECT().SaveUserHistoryRecord(gomock.Any(), gomock.Any()).Return(test.err)
			failures := metrics.Counter("application_errors", map[string]any{"type": "auth_event_subscriber_error"})
			processed := metrics.Counter("application_auth_event_subscriber_processed", nil)
			failuresBefore, processedBefore := failures.Get(), processed.Get()

			err = subscriber.handleEvent(t.Context(), payload)

			require.ErrorIs(t, err, storageError)
			assert.NotSame(t, err, events.Permanent(err), "storage failures must remain retryable")
			assert.Equal(t, uint64(1), failures.Get()-failuresBefore)
			assert.Equal(t, processedBefore, processed.Get())
		})
	}
}

func TestAuthEventSubscriberKeepsTransactionFailuresRetryable(t *testing.T) {
	transactionError := errors.New("transaction unavailable")
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "direct", err: transactionError},
		{name: "wrapped", err: fmt.Errorf("transaction: %w", transactionError)},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := mocks.NewMockrepository(gomock.NewController(t))
			subscriber := NewAuthEventSubscriber(repository)
			payload, err := json.Marshal(authEventFixture())
			require.NoError(t, err)
			repository.EXPECT().WithTransaction(gomock.Any(), gomock.Any()).Return(test.err)
			processed := metrics.Counter("application_auth_event_subscriber_processed", nil)
			processedBefore := processed.Get()

			err = subscriber.handleEvent(t.Context(), payload)

			require.ErrorIs(t, err, transactionError)
			assert.NotSame(t, err, events.Permanent(err), "transaction failures must remain retryable")
			assert.Equal(t, processedBefore, processed.Get())
		})
	}
}

func authEventFixture() outboxDomain.Event {
	return outboxDomain.Event{
		ID:            uuid.MustParse("dddddddd-dddd-4ddd-8ddd-dddddddddddd"),
		CreatedAt:     time.Date(2026, time.September, 8, 12, 30, 0, 0, time.UTC),
		AggregateID:   42,
		AggregateType: domain.EventTypeUserCreate,
		Payload:       []byte(`{"user":42}`),
	}
}

func assertPermanentAuthEventError(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	// Permanent returns an already marked error unchanged.
	assert.Same(t, err, events.Permanent(err))
}
