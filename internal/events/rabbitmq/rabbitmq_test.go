package rabbitmq

import (
	"context"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill-amqp/v3/pkg/amqp"
	"github.com/stretchr/testify/require"
)

func TestRabbitMQRejectsUnsafeOptionsBeforeConnecting(t *testing.T) {
	for _, option := range []Option{
		func(_ *AMQP, cfg *amqp.Config) { cfg.Publish.Mandatory = false },
		func(_ *AMQP, cfg *amqp.Config) { cfg.Consume.NoRequeueOnNack = true },
		WithConnectTimeout(0), WithConnectRetryTimeout(0), WithConnectRetryBackoff(time.Second, time.Millisecond),
		WithConsumeQosPrefetchCount(0), WithTLSConfig("", "client.pem", "", ""),
		WithTLSEnabled(true), // TLS must not be ignored by an amqp URI.
	} {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		backend, err := New(ctx, "amqp://guest:guest@127.0.0.1:1", "group", option)
		require.Error(t, err)
		require.NotErrorIs(t, err, context.Canceled)
		require.Nil(t, backend)
	}
}
