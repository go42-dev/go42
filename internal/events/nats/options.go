package nats

import (
	"log/slog"
	"maps"
	"time"

	"github.com/ThreeDotsLabs/watermill-nats/v2/pkg/nats"
	natsgo "github.com/nats-io/nats.go"
)

type Option func(*NATS, *nats.PublisherConfig, *nats.SubscriberConfig)

func WithConsumerBindings(bindings map[string]ConsumerBinding) Option {
	return func(engine *NATS, _ *nats.PublisherConfig, _ *nats.SubscriberConfig) {
		engine.consumerBindings = maps.Clone(bindings)
	}
}

func WithTLSEnabled(enabled bool) Option {
	return func(engine *NATS, _ *nats.PublisherConfig, _ *nats.SubscriberConfig) {
		engine.tlsEnabled = enabled
	}
}

// WithTLSConfig sets certificate files and server name, loaded by New when TLS is enabled.
func WithTLSConfig(caFile, certFile, keyFile, serverName string) Option {
	return func(engine *NATS, _ *nats.PublisherConfig, _ *nats.SubscriberConfig) {
		engine.tlsOpts.CAFile = caFile
		engine.tlsOpts.CertFile = certFile
		engine.tlsOpts.KeyFile = keyFile
		engine.tlsOpts.ServerName = serverName
	}
}

func WithCredentialsFile(path string) Option {
	return func(_ *NATS, publisher *nats.PublisherConfig, subscriber *nats.SubscriberConfig) {
		if len(path) > 0 {
			publisher.NatsOptions = append(publisher.NatsOptions, natsgo.UserCredentials(path))
			subscriber.NatsOptions = append(subscriber.NatsOptions, natsgo.UserCredentials(path))
		}
	}
}

func WithUserPassword(user, password string) Option {
	return func(_ *NATS, publisher *nats.PublisherConfig, subscriber *nats.SubscriberConfig) {
		if len(user) > 0 || len(password) > 0 {
			publisher.NatsOptions = append(publisher.NatsOptions, natsgo.UserInfo(user, password))
			subscriber.NatsOptions = append(subscriber.NatsOptions, natsgo.UserInfo(user, password))
		}
	}
}

func WithLogger(logger *slog.Logger) Option {
	return func(n *NATS, pubCfg *nats.PublisherConfig, subCfg *nats.SubscriberConfig) {
		n.logger = logger
	}
}

func WithClientName(name string) Option {
	return func(n *NATS, pubCfg *nats.PublisherConfig, subCfg *nats.SubscriberConfig) {
		pubCfg.NatsOptions = append(pubCfg.NatsOptions, natsgo.Name(name))
		subCfg.NatsOptions = append(subCfg.NatsOptions, natsgo.Name(name))
	}
}

func WithClientToken(token string) Option {
	return func(n *NATS, pubCfg *nats.PublisherConfig, subCfg *nats.SubscriberConfig) {
		if len(token) == 0 {
			return
		}
		pubCfg.NatsOptions = append(pubCfg.NatsOptions, natsgo.Token(token))
		subCfg.NatsOptions = append(subCfg.NatsOptions, natsgo.Token(token))
	}
}

func WithConnectTimeout(timeout time.Duration) Option {
	return func(n *NATS, pubCfg *nats.PublisherConfig, subCfg *nats.SubscriberConfig) {
		pubCfg.NatsOptions = append(pubCfg.NatsOptions, natsgo.Timeout(timeout))
		subCfg.NatsOptions = append(subCfg.NatsOptions, natsgo.Timeout(timeout))
	}
}

func WithConnectRetryTimeout(timeout time.Duration) Option {
	return func(n *NATS, pubCfg *nats.PublisherConfig, subCfg *nats.SubscriberConfig) {
		n.connectRetry.Timeout = timeout
	}
}

func WithConnectRetryBackoff(initial time.Duration, max time.Duration) Option {
	return func(n *NATS, pubCfg *nats.PublisherConfig, subCfg *nats.SubscriberConfig) {
		n.connectRetry.InitialBackoff = initial
		n.connectRetry.MaxBackoff = max
	}
}

func WithConnectionRetry(retry bool) Option {
	return func(n *NATS, pubCfg *nats.PublisherConfig, subCfg *nats.SubscriberConfig) {
		pubCfg.NatsOptions = append(pubCfg.NatsOptions, natsgo.RetryOnFailedConnect(retry))
		subCfg.NatsOptions = append(subCfg.NatsOptions, natsgo.RetryOnFailedConnect(retry))
	}
}

func WithMaxReconnects(maxReconnects int) Option {
	return func(n *NATS, pubCfg *nats.PublisherConfig, subCfg *nats.SubscriberConfig) {
		pubCfg.NatsOptions = append(pubCfg.NatsOptions, natsgo.MaxReconnects(maxReconnects))
		subCfg.NatsOptions = append(subCfg.NatsOptions, natsgo.MaxReconnects(maxReconnects))
	}
}

func WithReconnectDelay(delay time.Duration) Option {
	return func(n *NATS, pubCfg *nats.PublisherConfig, subCfg *nats.SubscriberConfig) {
		pubCfg.NatsOptions = append(pubCfg.NatsOptions, natsgo.ReconnectWait(delay))
		subCfg.NatsOptions = append(subCfg.NatsOptions, natsgo.ReconnectWait(delay))
	}
}

func WithJetStreamAutoProvision(autoProvision bool) Option {
	return func(n *NATS, pubCfg *nats.PublisherConfig, subCfg *nats.SubscriberConfig) {
		pubCfg.JetStream.AutoProvision = autoProvision
		subCfg.JetStream.AutoProvision = autoProvision
	}
}

// WithPublishAckTimeout limits the wait for a JetStream publish acknowledgement.
func WithPublishAckTimeout(timeout time.Duration) Option {
	return func(engine *NATS, _ *nats.PublisherConfig, _ *nats.SubscriberConfig) {
		engine.publishTimeout = timeout
	}
}

func WithSubGroupPrefix(prefix string) Option {
	return func(n *NATS, pubCfg *nats.PublisherConfig, subCfg *nats.SubscriberConfig) {
		subCfg.QueueGroupPrefix = prefix
		subCfg.JetStream.DurablePrefix = prefix
		subCfg.JetStream.DurableCalculator = func(prefix string, topic string) string {
			return prefix + "_" + topic
		}
	}
}

func WithSubWorkerCount(count int) Option {
	return func(n *NATS, pubCfg *nats.PublisherConfig, subCfg *nats.SubscriberConfig) {
		subCfg.SubscribersCount = count
	}
}

func WithSubTimeout(timeout time.Duration) Option {
	return func(n *NATS, pubCfg *nats.PublisherConfig, subCfg *nats.SubscriberConfig) {
		subCfg.SubscribeTimeout = timeout

	}
}

func WithSubAckTimeout(timeout time.Duration) Option {
	return func(n *NATS, pubCfg *nats.PublisherConfig, subCfg *nats.SubscriberConfig) {
		subCfg.AckWaitTimeout = timeout
	}
}

func WithSubCloseTimeout(timeout time.Duration) Option {
	return func(n *NATS, pubCfg *nats.PublisherConfig, subCfg *nats.SubscriberConfig) {
		subCfg.CloseTimeout = timeout
	}
}
