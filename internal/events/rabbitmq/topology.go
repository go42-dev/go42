package rabbitmq

import (
	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill-amqp/v3/pkg/amqp"
	amqpgo "github.com/rabbitmq/amqp091-go"
)

// existingTopology leaves queue, exchange and binding management to the broker operator.
type existingTopology struct{}

func (existingTopology) BuildTopology(
	_ *amqpgo.Channel, _ amqp.BuildTopologyParams, _ amqp.Config, _ watermill.LoggerAdapter,
) error {
	return nil
}

func (existingTopology) ExchangeDeclare(_ *amqpgo.Channel, _ string, _ amqp.Config) error {
	return nil
}
