package outbox_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/go42-dev/go42/internal/database"
	"github.com/go42-dev/go42/internal/events"
	"github.com/go42-dev/go42/internal/outbox/domain"
	"github.com/go42-dev/go42/internal/outbox/models"
	outboxRepository "github.com/go42-dev/go42/internal/outbox/repository"
	"github.com/go42-dev/go42/internal/outbox/workers"
	"github.com/go42-dev/go42/internal/outbox/workers/mocks"
)

func TestOutboxPublisherTimeoutCommitsProgressAndReleasesConnection(t *testing.T) {
	db, repo := newOutboxPublisherDatabase(t)
	pool, err := db.Master().DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	for range 3 {
		entry := newOutboxTestMessage()
		require.NoError(t, repo.NewOutboxMessage(t.Context(), &entry))
	}
	entered := make(chan struct{})
	released := make(chan struct{})
	finished := make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	var calls atomic.Int32
	published := make(chan uuid.UUID, 4)
	router := newOutboxTestRouter(t, func(_ string, messages ...*message.Message) error {
		var event domain.Event
		if err := json.Unmarshal(messages[0].Payload, &event); err != nil {
			return err
		}
		published <- event.ID
		if calls.Add(1) == 2 {
			close(entered)
			<-released // Keep the underlying broker call blocked beyond its caller's deadline.
			close(finished)
		}
		return nil
	}, events.WithPublishMaxInflight(1))
	worker := workers.NewOutboxMessagePublisher(repo, router,
		workers.OutboxMessagePublisherWithPublishTimeout(50*time.Millisecond),
		workers.OutboxMessagePublisherWithRetryBackoff(time.Hour, time.Hour))
	done := startOutboxPublisher(t, t.Context(), repo, worker, 3)
	waitForOutboxPublishSignal(t, entered)
	require.NoError(t, waitForOutboxRun(t, done))
	require.EqualValues(t, 2, calls.Load(), "the batch must stop on the first timeout")
	successID, timeoutID := <-published, <-published

	// These queries reuse the pool's only connection while the underlying publish is still blocked.
	queryCtx, queryCancel := context.WithTimeout(t.Context(), time.Second)
	defer queryCancel()
	require.NoError(t, db.Master().WithContext(queryCtx).Exec("SELECT 1").Error)
	var stored []models.Message
	require.NoError(t, db.Master().WithContext(queryCtx).Find(&stored).Error)
	require.Len(t, stored, 3)
	for _, entry := range stored {
		switch entry.ID {
		case successID:
			require.Equal(t, models.MessageStatusProcessed, entry.Status)
			require.True(t, entry.ProcessedAt.Valid)
		case timeoutID:
			require.Equal(t, models.MessageStatusPending, entry.Status)
			require.True(t, entry.NextAttemptAt.Valid)
			require.True(t, entry.NextAttemptAt.Time.After(time.Now()))
			require.Equal(t, 1, entry.RetryCount)
			require.Equal(t, context.DeadlineExceeded.Error(), entry.LastError)
		default:
			require.Equal(t, models.MessageStatusPending, entry.Status)
			require.Zero(t, entry.RetryCount)
			require.Empty(t, entry.LastError)
		}
	}
	// The untouched row cannot acquire capacity while the timed-out broker call holds the only slot.
	for range 3 {
		done = startOutboxPublisher(t, t.Context(), repo, worker, 3)
		require.NoError(t, waitForOutboxRun(t, done))
		var after []models.Message
		require.NoError(t, db.Master().WithContext(t.Context()).Find(&after).Error)
		require.ElementsMatch(t, stored, after, "waiting for capacity must not change any stored message")
		require.EqualValues(t, 2, calls.Load(), "no additional broker calls may start while capacity is full")
	}
	release()
	waitForOutboxPublishSignal(t, finished)
	var timedOut models.Message
	require.NoError(t, db.Master().WithContext(t.Context()).First(&timedOut, "id = ?", timeoutID).Error)
	require.Equal(t, models.MessageStatusPending, timedOut.Status, "late completion must not change stored state")
	require.Equal(t, 1, timedOut.RetryCount)

	done = startOutboxPublisher(t, t.Context(), repo, worker, 3)
	require.NoError(t, waitForOutboxRun(t, done))
	require.EqualValues(t, 3, calls.Load(), "future retry must not block an untouched message")
	require.NotEqual(t, successID, <-published, "committed successes must not be sent again")

	timedOut.NextAttemptAt = sql.NullTime{Time: time.Now().UTC().Add(-time.Second), Valid: true}
	require.NoError(t, repo.SaveFailedMessages(t.Context(), []models.Message{timedOut}))
	// A new worker uses the persisted schedule and the same event identity.
	worker = workers.NewOutboxMessagePublisher(repo, router)
	done = startOutboxPublisher(t, t.Context(), repo, worker, 3)
	require.NoError(t, waitForOutboxRun(t, done))
	require.EqualValues(t, 4, calls.Load())
	require.Equal(t, timeoutID, <-published)
	require.NoError(t, db.Master().WithContext(t.Context()).Find(&stored).Error)
	for _, entry := range stored {
		require.Equal(t, models.MessageStatusProcessed, entry.Status)
		require.False(t, entry.NextAttemptAt.Valid)
	}
}

