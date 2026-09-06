package events

import (
	"context"

	"github.com/ThreeDotsLabs/watermill/message"
)

type Publisher interface {
	Publish(ctx context.Context, topic string, event []byte) error
}

type Subscriber interface {
	Subscribe(
		topic string,
		handler func(ctx context.Context, event []byte) error,
	) error
}

type Backend interface {
	Publisher() message.Publisher
	Subscriber() message.Subscriber
	Shutdown(ctx context.Context) error
}

type TopicInitializer interface {
	InitializeTopic(topic string) error
}

// ---

type NoopEngine struct{}

func NewNoop() *NoopEngine {
	return &NoopEngine{}
}

func (e *NoopEngine) Publisher() message.Publisher {
	return e
}

func (e *NoopEngine) Subscriber() message.Subscriber {
	return e
}

func (e *NoopEngine) Publish(_ string, _ ...*message.Message) error {
	return nil
}

func (e *NoopEngine) Subscribe(
	ctx context.Context,
	_ string,
) (<-chan *message.Message, error) {
	messages := make(chan *message.Message)
	go func() {
		<-ctx.Done()
		close(messages)
	}()
	return messages, nil
}

func (e *NoopEngine) Close() error {
	return nil
}

func (e *NoopEngine) Shutdown(_ context.Context) error {
	return nil
}
