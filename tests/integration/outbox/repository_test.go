package outbox_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

func TestGetUnprocessedMessagesFiltersAndLimits(t *testing.T) {
	for _, test := range []struct {
		name     string
		statuses []string
		limit    int
		want     int
	}{
		{name: "mixed statuses", statuses: []string{"pending", "processed", "failed", "pending"}, limit: 10, want: 2},
		{name: "limited batch", statuses: []string{"pending", "processed", "failed", "pending"}, limit: 1, want: 1},
		{name: "no pending messages", statuses: []string{"processed", "failed"}, limit: 10},
		{name: "empty table", limit: 10},
		{name: "zero limit", statuses: []string{"pending"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, repo, _ := newOutboxRepository(t)
			before := insertOutboxPersistenceMessages(t, db, repo, test.statuses...)
			pending := make(map[uuid.UUID]models.Message)
			for _, entry := range before {
				if entry.Status == models.MessageStatusPending {
					pending[entry.ID] = entry
				}
			}
			got, err := repo.GetUnprocessedMessages(t.Context(), test.limit)
			require.NoError(t, err)
			require.Len(t, got, test.want)
			for _, entry := range got {
				expected, exists := pending[entry.ID]
				require.True(t, exists, "unexpected or repeated message %s", entry.ID)
				assert.Equal(t, expected, entry)
				delete(pending, entry.ID)
			}
			assert.ElementsMatch(t, before, storedOutboxMessages(t, db), "selecting a batch must not change its state")
		})
	}
}

func TestSaveProcessedMessagesUpdatesSelectedRows(t *testing.T) {
	for _, unusualIDs := range []bool{false, true} {
		name := "selected rows"
		if unusualIDs {
			name = "missing and repeated IDs"
		}
		t.Run(name, func(t *testing.T) {
			db, repo, _ := newOutboxRepository(t)
			before := insertOutboxPersistenceMessages(t, db, repo, "pending", "pending", "failed")
			selected := []models.Message{before[0], before[2]}
			if unusualIDs {
				selected = append(selected, before[0], models.Message{ID: uuid.New()})
			}
			started := time.Now().UTC().Truncate(time.Second)
			require.NoError(t, repo.SaveProcessedMessages(t.Context(), selected))
			finished := time.Now().UTC()
			stored := storedOutboxMessages(t, db)
			require.Len(t, stored, len(before))
			var processedAt time.Time
			for _, entry := range stored {
				index := slices.IndexFunc(
					before,
					func(candidate models.Message) bool { return candidate.ID == entry.ID },
				)
				require.NotEqual(t, -1, index)
				expected := before[index]
				if index != 1 {
					require.True(t, entry.ProcessedAt.Valid)
					assert.WithinRange(t, entry.ProcessedAt.Time, started, finished)
					if processedAt.IsZero() {
						processedAt = entry.ProcessedAt.Time
					} else {
						assert.True(
							t,
							processedAt.Equal(entry.ProcessedAt.Time),
							"a batch must share its processing time",
						)
					}
					expected.Status = models.MessageStatusProcessed
					expected.ProcessedAt = entry.ProcessedAt
				}
				assert.Equal(t, expected, entry, "processing must preserve payload, metadata, and retry history")
			}
		})
	}
}

func TestOutboxEmptyBatchesPreserveRows(t *testing.T) {
	for _, test := range []struct {
		name      string
		processed bool
		messages  []models.Message
	}{
		{name: "processed nil", processed: true},
		{name: "processed empty", processed: true, messages: []models.Message{}},
		{name: "failed nil"},
		{name: "failed empty", messages: []models.Message{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, repo, _ := newOutboxRepository(t)
			before := insertOutboxPersistenceMessages(t, db, repo, "pending", "processed", "failed")
			if test.processed {
				require.NoError(t, repo.SaveProcessedMessages(t.Context(), test.messages))
			} else {
				require.NoError(t, repo.SaveFailedMessages(t.Context(), test.messages))
			}
			assert.ElementsMatch(t, before, storedOutboxMessages(t, db))
		})
	}
}

