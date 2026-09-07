package workers

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/go42-dev/go42/internal/metrics"
)

type OutboxMessageCleaner struct {
	logger     *slog.Logger
	repository cleanupRepository
}

func NewOutboxMessageCleaner(
	repository cleanupRepository, opts ...OutboxMessageCleanerOption,
) *OutboxMessageCleaner {
	cleaner := &OutboxMessageCleaner{repository: repository}
	for _, opt := range opts {
		opt(cleaner)
	}
	if cleaner.logger == nil {
		cleaner.logger = slog.New(slog.DiscardHandler)
	}
	return cleaner
}

// Run deletes one batch at each interval. Retention starts when a message is
// successfully processed.
func (c *OutboxMessageCleaner) Run(ctx context.Context, interval, retention time.Duration, batchSize int) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := c.run(ctx, time.Now().UTC().Add(-retention), batchSize)
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

func (c *OutboxMessageCleaner) run(ctx context.Context, before time.Time, batchSize int) error {
	deleted, err := c.repository.DeleteProcessedMessages(ctx, before, batchSize)
	if err != nil {
		return fmt.Errorf("failed to delete processed outbox messages: %w", err)
	}
	if deleted == 0 {
		return nil
	}
	metrics.Counter("application_outbox_cleanup_messages_total", nil).AddInt64(deleted)
	c.logger.DebugContext(ctx, "deleted processed outbox messages", slog.Int64("deleted", deleted))
	return nil
}

type OutboxMessageCleanerOption func(*OutboxMessageCleaner)

func OutboxMessageCleanerWithLogger(logger *slog.Logger) OutboxMessageCleanerOption {
	return func(c *OutboxMessageCleaner) {
		c.logger = logger
	}
}
