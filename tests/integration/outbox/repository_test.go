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
	"github.com/go42-dev/go42/internal/events"
	"github.com/go42-dev/go42/internal/outbox"
	"github.com/go42-dev/go42/internal/outbox/domain"
	"github.com/go42-dev/go42/internal/outbox/models"
	"github.com/go42-dev/go42/internal/outbox/repository"
	"github.com/go42-dev/go42/internal/tools"
	"github.com/go42-dev/go42/tests/integration"
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
	db, _ := integration.NewDatabase(t)
	engine, dialect := "sqlite", goose.DialectSQLite3
	switch db.Master().Name() {
	case "postgres":
		engine, dialect = "pgsql", goose.DialectPostgres
	case "mysql":
		engine, dialect = "mysql", goose.DialectMySQL
	}
	sqlDB, err := db.Master().DB()
	require.NoError(t, err)
	migrations, err := goose.NewProvider(dialect, sqlDB,
		os.DirFS(filepath.Join("..", "..", "..", "migrate", engine)))
	require.NoError(t, err)
	_, err = migrations.Up(t.Context())
	require.NoError(t, err)
	return db, repository.New(database.NewBaseRepository(db)), migrations
}

func TestOutboxRetryScheduleSurvivesRepositoryRestart(t *testing.T) {
	db, repo, migrations := newOutboxRepository(t)
	first, future, last := newOutboxTestMessage(), newOutboxTestMessage(), newOutboxTestMessage()
	now := time.Now().UTC().Truncate(time.Microsecond)
	first.CreatedAt, future.CreatedAt, last.CreatedAt = now.Add(
		-3*time.Minute,
	), now.Add(
		-2*time.Minute,
	), now.Add(
		-time.Minute,
	)
	first.NextAttemptAt = sql.NullTime{Time: now.Add(-time.Second), Valid: true}
	future.NextAttemptAt = sql.NullTime{Time: now.Add(time.Hour), Valid: true}
	future.RetryCount, future.MaxRetries = 50, 3
	for _, entry := range []*models.Message{&last, &future, &first} {
		require.NoError(t, repo.NewOutboxMessage(t.Context(), entry))
	}
	// Migration reruns must preserve the schedule, and a new repository has no in-memory timer state.
	applied, err := migrations.Up(t.Context())
	require.NoError(t, err)
	require.Empty(t, applied)
	repo = repository.New(database.NewBaseRepository(db))
	due, err := repo.GetUnprocessedMessages(t.Context(), 10)
	require.NoError(t, err)
	require.Len(t, due, 2)
	require.Equal(t, first.ID, due[0].ID)
	require.Equal(t, last.ID, due[1].ID)
	var stored models.Message
	require.NoError(t, db.Master().WithContext(t.Context()).First(&stored, "id = ?", future.ID).Error)
	require.True(t, future.NextAttemptAt.Time.Equal(stored.NextAttemptAt.Time))
	require.Equal(t, 50, stored.RetryCount)

	stored.NextAttemptAt = sql.NullTime{Time: now.Add(-time.Second), Valid: true}
	require.NoError(t, repo.SaveFailedMessages(t.Context(), []models.Message{stored}))
	due, err = repo.GetUnprocessedMessages(t.Context(), 2)
	require.NoError(t, err)
	require.Len(t, due, 2)
	require.Equal(t, first.ID, due[0].ID)
	require.Equal(t, future.ID, due[1].ID)
	require.NoError(t, repo.SaveProcessedMessages(t.Context(), due))
	stored = models.Message{}
	require.NoError(t, db.Master().WithContext(t.Context()).First(&stored, "id = ?", future.ID).Error)
	require.False(t, stored.NextAttemptAt.Valid)
	require.Equal(t, models.MessageStatusProcessed, stored.Status)
}

