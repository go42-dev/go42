package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/IBM/sarama"
	"github.com/ThreeDotsLabs/watermill"
	wkafka "github.com/ThreeDotsLabs/watermill-kafka/v3/pkg/kafka"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/avast/retry-go/v4"

	"github.com/go42-dev/go42/internal/events"
	"github.com/go42-dev/go42/internal/metrics"
)

const (
	defaultConnectRetryTimeout        = time.Minute
	defaultConnectRetryInitialBackoff = 500 * time.Millisecond
	defaultConnectRetryMaxBackoff     = 5 * time.Second
	defaultMetadataTimeout            = 5 * time.Second
	defaultDialTimeout                = 5 * time.Second
	defaultReadTimeout                = 5 * time.Second
	defaultWriteTimeout               = 5 * time.Second
	defaultProducerTimeout            = 5 * time.Second
	defaultProducerRetryMax           = 3
	idempotentMaxOpenRequests         = 1
)

type Kafka struct {
	logger     *slog.Logger
	publisher  message.Publisher
	subscriber *subscriber
	client     sarama.Client
	configErr  error
	tlsEnabled bool
	tls        events.TLSOptions
	closeOnce  sync.Once
	closeDone  chan struct{}
	closeErr   error

	connectRetryTimeout        time.Duration
	connectRetryInitialBackoff time.Duration
	connectRetryMaxBackoff     time.Duration
}

type connectionResult struct {
	publisher  *wkafka.Publisher
	subscriber *wkafka.Subscriber
	client     sarama.Client
	err        error
}

func New(ctx context.Context, brokers []string, group string, opts ...Option) (*Kafka, error) {
	var (
		engine = &Kafka{
			closeDone:                  make(chan struct{}),
			connectRetryTimeout:        defaultConnectRetryTimeout,
			connectRetryInitialBackoff: defaultConnectRetryInitialBackoff,
			connectRetryMaxBackoff:     defaultConnectRetryMaxBackoff,
		}
		pubCfg = wkafka.DefaultSaramaSyncPublisherConfig()
		subCfg = wkafka.DefaultSaramaSubscriberConfig()
	)
	for _, config := range []*sarama.Config{pubCfg, subCfg} {
		config.Metadata.AllowAutoTopicCreation = false
		config.Metadata.Timeout = defaultMetadataTimeout
		config.Net.DialTimeout = defaultDialTimeout
		config.Net.ReadTimeout = defaultReadTimeout
		config.Net.WriteTimeout = defaultWriteTimeout
	}
	pubCfg.Producer.Timeout = defaultProducerTimeout
	pubCfg.Producer.Retry.Max = defaultProducerRetryMax
	subCfg.Consumer.Offsets.Initial = sarama.OffsetOldest
	subCfg.Consumer.Group.ResetInvalidOffsets = false
	subCfg.Consumer.IsolationLevel = sarama.ReadCommitted

	for _, opt := range opts {
		opt(engine, pubCfg, subCfg)
	}
	tlsConfig, err := engine.tls.LoadConfig(engine.tlsEnabled)
	if err != nil {
		return nil, err
	}
	if tlsConfig != nil {
		pubCfg.Net.TLS.Enable, subCfg.Net.TLS.Enable = true, true
		pubCfg.Net.TLS.Config, subCfg.Net.TLS.Config = tlsConfig.Clone(), tlsConfig.Clone()
	}

	pubCfg.Net.MaxOpenRequests = idempotentMaxOpenRequests
	pubCfg.Producer.RequiredAcks = sarama.WaitForAll
	pubCfg.Producer.Idempotent = true
	if err := validateConfig(engine, brokers, group, pubCfg, subCfg); err != nil {
		return nil, err
	}

	if engine.logger == nil {
		engine.logger = slog.New(slog.DiscardHandler)
	}

	retryCtx, cancel := context.WithTimeout(ctx, engine.connectRetryTimeout)
	defer cancel()

	err = retry.Do(func() error {
		resultLabel := "failure"
		defer func() {
			metrics.Counter("application_event_backend_connection_attempts_total", map[string]any{
				"backend": "kafka",
				"result":  resultLabel,
			}).Inc()
		}()

		result, err := connect(retryCtx, brokers, group, pubCfg, subCfg, engine.logger)
		if err != nil {
			return err
		}
		engine.publisher = &publisher{Publisher: result.publisher}
		engine.client = result.client
		engine.subscriber = &subscriber{Subscriber: result.subscriber, client: result.client}
		resultLabel = "success"
		return nil
	},
		retry.Context(retryCtx),
		retry.Attempts(0),
		retry.Delay(engine.connectRetryInitialBackoff),
		retry.MaxDelay(engine.connectRetryMaxBackoff),
		retry.DelayType(retry.FullJitterBackoffDelay),
		retry.WrapContextErrorWithLastError(true),
		retry.OnRetry(func(n uint, err error) {
			if retryCtx.Err() == nil {
				engine.logger.WarnContext(
					ctx,
					"broker connection attempt failed, retrying...",
					slog.Any("attempt", n+1),
					slog.Any("error", err),
				)
			}
		}),
	)
	if err != nil {
		return nil, err
	}

	return engine, nil
}

