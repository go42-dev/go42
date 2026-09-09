package nats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	wnats "github.com/ThreeDotsLabs/watermill-nats/v2/pkg/nats"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/avast/retry-go/v4"
	natsgo "github.com/nats-io/nats.go"

	"github.com/go42-dev/go42/internal/metrics"
	"github.com/go42-dev/go42/internal/tools"
)

const (
	defaultConnectRetryTimeout        = time.Minute
	defaultConnectRetryInitialBackoff = 500 * time.Millisecond
	defaultConnectRetryMaxBackoff     = 5 * time.Second
	defaultPublishTimeout             = 5 * time.Second
	defaultNakDelay                   = time.Second
	defaultConsumerMaxDeliver         = -1
	defaultConnectTimeout             = 5 * time.Second
	defaultMaxReconnects              = -1
	defaultReconnectDelay             = time.Second
	defaultConsumerGroup              = "default"
	defaultSubscribersCount           = 1
	defaultAckWaitTimeout             = 30 * time.Second
	defaultSubscribeTimeout           = 30 * time.Second
	defaultCloseTimeout               = 30 * time.Second
	disabledReconnectBufferSize       = -1
)

type NATS struct {
	mu     sync.Mutex
	closed bool

	logger               *slog.Logger
	publisher            *wnats.Publisher
	subscribers          []*wnats.Subscriber
	subscriberConfig     wnats.SubscriberConfig
	publishTimeout       time.Duration
	consumerBindings     map[string]ConsumerBinding
	consumerBindingsJSON string
	tlsEnabled           bool
	tlsOpts              tools.TLSOptions

	connectRetryTimeout        time.Duration
	connectRetryInitialBackoff time.Duration
	connectRetryMaxBackoff     time.Duration
}

type connectionResult struct {
	publisher *wnats.Publisher
	err       error
}