func TestOutboxPublisherCancellationRollsBackWithoutConsumingRetry(t *testing.T) {
	db, repo := newOutboxPublisherDatabase(t)
	for range 2 {
		entry := newOutboxTestMessage()
		require.NoError(t, repo.NewOutboxMessage(t.Context(), &entry))
	}
	entered := make(chan struct{})
	released := make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	var calls atomic.Int32
	router := newOutboxTestRouter(t, func(string, ...*message.Message) error {
		if calls.Add(1) == 2 {
			close(entered)
			<-released
		}
		return nil
	})
	worker := workers.NewOutboxMessagePublisher(repo, router)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := startOutboxPublisher(t, ctx, repo, worker, 2)
	waitForOutboxPublishSignal(t, entered)
	cancel()
	require.ErrorIs(t, waitForOutboxRun(t, done), context.Canceled)
	queryCtx, queryCancel := context.WithTimeout(t.Context(), time.Second)
	defer queryCancel()
	var stored []models.Message
	require.NoError(t, db.Master().WithContext(queryCtx).Find(&stored).Error)
	require.Len(t, stored, 2)
	for _, entry := range stored {
		require.Equal(t, models.MessageStatusPending, entry.Status)
		require.Zero(t, entry.RetryCount)
		require.Empty(t, entry.LastError)
	}
	release()
	worker = workers.NewOutboxMessagePublisher(repo, router)
	require.NoError(t, waitForOutboxRun(t, startOutboxPublisher(t, t.Context(), repo, worker, 2)))
	require.NoError(t, db.Master().WithContext(t.Context()).Find(&stored).Error)
	for _, entry := range stored {
		require.Equal(
			t,
			models.MessageStatusProcessed,
			entry.Status,
			"another worker must recover the rolled-back batch",
		)
		require.Zero(t, entry.RetryCount)
	}
}

