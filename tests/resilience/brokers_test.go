//go:build resilience

package resilience

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	watermillNATS "github.com/ThreeDotsLabs/watermill-nats/v2/pkg/nats"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/message/router/middleware"
	"github.com/stretchr/testify/require"

	"github.com/go42-dev/go42/internal/events"
	natsengine "github.com/go42-dev/go42/internal/events/nats"
	"github.com/go42-dev/go42/internal/metrics"
	"github.com/go42-dev/go42/internal/tools"
)

const (
	brokerTestTimeout      = 30 * time.Second
	brokerLatencyToxicName = "broker_response_latency"
)

func TestBrokerSubscribersRecoverAfterNetworkInterruption(t *testing.T) {
	for _, factory := range brokerDependencyFactories() {
		if factory.raceSensitive {
			continue
		}
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			testBrokerSubscriberRecovery(t, factory)
		})
	}
}

func TestRabbitMQSubscriberRecoversAfterNetworkInterruption(t *testing.T) {
	if raceDetectorEnabled {
		t.Skip("watermill-amqp reconnect has a known upstream data race")
	}
	testBrokerSubscriberRecovery(t, rabbitMQFactory())
}

func TestBrokersRecoverFromResponseLatency(t *testing.T) {
	for _, factory := range brokerDependencyFactories() {
		if factory.raceSensitive {
			continue
		}
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			testBrokerLatencyRecovery(t, factory)
		})
	}
}

func TestRabbitMQRecoversFromResponseLatency(t *testing.T) {
	if raceDetectorEnabled {
		t.Skip("watermill-amqp reconnect has a known upstream data race")
	}
	testRabbitMQLatencyRecovery(t, rabbitMQFactory())
}

func TestBrokersShutdownWhileDisconnected(t *testing.T) {
	for _, factory := range brokerDependencyFactories() {
		if factory.raceSensitive {
			continue
		}
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			testBrokerShutdownWhileDisconnected(t, factory)
		})
	}
}

func TestRabbitMQShutdownWhileDisconnected(t *testing.T) {
	if raceDetectorEnabled {
		t.Skip("watermill-amqp reconnect has a known upstream data race")
	}
	testBrokerShutdownWhileDisconnected(t, rabbitMQFactory())
}

