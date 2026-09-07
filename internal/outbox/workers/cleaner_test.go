package workers

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/go42-dev/go42/internal/metrics"
	"github.com/go42-dev/go42/internal/outbox/workers/mocks"
)

func TestOutboxCleanerDeletesOneBatchPerTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		repository := mocks.NewMockcleanupRepository(gomock.NewController(t))
		cleaner := NewOutboxMessageCleaner(repository)
		retention := 7 * 24 * time.Hour
		cutoff := time.Now().UTC().Add(time.Hour - retention)
		deleted := metrics.Counter("application_outbox_cleanup_messages_total", nil)
		deletedBefore := deleted.Get()
		runs := metrics.Counter("application_outbox_cleanup_runs_total", map[string]any{"result": "success"})
		runsBefore := runs.Get()
		gomock.InOrder(
			repository.EXPECT().DeleteProcessedMessages(ctx, cutoff, 2).Return(int64(2), nil),
			repository.EXPECT().DeleteProcessedMessages(ctx, cutoff.Add(time.Hour), 2).Return(int64(1), nil),
			repository.EXPECT().DeleteProcessedMessages(ctx, cutoff.Add(2*time.Hour), 2).Return(int64(0), nil),
		)
		done := make(chan struct{})
		go func() {
			defer close(done)
			cleaner.Run(ctx, time.Hour, retention, 2)
		}()
		time.Sleep(time.Hour - time.Nanosecond)
		synctest.Wait()
		assert.Equal(t, deletedBefore, deleted.Get())
		assert.Equal(t, runsBefore, runs.Get())
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		assert.Equal(t, uint64(2), deleted.Get()-deletedBefore)
		assert.Equal(t, uint64(1), runs.Get()-runsBefore)
		time.Sleep(time.Hour)
		synctest.Wait()
		assert.Equal(t, uint64(3), deleted.Get()-deletedBefore)
		assert.Equal(t, uint64(2), runs.Get()-runsBefore)
		time.Sleep(time.Hour)
		synctest.Wait()
		assert.Equal(t, uint64(3), deleted.Get()-deletedBefore)
		assert.Equal(t, uint64(3), runs.Get()-runsBefore)
		cancel()
		synctest.Wait()
		<-done
	})
}

func TestOutboxCleanerRetriesAtNextInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		repository := mocks.NewMockcleanupRepository(gomock.NewController(t))
		var output bytes.Buffer
		cleaner := NewOutboxMessageCleaner(repository, OutboxMessageCleanerWithLogger(
			slog.New(slog.NewJSONHandler(&output, nil)),
		))
		retention := 24 * time.Hour
		cutoff := time.Now().UTC().Add(time.Hour - retention)
		failures := metrics.Counter("application_outbox_cleanup_runs_total", map[string]any{"result": "error"})
		failuresBefore := failures.Get()
		successes := metrics.Counter("application_outbox_cleanup_runs_total", map[string]any{"result": "success"})
		successesBefore := successes.Get()
		gomock.InOrder(
			repository.EXPECT().DeleteProcessedMessages(ctx, cutoff, 1000).
				Return(int64(0), errors.New("database unavailable")),
			repository.EXPECT().DeleteProcessedMessages(ctx, cutoff.Add(time.Hour), 1000).Return(int64(0), nil),
		)
		done := make(chan struct{})
		go func() {
			defer close(done)
			cleaner.Run(ctx, time.Hour, retention, 1000)
		}()
		time.Sleep(time.Hour)
		synctest.Wait()
		assert.Equal(t, uint64(1), failures.Get()-failuresBefore)
		assert.Contains(t, output.String(), "database unavailable")
		time.Sleep(time.Hour - time.Nanosecond)
		synctest.Wait()
		assert.Equal(t, successesBefore, successes.Get())
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		assert.Equal(t, uint64(1), successes.Get()-successesBefore)
		cancel()
		synctest.Wait()
		<-done
	})
}

