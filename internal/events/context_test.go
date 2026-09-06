package events_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/go42-dev/go42/internal/events"
	"github.com/go42-dev/go42/internal/events/gochan"
	"github.com/go42-dev/go42/internal/tools"
)

type propagationLocalKey struct{}

type messageContextObservation struct {
	requestID string
	span      trace.SpanContext
	err       error
	attrs     []slog.Attr
}

type messageDeliveryTest struct {
	name      string
	permanent bool
	attempts  int
}

func TestPropagationRestoresCorrelationWithConsumerLifetime(t *testing.T) {
	state, err := trace.ParseTraceState("vendor=value")
	require.NoError(t, err)
	span := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceState: state,
	})
	producer := trace.ContextWithSpanContext(t.Context(), span)
	producer = tools.SetRequestIDToContext(producer, "request-42")
	producer, cancelProducer := context.WithCancel(producer)
	fields := events.PropagationFromContext(producer)
	cancelProducer()

	consumer := context.WithValue(t.Context(), propagationLocalKey{}, "local")
	consumer, cancelConsumer := context.WithTimeout(consumer, time.Minute)
	defer cancelConsumer()
	ctx := events.ContextWithPropagation(consumer, fields)
	assert.NoError(t, ctx.Err())
	assert.Equal(t, "local", ctx.Value(propagationLocalKey{}))
	assert.Equal(t, "request-42", tools.GetRequestIDFromContext(ctx))
	actualSpan := trace.SpanContextFromContext(ctx)
	assert.Equal(t, span.TraceID(), actualSpan.TraceID())
	assert.Equal(t, span.SpanID(), actualSpan.SpanID())
	assert.Equal(t, "vendor=value", actualSpan.TraceState().String())
	assert.True(t, actualSpan.IsRemote())
	assert.False(t, actualSpan.IsSampled())
	wantDeadline, _ := consumer.Deadline()
	deadline, ok := ctx.Deadline()
	assert.True(t, ok)
	assert.Equal(t, wantDeadline, deadline)
	cancelConsumer()
	assert.ErrorIs(t, ctx.Err(), context.Canceled)
}

func TestPropagationAcceptsMissingOrInvalidMetadata(t *testing.T) {
	for _, fields := range []map[string]string{nil, {}, {"traceparent": "invalid"}} {
		base := tools.SetRequestIDToContext(t.Context(), "unrelated-worker-request")
		ctx := events.ContextWithPropagation(base, fields)
		assert.Empty(t, tools.GetRequestIDFromContext(ctx))
		assert.False(t, trace.SpanContextFromContext(ctx).IsValid())
		assert.Empty(t, events.PropagationFromContext(ctx))
	}
}

func TestSubscribersKeepCorrelationThroughRetriesAndDeadLetters(t *testing.T) {
	recorder := installEventSpanRecorder(t)
	for _, test := range []messageDeliveryTest{
		{name: "exhausted retries", attempts: 3},
		{name: "permanent failure", permanent: true, attempts: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder.Reset()
			var output bytes.Buffer
			logger := slog.New(tools.SlogContextWrapper(slog.NewJSONHandler(&output, nil)))
			router, backend, ctx := newContextTestRouter(t, logger)
			deadLetters, err := backend.Subscriber().Subscribe(ctx, "failing_dlq")
			require.NoError(t, err)
			observations := make(chan messageContextObservation, test.attempts)
			require.NoError(t, router.Subscribe("failing", func(ctx context.Context, payload []byte) error {
				observations <- observeMessageContext(ctx)
				err := errors.New("invalid event")
				if test.permanent {
					return events.Permanent(err)
				}
				return err
			}))
			startRouter(t, router, ctx)
			source := trace.NewSpanContext(trace.SpanContextConfig{
				TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled,
			})
			producer := trace.ContextWithSpanContext(t.Context(), source)
			producer = tools.SetRequestIDToContext(producer, "request-42")
			require.NoError(t, router.Publish(producer, "failing", []byte("invalid JSON")))
			deadLetter := waitForRouterMessage(t, deadLetters)
			deadLetter.Ack()
			require.NoError(t, router.Shutdown(t.Context()))
			_, open := waitForRouterError(t, router.Errors())
			require.False(t, open)

			var consumerSpan trace.SpanContext
			for range test.attempts {
				var observation messageContextObservation
				select {
				case observation = <-observations:
				default:
					t.Fatal("missing delivery attempt")
				}
				assert.Equal(t, "request-42", observation.requestID)
				assert.NoError(t, observation.err)
				assert.Equal(t, source.TraceID(), observation.span.TraceID())
				assert.NotEqual(t, source.SpanID(), observation.span.SpanID())
				if consumerSpan.IsValid() {
					assert.Equal(t, consumerSpan.SpanID(), observation.span.SpanID())
				}
				consumerSpan = observation.span
			}
			assert.Equal(t, "request-42", deadLetter.Metadata.Get("request_id"))
			restored := events.ContextWithPropagation(t.Context(), deadLetter.Metadata)
			assert.Equal(t, source.TraceID(), trace.SpanContextFromContext(restored).TraceID())
			assert.Equal(t, "invalid JSON", string(deadLetter.Payload))

			retries, deadLetterLogs := 0, 0
			for _, entry := range readEventLogs(t, &output) {
				switch entry["msg"] {
				case "Error occurred, retrying":
					retries++
				case "event moved to dead-letter topic":
					deadLetterLogs++
				default:
					continue
				}
				assert.Equal(t, "request-42", entry["request_id"])
				assert.Equal(t, source.TraceID().String(), entry["trace_id"])
				assert.Equal(t, consumerSpan.SpanID().String(), entry["span_id"])
				assert.Equal(t, deadLetter.UUID, entry["message_uuid"])
			}
			assert.Equal(t, test.attempts-1, retries)
			assert.Equal(t, 1, deadLetterLogs)

			spans := recorder.Ended()
			require.Len(t, spans, 2)
			for _, span := range spans {
				if span.SpanKind() != trace.SpanKindConsumer {
					continue
				}
				assert.Equal(t, codes.Error, span.Status().Code)
				require.Len(t, span.Links(), 1)
				assert.Equal(t, span.Parent().SpanID(), span.Links()[0].SpanContext.SpanID())
				assert.Equal(t, trace.SpanContextFromContext(restored).SpanID(), span.Parent().SpanID())
			}
		})
	}
}

