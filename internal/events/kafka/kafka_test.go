package kafka

import (
	"context"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/stretchr/testify/require"
)

func TestKafkaDefaultsPreserveBacklogAndRequireDurablePublishing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var publisher, subscriber *sarama.Config
	_, err := New(ctx, []string{"127.0.0.1:1"}, "group",
		func(_ *Kafka, pub, sub *sarama.Config) { publisher, subscriber = pub, sub })
	require.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, publisher)
	require.True(t, publisher.Producer.Idempotent)
	require.Equal(t, sarama.WaitForAll, publisher.Producer.RequiredAcks)
	require.Equal(t, 1, publisher.Net.MaxOpenRequests)
	require.Equal(t, 3, publisher.Producer.Retry.Max)
	require.Equal(t, 5*time.Second, publisher.Producer.Timeout)
	require.False(t, publisher.Metadata.AllowAutoTopicCreation)
	require.False(t, subscriber.Metadata.AllowAutoTopicCreation)
	require.False(t, subscriber.Consumer.Group.ResetInvalidOffsets)
	require.Equal(t, sarama.ReadCommitted, subscriber.Consumer.IsolationLevel)
	require.Equal(t, sarama.OffsetOldest, subscriber.Consumer.Offsets.Initial)
}

func TestKafkaRejectsInvalidOptionsBeforeConnecting(t *testing.T) {
	for _, option := range []Option{
		WithProducerCompression("invalid"), WithConsumerGroupRebalanceStrategy("invalid"),
		WithKafkaVersion("invalid"), WithReadTimeout(0), WithProducerMetadataTimeout(0),
		WithProducerTimeout(0), WithProducerRetryMax(0),
		WithSASL("", "user", "password"), WithSASL("PLAIN", "user", ""),
		WithTLSConfig("", "client.pem", "", ""),
	} {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		backend, err := New(ctx, []string{"127.0.0.1:1"}, "group", option)
		require.Error(t, err)
		require.NotErrorIs(t, err, context.Canceled, "invalid options must fail before any network retry")
		require.Nil(t, backend)
	}
}
