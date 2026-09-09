package workers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"time"

	"github.com/go42-dev/go42/internal/events"
	"github.com/go42-dev/go42/internal/metrics"
	"github.com/go42-dev/go42/internal/outbox/domain"
	"github.com/go42-dev/go42/internal/outbox/models"
	"github.com/go42-dev/go42/internal/tools"
)

const (
	defaultPublishTimeout      = 10 * time.Second
	defaultRetryInitialBackoff = 5 * time.Second
	defaultRetryMaxBackoff     = 5 * time.Minute
)

type OutboxMessagePublisher struct {
	logger              *slog.Logger
	repository          repository
	publisher           publisher
	publishTimeout      time.Duration
	retryInitialBackoff time.Duration
	retryMaxBackoff     time.Duration
}

func NewOutboxMessagePublisher(
	repository repository,
	publisher publisher,
	opts ...OutboxMessagePublisherOption,
) *OutboxMessagePublisher {
	pub := &OutboxMessagePublisher{
		repository:          repository,
		publisher:           publisher,
		publishTimeout:      defaultPublishTimeout,
		retryInitialBackoff: defaultRetryInitialBackoff,
		retryMaxBackoff:     defaultRetryMaxBackoff,
	}
	for _, opt := range opts {
		opt(pub)
	}
	if pub.logger == nil {
		pub.logger = slog.New(slog.DiscardHandler)
	}
	return pub
}

func (p *OutboxMessagePublisher) Run(
	ctx context.Context, interval time.Duration, batchSize int,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := p.run(ctx, batchSize)
			result := "success"
			if err != nil {
				result = "error"
			}
			metrics.Counter("application_outbox_worker_runs_total", map[string]interface{}{
				"result": result,
			}).Inc()
		}
	}
}

func (p *OutboxMessagePublisher) run(ctx context.Context, batchSize int) error {
	err := p.repository.WithTransaction(ctx, func(txCtx context.Context) error {
		p.logger.DebugContext(txCtx, "running outbox publisher job")

		messages, err := p.repository.GetUnprocessedMessages(txCtx, batchSize)
		if err != nil {
			return fmt.Errorf("failed to get unprocessed messages: %w", err)
		}

		var (
			processed []models.Message
			failed    []models.Message
		)

		for _, message := range messages {
			messageCtx := tools.WithLogAttrs(
				events.ContextWithPropagation(txCtx, message.Metadata),
				slog.String("event_id", message.ID.String()),
				slog.String("topic", message.Topic),
			)

			event := domain.Event{
				ID:            message.ID,
				CreatedAt:     message.CreatedAt,
				AggregateID:   message.AggregateID,
				AggregateType: message.AggregateType,
				Payload:       message.Payload,
			}

			jsonBytes, err := json.Marshal(event)
			if err != nil {
				return fmt.Errorf("failed to marshal event: %w", err)
			}

			publishCtx, publishCtxCancel := context.WithTimeout(messageCtx, p.publishTimeout)
			err = p.publisher.Publish(publishCtx, message.Topic, message.ID.String(), jsonBytes)
			publishCtxCancel()

			// if parent context is canceled we should stop immediately,
			// this can happen due to transaction timeout or shutdown signal
			if err := txCtx.Err(); err != nil {
				return err
			}

			if err != nil {
				if message.RetryCount < math.MaxInt32 {
					message.RetryCount++
				}
				message.LastError = err.Error()
				result := "retry"
				message.NextAttemptAt = sql.NullTime{
					Time: time.Now().UTC().Add(p.retryDelay(message.RetryCount)), Valid: true,
				}

				if events.IsPermanent(err) {
					message.Status = models.MessageStatusFailed
					message.NextAttemptAt = sql.NullTime{}
					result = "permanently_failed"
				}

				observeDelivery(message.CreatedAt, result)
				failed = append(failed, message)

				p.logger.ErrorContext(messageCtx, "failed to publish message", slog.Any("error", err))
				metrics.Counter("application_errors", map[string]interface{}{
					"type": "outbox_publisher_error",
				}).Inc()

				if errors.Is(err, context.DeadlineExceeded) {
					// Commit earlier successes and this failed attempt using the active transaction context.
					break
				}

				continue
			}

			processed = append(processed, message)
			observeDelivery(message.CreatedAt, "processed")
			p.logger.DebugContext(messageCtx, "published message")
		}

		if len(processed) > 0 {
			err := p.repository.SaveProcessedMessages(txCtx, processed)
			if err != nil {
				return fmt.Errorf("failed to save processed messages: %w", err)
			}
		}

		if len(failed) > 0 {
			err := p.repository.SaveFailedMessages(txCtx, failed)
			if err != nil {
				return fmt.Errorf("failed to save failed messages: %w", err)
			}
		}

		return nil
	})

	if err != nil {
		p.logger.ErrorContext(ctx,
			"failed to run outbox publisher job", slog.Any("error", err))
		metrics.Counter("application_errors", map[string]interface{}{
			"type": "outbox_publisher_error",
		}).Inc()
	}

	return err
}

// retryDelay caps exponential growth and adds jitter without overflowing after long outages.
func (p *OutboxMessagePublisher) retryDelay(attempt int) time.Duration {
	delay := p.retryInitialBackoff
	for n := 1; n < attempt && delay < p.retryMaxBackoff; n++ {
		if delay > p.retryMaxBackoff/2 {
			delay = p.retryMaxBackoff
			break
		}
		delay *= 2
	}
	delay = min(delay, p.retryMaxBackoff)
	return time.Duration(float64(delay) * (0.5 + rand.Float64()/2))
}

func observeDelivery(createdAt time.Time, result string) {
	metrics.Counter("application_outbox_messages_total", map[string]interface{}{
		"result": result,
	}).Inc()

	delay := time.Since(createdAt).Seconds()
	if delay < 0 {
		delay = 0
	}
	metrics.Histogram("application_outbox_delivery_delay_seconds", map[string]interface{}{
		"result": result,
	}).Update(delay)
}

type OutboxMessagePublisherOption func(*OutboxMessagePublisher)

func OutboxMessagePublisherWithRetryBackoff(initial, maximum time.Duration) OutboxMessagePublisherOption {
	return func(p *OutboxMessagePublisher) {
		p.retryInitialBackoff = initial
		p.retryMaxBackoff = maximum
	}
}

func OutboxMessagePublisherWithLogger(logger *slog.Logger) OutboxMessagePublisherOption {
	return func(o *OutboxMessagePublisher) {
		o.logger = logger
	}
}

func OutboxMessagePublisherWithPublishTimeout(timeout time.Duration) OutboxMessagePublisherOption {
	return func(o *OutboxMessagePublisher) {
		o.publishTimeout = timeout
	}
}