func TestOutboxPublisherTimeoutPersistenceFailureRollsBackProgress(t *testing.T) {
	db, repo := newOutboxPublisherDatabase(t)
	for range 2 {
		entry := newOutboxTestMessage()
		require.NoError(t, repo.NewOutboxMessage(t.Context(), &entry))
	}
	rejectOutboxRetries(t, db)
	ctrl := gomock.NewController(t)
	publisher := mocks.NewMockpublisher(ctrl)
	gomock.InOrder(
		publisher.EXPECT().Publish(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil),
		publisher.EXPECT().
			Publish(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return(context.DeadlineExceeded),
	)
	worker := workers.NewOutboxMessagePublisher(repo, publisher)
	done := startOutboxPublisher(t, t.Context(), repo, worker, 2)
	require.ErrorContains(t, waitForOutboxRun(t, done), "reject_outbox_retry")
	var stored []models.Message
	require.NoError(t, db.Master().WithContext(t.Context()).Find(&stored).Error)
	require.Len(t, stored, 2)
	for _, entry := range stored {
		require.Equal(t, models.MessageStatusPending, entry.Status)
		require.False(t, entry.ProcessedAt.Valid)
		require.Zero(t, entry.RetryCount)
	}
}

func newOutboxPublisherDatabase(t *testing.T) (database.Database, *publisherTestRepository) {
	t.Helper()
	db, repo, _ := newOutboxRepository(t)
	return db, &publisherTestRepository{Repository: repo}
}

func rejectOutboxRetries(t *testing.T, db database.Database) {
	t.Helper()
	statement := "ALTER TABLE transactional_outbox ADD CONSTRAINT reject_outbox_retry CHECK (retry_count = 0)"
	cleanup := "ALTER TABLE transactional_outbox DROP CONSTRAINT reject_outbox_retry"
	switch db.Master().Name() {
	case "sqlite":
		statement = `CREATE TRIGGER reject_outbox_retry BEFORE UPDATE ON transactional_outbox
			WHEN NEW.retry_count > OLD.retry_count
			BEGIN SELECT RAISE(ABORT, 'reject_outbox_retry'); END`
		cleanup = "DROP TRIGGER reject_outbox_retry"
	case "mysql":
		cleanup = "ALTER TABLE transactional_outbox DROP CHECK reject_outbox_retry"
	}
	require.NoError(t, db.Master().WithContext(t.Context()).Exec(statement).Error)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, db.Master().WithContext(ctx).Exec(cleanup).Error)
	})
}

func TestOutboxPublisherAllowsConcurrentEnqueue(t *testing.T) {
	db, repo := newOutboxPublisherDatabase(t)
	if db.Master().Name() == "sqlite" {
		t.Skip("concurrent writers require PostgreSQL or MySQL")
	}
	entry := newOutboxTestMessage()
	require.NoError(t, repo.NewOutboxMessage(t.Context(), &entry))
	entered, released := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	router := newOutboxTestRouter(t, func(_ string, _ ...*message.Message) error {
		close(entered)
		<-released
		return nil
	})
	worker := workers.NewOutboxMessagePublisher(repo, router)
	// Scan beyond the current queue so the test also covers locks on the insertion gap.
	done := startOutboxPublisher(t, t.Context(), repo, worker, 1000)
	waitForOutboxPublishSignal(t, entered)

	next := newOutboxTestMessage()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	producer := database.NewBaseRepository(db)
	err := producer.WithTransaction(ctx, func(txCtx context.Context) error {
		return repo.NewOutboxMessage(txCtx, &next)
	})
	require.NoError(t, err, "enqueue must commit while the publisher holds its current batch")
	select {
	case err := <-done:
		t.Fatalf("publisher returned before its blocked publish was released: %v", err)
	default:
	}

	release()
	require.NoError(t, waitForOutboxRun(t, done))
	pending, err := repo.GetUnprocessedMessages(t.Context(), 1000)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, next.ID, pending[0].ID, "new messages must remain available for the next batch")
	var processed models.Message
	require.NoError(t, db.Master().WithContext(t.Context()).First(&processed, "id = ?", entry.ID).Error)
	require.Equal(t, models.MessageStatusProcessed, processed.Status)
}

