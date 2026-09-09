package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/message/router/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/go42-dev/go42/internal/metrics"
	"github.com/go42-dev/go42/internal/tools"
)

const (
	defaultMaxRetries               = 3
	defaultInitialBackoff           = 250 * time.Millisecond
	defaultMaxBackoff               = 5 * time.Second
	defaultRetryMultiplier          = 2
	defaultRetryRandomizationFactor = 0.2
	defaultDeadLetterTopicSuffix    = "_dlq"
)

type Router struct {
	backend               Backend
	publisher             message.Publisher
	router                *message.Router
	maxRetries            int
	initialBackoff        time.Duration
	maxBackoff            time.Duration
	deadLetterTopicSuffix string
	closeTimeout          time.Duration
	publishMaxInflight    int
	logger                *slog.Logger
	watermillLogger       watermill.LoggerAdapter
	handlerID             atomic.Uint64
	shuttingDown          atomic.Bool
	errors                chan error
}

func NewRouter(backend Backend, opts ...Option) (*Router, error) {
	r := &Router{
		backend:               backend,
		publishMaxInflight:    defaultPublishMaxInflight,
		maxRetries:            defaultMaxRetries,
		initialBackoff:        defaultInitialBackoff,
		maxBackoff:            defaultMaxBackoff,
		deadLetterTopicSuffix: defaultDeadLetterTopicSuffix,
		errors:                make(chan error, 1),
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.logger == nil {
		r.logger = slog.New(slog.DiscardHandler)
	}

	watermillLogger := watermill.NewSlogLogger(r.logger)
	watermillRouter, err := message.NewRouter(message.RouterConfig{
		CloseTimeout: r.closeTimeout,
	}, watermillLogger)
	if err != nil {
		return nil, fmt.Errorf("failed to create Watermill router: %w", err)
	}

	r.router = watermillRouter
	r.watermillLogger = watermillLogger
	r.publisher = newBoundedPublisher(backend.Publisher(), r.publishMaxInflight)

	return r, nil
}

// Start initializes subscriptions and begins processing messages.
// Call it once, after registering all handlers with Subscribe.
// The caller must exit on error; failed startup does not roll back subscriptions.
func (r *Router) Start(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)

	runErr := make(chan error, 1)
	go func() {
		runErr <- r.router.Run(runCtx)
	}()

	if err := r.waitUntilReady(ctx, runErr); err != nil {
		cancel()
		close(r.errors)
		return err
	}

	go r.monitor(ctx, runErr, cancel)

	return nil
}

func (r *Router) waitUntilReady(ctx context.Context, runErr <-chan error) error {
	select {
	case <-r.router.Running():
		return ctx.Err()
	case err := <-runErr:
		if err != nil {
			return err
		}
		return errors.New("event router stopped during startup")
	}
}

func (r *Router) monitor(ctx context.Context, runErr <-chan error, cancel context.CancelFunc) {
	defer close(r.errors)
	defer cancel()

	err := <-runErr
	if ctx.Err() != nil || r.shuttingDown.Load() {
		return
	}
	if err == nil {
		err = errors.New("event router stopped unexpectedly without error")
	} else {
		err = fmt.Errorf("event router stopped unexpectedly: %w", err)
	}

	r.errors <- err
	metrics.Counter("application_event_router_stops_total", map[string]any{
		"reason": "unexpected",
	}).Inc()
	r.logger.ErrorContext(ctx, "event router stopped", slog.Any("error", err))
}

// Errors reports at most one unexpected termination after Start succeeds.
// It closes when the router stops; normal shutdown closes it without an error.
func (r *Router) Errors() <-chan error {
	return r.errors
}

