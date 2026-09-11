package workers

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/go42-dev/go42/internal/metrics"
)

const (
	defaultCleanupInterval   = time.Minute
	defaultCleanupRetention  = 7 * 24 * time.Hour
	defaultCleanupBatchSize  = 1000
	defaultCleanupMaxBatches = 20
)

type OutboxMessageCleaner struct {
	logger     *slog.Logger
	repository cleanupRepository
	interval   time.Duration
	retention  time.Duration
	batchSize  int
	maxBatches int
}

func NewOutboxMessageCleaner(
	repository cleanupRepository, opts ...OutboxMessageCleanerOption,
) *OutboxMessageCleaner {
	cleaner := &OutboxMessageCleaner{
		repository: repository,
		interval:   defaultCleanupInterval,
		retention:  defaultCleanupRetention,
		batchSize:  defaultCleanupBatchSize,
		maxBatches: defaultCleanupMaxBatches,
	}
	for _, opt := range opts {
		opt(cleaner)
	}
	if cleaner.logger == nil {
		cleaner.logger = slog.New(slog.DiscardHandler)
	}
	return cleaner
}

// Run deletes up to maxBatches at each interval. Retention starts when a message
// is successfully processed, and each cycle uses a single retention cutoff.
func (c *OutboxMessageCleaner) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			err := c.run(ctx, time.Now().UTC().Add(-c.retention))
			result := "success"
			if err != nil {
				result = "error"
				c.logger.ErrorContext(ctx, "failed to clean up processed outbox messages", slog.Any("error", err))
				metrics.Counter("application_errors", map[string]any{
					"type": "outbox_cleanup_error",
				}).Inc()
			}
			metrics.Counter("application_outbox_cleanup_runs_total", map[string]any{
				"result": result,
			}).Inc()
		}
	}
}

func (c *OutboxMessageCleaner) run(ctx context.Context, before time.Time) error {
	limitHits := metrics.Counter("application_outbox_cleanup_batch_limit_hits_total", nil)
	for batch := 0; batch < c.maxBatches; batch++ {
		// every batch should check for shutdown to avoid starting a new query if the context is canceled
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		deleted, err := c.repository.DeleteProcessedMessages(ctx, before, c.batchSize)
		if err != nil {
			return fmt.Errorf("failed to delete processed outbox messages: %w", err)
		}

		if deleted > 0 {
			metrics.Counter("application_outbox_cleanup_messages_total", nil).AddInt64(deleted)
			c.logger.DebugContext(ctx, "deleted processed outbox messages", slog.Int64("deleted", deleted))
		}
		if deleted < int64(c.batchSize) {
			break
		}

		if batch == c.maxBatches-1 {
			limitHits.Inc()
		}
	}

	// Preserve completed work during shutdown without starting another query.
	select {
	case <-ctx.Done():
		return nil
	default:
	}

	oldest, found, err := c.repository.GetOldestProcessedMessageTime(ctx, before)
	if err != nil {
		return fmt.Errorf("failed to measure outbox cleanup lag: %w", err)
	}

	var lag float64
	if found {
		lag = max(0, before.Sub(oldest).Seconds())
	}

	metrics.Gauge("application_outbox_cleanup_lag_seconds", nil).Set(lag)
	return nil
}

type OutboxMessageCleanerOption func(*OutboxMessageCleaner)

func OutboxMessageCleanerWithInterval(interval time.Duration) OutboxMessageCleanerOption {
	return func(c *OutboxMessageCleaner) {
		c.interval = interval
	}
}

func OutboxMessageCleanerWithRetention(retention time.Duration) OutboxMessageCleanerOption {
	return func(c *OutboxMessageCleaner) {
		c.retention = retention
	}
}

func OutboxMessageCleanerWithBatchSize(batchSize int) OutboxMessageCleanerOption {
	return func(c *OutboxMessageCleaner) {
		c.batchSize = batchSize
	}
}

func OutboxMessageCleanerWithMaxBatches(maxBatches int) OutboxMessageCleanerOption {
	return func(c *OutboxMessageCleaner) {
		c.maxBatches = maxBatches
	}
}

func OutboxMessageCleanerWithLogger(logger *slog.Logger) OutboxMessageCleanerOption {
	return func(c *OutboxMessageCleaner) {
		c.logger = logger
	}
}
