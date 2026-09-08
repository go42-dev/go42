//go:build resilience

package resilience

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	authDomain "github.com/go42-dev/go42/internal/auth/domain"
	authModels "github.com/go42-dev/go42/internal/auth/models"
	authRepository "github.com/go42-dev/go42/internal/auth/repository"
	authWorkers "github.com/go42-dev/go42/internal/auth/workers"
	"github.com/go42-dev/go42/internal/database"
	"github.com/go42-dev/go42/internal/database/pgsql"
	pgsqlMigrate "github.com/go42-dev/go42/internal/database/pgsql/migrate"
	"github.com/go42-dev/go42/internal/events"
	outboxDomain "github.com/go42-dev/go42/internal/outbox/domain"
	"github.com/go42-dev/go42/internal/outbox/models"
	outboxRepository "github.com/go42-dev/go42/internal/outbox/repository"
	outboxWorkers "github.com/go42-dev/go42/internal/outbox/workers"
)

const outboxPublishTimeout = 2 * time.Second

func TestOutboxRecoversAfterBrokerDisconnection(t *testing.T) {
	for _, factory := range brokerDependencyFactories() {
		t.Run(factory.name, func(t *testing.T) {
			h := newOutboxResilienceHarness(t, factory)
			entry := h.queue(t)
			h.disconnect(t)

			h.runBatch(t, 100*time.Millisecond)
			failedAttempt := h.assertMessage(t, entry, models.MessageStatusPending, 1)

			setProxyEnabled(t, factory.proxy.Name, true)
			h.waitForBroker(t)
			h.runBatch(t, outboxPublishTimeout)
			stored := h.assertMessage(t, entry, models.MessageStatusProcessed, 1)
			require.Equal(t, failedAttempt.LastError, stored.LastError)
			h.assertHistory(t, entry, 1)
		})
	}
}

func TestOutboxTimeoutRedeliveryIsIdempotent(t *testing.T) {
	for _, factory := range brokerDependencyFactories() {
		t.Run(factory.name, func(t *testing.T) {
			h := newOutboxResilienceHarness(t, factory)
			entry := h.queue(t)
			h.delayResponses(t)

			h.runBatch(t, 100*time.Millisecond)
			stored := h.assertMessage(t, entry, models.MessageStatusPending, 1)
			require.Contains(t, stored.LastError, context.DeadlineExceeded.Error())

			removeToxic(t, factory.proxy.Name, brokerLatencyToxicName)
			h.waitForBroker(t)
			h.runBatch(t, outboxPublishTimeout)
			h.assertMessage(t, entry, models.MessageStatusProcessed, 1)
			h.assertHistory(t, entry, 1)

			// A timeout does not tell us whether the broker accepted the first attempt.
			// Replay the same event ID to exercise deduplication even if that attempt was lost.
			ctx, cancel := context.WithTimeout(t.Context(), outboxPublishTimeout)
			defer cancel()
			require.NoError(t, h.router.Publish(ctx, entry.Topic, outboxEventPayload(t, entry)))
			h.assertHistory(t, entry, 2)
		})
	}
}

