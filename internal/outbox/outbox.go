// Package outbox implements the transactional outbox pattern. Its service queues
// messages, a publisher sends them to the message broker, and a cleaner removes
// successfully processed messages after the configured retention period.
package outbox

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/go42-dev/go42/internal/events"
	"github.com/go42-dev/go42/internal/outbox/domain"
	"github.com/go42-dev/go42/internal/outbox/models"
	"github.com/go42-dev/go42/internal/tools"
)

//go:generate mockgen -source $GOFILE -package mocks -destination mocks/mocks.go

type repository interface {
	NewOutboxMessage(ctx context.Context, msg *models.Message) error
}

type Service struct {
	logger     *slog.Logger
	repository repository
}

func NewService(repository repository, opts ...Option) *Service {
	svc := &Service{
		repository: repository,
	}
	for _, opt := range opts {
		opt(svc)
	}
	if svc.logger == nil {
		svc.logger = slog.New(slog.DiscardHandler)
	}
	return svc
}

func (s *Service) NewOutboxMessage(ctx context.Context, topic string, msg *domain.Message) error {
	err := tools.ValidateStructCompact(msg)
	if err != nil {
		return err
	}

	var outboxMsg models.Message

	outboxMsg.ID = uuid.New()
	outboxMsg.AggregateID = msg.AggregateID
	outboxMsg.AggregateType = msg.AggregateType
	outboxMsg.Topic = topic
	outboxMsg.Payload = msg.Payload
	outboxMsg.Status = models.MessageStatusPending
	outboxMsg.MaxRetries = domain.MaxRetries
	outboxMsg.Metadata = events.PropagationFromContext(ctx)

	return s.repository.NewOutboxMessage(ctx, &outboxMsg)
}
