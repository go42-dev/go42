package kafka

import (
	"errors"
	"sync"

	"github.com/IBM/sarama"
	wkafka "github.com/ThreeDotsLabs/watermill-kafka/v3/pkg/kafka"
	"github.com/ThreeDotsLabs/watermill/message"

	"github.com/go42-dev/go42/internal/events"
)

type publisher struct {
	message.Publisher
	mu sync.RWMutex
}

func (p *publisher) Publish(topic string, messages ...*message.Message) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	err := p.Publisher.Publish(topic, messages...)
	_, encodingError := errors.AsType[sarama.PacketEncodingError](err)
	if encodingError || errors.Is(err, sarama.ErrMessageSizeTooLarge) || errors.Is(err, sarama.ErrInvalidMessageSize) ||
		errors.Is(err, sarama.ErrInvalidTopic) {
		return events.Permanent(err)
	}
	return err
}

func (p *publisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Publisher.Close()
}

type marshaler struct {
	wkafka.DefaultMarshaler
}

func (m marshaler) Marshal(topic string, msg *message.Message) (*sarama.ProducerMessage, error) {
	encoded, err := m.DefaultMarshaler.Marshal(topic, msg)
	if err != nil {
		return nil, events.Permanent(err)
	}
	return encoded, nil
}