func TestOutboxCancellationPreservesPendingMessages(t *testing.T) {
	for _, factory := range brokerDependencyFactories() {
		t.Run(factory.name, func(t *testing.T) {
			h := newOutboxResilienceHarness(t, factory)
			entries := []models.Message{h.queue(t), h.queue(t)}
			h.delayResponses(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := h.startBatch(t, ctx, 5*time.Second)

			started := time.NewTimer(outboxPublishTimeout)
			defer started.Stop()
			for {
				select {
				case id := <-h.publishStarted:
					if id == h.warmup.ID {
						continue
					}
					require.Contains(t, []uuid.UUID{entries[0].ID, entries[1].ID}, id)
				case <-started.C:
					t.Fatal("worker did not attempt to publish the pending batch")
				}
				break
			}
			select {
			case err := <-done:
				t.Fatalf("worker exited before cancellation: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			cancel()
			require.ErrorIs(t, waitForOutboxBatch(t, done, time.Second), context.Canceled)
			for _, entry := range entries {
				h.assertMessage(t, entry, models.MessageStatusPending, 0)
			}

			removeToxic(t, factory.proxy.Name, brokerLatencyToxicName)
			h.waitForBroker(t)
			h.runBatch(t, outboxPublishTimeout)
			for _, entry := range entries {
				h.assertMessage(t, entry, models.MessageStatusProcessed, 0)
				h.assertHistory(t, entry, 1)
			}
		})
	}
}

func TestOutboxRetryExhaustionPreservesFailedMessages(t *testing.T) {
	for _, factory := range brokerDependencyFactories() {
		t.Run(factory.name, func(t *testing.T) {
			h := newOutboxResilienceHarness(t, factory)
			entry := h.queue(t)
			h.disconnect(t)

			for attempt := 1; attempt <= entry.MaxRetries; attempt++ {
				h.runBatch(t, 100*time.Millisecond)
				status := models.MessageStatusPending
				if attempt == entry.MaxRetries {
					status = models.MessageStatusFailed
				}
				h.assertMessage(t, entry, status, attempt)
			}
			failed := h.assertMessage(t, entry, models.MessageStatusFailed, entry.MaxRetries)

			setProxyEnabled(t, factory.proxy.Name, true)
			h.waitForBroker(t)
			healthy := h.queue(t)
			h.runBatch(t, outboxPublishTimeout)
			h.assertMessage(t, healthy, models.MessageStatusProcessed, 0)
			h.assertHistory(t, healthy, 1)
			stored := h.assertMessage(t, entry, models.MessageStatusFailed, entry.MaxRetries)
			require.Equal(t, failed.LastError, stored.LastError)
			h.mu.Lock()
			attempts := h.publishAttempts[entry.ID]
			h.mu.Unlock()
			require.Equal(t, entry.MaxRetries, attempts, "failed messages must not be selected again")
		})
	}
}

type outboxResilienceHarness struct {
	db             *pgsql.Postgres
	repository     *outboxRepository.Repository
	router         *events.Router
	factory        brokerFactory
	topic          string
	userID         int
	warmup         models.Message
	publishStarted chan uuid.UUID

	mu              sync.Mutex
	deliveries      map[uuid.UUID]int
	publishAttempts map[uuid.UUID]int
}

func newOutboxResilienceHarness(t *testing.T, factory brokerFactory) *outboxResilienceHarness {
	t.Helper()
	if factory.raceSensitive && raceDetectorEnabled {
		t.Skip("watermill-amqp reconnect has a known upstream data race")
	}
	db := newOutboxResilienceDatabase(t)
	base := database.NewBaseRepository(db)
	authRepo := authRepository.New(base, nil, 0)
	user := authModels.User{
		UUID:   uuid.New(),
		Email:  uuid.NewString() + "@resilience.test",
		Status: authDomain.UserStatusActive,
	}
	ctx, cancel := context.WithTimeout(t.Context(), brokerTestTimeout)
	t.Cleanup(cancel)
	require.NoError(t, authRepo.CreateUser(ctx, &user))

	resetProxy(t, factory.proxy)
	backend, err := factory.open(ctx, startupTestTimeout)
	require.NoError(t, err)
	router, err := events.NewRouter(backend,
		events.WithCloseTimeout(2*time.Second),
		events.WithMaxRetries(3),
		events.WithInitialBackoff(10*time.Millisecond),
		events.WithMaxBackoff(50*time.Millisecond),
		events.WithDeadLetterTopicSuffix("_dlq"),
	)
	if err != nil {
		shutdownBackend(t, backend)
		t.Fatalf("create outbox event router: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, router.Shutdown(ctx))
	})
	h := &outboxResilienceHarness{
		db:              db,
		repository:      outboxRepository.New(base),
		router:          router,
		factory:         factory,
		topic:           uniqueTopic("outbox"),
		userID:          user.ID,
		publishStarted:  make(chan uuid.UUID, 16),
		deliveries:      make(map[uuid.UUID]int),
		publishAttempts: make(map[uuid.UUID]int),
	}
	require.NoError(t, authWorkers.NewAuthEventSubscriber(authRepo).Subscribe(h))
	require.NoError(t, router.Start(ctx))

	// Establish the topic and subscriber before injecting faults into the worker's next batch.
	h.warmup = h.queue(t)
	h.waitForBroker(t)
	h.runBatch(t, outboxPublishTimeout)
	h.assertMessage(t, h.warmup, models.MessageStatusProcessed, 0)
	h.assertHistory(t, h.warmup, 1)
	return h
}

// Route the real auth subscriber to this case's isolated topic and count committed deliveries.
func (h *outboxResilienceHarness) Subscribe(_ string, handler func(context.Context, []byte) error) error {
	return h.router.Subscribe(h.topic, func(ctx context.Context, payload []byte) error {
		if err := handler(ctx, payload); err != nil {
			return err
		}
		var event outboxDomain.Event
		if err := json.Unmarshal(payload, &event); err != nil {
			return err
		}
		h.mu.Lock()
		h.deliveries[event.ID]++
		h.mu.Unlock()
		return nil
	})
}

// Observe attempts without replacing the router or the broker publisher.
func (h *outboxResilienceHarness) Publish(ctx context.Context, topic string, payload []byte) error {
	var event outboxDomain.Event
	if err := json.Unmarshal(payload, &event); err != nil {
		return err
	}
	h.mu.Lock()
	h.publishAttempts[event.ID]++
	h.mu.Unlock()
	select {
	case h.publishStarted <- event.ID:
	default:
	}
	return h.router.Publish(ctx, topic, payload)
}

func (h *outboxResilienceHarness) queue(t *testing.T) models.Message {
	t.Helper()
	entry := models.Message{
		ID:            uuid.New(),
		AggregateID:   h.userID,
		AggregateType: "user.updated",
		Topic:         h.topic,
		Payload:       []byte(fmt.Sprintf(`{"marker":"%s"}`, uuid.NewString())),
		CreatedAt:     time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond),
		Status:        models.MessageStatusPending,
		MaxRetries:    outboxDomain.MaxRetries,
		Metadata:      map[string]string{"correlation_id": uuid.NewString(), "source": "outbox-resilience"},
	}
	ctx, cancel := context.WithTimeout(t.Context(), dependencyOperationTimeout)
	defer cancel()
	require.NoError(t, h.repository.NewOutboxMessage(ctx, &entry))
	return entry
}

func (h *outboxResilienceHarness) disconnect(t *testing.T) {
	t.Helper()
	setProxyEnabled(t, h.factory.proxy.Name, false)
	t.Cleanup(func() {
		_, _, err := toxiproxyRequest(context.Background(), http.MethodPost,
			"/proxies/"+h.factory.proxy.Name, proxyUpdate{Enabled: true})
		require.NoError(t, err)
	})
}

func (h *outboxResilienceHarness) delayResponses(t *testing.T) {
	t.Helper()
	addToxic(t, h.factory.proxy.Name, toxicConfig{
		Name:       brokerLatencyToxicName,
		Type:       "latency",
		Stream:     "downstream",
		Toxicity:   1,
		Attributes: map[string]any{"latency": 1000, "jitter": 0},
	})
}

func (h *outboxResilienceHarness) waitForBroker(t *testing.T) {
	t.Helper()
	payload := outboxEventPayload(t, h.warmup)
	assertEventuallySucceeds(t, "publish through the healthy broker", func() error {
		ctx, cancel := context.WithTimeout(t.Context(), outboxPublishTimeout)
		defer cancel()
		return h.router.Publish(ctx, h.topic, payload)
	})
}

func (h *outboxResilienceHarness) assertMessage(
	t *testing.T, entry models.Message, status string, retries int,
) models.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), dependencyOperationTimeout)
	defer cancel()
	var stored models.Message
	require.NoError(t, h.db.Master().WithContext(ctx).First(&stored, "id = ?", entry.ID).Error)
	require.Equal(t, entry.ID, stored.ID)
	require.Equal(t, entry.AggregateID, stored.AggregateID)
	require.Equal(t, entry.AggregateType, stored.AggregateType)
	require.Equal(t, entry.Topic, stored.Topic)
	require.Equal(t, entry.Payload, stored.Payload)
	require.True(t, entry.CreatedAt.Equal(stored.CreatedAt), "creation time must survive retries")
	require.Equal(t, entry.Metadata, stored.Metadata)
	require.Equal(t, entry.MaxRetries, stored.MaxRetries)
	require.Equal(t, status, stored.Status)
	require.Equal(t, retries, stored.RetryCount)
	require.Equal(t, status == models.MessageStatusProcessed, stored.ProcessedAt.Valid)
	if retries == 0 {
		require.Empty(t, stored.LastError)
	} else {
		require.NotEmpty(t, stored.LastError)
	}
	return stored
}