func TestOutboxRollbackReleasesLockedMessages(t *testing.T) {
	db, repo, _ := newOutboxRepository(t)
	if db.Master().Name() == "sqlite" {
		t.Skip("row locking requires PostgreSQL or MySQL")
	}
	entry := newOutboxTestMessage()
	entry.Metadata = map[string]string{"request_id": "rollback-request"}
	require.NoError(t, repo.NewOutboxMessage(t.Context(), &entry))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	first, err := repo.Begin(ctx, sql.LevelDefault)
	require.NoError(t, err)
	defer func() { _ = repo.Rollback(first) }()
	locked, err := repo.GetUnprocessedMessages(first, 1)
	require.NoError(t, err)
	require.Len(t, locked, 1)
	require.Equal(t, entry.ID, locked[0].ID)
	locked[0].RetryCount = 1
	locked[0].LastError = "rolled-back failure"
	require.NoError(t, repo.SaveFailedMessages(first, locked))
	second, err := repo.Begin(ctx, sql.LevelDefault)
	require.NoError(t, err)
	defer func() { _ = repo.Rollback(second) }()
	skipped, err := repo.GetUnprocessedMessages(second, 1)
	require.NoError(t, err)
	require.Empty(t, skipped, "a competing worker must skip the locked row")
	require.NoError(t, repo.Rollback(second))
	require.NoError(t, repo.Rollback(first))

	require.NoError(t, repo.WithTransaction(ctx, func(txCtx context.Context) error {
		recovered, err := repo.GetUnprocessedMessages(txCtx, 1)
		if err != nil {
			return err
		}
		require.Len(t, recovered, 1, "rollback must make the message available to another worker")
		assert.Equal(t, entry.ID, recovered[0].ID)
		assert.Equal(t, entry.Payload, recovered[0].Payload)
		assert.Equal(t, entry.Metadata, recovered[0].Metadata)
		assert.Zero(t, recovered[0].RetryCount)
		assert.Empty(t, recovered[0].LastError)
		return repo.SaveProcessedMessages(txCtx, recovered)
	}))
	remaining, err := repo.GetUnprocessedMessages(ctx, 1)
	require.NoError(t, err)
	assert.Empty(t, remaining)
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
		migrator := db.Master().WithContext(t.Context()).Migrator()
		assert.Equal(t, up, migrator.HasColumn(&models.Message{}, "next_attempt_at"))
		for _, index := range []string{
			"transactional_outbox_publisher",
			"transactional_outbox_cleanup",
			"transactional_outbox_retry_schedule",
		} {
			assert.Equal(t, up, migrator.HasIndex(&models.Message{}, index), index)
		}
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
			if db.Master().Name() == "mysql" {
				// MySQL's TIMESTAMP columns round values to whole seconds.
				finished = finished.Round(time.Second)
			}
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

func TestSaveFailedMessagesOnlyUpdatesExistingRows(t *testing.T) {
	db, repo, _ := newOutboxRepository(t)
	before := insertOutboxPersistenceMessages(t, db, repo, "pending", "pending")
	existing := before[0]
	existing.RetryCount++
	existing.LastError = "retry existing row"
	missing := newOutboxTestMessage()
	withoutID := newOutboxTestMessage()
	withoutID.ID = uuid.Nil
	updates, inserts := 0, 0
	updateCallbacks, createCallbacks := db.Master().Callback().Update(), db.Master().Callback().Create()
	require.NoError(t, updateCallbacks.Before("gorm:update").Register("test:retry_update", func(*gorm.DB) {
		updates++
	}))
	require.NoError(t, createCallbacks.Before("gorm:create").Register("test:retry_create", func(*gorm.DB) {
		inserts++
	}))
	t.Cleanup(func() {
		require.NoError(t, updateCallbacks.Remove("test:retry_update"))
		require.NoError(t, createCallbacks.Remove("test:retry_create"))
	})
	messages := []models.Message{existing, missing, withoutID, before[1]}
	require.NoError(t, repo.SaveFailedMessages(t.Context(), messages))
	assert.Equal(t, len(messages), updates, "each message must invoke update callbacks")
	assert.Zero(t, inserts, "missing IDs and unchanged rows must never trigger an insert")
	assert.ElementsMatch(t, []models.Message{existing, before[1]}, storedOutboxMessages(t, db))
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
