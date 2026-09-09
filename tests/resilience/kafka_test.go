//go:build resilience

package resilience

import (
	"context"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/ThreeDotsLabs/watermill"
	wkafka "github.com/ThreeDotsLabs/watermill-kafka/v3/pkg/kafka"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/stretchr/testify/require"

	"github.com/go42-dev/go42/internal/events"
	kafkaengine "github.com/go42-dev/go42/internal/events/kafka"
)

func newKafkaControl(t *testing.T, topic string) sarama.ClusterAdmin {
	t.Helper()
	resetProxy(t, proxyConfig{Name: kafkaProxyName, Listen: "0.0.0.0:19092", Upstream: "kafka:19093", Enabled: true})
	config := sarama.NewConfig()
	config.Version = sarama.V4_0_0_0
	config.Metadata.AllowAutoTopicCreation = false
	admin, err := sarama.NewClusterAdmin([]string{envOrDefault(kafkaAddressEnv, defaultKafkaAddress)}, config)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, admin.Close()) })
	require.NoError(t, admin.CreateTopic(topic, &sarama.TopicDetail{NumPartitions: 1, ReplicationFactor: 1}, false))
	t.Cleanup(func() { require.NoError(t, admin.DeleteTopic(topic)) })
	require.Eventually(t, func() bool {
		info, err := admin.DescribeTopics([]string{topic})
		return err == nil && len(info) == 1 && info[0].Err == sarama.ErrNoError && len(info[0].Partitions) == 1 &&
			info[0].Partitions[0].Leader >= 0
	}, 5*time.Second, 50*time.Millisecond)
	return admin
}

func openManagedKafka(t *testing.T, ctx context.Context, group string) *kafkaengine.Kafka {
	t.Helper()
	backend, err := kafkaengine.New(ctx, []string{envOrDefault(kafkaAddressEnv, defaultKafkaAddress)}, group,
		kafkaengine.WithKafkaVersion("4.0.0"), kafkaengine.WithConnectRetryTimeout(5*time.Second))
	require.NoError(t, err)
	return backend
}

func TestKafkaReadsExistingBacklogAndRedeliversUnacknowledgedMessages(t *testing.T) {
	topic, group := uniqueTopic("backlog"), uniqueTopic("consumer")
	admin := newKafkaControl(t, topic)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	backend := openManagedKafka(t, ctx, group)
	defer shutdownBackend(t, backend)
	first, second := newBrokerConsumerMessage(ctx), newBrokerConsumerMessage(ctx)
	require.NoError(t, backend.Publisher().Publish(topic, first, second))
	messages, err := backend.Subscriber().Subscribe(ctx, topic)
	require.NoError(t, err)
	received := receiveBrokerMessage(t, ctx, messages)
	require.Equal(t, first.UUID, received.UUID, "a fresh group must read events published before it subscribed")
	// Stop without acknowledging. The replacement consumer must resume with the same event.
	require.NoError(t, backend.Shutdown(ctx))
	backend = openManagedKafka(t, ctx, group)
	defer shutdownBackend(t, backend)
	messages, err = backend.Subscriber().Subscribe(ctx, topic)
	require.NoError(t, err)
	retried := receiveBrokerMessage(t, ctx, messages)
	require.Equal(t, first.UUID, retried.UUID)
	retried.Ack()
	received = receiveBrokerMessage(t, ctx, messages)
	require.Equal(t, second.UUID, received.UUID)
	received.Ack()
	require.Eventually(t, func() bool {
		offsets, err := admin.ListConsumerGroupOffsets(group, map[string][]int32{topic: {0}})
		if err != nil {
			return false
		}
		block := offsets.GetBlock(topic, 0)
		return block != nil && block.Offset == 2
	}, 5*time.Second, 50*time.Millisecond)
}

func TestKafkaDoesNotDeliverAbortedTransactions(t *testing.T) {
	topic := uniqueTopic("transactions")
	newKafkaControl(t, topic)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	config := sarama.NewConfig()
	config.Version = sarama.V4_0_0_0
	config.Net.MaxOpenRequests = 1
	config.Producer.RequiredAcks = sarama.WaitForAll
	config.Producer.Return.Successes = true
	config.Producer.Idempotent = true
	config.Producer.Transaction.ID = uniqueTopic("transaction")
	producer, err := sarama.NewSyncProducer([]string{envOrDefault(kafkaAddressEnv, defaultKafkaAddress)}, config)
	require.NoError(t, err)
	defer func() { require.NoError(t, producer.Close()) }()
	require.NoError(t, producer.BeginTxn())
	aborted := message.NewMessage(watermill.NewUUID(), []byte("aborted"))
	encoded, err := (wkafka.DefaultMarshaler{}).Marshal(topic, aborted)
	require.NoError(t, err)
	_, _, err = producer.SendMessage(encoded)
	require.NoError(t, err)
	require.NoError(t, producer.AbortTxn())
	backend := openManagedKafka(t, ctx, uniqueTopic("reader"))
	defer shutdownBackend(t, backend)
	committed := message.NewMessage(watermill.NewUUID(), []byte("committed"))
	committed.SetContext(ctx)
	require.NoError(t, backend.Publisher().Publish(topic, committed))
	messages, err := backend.Subscriber().Subscribe(ctx, topic)
	require.NoError(t, err)
	received := receiveBrokerMessage(t, ctx, messages)
	require.Equal(t, committed.UUID, received.UUID, "aborted records must remain invisible")
	received.Ack()
}

func TestKafkaDefaultDoesNotCreateMissingTopics(t *testing.T) {
	known, missing := uniqueTopic("known"), uniqueTopic("missing")
	admin := newKafkaControl(t, known)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	backend := openManagedKafka(t, ctx, uniqueTopic("reader"))
	defer shutdownBackend(t, backend)
	_, err := backend.Subscriber().Subscribe(ctx, missing)
	require.Error(t, err)
	router, err := events.NewRouter(backend)
	require.NoError(t, err)
	publishCtx, stop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer stop()
	require.Error(t, router.Publish(publishCtx, missing, watermill.NewUUID(), []byte("event")))
	topics, err := admin.ListTopics()
	require.NoError(t, err)
	require.NotContains(t, topics, missing)
}