func (h *outboxResilienceHarness) assertHistory(t *testing.T, entry models.Message, minDeliveries int) {
	t.Helper()
	var records []authModels.UserHistoryRecord
	assertEventuallySucceeds(t, "persist one history record for the delivered event", func() error {
		ctx, cancel := context.WithTimeout(t.Context(), dependencyOperationTimeout)
		defer cancel()
		if err := h.db.Master().WithContext(ctx).Where("id = ?", entry.ID).Find(&records).Error; err != nil {
			return err
		}
		h.mu.Lock()
		deliveries := h.deliveries[entry.ID]
		h.mu.Unlock()
		if len(records) != 1 || deliveries < minDeliveries {
			return fmt.Errorf("event %s: %d history records, %d successful deliveries (want at least %d)",
				entry.ID, len(records), deliveries, minDeliveries)
		}
		return nil
	})
	require.Equal(t, entry.AggregateID, records[0].UserID)
	require.Equal(t, entry.AggregateType, records[0].EventType)
	require.Equal(t, entry.Payload, records[0].Data)
	require.True(t, entry.CreatedAt.Equal(records[0].OccurredAt))
}

type outboxBatchRepository struct {
	*outboxRepository.Repository
	afterTransaction func(error)
}

func (r *outboxBatchRepository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	err := r.Repository.WithTransaction(ctx, fn)
	r.afterTransaction(err)
	return err
}

