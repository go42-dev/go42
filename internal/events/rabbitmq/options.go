package rabbitmq

import (
	"log/slog"
	"time"

	"github.com/ThreeDotsLabs/watermill-amqp/v3/pkg/amqp"
)

type Option func(*AMQP, *amqp.Config)

// WithAutoProvision allows declaring application queues, exchanges and bindings.
func WithAutoProvision(enabled bool) Option {
	return func(engine *AMQP, _ *amqp.Config) { engine.autoProvision = enabled }
}

func WithConnectTimeout(timeout time.Duration) Option {
	return func(engine *AMQP, _ *amqp.Config) { engine.connectTimeout = timeout }
}

func WithTLSEnabled(enabled bool) Option {
	return func(engine *AMQP, _ *amqp.Config) {
		engine.tlsEnabled = enabled
	}
}

// WithTLSConfig sets certificate files and server name, loaded by New when TLS is enabled.
func WithTLSConfig(caFile, certFile, keyFile, serverName string) Option {
	return func(engine *AMQP, _ *amqp.Config) {
		engine.tls.CAFile = caFile
		engine.tls.CertFile = certFile
		engine.tls.KeyFile = keyFile
		engine.tls.ServerName = serverName
	}
}

func WithLogger(logger *slog.Logger) Option {
	return func(amqp *AMQP, _ *amqp.Config) {
		amqp.logger = logger
	}
}

func WithConnectRetryTimeout(timeout time.Duration) Option {
	return func(amqp *AMQP, cfg *amqp.Config) {
		amqp.connectRetryTimeout = timeout
	}
}

func WithConnectRetryBackoff(initial time.Duration, max time.Duration) Option {
	return func(amqp *AMQP, cfg *amqp.Config) {
		amqp.connectRetryInitialBackoff = initial
		amqp.connectRetryMaxBackoff = max
	}
}

func WithReconnectBackoffInitialInterval(interval time.Duration) Option {
	return func(_ *AMQP, cfg *amqp.Config) {
		if cfg.Connection.Reconnect == nil {
			cfg.Connection.Reconnect = &amqp.ReconnectConfig{}
		}
		cfg.Connection.Reconnect.BackoffInitialInterval = interval
	}
}

func WithReconnectBackoffMultiplier(multiplier float64) Option {
	return func(_ *AMQP, cfg *amqp.Config) {
		if cfg.Connection.Reconnect == nil {
			cfg.Connection.Reconnect = &amqp.ReconnectConfig{}
		}
		cfg.Connection.Reconnect.BackoffMultiplier = multiplier
	}
}

func WithReconnectBackoffMaxInterval(interval time.Duration) Option {
	return func(_ *AMQP, cfg *amqp.Config) {
		if cfg.Connection.Reconnect == nil {
			cfg.Connection.Reconnect = &amqp.ReconnectConfig{}
		}
		cfg.Connection.Reconnect.BackoffMaxInterval = interval
	}
}

func WithPublishMandatory(mandatory bool) Option {
	return func(_ *AMQP, cfg *amqp.Config) {
		cfg.Publish.Mandatory = mandatory
	}
}

func WithConsumeNoRequeueOnNack(noRequeue bool) Option {
	return func(_ *AMQP, cfg *amqp.Config) {
		cfg.Consume.NoRequeueOnNack = noRequeue
	}
}

func WithConsumeConsumerName(name string) Option {
	return func(_ *AMQP, cfg *amqp.Config) {
		if name != "" {
			cfg.Consume.Consumer = name
		}
	}
}

func WithConsumeExclusive(exclusive bool) Option {
	return func(_ *AMQP, cfg *amqp.Config) {
		cfg.Consume.Exclusive = exclusive
	}
}

func WithConsumeQosPrefetchCount(count int) Option {
	return func(_ *AMQP, cfg *amqp.Config) {
		cfg.Consume.Qos.PrefetchCount = count
	}
}

func WithConsumeQosPrefetchSize(size int) Option {
	return func(_ *AMQP, cfg *amqp.Config) {
		cfg.Consume.Qos.PrefetchSize = size
	}
}

func WithConsumeQosGlobal(global bool) Option {
	return func(_ *AMQP, cfg *amqp.Config) {
		cfg.Consume.Qos.Global = global
	}
}

func WithMessageUUIDHeaderKey(headerKey string) Option {
	return func(_ *AMQP, cfg *amqp.Config) {
		if headerKey != "" {
			if m, ok := cfg.Marshaler.(amqp.DefaultMarshaler); ok {
				m.MessageUUIDHeaderKey = headerKey
				cfg.Marshaler = m
			}
		}
	}
}