func TestSaveFailedMessagesPersistsRetryState(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  string
		retries int
		err     string
	}{
		{name: "retryable", status: models.MessageStatusPending, retries: 2, err: "broker unavailable"},
		{name: "exhausted", status: models.MessageStatusFailed, retries: domain.MaxRetries, err: "retries exhausted"},
		{name: "cleared retry history", status: models.MessageStatusPending},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, repo, _ := newOutboxRepository(t)
			before := insertOutboxPersistenceMessages(t, db, repo, "pending", "pending")
			updated := before[0]
			updated.Status, updated.RetryCount, updated.LastError = test.status, test.retries, test.err
			require.NoError(t, repo.SaveFailedMessages(t.Context(), []models.Message{updated}))
			assert.ElementsMatch(t, []models.Message{updated, before[1]}, storedOutboxMessages(t, db))
		})
	}
}

func TestOutboxRepositoryCancellationPreservesRows(t *testing.T) {
	for _, operation := range []string{"insert", "select", "processed", "failed"} {
		t.Run(operation, func(t *testing.T) {
			db, repo, _ := newOutboxRepository(t)
			before := insertOutboxPersistenceMessages(t, db, repo, "pending", "failed")
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			var err error
			switch operation {
			case "insert":
				entry := newOutboxTestMessage()
				err = repo.NewOutboxMessage(ctx, &entry)
			case "select":
				var messages []models.Message
				messages, err = repo.GetUnprocessedMessages(ctx, 10)
				assert.Nil(t, messages)
			case "processed":
				err = repo.SaveProcessedMessages(ctx, before[:1])
			case "failed":
				updated := before[0]
				updated.Status, updated.RetryCount, updated.LastError = models.MessageStatusFailed, 3, "new error"
				err = repo.SaveFailedMessages(ctx, []models.Message{updated})
			}
			require.ErrorIs(t, err, context.Canceled)
			assert.ElementsMatch(t, before, storedOutboxMessages(t, db))
		})
	}
}

func TestOutboxBatchFailureRollsBackProcessedAndRetriedMessages(t *testing.T) {
	db, repo, _ := newOutboxRepository(t)
	before := insertOutboxPersistenceMessages(t, db, repo, "pending", "pending", "pending")
	successfulUpdates := 0
	callback := db.Master().Callback().Update()
	const name = "test:observe_outbox_updates"
	require.NoError(t, callback.After("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "transactional_outbox" && tx.Error == nil {
			successfulUpdates++
		}
	}))
	t.Cleanup(func() { require.NoError(t, callback.Remove(name)) })
	err := repo.WithTransaction(t.Context(), func(ctx context.Context) error {
		if err := repo.SaveProcessedMessages(ctx, before[:1]); err != nil {
			return err
		}
		retried := before[1]
		retried.RetryCount++
		retried.LastError = "retry after broker failure"
		invalid := before[2]
		invalid.Status = "invalid status"
		return repo.SaveFailedMessages(ctx, []models.Message{retried, invalid})
	})
	require.ErrorContains(t, err, before[2].ID.String())
	assert.ErrorContains(t, errors.Unwrap(err), "status")
	assert.Equal(t, 2, successfulUpdates, "processing and the first retry must succeed before the failing write")
	assert.ElementsMatch(t, before, storedOutboxMessages(t, db), "the transaction must roll back both earlier updates")
}

func insertOutboxPersistenceMessages(
	t *testing.T, db database.Database, repo *repository.Repository, statuses ...string,
) []models.Message {
	t.Helper()
	messages := make([]models.Message, len(statuses))
	for index, status := range statuses {
		entry := newOutboxTestMessage()
		entry.Status = status
		entry.AggregateID += index
		entry.Payload = []byte(fmt.Sprintf(`{"index":%d}`, index))
		entry.CreatedAt = entry.CreatedAt.UTC().Truncate(time.Second)
		entry.RetryCount = 1
		entry.LastError = "previous failure"
		entry.Metadata = map[string]string{"request_id": entry.ID.String()}
		if status == models.MessageStatusProcessed {
			entry.ProcessedAt = sql.NullTime{Time: entry.CreatedAt.Add(time.Second), Valid: true}
		}
		require.NoError(t, repo.NewOutboxMessage(t.Context(), &entry))
		require.NoError(t, db.Master().WithContext(t.Context()).First(&messages[index], "id = ?", entry.ID).Error)
	}
	return messages
}

func storedOutboxMessages(t *testing.T, db database.Database) []models.Message {
	t.Helper()
	var messages []models.Message
	require.NoError(t, db.Master().WithContext(t.Context()).Find(&messages).Error)
	return messages
}