func (r *Router) Shutdown(ctx context.Context) error {
	r.shuttingDown.Store(true)
	done := make(chan error, 1)
	go func() {
		routerErr := r.router.Close()
		backendErr := r.backend.Shutdown(ctx)
		done <- errors.Join(routerErr, backendErr)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

// ---

// Publish requires an event ID. Reuse the same ID when retrying an event.
func (r *Router) Publish(ctx context.Context, topic string, id string, event []byte) error {
	if len(topic) == 0 || len(id) == 0 {
		return Permanent(errors.New("event topic and ID are required"))
	}

	ctx, span := otel.Tracer("events").Start(ctx, "publish "+topic,
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.destination.name", topic),
			attribute.String("messaging.message.id", id),
		),
	)
	defer span.End()

	msg := message.NewMessageWithContext(ctx, id, event)
	msg.Metadata = PropagationFromContext(ctx)
	err := r.publisher.Publish(topic, msg)

	result := "success"

	if err != nil {
		result = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}

	metrics.Counter("application_event_publish_total", map[string]any{
		"result": result,
		"topic":  topic,
	}).Inc()

	return err
}

func (r *Router) Subscribe(
	topic string,
	handler func(ctx context.Context, event []byte) error,
) error {
	if len(topic) == 0 {
		return errors.New("event topic is required")
	}
	deadLetterTopic := topic + r.deadLetterTopicSuffix

	if initializer, ok := r.backend.(TopicInitializer); ok {
		if err := initializer.InitializeTopic(deadLetterTopic); err != nil {
			return fmt.Errorf(
				"failed to initialize dead-letter topic %q: %w",
				deadLetterTopic,
				err,
			)
		}
	}

	poisonQueue, err := middleware.PoisonQueue(
		&deadLetterPublisher{
			publisher: r.publisher,
			logger:    r.logger,
		},
		deadLetterTopic,
	)
	if err != nil {
		return fmt.Errorf("failed to configure dead-letter topic %q: %w", deadLetterTopic, err)
	}

	retry := middleware.Retry{
		MaxRetries:          r.maxRetries,
		InitialInterval:     r.initialBackoff,
		MaxInterval:         r.maxBackoff,
		Multiplier:          defaultRetryMultiplier,
		RandomizationFactor: defaultRetryRandomizationFactor,
		ResetContextOnRetry: true,
		Logger:              r.watermillLogger,
		ShouldRetry: func(params middleware.RetryParams) bool {
			var permanent *permanentError
			return !errors.As(params.Err, &permanent)
		},
	}

	watermillHandler := r.router.AddConsumerHandler(
		fmt.Sprintf("%s_%d", topic, r.handlerID.Add(1)),
		topic,
		r.backend.Subscriber(),
		func(msg *message.Message) error {
			return handler(msg.Context(), msg.Payload)
		},
	)

	// Restore correlation before `Retry` captures its baseline context.
	// `PoisonQueue` wraps the processing span so exhausted retries still record an error.
	watermillHandler.AddMiddleware(
		restoreMessageContext(topic),
		poisonQueue,
		traceMessage(topic),
		retryWithMetrics(topic, retry),
	)

	return nil
}

func retryWithMetrics(topic string, retry middleware.Retry) message.HandlerMiddleware {
	retryCounter := metrics.Counter("application_event_consumer_retries_total", map[string]any{
		"topic": topic,
	})

	return func(next message.HandlerFunc) message.HandlerFunc {
		return func(msg *message.Message) ([]*message.Message, error) {
			messageRetry := retry
			if messageRetry.Logger != nil {
				fields := watermill.LogFields{}
				for _, attr := range tools.LogAttrsFromContext(msg.Context()) {
					fields[attr.Key] = attr.Value.Resolve().Any()
				}
				messageRetry.Logger = messageRetry.Logger.With(fields)
			}
			attempt := 0
			retryHandler := messageRetry.Middleware(func(msg *message.Message) ([]*message.Message, error) {
				if attempt > 0 {
					retryCounter.Inc()
				}
				attempt++
				return next(msg)
			})
			return retryHandler(msg)
		}
	}
}
