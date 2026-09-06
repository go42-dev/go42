package repository_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/go42-dev/go42/internal/database"
	"github.com/go42-dev/go42/internal/database/sqlite"
	"github.com/go42-dev/go42/internal/events"
	"github.com/go42-dev/go42/internal/outbox"
	"github.com/go42-dev/go42/internal/outbox/domain"
	"github.com/go42-dev/go42/internal/outbox/models"
	"github.com/go42-dev/go42/internal/outbox/repository"
	"github.com/go42-dev/go42/internal/tools"
)

func TestOutboxMetadataRoundTrip(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "outbox.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Shutdown(context.Background())) })
	sqlDB, err := db.Master().DB()
	require.NoError(t, err)
	migrations, err := goose.NewProvider(goose.DialectSQLite3, sqlDB,
		os.DirFS(filepath.Join("..", "..", "..", "migrate", "sqlite")))
	require.NoError(t, err)
	_, err = migrations.Up(t.Context())
	require.NoError(t, err)
	noMetadataIDs := []uuid.UUID{uuid.New(), uuid.New()}
	for i, metadata := range []any{nil, ""} {
		require.NoError(t, db.Master().WithContext(t.Context()).Exec(
			`insert into transactional_outbox
			(id, aggregate_id, aggregate_type, topic, payload, status, retry_count, max_retries, last_error, metadata)
			values (?, 1, 'user.created', 'auth', ?, 'pending', 0, 3, '', ?)`,
			noMetadataIDs[i], []byte(`{"user":1}`), metadata,
		).Error)
	}
	applied, err := migrations.Up(t.Context())
	require.NoError(t, err)
	assert.Empty(t, applied)

	repo := repository.New(database.NewBaseRepository(db))
	service := outbox.NewService(repo)
	span := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}})
	producer := trace.ContextWithSpanContext(t.Context(), span)
	producer = tools.SetRequestIDToContext(producer, "request-42")
	producer, cancel := context.WithCancel(producer)
	defer cancel()
	require.NoError(t, repo.WithTransaction(producer, func(ctx context.Context) error {
		return service.NewOutboxMessage(ctx, "auth", &domain.Message{
			AggregateID: 42, AggregateType: "user.created", Payload: []byte(`{"user":42}`),
		})
	}))
	cancel()

	stored, err := repo.GetUnprocessedMessages(t.Context(), 10)
	require.NoError(t, err)
	require.Len(t, stored, len(noMetadataIDs)+1)
	for _, entry := range stored {
		if slices.Contains(noMetadataIDs, entry.ID) {
			assert.Empty(t, entry.Metadata)
			continue
		}
		assert.Equal(t, events.PropagationFromContext(producer), entry.Metadata)
		restored := events.ContextWithPropagation(t.Context(), entry.Metadata)
		assert.NoError(t, restored.Err())
		assert.Equal(t, "request-42", tools.GetRequestIDFromContext(restored))
		assert.Equal(t, span.TraceID(), trace.SpanContextFromContext(restored).TraceID())
		entry.RetryCount++
		require.NoError(t, repo.SaveFailedMessages(t.Context(), []models.Message{entry}))
		var retried models.Message
		require.NoError(t, db.Master().WithContext(t.Context()).First(&retried, "id = ?", entry.ID).Error)
		assert.Equal(t, entry.Metadata, retried.Metadata)
	}
}
