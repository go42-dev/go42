//go:build resilience

package resilience

import (
	"context"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill-amqp/v3/pkg/amqp"
	"github.com/ThreeDotsLabs/watermill/message"
	amqpgo "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"

	rabbitmqengine "github.com/go42-dev/go42/internal/events/rabbitmq"
)

func TestRabbitMQWatermillConfirmsAndUsesExistingRoute(t *testing.T) {
	resetProxy(t, rabbitMQFactory().proxy)
	uri := envOrDefault(rabbitmqAddressEnv, defaultRabbitMQAddress)
	control, err := amqpgo.Dial(uri)
	require.NoError(t, err)
	defer func() { require.NoError(t, control.Close()) }()
	channel, err := control.Channel()
	require.NoError(t, err)
	defer func() { require.NoError(t, channel.Close()) }()
	topic, group := uniqueTopic("managed"), "durability"
	queue := topic + "_" + group
	require.NoError(t, channel.ExchangeDeclare(topic, "fanout", true, false, false, false, nil))
	defer func() { require.NoError(t, channel.ExchangeDelete(topic, false, false)) }()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	backend, err := rabbitmqengine.New(ctx, uri, group)
	require.NoError(t, err)
	defer shutdownBackend(t, backend)
	entry := message.NewMessage(watermill.NewUUID(), []byte("durable event"))
	entry.SetContext(ctx)
	// Watermill reports the broker confirm even when mandatory publishing returns NO_ROUTE.
	require.NoError(t, backend.Publisher().Publish(topic, entry))
	_, err = channel.QueueDeclare(queue, true, false, false, false, nil)
	require.NoError(t, err)
	defer func() {
		_, err := channel.QueueDelete(queue, false, false, false)
		require.NoError(t, err)
	}()
	require.NoError(t, channel.QueueBind(queue, "", topic, false, nil))
	// Once the operator supplies the route, the standard publisher delivers the event.
	require.NoError(t, backend.Publisher().Publish(topic, entry))
	raw, ok, err := channel.Get(queue, false)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint8(amqpgo.Persistent), raw.DeliveryMode)
	require.Equal(t, entry.UUID, raw.Headers[amqp.DefaultMessageUUIDHeaderKey])
	require.NoError(t, raw.Nack(false, true))
	messages, err := backend.Subscriber().Subscribe(ctx, topic)
	require.NoError(t, err)
	received := receiveBrokerMessage(t, ctx, messages)
	require.Equal(t, entry.UUID, received.UUID)
	require.Equal(t, entry.Payload, received.Payload)
	received.Ack()
}
