package kafka

import (
	"log/slog"
	"time"

	"github.com/IBM/sarama"
)

type Option func(*Kafka, *sarama.Config, *sarama.Config)

func WithLogger(logger *slog.Logger) Option {
	return func(k *Kafka, pubCfg *sarama.Config, subCfg *sarama.Config) {
		k.logger = logger
	}
}

func WithConnectRetryTimeout(timeout time.Duration) Option {
	return func(k *Kafka, pubCfg *sarama.Config, subCfg *sarama.Config) {
		k.connectRetryTimeout = timeout
	}
}

func WithConnectRetryBackoff(initial time.Duration, max time.Duration) Option {
	return func(k *Kafka, pubCfg *sarama.Config, subCfg *sarama.Config) {
		k.connectRetryInitialBackoff = initial
		k.connectRetryMaxBackoff = max
	}
}

func WithClientID(clientID string) Option {
	return func(k *Kafka, pubCfg *sarama.Config, subCfg *sarama.Config) {
		if clientID != "" {
			pubCfg.ClientID = clientID
			subCfg.ClientID = clientID
		}
	}
}

func WithDialTimeout(timeout time.Duration) Option {
	return func(k *Kafka, pubCfg *sarama.Config, subCfg *sarama.Config) {
		pubCfg.Net.DialTimeout = timeout
		subCfg.Net.DialTimeout = timeout
	}
}

func WithReadTimeout(timeout time.Duration) Option {
	return func(k *Kafka, pubCfg *sarama.Config, subCfg *sarama.Config) {
		pubCfg.Net.ReadTimeout = timeout
		subCfg.Net.ReadTimeout = timeout
	}
}

func WithWriteTimeout(timeout time.Duration) Option {
	return func(k *Kafka, pubCfg *sarama.Config, subCfg *sarama.Config) {
		pubCfg.Net.WriteTimeout = timeout
		subCfg.Net.WriteTimeout = timeout
	}
}

func WithKeepAlive(keepAlive time.Duration) Option {
	return func(k *Kafka, pubCfg *sarama.Config, subCfg *sarama.Config) {
		pubCfg.Net.KeepAlive = keepAlive
		subCfg.Net.KeepAlive = keepAlive
	}
}

func WithProducerRetryBackoff(backoff time.Duration) Option {
	return func(k *Kafka, pubCfg *sarama.Config, subCfg *sarama.Config) {
		pubCfg.Producer.Retry.Backoff = backoff
	}
}

// WithProducerTimeout limits how long the broker waits for acknowledgements.
// Network I/O, metadata lookups and retries have separate limits.
func WithProducerTimeout(timeout time.Duration) Option {
	return func(_ *Kafka, pubCfg *sarama.Config, _ *sarama.Config) {
		pubCfg.Producer.Timeout = timeout
	}
}

func WithProducerMetadataTimeout(timeout time.Duration) Option {
	return func(_ *Kafka, pubCfg *sarama.Config, _ *sarama.Config) {
		pubCfg.Metadata.Timeout = timeout
	}
}

func WithProducerRetryMax(maxRetries int) Option {
	return func(_ *Kafka, pubCfg *sarama.Config, _ *sarama.Config) {
		pubCfg.Producer.Retry.Max = maxRetries
	}
}

func WithProducerMaxMessageBytes(bytes int) Option {
	return func(k *Kafka, pubCfg *sarama.Config, subCfg *sarama.Config) {
		pubCfg.Producer.MaxMessageBytes = bytes
	}
}

func WithProducerCompression(compression string) Option {
	return func(k *Kafka, pubCfg *sarama.Config, subCfg *sarama.Config) {
		switch compression {
		case "none":
			pubCfg.Producer.Compression = sarama.CompressionNone
		case "gzip":
			pubCfg.Producer.Compression = sarama.CompressionGZIP
		case "snappy":
			pubCfg.Producer.Compression = sarama.CompressionSnappy
		case "lz4":
			pubCfg.Producer.Compression = sarama.CompressionLZ4
		case "zstd":
			pubCfg.Producer.Compression = sarama.CompressionZSTD
		}
	}
}

func WithConsumerRetryBackoff(backoff time.Duration) Option {
	return func(k *Kafka, pubCfg *sarama.Config, subCfg *sarama.Config) {
		subCfg.Consumer.Retry.Backoff = backoff
	}
}

func WithConsumerMaxWaitTime(duration time.Duration) Option {
	return func(k *Kafka, pubCfg *sarama.Config, subCfg *sarama.Config) {
		subCfg.Consumer.MaxWaitTime = duration
	}
}

func WithConsumerMaxProcessingTime(duration time.Duration) Option {
	return func(k *Kafka, pubCfg *sarama.Config, subCfg *sarama.Config) {
		subCfg.Consumer.MaxProcessingTime = duration
	}
}

func WithConsumerOffsetInitial(offset int64) Option {
	return func(k *Kafka, pubCfg *sarama.Config, subCfg *sarama.Config) {
		subCfg.Consumer.Offsets.Initial = offset
	}
}

func WithConsumerGroupSessionTimeout(timeout time.Duration) Option {
	return func(k *Kafka, pubCfg *sarama.Config, subCfg *sarama.Config) {
		subCfg.Consumer.Group.Session.Timeout = timeout
	}
}

func WithConsumerGroupHeartbeatInterval(interval time.Duration) Option {
	return func(k *Kafka, pubCfg *sarama.Config, subCfg *sarama.Config) {
		subCfg.Consumer.Group.Heartbeat.Interval = interval
	}
}

func WithConsumerGroupRebalanceStrategy(strategy string) Option {
	return func(k *Kafka, pubCfg *sarama.Config, subCfg *sarama.Config) {
		switch strategy {
		case "range":
			subCfg.Consumer.Group.Rebalance.Strategy = sarama.NewBalanceStrategyRange()
		case "roundrobin":
			subCfg.Consumer.Group.Rebalance.Strategy = sarama.NewBalanceStrategyRoundRobin()
		case "sticky":
			subCfg.Consumer.Group.Rebalance.Strategy = sarama.NewBalanceStrategySticky()
		}
	}
}

func WithMetadataRefreshFrequency(frequency time.Duration) Option {
	return func(k *Kafka, pubCfg *sarama.Config, subCfg *sarama.Config) {
		pubCfg.Metadata.RefreshFrequency = frequency
		subCfg.Metadata.RefreshFrequency = frequency
	}
}

func WithKafkaVersion(version string) Option {
	return func(k *Kafka, pubCfg *sarama.Config, subCfg *sarama.Config) {
		if version != "" {
			if v, err := sarama.ParseKafkaVersion(version); err == nil {
				pubCfg.Version = v
				subCfg.Version = v
			}
		}
	}
}
