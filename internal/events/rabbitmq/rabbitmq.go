package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill-amqp/v3/pkg/amqp"
	"github.com/ThreeDotsLabs/watermill/message"
	amqpgo "github.com/rabbitmq/amqp091-go"

	"github.com/go42-dev/go42/internal/tools"
)

const defaultConnectTimeout = 5 * time.Second

type AMQP struct {
	logger         *slog.Logger
	publisher      *amqp.Publisher
	subscriber     *amqp.Subscriber
	autoProvision  bool
	connectTimeout time.Duration
	tlsEnabled     bool
	tlsOpts        tools.TLSOptions

	connectRetry tools.StartupRetryPolicy
}

func New(ctx context.Context, dsn string, consumerGroup string, opts ...Option) (*AMQP, error) {
	if len(consumerGroup) == 0 {
		return nil, errors.New("consumer group is required")
	}

	var (
		engine = &AMQP{
			connectRetry:   tools.DefaultStartupRetryPolicy(),
			connectTimeout: defaultConnectTimeout,
		}
		amqpConfig = amqp.NewDurablePubSubConfig(
			dsn,
			amqp.GenerateQueueNameTopicNameWithSuffix(consumerGroup),
		)
	)

	amqpConfig.Publish.ConfirmDelivery = true
	amqpConfig.Publish.Mandatory = true
	amqpConfig.Consume.NoRequeueOnNack = false
	amqpConfig.Connection.Reconnect = amqp.DefaultReconnectConfig()

	for _, opt := range opts {
		opt(engine, &amqpConfig)
	}
	if !engine.autoProvision {
		amqpConfig.TopologyBuilder = existingTopology{}
	}
	// Use the client's standard dialer to bound TCP and AMQP handshakes.
	if amqpConfig.Connection.AmqpConfig == nil {
		amqpConfig.Connection.AmqpConfig = &amqpgo.Config{}
	}
	amqpConfig.Connection.AmqpConfig.Dial = amqpgo.DefaultDial(engine.connectTimeout)
	tlsConfig, err := engine.tlsOpts.LoadConfig(engine.tlsEnabled)
	if err != nil {
		return nil, err
	}
	if tlsConfig != nil {
		amqpConfig.Connection.AmqpConfig.TLSClientConfig = tlsConfig
	}
	if amqpConfig.Connection.TLSConfig != nil {
		if amqpConfig.Connection.AmqpConfig.TLSClientConfig != nil {
			return nil, errors.New("AMQP TLS configuration must be supplied only once")
		}
		amqpConfig.Connection.AmqpConfig.TLSClientConfig = amqpConfig.Connection.TLSConfig
		amqpConfig.Connection.TLSConfig = nil
	}
	if err := validateConfig(engine, amqpConfig); err != nil {
		return nil, err
	}

	if engine.logger == nil {
		engine.logger = slog.New(slog.DiscardHandler)
	}

	err = engine.connectRetry.Do(ctx, "rabbitmq", engine.logger, func(_ context.Context) error {
		publisher, err := amqp.NewPublisher(amqpConfig, watermill.NewSlogLogger(engine.logger))
		if err != nil {
			return fmt.Errorf("error creating amqp publisher: %w", err)
		}
		subscriber, err := amqp.NewSubscriber(amqpConfig, watermill.NewSlogLogger(engine.logger))
		if err != nil {
			return errors.Join(fmt.Errorf("error creating amqp subscriber: %w", err), publisher.Close())
		}

		engine.publisher = publisher
		engine.subscriber = subscriber
		return nil
	})
	if err != nil {
		return nil, err
	}

	return engine, nil
}

func (rmq *AMQP) Publisher() message.Publisher {
	return rmq.publisher
}

func (rmq *AMQP) Subscriber() message.Subscriber {
	return rmq.subscriber
}

func (rmq *AMQP) InitializeTopic(topic string) error {
	if !rmq.autoProvision {
		return nil
	}
	return rmq.subscriber.SubscribeInitialize(topic)
}

func (rmq *AMQP) Shutdown(ctx context.Context) error {
	done := make(chan error, 1)
	go func() {
		var errs []error
		if err := rmq.publisher.Close(); err != nil {
			errs = append(errs, fmt.Errorf("publisher close: %w", err))
		}
		if err := rmq.subscriber.Close(); err != nil {
			errs = append(errs, fmt.Errorf("subscriber close: %w", err))
		}
		done <- errors.Join(errs...)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func validateConfig(engine *AMQP, config amqp.Config) error {
	uri, err := amqpgo.ParseURI(config.Connection.AmqpURI)
	if err != nil {
		return errors.New("invalid AMQP connection URI")
	}
	tlsConfig := config.Connection.TLSConfig
	if tlsConfig == nil && config.Connection.AmqpConfig != nil {
		tlsConfig = config.Connection.AmqpConfig.TLSClientConfig
	}
	if err := tools.ValidateTLSConfig(tlsConfig); err != nil {
		return err
	}
	if tlsConfig != nil && uri.Scheme != "amqps" {
		return errors.New("AMQP TLS requires an amqps connection URI")
	}
	if err := errors.Join(config.ValidatePublisher(), config.ValidateSubscriber()); err != nil {
		return err
	}
	if engine.connectTimeout <= 0 {
		return errors.New("AMQP connection timeout must be positive")
	}
	if !config.Publish.ConfirmDelivery || !config.Publish.Mandatory || config.Consume.NoRequeueOnNack {
		return errors.New("AMQP requires publisher confirms, mandatory routing and requeue on failed processing")
	}
	if config.Consume.Qos.PrefetchCount <= 0 || config.Consume.Qos.PrefetchSize < 0 {
		return errors.New("AMQP prefetch count must be positive and size nonnegative")
	}
	if config.Connection.Reconnect == nil || config.Connection.Reconnect.BackoffInitialInterval <= 0 ||
		config.Connection.Reconnect.BackoffMultiplier < 1 ||
		config.Connection.Reconnect.BackoffMaxInterval < config.Connection.Reconnect.BackoffInitialInterval {
		return errors.New("invalid AMQP reconnect backoff")
	}
	return nil
}