// Stop the public worker loop after one real transaction, so each retry can be inspected.
func (h *outboxResilienceHarness) startBatch(
	t *testing.T, ctx context.Context, publishTimeout time.Duration,
) <-chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	var completed sync.Once
	result := errors.New("outbox worker stopped before its first transaction")
	repository := &outboxBatchRepository{Repository: h.repository}
	repository.afterTransaction = func(err error) {
		completed.Do(func() {
			result = err
			cancel()
		})
	}
	worker := outboxWorkers.NewOutboxMessagePublisher(repository, h,
		outboxWorkers.OutboxMessagePublisherWithPublishTimeout(publishTimeout))
	done := make(chan error, 1)
	stopped := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Error("outbox worker did not stop during cleanup")
		}
	})
	go func() {
		defer close(stopped)
		worker.Run(ctx, time.Millisecond, 10)
		done <- result
	}()
	return done
}

func (h *outboxResilienceHarness) runBatch(t *testing.T, publishTimeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, waitForOutboxBatch(t, h.startBatch(t, ctx, publishTimeout), 5*time.Second))
}

func waitForOutboxBatch(t *testing.T, done <-chan error, timeout time.Duration) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		t.Fatalf("outbox worker did not stop within %s", timeout)
		return nil
	}
}

func outboxEventPayload(t *testing.T, entry models.Message) []byte {
	t.Helper()
	payload, err := json.Marshal(outboxDomain.Event{
		ID:            entry.ID,
		CreatedAt:     entry.CreatedAt,
		AggregateID:   entry.AggregateID,
		AggregateType: entry.AggregateType,
		Payload:       entry.Payload,
	})
	require.NoError(t, err)
	return payload
}

func newOutboxResilienceDatabase(t *testing.T) *pgsql.Postgres {
	t.Helper()
	resetProxy(t, proxyConfig{
		Name: postgresProxyName, Listen: "0.0.0.0:15432", Upstream: "pgsql:5432", Enabled: true,
	})
	ctx, cancel := context.WithTimeout(t.Context(), startupTestTimeout)
	defer cancel()
	dsn := fmt.Sprintf("postgres://user:qwerty@%s/go42?sslmode=disable",
		envOrDefault(postgresAddressEnv, defaultPostgresAddress))
	options := []pgsql.Option{
		pgsql.WithConnectRetryTimeout(startupTestTimeout),
		pgsql.WithConnectRetryBackoff(50*time.Millisecond, 200*time.Millisecond),
		pgsql.WithMaxIdleConns(2),
		pgsql.WithMaxOpenConns(4),
		pgsql.WithQueryTimeout(5 * time.Second),
	}
	admin, err := pgsql.Open(ctx, dsn, "", options...)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, admin.Shutdown(ctx))
	})
	schema := "outbox_resilience_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, admin.Master().WithContext(ctx).Exec(`CREATE SCHEMA "`+schema+`"`).Error)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, admin.Master().WithContext(ctx).Exec(`DROP SCHEMA "`+schema+`" CASCADE`).Error)
	})
	dsn += "&search_path=" + schema
	require.NoError(t, pgsqlMigrate.Migrate(ctx, dsn, filepath.Join(findRepositoryRoot(t), "migrate", "pgsql")))
	db, err := pgsql.Open(ctx, dsn, "", options...)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, db.Shutdown(ctx))
	})
	return db
}
