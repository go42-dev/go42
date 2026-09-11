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
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/go42-dev/go42/internal/metrics"
	"github.com/go42-dev/go42/internal/outbox/workers/mocks"
)

func TestOutboxCleanerDeletesBatchesPerTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		repository := mocks.NewMockcleanupRepository(gomock.NewController(t))
		retention := 3 * 24 * time.Hour
		cleaner := NewOutboxMessageCleaner(repository,
			OutboxMessageCleanerWithInterval(time.Hour),
			OutboxMessageCleanerWithRetention(retention),
			OutboxMessageCleanerWithBatchSize(2),
		)
		cutoff := time.Now().UTC().Add(time.Hour - retention)
		deleted := metrics.Counter("application_outbox_cleanup_messages_total", nil)
		deletedBefore := deleted.Get()
		runs := metrics.Counter("application_outbox_cleanup_runs_total", map[string]any{"result": "success"})
		runsBefore := runs.Get()
		limitHits := metrics.Counter("application_outbox_cleanup_batch_limit_hits_total", nil)
		limitHitsBefore := limitHits.Get()
		gomock.InOrder(
			repository.EXPECT().DeleteProcessedMessages(ctx, cutoff, 2).Return(int64(2), nil),
			repository.EXPECT().DeleteProcessedMessages(ctx, cutoff, 2).Return(int64(2), nil),
			repository.EXPECT().DeleteProcessedMessages(ctx, cutoff, 2).Return(int64(1), nil),
			repository.EXPECT().GetOldestProcessedMessageTime(ctx, cutoff).Return(time.Time{}, false, nil),
			repository.EXPECT().DeleteProcessedMessages(ctx, cutoff.Add(time.Hour), 2).Return(int64(1), nil),
			repository.EXPECT().
				GetOldestProcessedMessageTime(ctx, cutoff.Add(time.Hour)).
				Return(time.Time{}, false, nil),
			repository.EXPECT().DeleteProcessedMessages(ctx, cutoff.Add(2*time.Hour), 2).Return(int64(0), nil),
			repository.EXPECT().
				GetOldestProcessedMessageTime(ctx, cutoff.Add(2*time.Hour)).
				Return(time.Time{}, false, nil),
		)
		done := make(chan struct{})
		go func() {
			defer close(done)
			cleaner.Run(ctx)
		}()
		time.Sleep(time.Hour - time.Nanosecond)
		synctest.Wait()
		assert.Equal(t, deletedBefore, deleted.Get())
		assert.Equal(t, runsBefore, runs.Get())
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		assert.Equal(t, uint64(5), deleted.Get()-deletedBefore)
		assert.Equal(t, uint64(1), runs.Get()-runsBefore)
		time.Sleep(time.Hour)
		synctest.Wait()
		assert.Equal(t, uint64(6), deleted.Get()-deletedBefore)
		assert.Equal(t, uint64(2), runs.Get()-runsBefore)
		time.Sleep(time.Hour)
		synctest.Wait()
		assert.Equal(t, uint64(6), deleted.Get()-deletedBefore)
		assert.Equal(t, uint64(3), runs.Get()-runsBefore)
		assert.Equal(t, limitHitsBefore, limitHits.Get())
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
		retention := 24 * time.Hour
		cleaner := NewOutboxMessageCleaner(repository,
			OutboxMessageCleanerWithInterval(time.Hour),
			OutboxMessageCleanerWithRetention(retention),
			OutboxMessageCleanerWithLogger(slog.New(slog.NewJSONHandler(&output, nil))),
		)
		cutoff := time.Now().UTC().Add(time.Hour - retention)
		failures := metrics.Counter("application_outbox_cleanup_runs_total", map[string]any{"result": "error"})
		failuresBefore := failures.Get()
		successes := metrics.Counter("application_outbox_cleanup_runs_total", map[string]any{"result": "success"})
		successesBefore := successes.Get()
		deleted := metrics.Counter("application_outbox_cleanup_messages_total", nil)
		deletedBefore := deleted.Get()
		gomock.InOrder(
			repository.EXPECT().DeleteProcessedMessages(ctx, cutoff, 1000).Return(int64(1000), nil),
			repository.EXPECT().DeleteProcessedMessages(ctx, cutoff, 1000).
				Return(int64(0), errors.New("database unavailable")),
			repository.EXPECT().DeleteProcessedMessages(ctx, cutoff.Add(time.Hour), 1000).Return(int64(0), nil),
			repository.EXPECT().
				GetOldestProcessedMessageTime(ctx, cutoff.Add(time.Hour)).
				Return(time.Time{}, false, nil),
		)
		done := make(chan struct{})
		go func() {
			defer close(done)
			cleaner.Run(ctx)
		}()
		time.Sleep(time.Hour)
		synctest.Wait()
		assert.Equal(t, uint64(1), failures.Get()-failuresBefore)
		assert.Equal(t, uint64(1000), deleted.Get()-deletedBefore)
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
		cleaner := NewOutboxMessageCleaner(repository,
			OutboxMessageCleanerWithInterval(time.Hour),
			OutboxMessageCleanerWithBatchSize(10),
			OutboxMessageCleanerWithLogger(slog.New(slog.NewJSONHandler(&output, nil))),
		)
		failures := metrics.Counter("application_outbox_cleanup_runs_total", map[string]any{"result": "error"})
		failuresBefore := failures.Get()
		applicationErrors := metrics.Counter("application_errors", map[string]any{"type": "outbox_cleanup_error"})
		errorsBefore := applicationErrors.Get()
		done := make(chan struct{})
		go func() {
			defer close(done)
			cleaner.Run(ctx)
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
	for _, test := range []struct {
		name string
		rows int64
	}{
		{name: "partial batch", rows: 3},
		{name: "full batch", rows: 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				repository := mocks.NewMockcleanupRepository(gomock.NewController(t))
				repository.EXPECT().DeleteProcessedMessages(ctx, gomock.Any(), 10).
					DoAndReturn(func(context.Context, time.Time, int) (int64, error) {
						// Stop before another batch or lag query while preserving completed work.
						cancel()
						return test.rows, nil
					})
				deleted := metrics.Counter("application_outbox_cleanup_messages_total", nil)
				deletedBefore := deleted.Get()
				successes := metrics.Counter(
					"application_outbox_cleanup_runs_total",
					map[string]any{"result": "success"},
				)
				successesBefore := successes.Get()
				NewOutboxMessageCleaner(repository, OutboxMessageCleanerWithBatchSize(10)).Run(ctx)
				assert.Equal(t, uint64(test.rows), deleted.Get()-deletedBefore)
				assert.Equal(t, uint64(1), successes.Get()-successesBefore)
			})
		})
	}
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
		cleaner := NewOutboxMessageCleaner(repository,
			OutboxMessageCleanerWithBatchSize(10),
			OutboxMessageCleanerWithLogger(slog.New(slog.NewJSONHandler(&output, nil))),
		)
		failures := metrics.Counter("application_outbox_cleanup_runs_total", map[string]any{"result": "error"})
		failuresBefore := failures.Get()
		applicationErrors := metrics.Counter("application_errors", map[string]any{"type": "outbox_cleanup_error"})
		errorsBefore := applicationErrors.Get()
		cleaner.Run(ctx)
		assert.Equal(t, uint64(1), failures.Get()-failuresBefore)
		assert.Equal(t, uint64(1), applicationErrors.Get()-errorsBefore)
		assert.Contains(t, output.String(), "database unavailable")
	})
}

func TestOutboxCleanerSkipsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cleaner := NewOutboxMessageCleaner(mocks.NewMockcleanupRepository(gomock.NewController(t)))
	cleaner.Run(ctx)
}

func TestOutboxCleanerHonorsBatchLimit(t *testing.T) {
	for _, test := range []struct {
		name          string
		options       []OutboxMessageCleanerOption
		batchSize     int
		maxBatches    int
		lastBatchRows int64
		remaining     bool
		wantLimitHits uint64
	}{
		{
			name: "defaults", batchSize: 1000, maxBatches: 20, lastBatchRows: 1000,
			remaining: true, wantLimitHits: 1,
		},
		{
			name: "overridden limit",
			options: []OutboxMessageCleanerOption{
				OutboxMessageCleanerWithBatchSize(3), OutboxMessageCleanerWithMaxBatches(2),
			},
			batchSize: 3, maxBatches: 2, lastBatchRows: 3, remaining: true, wantLimitHits: 1,
		},
		{
			name: "partial final batch",
			options: []OutboxMessageCleanerOption{
				OutboxMessageCleanerWithBatchSize(3), OutboxMessageCleanerWithMaxBatches(2),
			},
			batchSize: 3, maxBatches: 2, lastBatchRows: 1,
		},
		{
			name: "full final batch without backlog",
			options: []OutboxMessageCleanerOption{
				OutboxMessageCleanerWithBatchSize(3), OutboxMessageCleanerWithMaxBatches(2),
			},
			batchSize: 3, maxBatches: 2, lastBatchRows: 3, wantLimitHits: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				repository := mocks.NewMockcleanupRepository(gomock.NewController(t))
				cutoff := time.Now().UTC().Add(time.Minute - 7*24*time.Hour)
				gomock.InOrder(
					repository.EXPECT().DeleteProcessedMessages(ctx, cutoff, test.batchSize).
						Return(int64(test.batchSize), nil).Times(test.maxBatches-1),
					repository.EXPECT().DeleteProcessedMessages(ctx, cutoff, test.batchSize).
						Return(test.lastBatchRows, nil),
					repository.EXPECT().GetOldestProcessedMessageTime(ctx, cutoff).
						Return(cutoff.Add(-time.Hour), test.remaining, nil),
				)
				deleted := metrics.Counter("application_outbox_cleanup_messages_total", nil)
				deletedBefore := deleted.Get()
				limitHits := metrics.Counter("application_outbox_cleanup_batch_limit_hits_total", nil)
				limitHitsBefore := limitHits.Get()
				successes := metrics.Counter(
					"application_outbox_cleanup_runs_total",
					map[string]any{"result": "success"},
				)
				successesBefore := successes.Get()
				done := make(chan struct{})
				go func() {
					defer close(done)
					NewOutboxMessageCleaner(repository, test.options...).Run(ctx)
				}()
				time.Sleep(time.Minute - time.Nanosecond)
				synctest.Wait()
				assert.Equal(t, successesBefore, successes.Get())
				time.Sleep(time.Nanosecond)
				synctest.Wait()
				wantDeleted := int64(test.batchSize*(test.maxBatches-1)) + test.lastBatchRows
				assert.Equal(t, uint64(wantDeleted), deleted.Get()-deletedBefore)
				assert.Equal(t, test.wantLimitHits, limitHits.Get()-limitHitsBefore)
				assert.Equal(t, uint64(1), successes.Get()-successesBefore)
				var wantLag float64
				if test.remaining {
					wantLag = time.Hour.Seconds()
				}
				assert.Equal(t, wantLag, metrics.Gauge("application_outbox_cleanup_lag_seconds", nil).Get())
				cancel()
				synctest.Wait()
				<-done
			})
		})
	}
}

