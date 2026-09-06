package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/go42-dev/go42/internal/auth/domain"
	"github.com/go42-dev/go42/internal/auth/models"
	"github.com/go42-dev/go42/internal/events"
	"github.com/go42-dev/go42/internal/metrics"
	outboxDomain "github.com/go42-dev/go42/internal/outbox/domain"
	"github.com/go42-dev/go42/internal/tools"
)

type AuthEventSubscriber struct {
	logger     *slog.Logger
	repository repository
}

func NewAuthEventSubscriber(
	repository repository,
	opts ...AuthEventSubscriberOption,
) *AuthEventSubscriber {
	sub := &AuthEventSubscriber{
		repository: repository,
	}
	for _, o := range opts {
		o(sub)
	}
	if sub.logger == nil {
		sub.logger = slog.New(slog.DiscardHandler)
	}
	return sub
}

func (s *AuthEventSubscriber) Subscribe(subscriber subscriber) error {
	return subscriber.Subscribe(domain.TopicNameAuthEvents, s.handleEvent)
}

func (s *AuthEventSubscriber) handleEvent(ctx context.Context, eventData []byte) error {
	event := new(outboxDomain.Event)
	err := json.Unmarshal(eventData, event)
	if err != nil {
		s.logger.ErrorContext(ctx, "failed to unmarshal event data", slog.Any("error", err))
		metrics.Counter("application_errors", map[string]interface{}{
			"type": "auth_event_subscriber_error",
		}).Inc()
		return events.Permanent(fmt.Errorf("failed to unmarshal event: %w", err))
	}

	if err := s.validateEvent(ctx, event); err != nil {
		return err
	}

	ctx = tools.WithLogAttrs(ctx,
		slog.String("event_id", event.ID.String()),
		slog.Int("aggregate_id", event.AggregateID),
		slog.String("aggregate_type", event.AggregateType),
	)

	s.logger.DebugContext(ctx, "received event")

	return s.repository.WithTransaction(ctx, func(txCtx context.Context) error {
		eventLog := &models.UserHistoryRecord{
			ID:         event.ID,
			OccurredAt: event.CreatedAt,
			UserID:     event.AggregateID,
			EventType:  event.AggregateType,
			Data:       event.Payload,
		}
		err := s.repository.SaveUserHistoryRecord(txCtx, eventLog)
		if err != nil {
			s.logger.ErrorContext(txCtx, "failed to save event", slog.Any("error", err))
			metrics.Counter("application_errors", map[string]interface{}{
				"type": "auth_event_subscriber_error",
			}).Inc()
			return fmt.Errorf("failed to save log: %w", err)
		}
		s.logger.DebugContext(txCtx, "event saved")
		metrics.Counter("application_auth_event_subscriber_processed", nil).Inc()
		return nil
	})
}

func (s *AuthEventSubscriber) validateEvent(ctx context.Context, event *outboxDomain.Event) error {
	if event.ID != uuid.Nil && !event.CreatedAt.IsZero() &&
		event.AggregateID > 0 && event.AggregateType != "" {
		return nil
	}

	err := fmt.Errorf("event is missing required fields")

	s.logger.ErrorContext(ctx, "invalid event data", slog.Any("error", err))

	metrics.Counter("application_errors", map[string]interface{}{
		"type": "auth_event_subscriber_error",
	}).Inc()

	return events.Permanent(fmt.Errorf("invalid event: %w", err))
}

type AuthEventSubscriberOption func(*AuthEventSubscriber)

func AuthEventSubscriberWithLogger(logger *slog.Logger) AuthEventSubscriberOption {
	return func(o *AuthEventSubscriber) {
		o.logger = logger
	}
}
