package nats

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	wnats "github.com/ThreeDotsLabs/watermill-nats/v2/pkg/nats"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/avast/retry-go/v4"
	natsgo "github.com/nats-io/nats.go"

	"github.com/go42-dev/go42/internal/metrics"
)

const (
	defaultConnectRetryTimeout        = time.Minute
	defaultConnectRetryInitialBackoff = 500 * time.Millisecond
	defaultConnectRetryMaxBackoff     = 5 * time.Second
)

type NATS struct {
	logger     *slog.Logger
	publisher  *wnats.Publisher
	subscriber *wnats.Subscriber

	connectRetryTimeout        time.Duration
	connectRetryInitialBackoff time.Duration
	connectRetryMaxBackoff     time.Duration
}

func New(ctx context.Context, dsn string, opts ...Option) (*NATS, error) {
	var (
		engine = &NATS{
			connectRetryTimeout:        defaultConnectRetryTimeout,
			connectRetryInitialBackoff: defaultConnectRetryInitialBackoff,
			connectRetryMaxBackoff:     defaultConnectRetryMaxBackoff,
		}
		jetStreamConfig = wnats.JetStreamConfig{
			AutoProvision: true,
			SubscribeOptions: []natsgo.SubOpt{
				natsgo.DeliverAll(),
				natsgo.AckExplicit(),
				// Watermill decides when processing has completed and acknowledgement is safe.
				natsgo.ManualAck(),
			},
			TrackMsgID: true,
			AckAsync:   false,
		}
		pubCfg = &wnats.PublisherConfig{
			URL:       dsn,
			JetStream: jetStreamConfig,
			Marshaler: new(wnats.GobMarshaler),
		}
		subCfg = &wnats.SubscriberConfig{
			URL:         dsn,
			JetStream:   jetStreamConfig,
			Unmarshaler: new(wnats.GobMarshaler),
		}
	)

	for _, o := range opts {
		o(engine, pubCfg, subCfg)
	}

	if engine.logger == nil {
		engine.logger = slog.New(slog.DiscardHandler)
	}

	pubCfg.NatsOptions = append(pubCfg.NatsOptions, handlers(engine.logger, "publisher")...)
	subCfg.NatsOptions = append(subCfg.NatsOptions, handlers(engine.logger, "subscriber")...)

	retryCtx, cancel := context.WithTimeout(ctx, engine.connectRetryTimeout)
	defer cancel()

	err := retry.Do(func() error {
		result := "failure"
		defer func() {
			metrics.Counter("application_event_backend_connection_attempts_total", map[string]any{
				"backend": "nats",
				"result":  result,
			}).Inc()
		}()

		publisher, err := wnats.NewPublisher(*pubCfg, watermill.NewSlogLogger(engine.logger))
		if err != nil {
			return fmt.Errorf("error creating nats publisher: %w", err)
		}

		subscriber, err := wnats.NewSubscriber(*subCfg, watermill.NewSlogLogger(engine.logger))
		if err != nil {
			return errors.Join(
				fmt.Errorf("error creating nats subscriber: %w", err),
				publisher.Close(),
			)
		}

		engine.publisher = publisher
		engine.subscriber = subscriber
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

func (n *NATS) Publisher() message.Publisher {
	return n.publisher
}

func (n *NATS) Subscriber() message.Subscriber {
	return n.subscriber
}

func (n *NATS) Shutdown(ctx context.Context) error {
	done := make(chan error, 1)
	go func() {
		var errs []error
		if err := n.publisher.Close(); err != nil {
			errs = append(errs, fmt.Errorf("publisher close: %w", err))
		}
		if err := n.subscriber.Close(); err != nil {
			errs = append(errs, fmt.Errorf("subscriber close: %w", err))
		}
		done <- errors.Join(errs...)
	}()
	select {
	case <-ctx.Done():
		return errors.New("timeout")
	case err := <-done:
		return err
	}
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