func TestOutboxCleanerKeepsCutoffAcrossBatches(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		repository := mocks.NewMockcleanupRepository(gomock.NewController(t))
		cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour)
		gomock.InOrder(
			repository.EXPECT().DeleteProcessedMessages(ctx, cutoff, 2).
				DoAndReturn(func(context.Context, time.Time, int) (int64, error) {
					time.Sleep(time.Second)
					return 2, nil
				}),
			repository.EXPECT().DeleteProcessedMessages(ctx, cutoff, 2).Return(int64(0), nil),
			repository.EXPECT().GetOldestProcessedMessageTime(ctx, cutoff).Return(time.Time{}, false, nil),
		)
		cleaner := NewOutboxMessageCleaner(repository, OutboxMessageCleanerWithBatchSize(2))
		require.NoError(t, cleaner.run(ctx, cutoff))
	})
}

func TestOutboxCleanerRetainsLagOnQueryErrorAndRecovers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		repository := mocks.NewMockcleanupRepository(gomock.NewController(t))
		cutoff := time.Now().UTC().Add(time.Minute - 7*24*time.Hour)
		gomock.InOrder(
			repository.EXPECT().DeleteProcessedMessages(ctx, cutoff, 1000).Return(int64(1000), nil),
			repository.EXPECT().GetOldestProcessedMessageTime(ctx, cutoff).
				Return(cutoff.Add(-2*time.Hour), true, nil),
			repository.EXPECT().DeleteProcessedMessages(ctx, cutoff.Add(time.Minute), 1000).Return(int64(3), nil),
			repository.EXPECT().GetOldestProcessedMessageTime(ctx, cutoff.Add(time.Minute)).
				Return(time.Time{}, false, errors.New("lag query unavailable")),
			repository.EXPECT().DeleteProcessedMessages(ctx, cutoff.Add(2*time.Minute), 1000).Return(int64(0), nil),
			repository.EXPECT().GetOldestProcessedMessageTime(ctx, cutoff.Add(2*time.Minute)).
				Return(time.Time{}, false, nil),
		)
		lag := metrics.Gauge("application_outbox_cleanup_lag_seconds", nil)
		deleted := metrics.Counter("application_outbox_cleanup_messages_total", nil)
		deletedBefore := deleted.Get()
		failures := metrics.Counter("application_outbox_cleanup_runs_total", map[string]any{"result": "error"})
		failuresBefore := failures.Get()
		applicationErrors := metrics.Counter("application_errors", map[string]any{"type": "outbox_cleanup_error"})
		errorsBefore := applicationErrors.Get()
		successes := metrics.Counter("application_outbox_cleanup_runs_total", map[string]any{"result": "success"})
		successesBefore := successes.Get()
		var output bytes.Buffer
		cleaner := NewOutboxMessageCleaner(repository,
			OutboxMessageCleanerWithMaxBatches(1),
			OutboxMessageCleanerWithLogger(slog.New(slog.NewJSONHandler(&output, nil))),
		)
		done := make(chan struct{})
		go func() {
			defer close(done)
			cleaner.Run(ctx)
		}()
		time.Sleep(time.Minute)
		synctest.Wait()
		assert.Equal(t, (2 * time.Hour).Seconds(), lag.Get())
		assert.Equal(t, uint64(1), successes.Get()-successesBefore)
		time.Sleep(time.Minute)
		synctest.Wait()
		assert.Equal(t, (2 * time.Hour).Seconds(), lag.Get())
		assert.Equal(t, uint64(1003), deleted.Get()-deletedBefore)
		assert.Equal(t, uint64(1), failures.Get()-failuresBefore)
		assert.Equal(t, uint64(1), applicationErrors.Get()-errorsBefore)
		assert.Contains(t, output.String(), "lag query unavailable")
		time.Sleep(time.Minute)
		synctest.Wait()
		assert.Zero(t, lag.Get())
		assert.Equal(t, uint64(2), successes.Get()-successesBefore)
		cancel()
		synctest.Wait()
		<-done
	})
}
