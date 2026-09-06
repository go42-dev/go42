package events

import (
	"log/slog"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/message/router/middleware"

	"github.com/go42-dev/go42/internal/metrics"
)

type deadLetterPublisher struct {
	publisher message.Publisher
	logger    *slog.Logger
}

func (p *deadLetterPublisher) Publish(topic string, messages ...*message.Message) error {
	err := p.publisher.Publish(topic, messages...)

	result := "published"
	if err != nil {
		result = "failed"
	}
	metrics.Counter("application_event_consumer_dead_letters_total", map[string]any{
		"result": result,
		"topic":  topic,
	}).Inc()

	if err != nil {
		return err
	}

	for _, msg := range messages {
		p.logger.ErrorContext(
			msg.Context(),
			"event moved to dead-letter topic",
			slog.String("dead_letter_topic", topic),
			slog.String("source_topic", msg.Metadata.Get(middleware.PoisonedTopicKey)),
			slog.String("reason", msg.Metadata.Get(middleware.ReasonForPoisonedKey)),
		)
	}

	return nil
}

func (p *deadLetterPublisher) Close() error {
	// because p.publisher is the shared backend publisher
	return nil
}
