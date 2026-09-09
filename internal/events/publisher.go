package events

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/ThreeDotsLabs/watermill/message"
	"golang.org/x/sync/semaphore"
)

const defaultPublishMaxInflight = 1 << 5

// ErrPublishCapacity means waiting for an available publisher slot failed.
var ErrPublishCapacity = errors.New("publish capacity unavailable")

// boundedPublisher limits active broker calls and returns when a message's context is canceled.
type boundedPublisher struct {
	message.Publisher
	slots *semaphore.Weighted
}

func newBoundedPublisher(publisher message.Publisher, maxInflight int) *boundedPublisher {
	return &boundedPublisher{
		Publisher: publisher,
		slots:     semaphore.NewWeighted(int64(maxInflight)),
	}
}

func (p *boundedPublisher) Publish(topic string, messages ...*message.Message) error {
	for _, msg := range messages {
		if err := p.publish(topic, msg); err != nil {
			return err
		}
	}
	return nil
}

func (p *boundedPublisher) publish(topic string, msg *message.Message) error {
	ctx := msg.Context()
	if err := p.slots.Acquire(ctx, 1); err != nil {
		return fmt.Errorf("%w: %w", ErrPublishCapacity, err)
	}

	// The backend may still read the message after the caller returns and reuses it.
	snapshot := msg.CopyWithContext()
	snapshot.Payload = bytes.Clone(msg.Payload)

	result := make(chan error, 1)

	go func() {
		// A timeout stops waiting; the slot stays occupied until the broker returns.
		defer p.slots.Release(1)
		if err := ctx.Err(); err != nil {
			result <- err
			return
		}
		result <- p.Publisher.Publish(topic, snapshot)
	}()

	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