func New(ctx context.Context, dsn string, opts ...Option) (*NATS, error) {
	var (
		engine = &NATS{
			connectRetryTimeout:        defaultConnectRetryTimeout,
			connectRetryInitialBackoff: defaultConnectRetryInitialBackoff,
			connectRetryMaxBackoff:     defaultConnectRetryMaxBackoff,
			publishTimeout:             defaultPublishTimeout,
		}
		jetStreamConfig = wnats.JetStreamConfig{
			AutoProvision:     false,
			DurablePrefix:     defaultConsumerGroup,
			DurableCalculator: func(prefix, topic string) string { return prefix + "_" + topic },
			AckAsync:          false,
		}
		pubCfg = &wnats.PublisherConfig{
			URL: dsn,
			NatsOptions: []natsgo.Option{
				natsgo.Timeout(defaultConnectTimeout),
				natsgo.MaxReconnects(defaultMaxReconnects),
				natsgo.ReconnectWait(defaultReconnectDelay),
			},
			JetStream: jetStreamConfig,
			Marshaler: gobMarshaler{},
		}
		subCfg = &wnats.SubscriberConfig{
			URL: dsn,
			NatsOptions: []natsgo.Option{
				natsgo.Timeout(defaultConnectTimeout),
				natsgo.MaxReconnects(defaultMaxReconnects),
				natsgo.ReconnectWait(defaultReconnectDelay),
			},
			JetStream:        jetStreamConfig,
			Unmarshaler:      new(wnats.GobMarshaler),
			NakDelay:         wnats.NewStaticDelay(defaultNakDelay),
			QueueGroupPrefix: defaultConsumerGroup,
			SubscribersCount: defaultSubscribersCount,
			AckWaitTimeout:   defaultAckWaitTimeout,
			SubscribeTimeout: defaultSubscribeTimeout,
			CloseTimeout:     defaultCloseTimeout,
		}
	)

	for _, o := range opts {
		o(engine, pubCfg, subCfg)
	}
	tlsConfig, err := engine.tlsOpts.LoadConfig(engine.tlsEnabled)
	if err != nil {
		return nil, err
	}
	if tlsConfig != nil {
		pubCfg.NatsOptions = append(pubCfg.NatsOptions, natsgo.Secure(tlsConfig.Clone()))
		subCfg.NatsOptions = append(subCfg.NatsOptions, natsgo.Secure(tlsConfig.Clone()))
	}
	if len(engine.consumerBindingsJSON) > 0 {
		if err := json.Unmarshal([]byte(engine.consumerBindingsJSON), &engine.consumerBindings); err != nil {
			return nil, fmt.Errorf("invalid NATS consumer bindings: %w", err)
		}
	}
	if err := validateConfig(engine, pubCfg, subCfg); err != nil {
		return nil, err
	}
	// The marshaler supplies a destination-specific ID; TrackMsgID would replace it with the UUID.
	pubCfg.JetStream.TrackMsgID = false
	pubCfg.JetStream.PublishOptions = append(pubCfg.JetStream.PublishOptions, natsgo.AckWait(engine.publishTimeout))
	subCfg.JetStream.ConnectOptions = append(subCfg.JetStream.ConnectOptions, natsgo.MaxWait(subCfg.SubscribeTimeout))

	if engine.logger == nil {
		engine.logger = slog.New(slog.DiscardHandler)
	}

	pubCfg.NatsOptions = append(pubCfg.NatsOptions, handlers(engine.logger, "publisher")...)
	subCfg.NatsOptions = append(subCfg.NatsOptions, handlers(engine.logger, "subscriber")...)
	// The durable outbox owns retries; do not queue timed out writes in client reconnect buffers.
	pubCfg.NatsOptions = append(
		pubCfg.NatsOptions,
		natsgo.ReconnectBufSize(disabledReconnectBufferSize),
		natsgo.FlusherTimeout(engine.publishTimeout),
	)
	subCfg.NatsOptions = append(
		subCfg.NatsOptions,
		natsgo.ReconnectBufSize(disabledReconnectBufferSize),
		natsgo.FlusherTimeout(engine.publishTimeout),
	)
	engine.subscriberConfig = *subCfg

	retryCtx, cancel := context.WithTimeout(ctx, engine.connectRetryTimeout)
	defer cancel()

	err = retry.Do(func() error {
		result := "failure"
		defer func() {
			metrics.Counter("application_event_backend_connection_attempts_total", map[string]any{
				"backend": "nats",
				"result":  result,
			}).Inc()
		}()

		connections, err := connect(retryCtx, *pubCfg, engine.logger)
		if err != nil {
			return err
		}

		engine.publisher = connections.publisher
		result = "success"
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

// NATS constructors do not accept a context. Bound startup even if a server accepts
// TCP but stalls its handshake, and close any connections completed after cancellation.
func connect(
	ctx context.Context,
	pubCfg wnats.PublisherConfig,
	logger *slog.Logger,
) (connectionResult, error) {
	ready := make(chan connectionResult)
	go func() {
		result := connectionResult{}
		result.publisher, result.err = wnats.NewPublisher(pubCfg, watermill.NewSlogLogger(logger))
		select {
		case ready <- result:
		case <-ctx.Done():
			result.close()
		}
	}()
	select {
	case result := <-ready:
		if err := ctx.Err(); err != nil {
			result.close()
			return connectionResult{}, err
		}
		if result.err != nil {
			result.close()
		}
		return result, result.err
	case <-ctx.Done():
		return connectionResult{}, ctx.Err()
	}
}

func (r connectionResult) close() {
	if r.publisher != nil {
		_ = r.publisher.Close()
	}
}

func (n *NATS) Publisher() message.Publisher {
	return n.publisher
}

func (n *NATS) Subscriber() message.Subscriber {
	return n
}

// Close delegates each subscription's shutdown to Watermill.
func (n *NATS) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return nil
	}
	n.closed = true
	var errs []error
	for _, subscriber := range n.subscribers {
		errs = append(errs, subscriber.Close())
	}
	return errors.Join(errs...)
}

func (n *NATS) Shutdown(ctx context.Context) error {
	done := make(chan error, 1)
	go func() {
		var errs []error
		if err := n.publisher.Close(); err != nil {
			errs = append(errs, fmt.Errorf("publisher close: %w", err))
		}
		if err := n.Close(); err != nil {
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

func validateConfig(engine *NATS, publisher *wnats.PublisherConfig, subscriber *wnats.SubscriberConfig) error {
	for _, address := range strings.Split(publisher.URL, ",") {
		parsed, err := url.Parse(strings.TrimSpace(address))
		if err != nil || parsed.Hostname() == "" ||
			(parsed.Scheme != "nats" && parsed.Scheme != "tls" && parsed.Scheme != "ws" && parsed.Scheme != "wss") {
			return errors.New("invalid NATS connection URL")
		}
	}
	for _, options := range [][]natsgo.Option{publisher.NatsOptions, subscriber.NatsOptions} {
		config := natsgo.GetDefaultOptions()
		for _, option := range options {
			if err := option(&config); err != nil {
				return fmt.Errorf("invalid NATS connection option: %w", err)
			}
		}
		if config.Timeout <= 0 || config.ReconnectWait <= 0 || config.MaxReconnect < -1 {
			return errors.New(
				"NATS connection timeout and reconnect delay must be positive; reconnect limit must be at least -1",
			)
		}
		if err := tools.ValidateTLSConfig(config.TLSConfig); err != nil {
			return err
		}
	}
	if publisher.JetStream.Disabled || subscriber.JetStream.Disabled || subscriber.JetStream.AckAsync {
		return errors.New("NATS requires JetStream and confirmed acknowledgements")
	}
	if engine.publishTimeout <= 0 || engine.connectRetryTimeout <= 0 ||
		engine.connectRetryInitialBackoff <= 0 || engine.connectRetryMaxBackoff < engine.connectRetryInitialBackoff ||
		subscriber.AckWaitTimeout <= 0 || subscriber.SubscribeTimeout <= 0 || subscriber.CloseTimeout <= 0 ||
		subscriber.SubscribersCount <= 0 {
		return errors.New("NATS timeouts, worker count and retry backoff must be positive and ordered")
	}
	for topic, binding := range engine.consumerBindings {
		if len(topic) == 0 || len(binding.Consumer) == 0 || len(binding.Stream) == 0 ||
			strings.ContainsAny(binding.Consumer, " .*></\\\t\r\n") {
			return errors.New("NATS consumer bindings require a topic, stream and valid durable consumer name")
		}
	}
	return nil
}

func handlers(l *slog.Logger, connection string) []natsgo.Option {
	observe := func(event string) {
		metrics.Counter("application_event_backend_connection_events_total", map[string]any{
			"backend":    "nats",
			"connection": connection,
			"event":      event,
		}).Inc()
	}

	return []natsgo.Option{
		natsgo.ConnectHandler(func(conn *natsgo.Conn) {
			l.Info("connection established", slog.String("connection", connection))
		}),
		natsgo.ErrorHandler(func(conn *natsgo.Conn, sub *natsgo.Subscription, err error) {
			if err != nil {
				observe("error")
				l.Warn("connection error",
					slog.String("connection", connection),
					slog.String("error", err.Error()),
				)
			}
		}),
		natsgo.DisconnectErrHandler(func(conn *natsgo.Conn, err error) {
			if err != nil {
				observe("disconnect")
				l.Warn("disconnection error",
					slog.String("connection", connection),
					slog.String("error", err.Error()),
				)
			}
		}),
		natsgo.LameDuckModeHandler(func(conn *natsgo.Conn) {
			l.Warn("server entering lame duck mode", slog.String("connection", connection))
		}),
		natsgo.ClosedHandler(func(conn *natsgo.Conn) {
			if err := conn.LastError(); err != nil {
				observe("closed")
				l.Error("connection closed",
					slog.String("connection", connection),
					slog.Any("error", err),
				)
				return
			}
			l.Info("connection closed", slog.String("connection", connection))
		}),
		natsgo.ReconnectHandler(func(conn *natsgo.Conn) {
			observe("reconnect")
			l.Info("reconnected", slog.String("connection", connection))
		}),
		natsgo.ReconnectErrHandler(func(conn *natsgo.Conn, err error) {
			observe("error")
			l.Debug("reconnect error",
				slog.String("connection", connection),
				slog.String("error", err.Error()),
			)
		}),
	}
}
