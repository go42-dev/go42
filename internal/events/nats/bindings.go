package nats

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/ThreeDotsLabs/watermill"
	wnats "github.com/ThreeDotsLabs/watermill-nats/v2/pkg/nats"
	"github.com/ThreeDotsLabs/watermill/message"
	natsgo "github.com/nats-io/nats.go"
)

type ConsumerBinding struct {
	Stream   string `json:"stream"`
	Consumer string `json:"consumer"`
}

// Subscribe resolves the topic's configuration, then delegates delivery and acknowledgements to Watermill.
func (n *NATS) Subscribe(ctx context.Context, topic string) (<-chan *message.Message, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return nil, errors.New("NATS subscriber is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := natsgo.Connect(n.subscriberConfig.URL, n.subscriberConfig.NatsOptions...)
	if err != nil {
		return nil, err
	}
	config, err := n.subscriptionConfig(ctx, conn, topic)
	if err != nil {
		conn.Close()
		return nil, err
	}
	subscriber, err := wnats.NewSubscriberWithNatsConn(
		conn, config.GetSubscriberSubscriptionConfig(), watermill.NewSlogLogger(n.logger),
	)
	if err != nil {
		conn.Close()
		return nil, err
	}
	n.subscribers = append(n.subscribers, subscriber)
	return subscriber.Subscribe(ctx, topic)
}

func (n *NATS) subscriptionConfig(
	ctx context.Context,
	conn *natsgo.Conn,
	topic string,
) (wnats.SubscriberConfig, error) {
	config := n.subscriberConfig
	config.JetStream.SubscribeOptions = append(slices.Clone(config.JetStream.SubscribeOptions), natsgo.ManualAck())
	binding, explicit := n.consumerBindings[topic]
	if config.JetStream.AutoProvision && !explicit {
		config.JetStream.SubscribeOptions = append([]natsgo.SubOpt{
			natsgo.DeliverAll(), natsgo.AckExplicit(), natsgo.AckWait(config.AckWaitTimeout),
			natsgo.MaxDeliver(defaultConsumerMaxDeliver),
		}, config.JetStream.SubscribeOptions...)
		return config, nil
	}
	// Bind explicitly so Watermill cannot create, change or delete an operator's consumer.
	config.JetStream.AutoProvision = false
	ctx, cancel := context.WithTimeout(ctx, config.SubscribeTimeout)
	defer cancel()
	js, err := conn.JetStream(config.JetStream.ConnectOptions...)
	if err != nil {
		return config, err
	}
	if len(binding.Consumer) == 0 {
		binding.Consumer = config.JetStream.CalculateDurableName(topic)
	}
	if len(binding.Stream) == 0 {
		binding.Stream, err = js.StreamNameBySubject(topic, natsgo.Context(ctx))
		if err != nil {
			return config, fmt.Errorf("find stream for %q: %w", topic, err)
		}
	}
	info, err := js.ConsumerInfo(binding.Stream, binding.Consumer, natsgo.Context(ctx))
	if err != nil {
		return config, fmt.Errorf("bind consumer %q on stream %q: %w", binding.Consumer, binding.Stream, err)
	}
	if len(info.Config.DeliverSubject) == 0 {
		return config, fmt.Errorf(
			"NATS consumer %q requires pull subscriptions, which Watermill does not support",
			binding.Consumer,
		)
	}
	if info.Config.AckPolicy != natsgo.AckExplicitPolicy || len(info.Config.Durable) == 0 || info.Config.AckWait <= 0 {
		return config, errors.New(
			"NATS consumer must be durable with explicit acknowledgements and a positive acknowledgement deadline",
		)
	}
	if info.Config.FilterSubject != topic &&
		(len(info.Config.FilterSubjects) != 1 || info.Config.FilterSubjects[0] != topic) {
		return config, errors.New("NATS consumer filter must match its application topic")
	}
	if len(info.Config.DeliverGroup) == 0 && config.SubscribersCount > 1 {
		return config, errors.New("NATS consumer requires a delivery group for multiple workers")
	}
	config.QueueGroupPrefix = info.Config.DeliverGroup
	config.JetStream.DurablePrefix = binding.Consumer
	config.JetStream.DurableCalculator = nil
	config.JetStream.SubscribeOptions = append(
		config.JetStream.SubscribeOptions,
		natsgo.Bind(binding.Stream, binding.Consumer),
	)
	return config, nil
}