func TestOutboxConcurrentPublishersProcessDisjointBatches(t *testing.T) {
	db, first := newOutboxPublisherDatabase(t)
	if db.Master().Name() == "sqlite" {
		t.Skip("row locking requires PostgreSQL or MySQL")
	}
	ids := make([]uuid.UUID, 0, 4)
	for range 4 {
		entry := newOutboxTestMessage()
		require.NoError(t, first.NewOutboxMessage(t.Context(), &entry))
		ids = append(ids, entry.ID)
	}
	entered, released := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	var calls atomic.Int32
	published := make(chan uuid.UUID, len(ids))
	router := newOutboxTestRouter(t, func(_ string, messages ...*message.Message) error {
		var event domain.Event
		if err := json.Unmarshal(messages[0].Payload, &event); err != nil {
			return err
		}
		published <- event.ID
		if calls.Add(1) == 1 {
			close(entered)
			<-released
		}
		return nil
	})
	firstWorker := workers.NewOutboxMessagePublisher(first, router)
	firstDone := startOutboxPublisher(t, t.Context(), first, firstWorker, 2)
	waitForOutboxPublishSignal(t, entered)
	second := &publisherTestRepository{Repository: first.Repository}
	secondWorker := workers.NewOutboxMessagePublisher(second, router)
	require.NoError(t, waitForOutboxRun(t, startOutboxPublisher(t, t.Context(), second, secondWorker, 2)))
	var processed int64
	require.NoError(t, db.Master().WithContext(t.Context()).Model(&models.Message{}).
		Where("status = ?", models.MessageStatusProcessed).Count(&processed).Error)
	require.EqualValues(t, 2, processed, "the second worker must commit while the first holds its row locks")
	select {
	case err := <-firstDone:
		t.Fatalf("first worker returned before its blocked publish was released: %v", err)
	default:
	}
	release()
	require.NoError(t, waitForOutboxRun(t, firstDone))
	require.EqualValues(t, len(ids), calls.Load())
	require.Len(t, published, len(ids))
	actual := make([]uuid.UUID, 0, len(ids))
	for range ids {
		actual = append(actual, <-published)
	}
	require.ElementsMatch(t, ids, actual, "each message belongs to exactly one worker's batch")
	remaining, err := first.GetUnprocessedMessages(t.Context(), len(ids))
	require.NoError(t, err)
	require.Empty(t, remaining)
	var stored []models.Message
	require.NoError(t, db.Master().WithContext(t.Context()).Find(&stored).Error)
	require.Len(t, stored, len(ids))
	for _, entry := range stored {
		require.Equal(t, models.MessageStatusProcessed, entry.Status)
		require.True(t, entry.ProcessedAt.Valid)
		require.Zero(t, entry.RetryCount)
	}
}

type publisherTestRepository struct {
	*outboxRepository.Repository
	afterTransaction func(error)
}

func (r *publisherTestRepository) WithTransactionIsolation(
	ctx context.Context, isolationLvl sql.IsolationLevel, fn func(context.Context) error,
) error {
	err := r.Repository.WithTransactionIsolation(ctx, isolationLvl, fn)
	r.afterTransaction(err)
	return err
}

// Stop the public worker loop after its first transaction commits or rolls back.
func startOutboxPublisher(
	t *testing.T,
	ctx context.Context,
	repository *publisherTestRepository,
	worker *workers.OutboxMessagePublisher,
	batchSize int,
) <-chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	var completed sync.Once
	result := errors.New("publisher stopped before its first transaction")
	repository.afterTransaction = func(err error) {
		completed.Do(func() {
			result = err
			cancel()
		})
	}
	done := make(chan error, 1)
	stopped := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(3 * time.Second):
			t.Error("outbox worker did not stop after cancellation")
		}
	})
	go func() {
		defer close(stopped)
		worker.Run(ctx, time.Millisecond, batchSize)
		done <- result
	}()
	return done
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

func newOutboxTestRouter(
	t *testing.T, publish func(string, ...*message.Message) error, opts ...events.Option,
) *events.Router {
	t.Helper()
	backend := &outboxTestBackend{NoopEngine: events.NewNoop(), publish: publish}
	router, err := events.NewRouter(backend, append([]events.Option{events.WithCloseTimeout(time.Second)}, opts...)...)
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