func TestOutboxCleanerRecordsCanceledRunAndStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		repository := mocks.NewMockcleanupRepository(gomock.NewController(t))
		repository.EXPECT().DeleteProcessedMessages(ctx, gomock.Any(), 10).
			DoAndReturn(func(ctx context.Context, _ time.Time, _ int) (int64, error) {
				<-ctx.Done()
				return 0, ctx.Err()
			})
		var output bytes.Buffer
		cleaner := NewOutboxMessageCleaner(repository, OutboxMessageCleanerWithLogger(
			slog.New(slog.NewJSONHandler(&output, nil)),
		))
		failures := metrics.Counter("application_outbox_cleanup_runs_total", map[string]any{"result": "error"})
		failuresBefore := failures.Get()
		applicationErrors := metrics.Counter("application_errors", map[string]any{"type": "outbox_cleanup_error"})
		errorsBefore := applicationErrors.Get()
		done := make(chan struct{})
		go func() {
			defer close(done)
			cleaner.Run(ctx, time.Hour, 24*time.Hour, 10)
		}()
		time.Sleep(time.Hour)
		synctest.Wait()
		cancel()
		synctest.Wait()
		<-done
		assert.Contains(t, output.String(), context.Canceled.Error())
		assert.Equal(t, uint64(1), failures.Get()-failuresBefore)
		assert.Equal(t, uint64(1), applicationErrors.Get()-errorsBefore)
	})
}

func TestOutboxCleanerRecordsCompletedRunDuringCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		repository := mocks.NewMockcleanupRepository(gomock.NewController(t))
		repository.EXPECT().DeleteProcessedMessages(ctx, gomock.Any(), 10).
			DoAndReturn(func(context.Context, time.Time, int) (int64, error) {
				// The batch completed successfully as shutdown began.
				cancel()
				return 3, nil
			})
		deleted := metrics.Counter("application_outbox_cleanup_messages_total", nil)
		deletedBefore := deleted.Get()
		successes := metrics.Counter("application_outbox_cleanup_runs_total", map[string]any{"result": "success"})
		successesBefore := successes.Get()
		NewOutboxMessageCleaner(repository).Run(ctx, time.Hour, 24*time.Hour, 10)
		assert.Equal(t, uint64(3), deleted.Get()-deletedBefore)
		assert.Equal(t, uint64(1), successes.Get()-successesBefore)
	})
}

func TestOutboxCleanerRecordsFailureDuringCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		repository := mocks.NewMockcleanupRepository(gomock.NewController(t))
		repository.EXPECT().DeleteProcessedMessages(ctx, gomock.Any(), 10).
			DoAndReturn(func(context.Context, time.Time, int) (int64, error) {
				cancel()
				return 0, errors.New("database unavailable")
			})
		var output bytes.Buffer
		cleaner := NewOutboxMessageCleaner(repository, OutboxMessageCleanerWithLogger(
			slog.New(slog.NewJSONHandler(&output, nil)),
		))
		failures := metrics.Counter("application_outbox_cleanup_runs_total", map[string]any{"result": "error"})
		failuresBefore := failures.Get()
		applicationErrors := metrics.Counter("application_errors", map[string]any{"type": "outbox_cleanup_error"})
		errorsBefore := applicationErrors.Get()
		cleaner.Run(ctx, time.Hour, 24*time.Hour, 10)
		assert.Equal(t, uint64(1), failures.Get()-failuresBefore)
		assert.Equal(t, uint64(1), applicationErrors.Get()-errorsBefore)
		assert.Contains(t, output.String(), "database unavailable")
	})
}

func TestOutboxCleanerSkipsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cleaner := NewOutboxMessageCleaner(mocks.NewMockcleanupRepository(gomock.NewController(t)))
	cleaner.Run(ctx, time.Hour, 24*time.Hour, 10)
}
