package outbox_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"

	"github.com/go42-dev/go42/internal/database"
	"github.com/go42-dev/go42/internal/database/mysql"
	"github.com/go42-dev/go42/internal/database/pgsql"
	"github.com/go42-dev/go42/internal/database/sqlite"
	"github.com/go42-dev/go42/internal/events"
	"github.com/go42-dev/go42/internal/outbox"
	"github.com/go42-dev/go42/internal/outbox/domain"
	"github.com/go42-dev/go42/internal/outbox/models"
	"github.com/go42-dev/go42/internal/outbox/repository"
	"github.com/go42-dev/go42/internal/tools"
)

func TestOutboxMetadataRoundTrip(t *testing.T) {
	db, repo, migrations := newOutboxRepository(t)
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

func newOutboxRepository(t *testing.T) (database.Database, *repository.Repository, *goose.Provider) {
	t.Helper()
	var db database.Database
	var err error
	engine, dialect := "sqlite", goose.DialectSQLite3
	// Optional DSNs must point to disposable test databases: these tests clear
	// the outbox table. By default, tests use a separate SQLite database per test.
	switch {
	case os.Getenv("GO42_OUTBOX_TEST_PGSQL_DSN") != "":
		engine, dialect = "pgsql", goose.DialectPostgres
		db, err = pgsql.Open(t.Context(), os.Getenv("GO42_OUTBOX_TEST_PGSQL_DSN"), "")
	case os.Getenv("GO42_OUTBOX_TEST_MYSQL_DSN") != "":
		engine, dialect = "mysql", goose.DialectMySQL
		db, err = mysql.Open(t.Context(), os.Getenv("GO42_OUTBOX_TEST_MYSQL_DSN"), "")
	default:
		db, err = sqlite.Open(filepath.Join(t.TempDir(), "outbox.db"))
	}
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, db.Shutdown(ctx))
	})
	sqlDB, err := db.Master().DB()
	require.NoError(t, err)
	migrations, err := goose.NewProvider(dialect, sqlDB,
		os.DirFS(filepath.Join("..", "..", "..", "migrate", engine)))
	require.NoError(t, err)
	_, err = migrations.Up(t.Context())
	require.NoError(t, err)
	require.NoError(t, db.Master().WithContext(t.Context()).Exec("delete from transactional_outbox").Error)
	return db, repository.New(database.NewBaseRepository(db)), migrations
}

type cleanupRetentionCase struct {
	name        string
	status      string
	processedAt sql.NullTime
	deleted     bool
}

func TestDeleteProcessedMessagesRetainsUndeliveredAndRecentMessages(t *testing.T) {
	db, repo, _ := newOutboxRepository(t)
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour).Truncate(time.Second)
	old := sql.NullTime{Time: cutoff.Add(-time.Hour), Valid: true}
	cases := []cleanupRetentionCase{
		{name: "expired processed", status: models.MessageStatusProcessed, processedAt: old, deleted: true},
		{name: "pending", status: models.MessageStatusPending},
		{name: "pending with timestamp", status: models.MessageStatusPending, processedAt: old},
		{name: "failed", status: models.MessageStatusFailed},
		{name: "failed with timestamp", status: models.MessageStatusFailed, processedAt: old},
		{name: "processed without timestamp", status: models.MessageStatusProcessed},
		{name: "exact cutoff", status: models.MessageStatusProcessed,
			processedAt: sql.NullTime{Time: cutoff, Valid: true}},
		{name: "recently processed", status: models.MessageStatusProcessed,
			processedAt: sql.NullTime{Time: cutoff.Add(time.Hour), Valid: true}},
	}
	messages := make([]models.Message, 0, len(cases))
	for _, test := range cases {
		message := newCleanupMessage(cutoff.Add(-30 * 24 * time.Hour))
		message.Status = test.status
		message.ProcessedAt = test.processedAt
		message.RetryCount = 1
		require.NoError(t, repo.NewOutboxMessage(t.Context(), &message))
		messages = append(messages, message)
	}
	// A cutoff expressed in another timezone must select the same messages.
	deleted, err := repo.DeleteProcessedMessages(t.Context(), cutoff.In(time.FixedZone("test", 14*60*60)), 1000)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	for i, test := range cases {
		var count int64
		require.NoError(t, db.Master().WithContext(t.Context()).Model(&models.Message{}).
			Where("id = ?", messages[i].ID).Count(&count).Error)
		if test.deleted {
			assert.Zero(t, count, test.name)
		} else {
			assert.Equal(t, int64(1), count, test.name)
		}
	}
}

