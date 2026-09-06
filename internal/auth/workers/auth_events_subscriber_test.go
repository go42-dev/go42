package workers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/go42-dev/go42/internal/auth/models"
	"github.com/go42-dev/go42/internal/auth/workers/mocks"
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