// connect enforces a hard startup deadline around the synchronous Watermill/Sarama constructors.
// Those constructors do not accept a context and may otherwise continue blocking after the
// configured connection retry timeout has expired.
func connect(
	ctx context.Context,
	brokers []string,
	group string,
	pubCfg *sarama.Config,
	subCfg *sarama.Config,
	logger *slog.Logger,
) (connectionResult, error) {
	resultChan := make(chan connectionResult, 1)
	go func() {
		resultChan <- openConnections(brokers, group, pubCfg, subCfg, logger)
	}()

	select {
	case result := <-resultChan:
		if err := ctx.Err(); err != nil {
			closeConnections(result)
			return connectionResult{}, err
		}
		return result, result.err
	case <-ctx.Done():
		// The constructors cannot be interrupted. Return to the caller immediately, then close
		// any connections they create when the in-flight attempt eventually completes.
		go func() {
			closeConnections(<-resultChan)
		}()
		return connectionResult{}, ctx.Err()
	}
}

func openConnections(
	brokers []string,
	group string,
	pubCfg *sarama.Config,
	subCfg *sarama.Config,
	logger *slog.Logger,
) connectionResult {
	publisher, err := wkafka.NewPublisher(
		wkafka.PublisherConfig{
			Brokers:               brokers,
			Marshaler:             marshaler{},
			OverwriteSaramaConfig: pubCfg,
		},
		watermill.NewSlogLogger(logger),
	)
	if err != nil {
		return connectionResult{err: fmt.Errorf("error creating kafka publisher: %w", err)}
	}

	subscriber, err := wkafka.NewSubscriber(
		wkafka.SubscriberConfig{
			Brokers:               brokers,
			Unmarshaler:           wkafka.DefaultMarshaler{},
			OverwriteSaramaConfig: subCfg,
			ConsumerGroup:         group,
		},
		watermill.NewSlogLogger(logger),
	)
	if err != nil {
		return connectionResult{
			err: errors.Join(
				fmt.Errorf("error creating kafka subscriber: %w", err),
				publisher.Close(),
			),
		}
	}
	client, err := sarama.NewClient(brokers, subCfg)
	if err != nil {
		return connectionResult{err: errors.Join(err, publisher.Close(), subscriber.Close())}
	}

	return connectionResult{publisher: publisher, subscriber: subscriber, client: client}
}

func closeConnections(result connectionResult) {
	if result.publisher != nil {
		_ = result.publisher.Close()
	}
	if result.subscriber != nil {
		_ = result.subscriber.Close()
	}
	if result.client != nil {
		_ = result.client.Close()
	}
}

func (k *Kafka) Publisher() message.Publisher {
	return k.publisher
}

func (k *Kafka) Subscriber() message.Subscriber {
	return k.subscriber
}

// InitializeTopic verifies access using ordinary topic metadata; it needs no administration API.
func (k *Kafka) InitializeTopic(topic string) error {
	return verifyTopic(context.Background(), k.client, topic)
}

func (k *Kafka) Shutdown(ctx context.Context) error {
	k.closeOnce.Do(func() {
		go func() {
			var errs []error
			if err := k.publisher.Close(); err != nil {
				errs = append(errs, fmt.Errorf("publisher close: %w", err))
			}
			if err := k.subscriber.Close(); err != nil {
				errs = append(errs, fmt.Errorf("subscriber close: %w", err))
			}
			if err := k.client.Close(); err != nil {
				errs = append(errs, fmt.Errorf("metadata client close: %w", err))
			}
			k.closeErr = errors.Join(errs...)
			close(k.closeDone)
		}()
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-k.closeDone:
		return k.closeErr
	}
}

func validateConfig(engine *Kafka, brokers []string, group string, publisher, subscriber *sarama.Config) error {
	if engine.configErr != nil {
		return engine.configErr
	}
	if publisher.Producer.Timeout <= 0 || publisher.Producer.Retry.Max <= 0 {
		return errors.New("kafka producer timeout and retry count must be positive")
	}
	if len(brokers) == 0 || len(group) == 0 {
		return errors.New("kafka brokers and consumer group are required")
	}
	if engine.connectRetryTimeout <= 0 || engine.connectRetryInitialBackoff <= 0 ||
		engine.connectRetryMaxBackoff < engine.connectRetryInitialBackoff {
		return errors.New("kafka startup timeout and backoff must be positive and ordered")
	}
	for _, config := range []*sarama.Config{publisher, subscriber} {
		if config.Metadata.Timeout <= 0 || config.Net.DialTimeout <= 0 || config.Net.ReadTimeout <= 0 ||
			config.Net.WriteTimeout <= 0 {
			return errors.New("kafka network and metadata timeouts must be positive")
		}
		if err := errors.Join(config.Validate(), events.ValidateTLSConfig(config.Net.TLS.Config)); err != nil {
			return err
		}
	}
	return nil
}