func TestDeleteProcessedMessagesBatchesOldestFirst(t *testing.T) {
	db, repo, _ := newOutboxRepository(t)
	cutoff := time.Now().UTC().Truncate(time.Second)
	newest := newCleanupMessage(cutoff.Add(-time.Hour))
	oldest := newCleanupMessage(cutoff.Add(-3 * time.Hour))
	middle := newCleanupMessage(cutoff.Add(-2 * time.Hour))
	for _, message := range []models.Message{newest, oldest, middle} {
		require.NoError(t, repo.NewOutboxMessage(t.Context(), &message))
	}
	deleted, err := repo.DeleteProcessedMessages(t.Context(), cutoff, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted)
	var remaining []models.Message
	require.NoError(t, db.Master().WithContext(t.Context()).Find(&remaining).Error)
	require.Len(t, remaining, 1)
	assert.Equal(t, newest.ID, remaining[0].ID)
	deleted, err = repo.DeleteProcessedMessages(t.Context(), cutoff, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	deleted, err = repo.DeleteProcessedMessages(t.Context(), cutoff, 2)
	require.NoError(t, err)
	assert.Zero(t, deleted)
}

func TestDeleteProcessedMessagesRechecksEligibility(t *testing.T) {
	cutoff := time.Now().UTC().Truncate(time.Second)
	for name, updates := range map[string]map[string]any{
		"status changed":          {"status": models.MessageStatusPending},
		"processing time changed": {"processed_at": cutoff},
		"processing time cleared": {"processed_at": nil},
	} {
		t.Run(name, func(t *testing.T) {
			db, repo, _ := newOutboxRepository(t)
			message := newCleanupMessage(cutoff.Add(-time.Hour))
			require.NoError(t, repo.NewOutboxMessage(t.Context(), &message))
			// Change the selected row immediately before the delete statement.
			callback := db.Master().Callback().Delete()
			require.NoError(t, callback.Before("gorm:delete").Register("test:change_outbox_message", func(tx *gorm.DB) {
				err := tx.Session(&gorm.Session{NewDB: true}).Model(&models.Message{}).
					Where("id = ?", message.ID).Updates(updates).Error
				if err != nil {
					_ = tx.AddError(err)
				}
			}))
			t.Cleanup(func() { require.NoError(t, callback.Remove("test:change_outbox_message")) })
			deleted, err := repo.DeleteProcessedMessages(t.Context(), cutoff, 10)
			require.NoError(t, err)
			assert.Zero(t, deleted)
			var stored models.Message
			require.NoError(t, db.Master().WithContext(t.Context()).First(&stored, "id = ?", message.ID).Error)
		})
	}
}

func TestDeleteProcessedMessagesRejectsNonpositiveBatchSize(t *testing.T) {
	db, repo, _ := newOutboxRepository(t)
	cutoff := time.Now().UTC()
	message := newCleanupMessage(cutoff.Add(-time.Hour))
	require.NoError(t, repo.NewOutboxMessage(t.Context(), &message))
	for _, limit := range []int{0, -1} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			deleted, err := repo.DeleteProcessedMessages(t.Context(), cutoff, limit)
			require.ErrorContains(t, err, "batch size must be positive")
			assert.Zero(t, deleted)
		})
	}
	var stored models.Message
	require.NoError(t, db.Master().WithContext(t.Context()).First(&stored, "id = ?", message.ID).Error)
}

func TestDeleteProcessedMessagesPropagatesCancellation(t *testing.T) {
	_, repo, _ := newOutboxRepository(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	deleted, err := repo.DeleteProcessedMessages(ctx, time.Now(), 10)
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, deleted)
}

func TestDeleteProcessedMessagesPropagatesDeleteError(t *testing.T) {
	db, repo, _ := newOutboxRepository(t)
	cutoff := time.Now().UTC()
	message := newCleanupMessage(cutoff.Add(-time.Hour))
	require.NoError(t, repo.NewOutboxMessage(t.Context(), &message))
	wantErr := errors.New("delete unavailable")
	callback := db.Master().Callback().Delete()
	require.NoError(t, callback.Before("gorm:delete").Register("test:fail_outbox_delete", func(tx *gorm.DB) {
		_ = tx.AddError(wantErr)
	}))
	t.Cleanup(func() { require.NoError(t, callback.Remove("test:fail_outbox_delete")) })
	deleted, err := repo.DeleteProcessedMessages(t.Context(), cutoff, 10)
	require.ErrorIs(t, err, wantErr)
	assert.Zero(t, deleted)
	var stored models.Message
	require.NoError(t, db.Master().WithContext(t.Context()).First(&stored, "id = ?", message.ID).Error)
}

func newCleanupMessage(processedAt time.Time) models.Message {
	return models.Message{
		ID: uuid.New(), AggregateID: 42, AggregateType: "user.created", Topic: "auth",
		CreatedAt: processedAt.Add(-time.Hour), ProcessedAt: sql.NullTime{Time: processedAt, Valid: true},
		Status: models.MessageStatusProcessed, MaxRetries: domain.MaxRetries,
	}
}

func TestOutboxMigrationIsReversibleAndIdempotent(t *testing.T) {
	db, _, _ := newOutboxRepository(t)
	engine, dialect := "sqlite", goose.DialectSQLite3
	switch db.Master().Name() {
	case "postgres":
		engine, dialect = "pgsql", goose.DialectPostgres
	case "mysql":
		engine, dialect = "mysql", goose.DialectMySQL
	}
	sqlDB, err := db.Master().DB()
	require.NoError(t, err)
	provider, err := goose.NewProvider(dialect, sqlDB,
		os.DirFS(filepath.Join("..", "..", "..", "migrate", engine)),
		goose.WithDisableVersioning(true))
	require.NoError(t, err)
	for _, up := range []bool{true, true, false, false, true} {
		_, err := provider.ApplyVersion(t.Context(), 20250717175100, up)
		require.NoError(t, err)
		assert.Equal(t, up, db.Master().WithContext(t.Context()).Migrator().
			HasIndex(&models.Message{}, "transactional_outbox_cleanup"))
	}
}
