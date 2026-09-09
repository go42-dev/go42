//go:build resilience

package resilience

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	wnats "github.com/ThreeDotsLabs/watermill-nats/v2/pkg/nats"
	"github.com/ThreeDotsLabs/watermill/message"
	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/go42-dev/go42/internal/events"
	natsengine "github.com/go42-dev/go42/internal/events/nats"
)

func newNATSControl(t *testing.T) natsgo.JetStreamContext {
	t.Helper()
	resetProxy(t, proxyConfig{Name: natsProxyName, Listen: "0.0.0.0:14222", Upstream: "nats:4222", Enabled: true})
	conn, err := natsgo.Connect(envOrDefault(natsAddressEnv, defaultNATSAddress))
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	js, err := conn.JetStream()
	require.NoError(t, err)
	return js
}

func TestNATSBindsExistingPushConsumersAndRejectsPullConsumers(t *testing.T) {
	for _, push := range []bool{false, true} {
		name := "pull"
		if push {
			name = "push"
		}
		t.Run(name, func(t *testing.T) {
			js := newNATSControl(t)
			topic, stream, consumer := uniqueTopic("managed"), uniqueTopic("stream"), uniqueTopic("consumer")
			streamInfo, err := js.AddStream(
				&natsgo.StreamConfig{Name: stream, Subjects: []string{topic}, Storage: natsgo.FileStorage},
			)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, js.DeleteStream(stream)) })
			config := &natsgo.ConsumerConfig{Durable: consumer, FilterSubject: topic,
				AckPolicy: natsgo.AckExplicitPolicy, AckWait: 5 * time.Second, MaxAckPending: 1, MaxDeliver: -1}
			if push {
				config.DeliverSubject, config.DeliverGroup = uniqueTopic("delivery"), uniqueTopic("group")
			}
			consumerInfo, err := js.AddConsumer(stream, config)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			backend, err := natsengine.New(
				ctx,
				envOrDefault(natsAddressEnv, defaultNATSAddress),
				natsengine.WithSubAckTimeout(3*time.Second),
				natsengine.WithConsumerBindings(
					map[string]natsengine.ConsumerBinding{topic: {Stream: stream, Consumer: consumer}},
				),
			)
			require.NoError(t, err)
			t.Cleanup(func() { shutdownBackend(t, backend) })
			messages, err := backend.Subscriber().Subscribe(ctx, topic)
			if !push {
				require.ErrorContains(t, err, "requires pull subscriptions")
				require.Nil(t, messages)
				preserved, err := js.ConsumerInfo(stream, consumer)
				require.NoError(t, err)
				require.Equal(t, consumerInfo.Config, preserved.Config)
				return
			}
			require.NoError(t, err)
			entry := message.NewMessage(watermill.NewUUID(), []byte("managed consumer"))
			entry.Metadata.Set("request_id", "request-42")
			entry.SetContext(ctx)
			for range 2 {
				require.NoError(t, backend.Publisher().Publish(topic, entry))
			}
			stored, err := js.StreamInfo(stream)
			require.NoError(t, err)
			require.EqualValues(t, 1, stored.State.Msgs, "retrying a stable ID must deduplicate")
			received := receiveBrokerMessage(t, ctx, messages)
			require.Equal(t, entry.UUID, received.UUID)
			require.Equal(t, entry.Payload, received.Payload)
			require.Equal(t, entry.Metadata, received.Metadata)
			// Watermill does not extend AckWait; processing must finish within the operator's deadline.
			active, err := js.ConsumerInfo(stream, consumer)
			require.NoError(t, err)
			require.EqualValues(t, 1, active.Delivered.Consumer)
			received.Ack()
			require.Eventually(t, func() bool {
				active, err := js.ConsumerInfo(stream, consumer)
				return err == nil && active.NumAckPending == 0 && active.AckFloor.Stream == 1
			}, 3*time.Second, 20*time.Millisecond)
			require.NoError(t, backend.Shutdown(ctx))
			preserved, err := js.ConsumerInfo(stream, consumer)
			require.NoError(t, err, "closing a bound subscription must not delete the durable consumer")
			require.Equal(t, consumerInfo.Config, preserved.Config)
			stored, err = js.StreamInfo(stream)
			require.NoError(t, err)
			require.Equal(t, streamInfo.Config, stored.Config)
		})
	}
}

