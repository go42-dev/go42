package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/go42-dev/go42/internal/database"
	"github.com/go42-dev/go42/internal/database/sqlite"
	"github.com/go42-dev/go42/internal/events"
	"github.com/go42-dev/go42/internal/outbox/domain"
	"github.com/go42-dev/go42/internal/outbox/models"
	outboxRepository "github.com/go42-dev/go42/internal/outbox/repository"
	"github.com/go42-dev/go42/internal/outbox/workers"
	"github.com/go42-dev/go42/internal/outbox/workers/mocks"
)

func TestOutboxPublisherTimeoutCommitsProgressAndReleasesSQLite(t *testing.T) {
	db, repo := newOutboxPublisherDatabase(t)
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
	})
	worker := workers.NewOutboxMessagePublisher(repo, router,
		workers.OutboxMessagePublisherWithPublishTimeout(50*time.Millisecond))
	done := startOutboxPublisher(t, t.Context(), repo, worker, 3)
	waitForOutboxPublishSignal(t, entered)
	require.NoError(t, waitForOutboxRun(t, done))
	require.EqualValues(t, 2, calls.Load(), "the batch must stop on the first timeout")
	successID, timeoutID := <-published, <-published

	// These queries use SQLite's only connection while the underlying publish is still blocked.
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
			require.Equal(t, 1, entry.RetryCount)
			require.Equal(t, context.DeadlineExceeded.Error(), entry.LastError)
		default:
			require.Equal(t, models.MessageStatusPending, entry.Status)
			require.Zero(t, entry.RetryCount)
			require.Empty(t, entry.LastError)
		}
	}
	release()
	waitForOutboxPublishSignal(t, finished)
	var timedOut models.Message
	require.NoError(t, db.Master().WithContext(t.Context()).First(&timedOut, "id = ?", timeoutID).Error)
	require.Equal(t, models.MessageStatusPending, timedOut.Status, "late completion must not change stored state")
	require.Equal(t, 1, timedOut.RetryCount)

	done = startOutboxPublisher(t, t.Context(), repo, worker, 3)
	require.NoError(t, waitForOutboxRun(t, done))
	require.EqualValues(t, 4, calls.Load())
	for range 2 {
		require.NotEqual(t, successID, <-published, "committed successes must not be sent again")
	}
	require.NoError(t, db.Master().WithContext(t.Context()).Find(&stored).Error)
	for _, entry := range stored {
		require.Equal(t, models.MessageStatusProcessed, entry.Status)
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
}

func TestOutboxPublisherTimeoutPersistenceFailureRollsBackProgress(t *testing.T) {
	db, repo := newOutboxPublisherDatabase(t)
	for range 2 {
		entry := newOutboxTestMessage()
		require.NoError(t, repo.NewOutboxMessage(t.Context(), &entry))
	}
	require.NoError(t, db.Master().WithContext(t.Context()).Exec(`
		CREATE TRIGGER reject_outbox_retry BEFORE UPDATE ON transactional_outbox
		WHEN NEW.retry_count > OLD.retry_count
		BEGIN SELECT RAISE(ABORT, 'retry write rejected'); END
	`).Error)
	ctrl := gomock.NewController(t)
	publisher := mocks.NewMockpublisher(ctrl)
	gomock.InOrder(
		publisher.EXPECT().Publish(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil),
		publisher.EXPECT().Publish(gomock.Any(), gomock.Any(), gomock.Any()).Return(context.DeadlineExceeded),
	)
	worker := workers.NewOutboxMessagePublisher(repo, publisher)
	done := startOutboxPublisher(t, t.Context(), repo, worker, 2)
	require.ErrorContains(t, waitForOutboxRun(t, done), "retry write rejected")
	var stored []models.Message
	require.NoError(t, db.Master().WithContext(t.Context()).Find(&stored).Error)
	require.Len(t, stored, 2)
	for _, entry := range stored {
		require.Equal(t, models.MessageStatusPending, entry.Status)
		require.False(t, entry.ProcessedAt.Valid)
		require.Zero(t, entry.RetryCount)
	}
}

func newOutboxPublisherDatabase(t *testing.T) (*sqlite.Sqlite, *publisherTestRepository) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "outbox.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, db.Shutdown(ctx))
	})
	sqlDB, err := db.Master().DB()
	require.NoError(t, err)
	migrations, err := goose.NewProvider(goose.DialectSQLite3, sqlDB,
		os.DirFS(filepath.Join("..", "..", "..", "migrate", "sqlite")))
	require.NoError(t, err)
	_, err = migrations.Up(t.Context())
	require.NoError(t, err)
	return db, &publisherTestRepository{Repository: outboxRepository.New(database.NewBaseRepository(db))}
}

type publisherTestRepository struct {
	*outboxRepository.Repository
	afterTransaction func(error)
}

func (r *publisherTestRepository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	err := r.Repository.WithTransaction(ctx, fn)
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
	t.Cleanup(cancel)
	var completed sync.Once
	result := errors.New("publisher stopped before its first transaction")
	repository.afterTransaction = func(err error) {
		completed.Do(func() {
			result = err
			cancel()
		})
	}
	done := make(chan error, 1)
	go func() {
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