func TestNATSRedeliversMessagesAfterProcessingTimeout(t *testing.T) {
	resetProxy(t, proxyConfig{
		Name: natsProxyName, Listen: "0.0.0.0:14222", Upstream: "nats:4222", Enabled: true,
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	backend, err := natsengine.New(ctx, envOrDefault(natsAddressEnv, defaultNATSAddress),
		natsengine.WithSubGroupPrefix("resilience_"+watermill.NewUUID()),
		natsengine.WithSubAckTimeout(time.Second),
		func(_ *natsengine.NATS, _ *watermillNATS.PublisherConfig, subscriber *watermillNATS.SubscriberConfig) {
			// End local processing before the broker retries, exposing any automatic acknowledgement.
			subscriber.AckWaitTimeout = 100 * time.Millisecond
		},
	)
	require.NoError(t, err)
	defer shutdownBackend(t, backend)
	topic := uniqueTopic("processing_timeout")
	messages, err := backend.Subscriber().Subscribe(ctx, topic)
	require.NoError(t, err)
	entry := newBrokerConsumerMessage(ctx)
	require.NoError(t, backend.Publisher().Publish(topic, entry))
	var first *message.Message
	select {
	case first = <-messages:
		require.NotNil(t, first)
	case <-ctx.Done():
		t.Fatal("source message was not delivered")
	}
	require.Equal(t, entry.UUID, first.UUID)
	// Neither acknowledge nor reject this delivery: processing times out before completion.
	select {
	case <-first.Context().Done():
	case <-ctx.Done():
		t.Fatal("subscriber did not expire the processing context")
	}
	select {
	case retried := <-messages:
		require.NotNil(t, retried)
		retried.Ack()
		require.NotSame(t, first, retried)
		require.Equal(t, entry.UUID, retried.UUID)
		require.Equal(t, entry.Payload, retried.Payload)
		require.Equal(t, entry.Metadata, retried.Metadata)
	case <-ctx.Done():
		t.Fatal("timed-out delivery was acknowledged instead of being retried")
	}
	assertMessageRoundTrip(t, ctx, backend, messages, topic)
}

func TestBrokerConsumersRetryTransientFailures(t *testing.T) {
	for _, factory := range brokerDependencyFactories() {
		t.Run(factory.name, func(t *testing.T) {
			entry := newBrokerConsumerMessage(t.Context())
			var attempts atomic.Int32
			processed := make(chan struct{})
			complete := sync.OnceFunc(func() { close(processed) })
			h := newBrokerConsumerHarness(t, factory, func(ctx context.Context, payload []byte) error {
				if !bytes.Equal(payload, entry.Payload) ||
					tools.GetRequestIDFromContext(ctx) != entry.Metadata.Get("request_id") {
					return events.Permanent(errors.New("payload or request ID changed during delivery"))
				}
				if attempts.Add(1) < 3 {
					return errors.New("temporary handler failure")
				}
				complete()
				return nil
			})
			h.publish(t, entry)
			select {
			case <-processed:
			case <-h.ctx.Done():
				t.Fatalf("consumer did not recover after %d attempts: %v", attempts.Load(), h.ctx.Err())
			}
			h.assertHealthy(t)
			h.assertNoDeadLetters(t)
			require.EqualValues(t, 3, attempts.Load())
			require.EqualValues(t, 2, metrics.Counter("application_event_consumer_retries_total",
				map[string]any{"topic": h.topic}).Get())
			require.Zero(t, h.deadLetterCount("published"))
			require.Zero(t, h.deadLetterCount("failed"))
		})
	}
}

func TestBrokerConsumersSkipRetriesForPermanentFailures(t *testing.T) {
	for _, factory := range brokerDependencyFactories() {
		t.Run(factory.name, func(t *testing.T) {
			assertBrokerConsumerDeadLetters(t, factory, true, 1)
		})
	}
}

func TestBrokerConsumersDeadLetterAfterRetryExhaustion(t *testing.T) {
	for _, factory := range brokerDependencyFactories() {
		t.Run(factory.name, func(t *testing.T) {
			assertBrokerConsumerDeadLetters(t, factory, false, 3)
		})
	}
}

func TestBrokerConsumersRecoverDeadLetterDelivery(t *testing.T) {
	for _, factory := range brokerDependencyFactories() {
		t.Run(factory.name, func(t *testing.T) {
			entry := newBrokerConsumerMessage(t.Context())
			handlerErr := errors.New("invalid event")
			var attempts atomic.Int32
			entered := make(chan struct{})
			announce := sync.OnceFunc(func() { close(entered) })
			released := make(chan struct{})
			release := sync.OnceFunc(func() { close(released) })
			defer release()
			h := newBrokerConsumerHarness(t, factory, func(ctx context.Context, _ []byte) error {
				attempts.Add(1)
				announce()
				select {
				case <-released:
					return events.Permanent(handlerErr)
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			h.publish(t, entry)
			select {
			case <-entered:
			case <-h.ctx.Done():
				t.Fatalf("consumer did not receive the source message: %v", h.ctx.Err())
			}

			// The source message is accepted before the connection drops during dead-letter publishing.
			setProxyEnabled(t, factory.proxy.Name, false)
			t.Cleanup(func() {
				status, _, err := toxiproxyRequest(context.Background(), http.MethodPost,
					"/proxies/"+factory.proxy.Name, proxyUpdate{Enabled: true})
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, status)
			})
			release()
			assertEventuallySucceeds(t, "observe failed dead-letter publishing during the outage", func() error {
				if h.deadLetterCount("failed") == 0 {
					return errors.New("no failed dead-letter publish recorded")
				}
				return nil
			})
			require.Zero(t, h.deadLetterCount("published"))

			setProxyEnabled(t, factory.proxy.Name, true)
			// Recovery must redeliver the original message; the test does not republish it.
			h.assertDeadLetter(t, entry, handlerErr)
			require.GreaterOrEqual(t, attempts.Load(), int32(2))
			h.assertHealthy(t)
		})
	}
}

func assertBrokerConsumerDeadLetters(t *testing.T, factory brokerFactory, permanent bool, wantAttempts int32) {
	t.Helper()
	entry := newBrokerConsumerMessage(t.Context())
	handlerErr := errors.New("handler rejected event")
	var attempts atomic.Int32
	h := newBrokerConsumerHarness(t, factory, func(context.Context, []byte) error {
		attempts.Add(1)
		if permanent {
			return events.Permanent(handlerErr)
		}
		return handlerErr
	})
	h.publish(t, entry)
	h.assertDeadLetter(t, entry, handlerErr)
	h.assertHealthy(t)
	h.assertNoDeadLetters(t)
	require.Equal(t, wantAttempts, attempts.Load())
	require.EqualValues(t, wantAttempts-1, metrics.Counter("application_event_consumer_retries_total",
		map[string]any{"topic": h.topic}).Get())
	require.EqualValues(t, 1, h.deadLetterCount("published"))
	require.Zero(t, h.deadLetterCount("failed"))
}

const brokerConsumerProbePrefix = "consumer_ready_"

type brokerConsumerHarness struct {
	ctx         context.Context
	backend     events.Backend
	router      *events.Router
	topic       string
	deadTopic   string
	deadLetters <-chan *message.Message
	healthy     chan string
}

func newBrokerConsumerHarness(
	t *testing.T, factory brokerFactory, handler func(context.Context, []byte) error,
) *brokerConsumerHarness {
	t.Helper()
	if factory.raceSensitive && raceDetectorEnabled {
		t.Skip("watermill-amqp reconnect has a known upstream data race")
	}
	resetProxy(t, factory.proxy)
	ctx, cancel := context.WithTimeout(t.Context(), brokerTestTimeout)
	t.Cleanup(cancel)
	backend, err := factory.open(ctx, startupTestTimeout)
	require.NoError(t, err)
	router, err := events.NewRouter(backend,
		events.WithMaxRetries(2),
		events.WithInitialBackoff(10*time.Millisecond),
		events.WithMaxBackoff(20*time.Millisecond),
		events.WithDeadLetterTopicSuffix("_dlq"),
		events.WithCloseTimeout(2*time.Second),
	)
	if err != nil {
		shutdownBackend(t, backend)
		t.Fatalf("create broker consumer router: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, router.Shutdown(ctx))
	})
	topic := uniqueTopic("consumer")
	h := &brokerConsumerHarness{
		ctx: ctx, backend: backend, router: router, topic: topic, deadTopic: topic + "_dlq",
		healthy: make(chan string, 16),
	}
	require.NoError(t, router.Subscribe(topic, func(ctx context.Context, payload []byte) error {
		if strings.HasPrefix(string(payload), brokerConsumerProbePrefix) {
			select {
			case h.healthy <- string(payload):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return handler(ctx, payload)
	}))
	h.deadLetters, err = backend.Subscriber().Subscribe(ctx, h.deadTopic)
	require.NoError(t, err)
	require.NoError(t, router.Start(ctx))
	h.assertHealthy(t)

	// Establish the dead-letter subscription too, before any handler can fail.
	probe := brokerConsumerProbePrefix + watermill.NewUUID()
	h.publishProbe(t, h.deadTopic, probe)
	ready := h.receiveDeadLetter(t)
	ready.Ack()
	require.Equal(t, probe, string(ready.Payload))
	return h
}

func newBrokerConsumerMessage(ctx context.Context) *message.Message {
	id := watermill.NewUUID()
	entry := message.NewMessageWithContext(ctx, id, []byte(fmt.Sprintf(`{"event_id":"%s","value":"café"}`, id)))
	entry.Metadata.Set("request_id", watermill.NewUUID())
	entry.Metadata.Set("tenant", "resilience-test")
	entry.Metadata.Set("traceparent", "00-11111111111111111111111111111111-2222222222222222-01")
	entry.Metadata.Set("tracestate", "vendor=value")
	return entry
}

func (h *brokerConsumerHarness) publish(t *testing.T, entry *message.Message) {
	t.Helper()
	// Faults are injected only after this source publish has completed.
	ctx, cancel := context.WithTimeout(h.ctx, 2*time.Second)
	defer cancel()
	entry.SetContext(ctx)
	require.NoError(t, h.backend.Publisher().Publish(h.topic, entry))
}

func (h *brokerConsumerHarness) publishProbe(t *testing.T, topic, probe string) {
	t.Helper()
	assertEventuallySucceeds(t, "publish broker readiness probe", func() error {
		ctx, cancel := context.WithTimeout(h.ctx, 2*time.Second)
		defer cancel()
		return h.router.Publish(ctx, topic, []byte(probe))
	})
}

func (h *brokerConsumerHarness) assertHealthy(t *testing.T) {
	t.Helper()
	probe := brokerConsumerProbePrefix + watermill.NewUUID()
	h.publishProbe(t, h.topic, probe)
	for {
		select {
		case received := <-h.healthy:
			if received == probe {
				return
			}
		case <-h.ctx.Done():
			t.Fatalf("consumer did not process a healthy message: %v", h.ctx.Err())
		}
	}
}

func (h *brokerConsumerHarness) receiveDeadLetter(t *testing.T) *message.Message {
	t.Helper()
	select {
	case msg, open := <-h.deadLetters:
		require.True(t, open, "dead-letter subscription closed unexpectedly")
		return msg
	case <-h.ctx.Done():
		t.Fatalf("dead-letter message was not delivered: %v", h.ctx.Err())
		return nil
	}
}

func (h *brokerConsumerHarness) assertDeadLetter(t *testing.T, entry *message.Message, reason error) {
	t.Helper()
	var received *message.Message
	for {
		received = h.receiveDeadLetter(t)
		received.Ack()
		if !strings.HasPrefix(string(received.Payload), brokerConsumerProbePrefix) {
			break
		}
	}
	require.Equal(t, entry.UUID, received.UUID)
	require.Equal(t, []byte(entry.Payload), []byte(received.Payload))
	for key, value := range entry.Metadata {
		require.Equal(t, value, received.Metadata.Get(key), "dead-letter metadata %q", key)
	}
	require.Equal(t, h.topic, received.Metadata.Get(middleware.PoisonedTopicKey))
	require.Equal(t, reason.Error(), received.Metadata.Get(middleware.ReasonForPoisonedKey))
	assertEventuallySucceeds(t, "confirm dead-letter publication", func() error {
		if h.deadLetterCount("published") == 0 {
			return errors.New("dead-letter publish has not completed")
		}
		return nil
	})
}

func (h *brokerConsumerHarness) assertNoDeadLetters(t *testing.T) {
	t.Helper()
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case msg, open := <-h.deadLetters:
			require.True(t, open, "dead-letter subscription closed unexpectedly")
			msg.Ack()
			if !strings.HasPrefix(string(msg.Payload), brokerConsumerProbePrefix) {
				t.Fatalf("unexpected dead-letter message %q: %s", msg.UUID, msg.Payload)
			}
		case <-timer.C:
			return
		case <-h.ctx.Done():
			t.Fatalf("consumer stopped during dead-letter check: %v", h.ctx.Err())
		}
	}
}

func (h *brokerConsumerHarness) deadLetterCount(result string) uint64 {
	return metrics.Counter("application_event_consumer_dead_letters_total",
		map[string]any{"topic": h.deadTopic, "result": result}).Get()
}

func testBrokerSubscriberRecovery(t *testing.T, factory brokerFactory) {
	t.Helper()
	resetProxy(t, factory.proxy)
	ctx, cancel := context.WithTimeout(t.Context(), brokerTestTimeout)
	defer cancel()
	backend, err := factory.open(ctx, startupTestTimeout)
	if err != nil {
		t.Fatalf("initialize broker: %v", err)
	}
	defer shutdownBackend(t, backend)

	topic := uniqueTopic("delivery")
	assertEventuallySucceeds(t, "create broker topic", func() error {
		return publishMessage(ctx, backend, topic)
	})
	messages, err := backend.Subscriber().Subscribe(ctx, topic)
	if err != nil {
		t.Fatalf("subscribe before outage: %v", err)
	}
	assertMessageRoundTrip(t, ctx, backend, messages, topic)

	setProxyEnabled(t, factory.proxy.Name, false)
	assertEventuallyFails(t, "publish while broker is unavailable", func() error {
		return publishMessage(ctx, backend, topic)
	})
	setProxyEnabled(t, factory.proxy.Name, true)
	assertMessageRoundTrip(t, ctx, backend, messages, topic)

	errs := publishConcurrently(ctx, backend, uniqueTopic("concurrent"), concurrentOperationCount)
	for index, err := range errs {
		if err != nil {
			t.Errorf("concurrent publish %d failed after recovery: %v", index, err)
		}
	}
}

func testBrokerLatencyRecovery(t *testing.T, factory brokerFactory) {
	t.Helper()
	resetProxy(t, factory.proxy)
	ctx, cancel := context.WithTimeout(t.Context(), brokerTestTimeout)
	defer cancel()
	backend, err := factory.open(ctx, startupTestTimeout)
	if err != nil {
		t.Fatalf("initialize broker: %v", err)
	}
	defer shutdownBackend(t, backend)

	topic := uniqueTopic("latency")
	if err := publishMessage(ctx, backend, topic); err != nil {
		t.Fatalf("publish before latency injection: %v", err)
	}
	addToxic(t, factory.proxy.Name, toxicConfig{
		Name:     brokerLatencyToxicName,
		Type:     "latency",
		Stream:   "downstream",
		Toxicity: 1,
		Attributes: map[string]any{
			"latency": 6000,
			"jitter":  0,
		},
	})
	assertEventuallyFails(t, "publish while broker responses exceed timeout", func() error {
		operationCtx, operationCancel := context.WithTimeout(ctx, time.Second)
		defer operationCancel()
		return publishMessage(operationCtx, backend, topic)
	})

	removeToxic(t, factory.proxy.Name, brokerLatencyToxicName)
	assertEventuallySucceeds(t, "publish after broker latency recovers", func() error {
		return publishMessage(ctx, backend, topic)
	})
}

func testRabbitMQLatencyRecovery(t *testing.T, factory brokerFactory) {
	t.Helper()
	resetProxy(t, factory.proxy)
	ctx, cancel := context.WithTimeout(t.Context(), brokerTestTimeout)
	defer cancel()
	backend, err := factory.open(ctx, startupTestTimeout)
	if err != nil {
		t.Fatalf("initialize broker: %v", err)
	}
	defer shutdownBackend(t, backend)

	topic := uniqueTopic("latency")
	addToxic(t, factory.proxy.Name, toxicConfig{
		Name:     brokerLatencyToxicName,
		Type:     "latency",
		Stream:   "downstream",
		Toxicity: 1,
		Attributes: map[string]any{
			"latency": 500,
			"jitter":  0,
		},
	})
	operationCtx, operationCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	started := time.Now()
	_ = publishMessage(operationCtx, backend, topic)
	operationCancel()
	if elapsed := time.Since(started); elapsed < 400*time.Millisecond {
		t.Errorf("RabbitMQ response latency was not applied: publish returned after %s", elapsed)
	}

	removeToxic(t, factory.proxy.Name, brokerLatencyToxicName)
	assertEventuallySucceeds(t, "publish after RabbitMQ latency recovers", func() error {
		return publishMessage(ctx, backend, topic)
	})
}

func testBrokerShutdownWhileDisconnected(t *testing.T, factory brokerFactory) {
	t.Helper()
	resetProxy(t, factory.proxy)
	backend, err := factory.open(t.Context(), startupTestTimeout)
	if err != nil {
		t.Fatalf("initialize broker: %v", err)
	}
	topic := uniqueTopic("shutdown")
	setProxyEnabled(t, factory.proxy.Name, false)
	assertEventuallyFails(t, "observe broker outage before shutdown", func() error {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		return publishMessage(ctx, backend, topic)
	})

	shutdownCtx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	started := time.Now()
	err = backend.Shutdown(shutdownCtx)
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Errorf("shutdown exceeded its deadline: elapsed %s, error %v", elapsed, err)
	}
}

func assertMessageRoundTrip(
	t *testing.T,
	ctx context.Context,
	backend events.Backend,
	messages <-chan *message.Message,
	topic string,
) {
	t.Helper()
	wantID := watermill.NewUUID()
	msg := message.NewMessage(wantID, []byte("resilience check"))
	msg.SetContext(ctx)
	assertEventuallySucceeds(t, "publish message for round trip", func() error {
		return backend.Publisher().Publish(topic, msg)
	})

	for {
		select {
		case <-ctx.Done():
			t.Fatalf("receive message %q: %v", wantID, ctx.Err())
		case received, open := <-messages:
			if !open {
				t.Fatalf("subscriber closed before receiving message %q", wantID)
			}
			if received.UUID == wantID {
				received.Ack()
				return
			}
			received.Ack()
		}
	}
}

func publishMessage(ctx context.Context, backend events.Backend, topic string) error {
	msg := message.NewMessage(watermill.NewUUID(), []byte("resilience check"))
	msg.SetContext(ctx)
	return backend.Publisher().Publish(topic, msg)
}

func publishConcurrently(
	ctx context.Context,
	backend events.Backend,
	topic string,
	count int,
) []error {
	errs := make([]error, count)
	var waitGroup sync.WaitGroup
	waitGroup.Add(count)
	for index := range count {
		go func() {
			defer waitGroup.Done()
			operationCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			errs[index] = publishMessage(operationCtx, backend, topic)
		}()
	}
	waitGroup.Wait()
	return errs
}

func shutdownBackend(t *testing.T, backend events.Backend) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := backend.Shutdown(ctx); err != nil {
		t.Errorf("shutdown broker: %v", err)
	}
}

func rabbitMQFactory() brokerFactory {
	for _, factory := range brokerDependencyFactories() {
		if factory.name == "RabbitMQ" {
			return factory
		}
	}
	panic(fmt.Errorf("RabbitMQ resilience factory is missing"))
}