func TestSubscriberDoesNotInheritProducerCancellation(t *testing.T) {
	router, _, ctx := newContextTestRouter(t, slog.New(slog.DiscardHandler))
	producerFinished := make(chan struct{})
	observations := make(chan messageContextObservation, 1)
	require.NoError(t, router.Subscribe("independent", func(ctx context.Context, payload []byte) error {
		<-producerFinished
		observations <- observeMessageContext(ctx)
		return nil
	}))
	startRouter(t, router, ctx)
	producer, cancel := context.WithCancel(tools.SetRequestIDToContext(t.Context(), "request-42"))
	defer cancel()
	require.NoError(t, router.Publish(producer, "independent", []byte("event")))
	cancel()
	close(producerFinished)
	select {
	case observation := <-observations:
		assert.NoError(t, observation.err)
		assert.Equal(t, "request-42", observation.requestID)
	case <-time.After(routerTestTimeout):
		t.Fatal("subscriber did not process the event")
	}
}

func TestSubscriberAcceptsMessagesWithoutPropagation(t *testing.T) {
	router, backend, ctx := newContextTestRouter(t, slog.New(slog.DiscardHandler))
	observations := make(chan messageContextObservation, 1)
	require.NoError(t, router.Subscribe("legacy", func(ctx context.Context, payload []byte) error {
		observations <- observeMessageContext(ctx)
		return nil
	}))
	startRouter(t, router, ctx)
	require.NoError(t, backend.Publisher().Publish("legacy", message.NewMessage("legacy-id", []byte("event"))))
	select {
	case observation := <-observations:
		assert.Empty(t, observation.requestID)
		assert.NoError(t, observation.err)
		assert.Contains(t, observation.attrs, slog.String("message_uuid", "legacy-id"))
		assert.Contains(t, observation.attrs, slog.String("topic", "legacy"))
	case <-time.After(routerTestTimeout):
		t.Fatal("subscriber did not process the event")
	}
}

func TestConcurrentSubscriberRetriesKeepTheirOwnFields(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(tools.SlogContextWrapper(slog.NewJSONHandler(&output, nil)))
	router, _, ctx := newContextTestRouter(t, logger)
	var attempts sync.Map
	processed := make(chan messageContextObservation, 16)
	require.NoError(t, router.Subscribe("concurrent", func(ctx context.Context, payload []byte) error {
		if _, loaded := attempts.LoadOrStore(string(payload), true); !loaded {
			return errors.New("retry once")
		}
		observation := observeMessageContext(ctx)
		if observation.requestID != string(payload) {
			observation.err = errors.New("request ID differs from payload")
		}
		processed <- observation
		return nil
	}))
	startRouter(t, router, ctx)
	for i := range 16 {
		id := fmt.Sprintf("request-%d", i)
		require.NoError(t, router.Publish(tools.SetRequestIDToContext(t.Context(), id), "concurrent", []byte(id)))
	}
	for range 16 {
		select {
		case observation := <-processed:
			assert.NoError(t, observation.err)
		case <-time.After(routerTestTimeout):
			t.Fatal("subscriber did not finish retries")
		}
	}
	require.NoError(t, router.Shutdown(t.Context()))
	_, open := waitForRouterError(t, router.Errors())
	require.False(t, open)
	requests := make(map[string]bool)
	for _, entry := range readEventLogs(t, &output) {
		if entry["msg"] == "Error occurred, retrying" {
			id, ok := entry["request_id"].(string)
			require.True(t, ok)
			assert.NotEmpty(t, entry["message_uuid"])
			requests[id] = true
		}
	}
	assert.Len(t, requests, 16)
}

func newContextTestRouter(t *testing.T, logger *slog.Logger) (*events.Router, *gochan.GoChan, context.Context) {
	t.Helper()
	backend := gochan.New()
	router, err := events.NewRouter(backend, events.DeliveryPolicy{
		MaxRetries: 2, InitialBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond,
		DeadLetterTopicSuffix: routerDLQSuffix, CloseTimeout: time.Second,
	}, events.WithLogger(logger))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	registerRouterCleanup(t, router, cancel)
	return router, backend, ctx
}

func observeMessageContext(ctx context.Context) messageContextObservation {
	return messageContextObservation{
		requestID: tools.GetRequestIDFromContext(ctx), span: trace.SpanContextFromContext(ctx),
		err: ctx.Err(), attrs: tools.LogAttrsFromContext(ctx),
	}
}

func readEventLogs(t *testing.T, output io.Reader) []map[string]any {
	t.Helper()
	var entries []map[string]any
	decoder := json.NewDecoder(output)
	for {
		var entry map[string]any
		err := decoder.Decode(&entry)
		if errors.Is(err, io.EOF) {
			return entries
		}
		require.NoError(t, err)
		entries = append(entries, entry)
	}
}

func installEventSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		require.NoError(t, provider.Shutdown(context.Background()))
	})
	return recorder
}
