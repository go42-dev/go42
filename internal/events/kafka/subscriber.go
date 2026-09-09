package kafka

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/IBM/sarama"
	"github.com/ThreeDotsLabs/watermill/message"
)

const topicMetadataRetryInterval = 100 * time.Millisecond

type subscriber struct {
	message.Subscriber
	client sarama.Client
}

func (s *subscriber) Subscribe(ctx context.Context, topic string) (<-chan *message.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := verifyTopic(ctx, s.client, topic); err != nil {
		return nil, fmt.Errorf("kafka topic %q is unavailable: %w", topic, err)
	}
	return s.Subscriber.Subscribe(ctx, topic)
}

func verifyTopic(ctx context.Context, client sarama.Client, topic string) error {
	ctx, cancel := context.WithTimeout(ctx, client.Config().Metadata.Timeout)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := client.Partitions(topic)
		if err == nil || !client.Config().Metadata.AllowAutoTopicCreation ||
			(!errors.Is(err, sarama.ErrUnknownTopicOrPartition) && !errors.Is(err, sarama.ErrLeaderNotAvailable)) {
			return err
		}
		// Opt-in topic creation is asynchronous. Wait for metadata to reflect the new topic.
		timer := time.NewTimer(topicMetadataRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