func TestNATSDeduplicationDoesNotSuppressSharedStreamDeadLetters(t *testing.T) {
	js := newNATSControl(t)
	topic, stream, consumer := uniqueTopic("poison"), uniqueTopic("stream"), uniqueTopic("consumer")
	_, err := js.AddStream(
		&natsgo.StreamConfig{Name: stream, Subjects: []string{topic, topic + "_dlq"}, Storage: natsgo.FileStorage},
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, js.DeleteStream(stream)) })
	_, err = js.AddConsumer(stream, &natsgo.ConsumerConfig{Durable: consumer, FilterSubject: topic,
		AckPolicy: natsgo.AckExplicitPolicy, AckWait: time.Second, MaxDeliver: -1,
		DeliverSubject: uniqueTopic("delivery")})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	backend, err := natsengine.New(
		ctx,
		envOrDefault(natsAddressEnv, defaultNATSAddress),
		natsengine.WithConsumerBindings(
			map[string]natsengine.ConsumerBinding{topic: {Stream: stream, Consumer: consumer}},
		),
	)
	require.NoError(t, err)
	router, err := events.NewRouter(backend)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, router.Shutdown(ctx))
	})
	require.NoError(t, router.Subscribe(topic, func(context.Context, []byte) error {
		return events.Permanent(errors.New("invalid event"))
	}))
	require.NoError(t, router.Start(ctx))
	id := watermill.NewUUID()
	require.NoError(t, router.Publish(ctx, topic, id, []byte("event")))
	require.Eventually(t, func() bool {
		info, err := js.StreamInfo(stream)
		return err == nil && info.State.Msgs == 2
	}, 3*time.Second, 20*time.Millisecond)
	source, err := js.GetMsg(stream, 1)
	require.NoError(t, err)
	dead, err := js.GetMsg(stream, 2)
	require.NoError(t, err)
	require.Equal(t, topic+"_dlq", dead.Subject)
	require.NotEqual(t, source.Header.Get(natsgo.MsgIdHdr), dead.Header.Get(natsgo.MsgIdHdr))
	decoded, err := (wnats.GobMarshaler{}).Unmarshal(&natsgo.Msg{Data: dead.Data, Header: dead.Header})
	require.NoError(t, err)
	require.Equal(t, id, decoded.UUID)
	require.Equal(t, []byte("event"), []byte(decoded.Payload))
	require.Eventually(t, func() bool {
		info, err := js.ConsumerInfo(stream, consumer)
		return err == nil && info.NumAckPending == 0
	}, 3*time.Second, 20*time.Millisecond)
}

func TestNATSDefaultDoesNotProvisionMissingResources(t *testing.T) {
	js := newNATSControl(t)
	topic := uniqueTopic("missing")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	backend, err := natsengine.New(ctx, envOrDefault(natsAddressEnv, defaultNATSAddress))
	require.NoError(t, err)
	defer shutdownBackend(t, backend)
	_, err = backend.Subscriber().Subscribe(ctx, topic)
	require.Error(t, err)
	entry := message.NewMessage(watermill.NewUUID(), []byte("event"))
	entry.SetContext(ctx)
	require.Error(t, backend.Publisher().Publish(topic, entry))
	_, err = js.StreamNameBySubject(topic)
	require.ErrorIs(t, err, natsgo.ErrNoMatchingStream)
	stream := uniqueTopic("existing")
	_, err = js.AddStream(&natsgo.StreamConfig{Name: stream, Subjects: []string{topic}, Storage: natsgo.FileStorage})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, js.DeleteStream(stream)) })
	_, err = backend.Subscriber().Subscribe(ctx, topic)
	require.ErrorIs(t, err, natsgo.ErrConsumerNotFound)
	_, err = js.ConsumerInfo(stream, "default_"+topic)
	require.ErrorIs(t, err, natsgo.ErrConsumerNotFound, "binding must not create a missing consumer")
}

func receiveBrokerMessage(t *testing.T, ctx context.Context, messages <-chan *message.Message) *message.Message {
	t.Helper()
	select {
	case entry, open := <-messages:
		require.True(t, open)
		require.NotNil(t, entry)
		return entry
	case <-ctx.Done():
		t.Fatalf("delivery timed out: %v", ctx.Err())
		return nil
	}
}
