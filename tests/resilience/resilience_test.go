//go:build resilience

package resilience

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"

	"github.com/go42-dev/go42/internal/cache/memcached"
	"github.com/go42-dev/go42/internal/cache/redis"
	"github.com/go42-dev/go42/internal/database/mysql"
	"github.com/go42-dev/go42/internal/database/pgsql"
	kafkaengine "github.com/go42-dev/go42/internal/events/kafka"
	natsengine "github.com/go42-dev/go42/internal/events/nats"
	rabbitmqengine "github.com/go42-dev/go42/internal/events/rabbitmq"
)

const (
	defaultToxiproxyAPIAddress = "http://127.0.0.1:8474"
	defaultPostgresAddress     = "127.0.0.1:15432"
	defaultMySQLAddress        = "127.0.0.1:13306"
	defaultRedisAddress        = "127.0.0.1:16379"
	defaultMemcachedAddress    = "127.0.0.1:11212"
	defaultNATSAddress         = "nats://127.0.0.1:14222"
	defaultKafkaAddress        = "127.0.0.1:19092"
	defaultRabbitMQAddress     = "amqp://guest:guest@127.0.0.1:15673/"

	toxiproxyAPIAddressEnv = "TOXIPROXY_API_ADDRESS"
	postgresAddressEnv     = "RESILIENCE_POSTGRES_ADDRESS"
	mysqlAddressEnv        = "RESILIENCE_MYSQL_ADDRESS"
	redisAddressEnv        = "RESILIENCE_REDIS_ADDRESS"
	memcachedAddressEnv    = "RESILIENCE_MEMCACHED_ADDRESS"
	natsAddressEnv         = "RESILIENCE_NATS_ADDRESS"
	kafkaAddressEnv        = "RESILIENCE_KAFKA_ADDRESS"
	rabbitmqAddressEnv     = "RESILIENCE_RABBITMQ_ADDRESS"
)

const (
	postgresProxyName  = "postgres"
	mysqlProxyName     = "mysql"
	redisProxyName     = "redis"
	memcachedProxyName = "memcached"
	natsProxyName      = "nats"
	kafkaProxyName     = "kafka"
	rabbitmqProxyName  = "rabbitmq"
)

type proxyConfig struct {
	Name     string `json:"name"`
	Listen   string `json:"listen"`
	Upstream string `json:"upstream"`
	Enabled  bool   `json:"enabled"`
}

type proxyUpdate struct {
	Enabled bool `json:"enabled"`
}

type toxicConfig struct {
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Stream     string         `json:"stream"`
	Toxicity   float64        `json:"toxicity"`
	Attributes map[string]any `json:"attributes"`
}

func TestPostgresReconnectsAfterNetworkInterruption(t *testing.T) {
	resetProxy(t, proxyConfig{
		Name:     postgresProxyName,
		Listen:   "0.0.0.0:15432",
		Upstream: "pgsql:5432",
		Enabled:  true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dsn := fmt.Sprintf(
		"postgres://user:qwerty@%s/go42?sslmode=disable",
		envOrDefault(postgresAddressEnv, defaultPostgresAddress),
	)
	database, err := pgsql.Open(
		ctx,
		dsn,
		"",
		pgsql.WithLogger(slog.New(slog.DiscardHandler)),
		pgsql.WithConnectRetryTimeout(15*time.Second),
		pgsql.WithConnectRetryBackoff(100*time.Millisecond, time.Second),
		pgsql.WithMaxIdleConns(1),
	)
	if err != nil {
		t.Fatalf("open PostgreSQL through Toxiproxy: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = database.Shutdown(shutdownCtx)
	})

	assertPingSucceeds(t, database.Ping)
	setProxyEnabled(t, postgresProxyName, false)
	assertPingFails(t, database.Ping)
	setProxyEnabled(t, postgresProxyName, true)
	assertPingSucceeds(t, database.Ping)
}

func TestMySQLReconnectsAfterNetworkInterruption(t *testing.T) {
	resetProxy(t, proxyConfig{
		Name:     mysqlProxyName,
		Listen:   "0.0.0.0:13306",
		Upstream: "mysql:3306",
		Enabled:  true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dsn := fmt.Sprintf(
		"user:qwerty@tcp(%s)/go42?parseTime=true",
		envOrDefault(mysqlAddressEnv, defaultMySQLAddress),
	)
	database, err := mysql.Open(
		ctx,
		dsn,
		"",
		mysql.WithLogger(slog.New(slog.DiscardHandler)),
		mysql.WithConnectRetryTimeout(15*time.Second),
		mysql.WithConnectRetryBackoff(100*time.Millisecond, time.Second),
		mysql.WithMaxIdleConns(1),
	)
	if err != nil {
		t.Fatalf("open MySQL through Toxiproxy: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = database.Shutdown(shutdownCtx)
	})

	assertPingSucceeds(t, database.Ping)
	setProxyEnabled(t, mysqlProxyName, false)
	assertPingFails(t, database.Ping)
	setProxyEnabled(t, mysqlProxyName, true)
	assertPingSucceeds(t, database.Ping)
}

func TestRedisReconnectsAfterNetworkInterruption(t *testing.T) {
	resetProxy(t, proxyConfig{
		Name:     redisProxyName,
		Listen:   "0.0.0.0:16379",
		Upstream: "redis:6379",
		Enabled:  true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cache, err := redis.Open(
		ctx,
		envOrDefault(redisAddressEnv, defaultRedisAddress),
		0,
		redis.WithLogger(slog.New(slog.DiscardHandler)),
		redis.WithConnectRetryTimeout(15*time.Second),
		redis.WithConnectRetryBackoff(100*time.Millisecond, time.Second),
		redis.WithDialTimeout(time.Second),
		redis.WithReadTimeout(time.Second),
		redis.WithWriteTimeout(time.Second),
	)
	if err != nil {
		t.Fatalf("open Redis through Toxiproxy: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = cache.Shutdown(shutdownCtx)
	})

	assertPingSucceeds(t, cache.Ping)
	setProxyEnabled(t, redisProxyName, false)
	assertPingFails(t, cache.Ping)
	setProxyEnabled(t, redisProxyName, true)
	assertPingSucceeds(t, cache.Ping)
}

func TestMemcachedReconnectsAfterNetworkInterruption(t *testing.T) {
	resetProxy(t, proxyConfig{
		Name:     memcachedProxyName,
		Listen:   "0.0.0.0:11212",
		Upstream: "memcached:11211",
		Enabled:  true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cache, err := memcached.Open(
		ctx,
		[]string{envOrDefault(memcachedAddressEnv, defaultMemcachedAddress)},
		memcached.WithLogger(slog.New(slog.DiscardHandler)),
		memcached.WithConnectRetryTimeout(15*time.Second),
		memcached.WithConnectRetryBackoff(100*time.Millisecond, time.Second),
		memcached.WithTimeout(time.Second),
		memcached.WithMaxIdleConns(1),
	)
	if err != nil {
		t.Fatalf("open Memcached through Toxiproxy: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = cache.Shutdown(shutdownCtx)
	})

	assertPingSucceeds(t, cache.Ping)
	setProxyEnabled(t, memcachedProxyName, false)
	assertPingFails(t, cache.Ping)
	setProxyEnabled(t, memcachedProxyName, true)
	assertPingSucceeds(t, cache.Ping)
}

func TestNATSReconnectsAfterNetworkInterruption(t *testing.T) {
	resetProxy(t, proxyConfig{
		Name:     natsProxyName,
		Listen:   "0.0.0.0:14222",
		Upstream: "nats:4222",
		Enabled:  true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	backend, err := natsengine.New(
		ctx,
		envOrDefault(natsAddressEnv, defaultNATSAddress),
		natsengine.WithJetStreamAutoProvision(true),
		natsengine.WithLogger(slog.New(slog.DiscardHandler)),
		natsengine.WithConnectTimeout(time.Second),
		natsengine.WithConnectRetryTimeout(15*time.Second),
		natsengine.WithConnectRetryBackoff(100*time.Millisecond, time.Second),
		natsengine.WithMaxReconnects(-1),
		natsengine.WithReconnectDelay(100*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("open NATS through Toxiproxy: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = backend.Shutdown(shutdownCtx)
	})

	publish := func() error {
		return backend.Publisher().Publish(
			"resilience_events",
			message.NewMessage(watermill.NewUUID(), []byte("resilience check")),
		)
	}
	assertEventuallySucceeds(t, "publish to NATS", publish)
	setProxyEnabled(t, natsProxyName, false)
	assertEventuallyFails(t, "publish to interrupted NATS", publish)
	setProxyEnabled(t, natsProxyName, true)
	assertEventuallySucceeds(t, "publish to recovered NATS", publish)
}

func TestKafkaReconnectsAfterNetworkInterruption(t *testing.T) {
	resetProxy(t, proxyConfig{
		Name:     kafkaProxyName,
		Listen:   "0.0.0.0:19092",
		Upstream: "kafka:19093",
		Enabled:  true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	backend, err := kafkaengine.New(
		ctx,
		[]string{envOrDefault(kafkaAddressEnv, defaultKafkaAddress)},
		"resilience",
		kafkaengine.WithTopicAutoCreation(true),
		kafkaengine.WithLogger(slog.New(slog.DiscardHandler)),
		kafkaengine.WithConnectRetryTimeout(15*time.Second),
		kafkaengine.WithConnectRetryBackoff(100*time.Millisecond, time.Second),
		kafkaengine.WithDialTimeout(time.Second),
		kafkaengine.WithReadTimeout(time.Second),
		kafkaengine.WithWriteTimeout(time.Second),
		kafkaengine.WithProducerRetryBackoff(100*time.Millisecond),
		kafkaengine.WithMetadataRefreshFrequency(100*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("open Kafka through Toxiproxy: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = backend.Shutdown(shutdownCtx)
	})

	publish := func() error {
		return backend.Publisher().Publish(
			"resilience_events",
			message.NewMessage(watermill.NewUUID(), []byte("resilience check")),
		)
	}
	assertEventuallySucceeds(t, "publish to Kafka", publish)
	setProxyEnabled(t, kafkaProxyName, false)
	assertEventuallyFails(t, "publish to interrupted Kafka", publish)
	setProxyEnabled(t, kafkaProxyName, true)
	assertEventuallySucceeds(t, "publish to recovered Kafka", publish)
}

func TestRabbitMQReconnectsAfterNetworkInterruption(t *testing.T) {
	resetProxy(t, proxyConfig{
		Name:     rabbitmqProxyName,
		Listen:   "0.0.0.0:15673",
		Upstream: "rabbitmq:5672",
		Enabled:  true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	backend, err := rabbitmqengine.New(
		ctx,
		envOrDefault(rabbitmqAddressEnv, defaultRabbitMQAddress),
		"resilience",
		rabbitmqengine.WithAutoProvision(true),
		rabbitmqengine.WithLogger(slog.New(slog.DiscardHandler)),
		rabbitmqengine.WithConnectRetryTimeout(15*time.Second),
		rabbitmqengine.WithConnectRetryBackoff(100*time.Millisecond, time.Second),
		rabbitmqengine.WithReconnectBackoffInitialInterval(100*time.Millisecond),
		rabbitmqengine.WithReconnectBackoffMultiplier(1.2),
		rabbitmqengine.WithReconnectBackoffMaxInterval(time.Second),
	)
	if err != nil {
		t.Fatalf("open RabbitMQ through Toxiproxy: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = backend.Shutdown(shutdownCtx)
	})

	if err := backend.InitializeTopic("resilience_events"); err != nil {
		t.Fatalf("initialize RabbitMQ route: %v", err)
	}

	publish := func() error {
		return backend.Publisher().Publish(
			"resilience_events",
			message.NewMessage(watermill.NewUUID(), []byte("resilience check")),
		)
	}
	assertEventuallySucceeds(t, "publish to RabbitMQ", publish)
	setProxyEnabled(t, rabbitmqProxyName, false)
	assertEventuallyFails(t, "publish to interrupted RabbitMQ", publish)
	setProxyEnabled(t, rabbitmqProxyName, true)
	assertEventuallySucceeds(t, "publish to recovered RabbitMQ", publish)
}

func resetProxy(t *testing.T, cfg proxyConfig) {
	t.Helper()
	waitForToxiproxy(t)

	status, body, err := toxiproxyRequest(t.Context(), http.MethodDelete, "/proxies/"+cfg.Name, nil)
	if err != nil {
		t.Fatalf("delete Toxiproxy proxy %q: %v", cfg.Name, err)
	}
	if status != http.StatusNoContent && status != http.StatusNotFound {
		t.Fatalf("delete Toxiproxy proxy %q: status %d: %s", cfg.Name, status, body)
	}

	status, body, err = toxiproxyRequest(t.Context(), http.MethodPost, "/proxies", cfg)
	if err != nil {
		t.Fatalf("create Toxiproxy proxy %q: %v", cfg.Name, err)
	}
	if status != http.StatusCreated {
		t.Fatalf("create Toxiproxy proxy %q: status %d: %s", cfg.Name, status, body)
	}

	t.Cleanup(func() {
		_, _, _ = toxiproxyRequest(context.Background(), http.MethodPost, "/proxies/"+cfg.Name, proxyUpdate{
			Enabled: true,
		})
	})
}

func setProxyEnabled(t *testing.T, name string, enabled bool) {
	t.Helper()
	status, body, err := toxiproxyRequest(t.Context(), http.MethodPost, "/proxies/"+name, proxyUpdate{
		Enabled: enabled,
	})
	if err != nil {
		t.Fatalf("set Toxiproxy proxy %q enabled=%t: %v", name, enabled, err)
	}
	if status != http.StatusOK {
		t.Fatalf("set Toxiproxy proxy %q enabled=%t: status %d: %s", name, enabled, status, body)
	}
}

func addToxic(t *testing.T, proxyName string, toxic toxicConfig) {
	t.Helper()
	status, body, err := toxiproxyRequest(
		t.Context(),
		http.MethodPost,
		"/proxies/"+proxyName+"/toxics",
		toxic,
	)
	if err != nil {
		t.Fatalf("add Toxiproxy toxic %q: %v", toxic.Name, err)
	}
	if status != http.StatusOK {
		t.Fatalf("add Toxiproxy toxic %q: status %d: %s", toxic.Name, status, body)
	}
	t.Cleanup(func() {
		_, _, _ = toxiproxyRequest(
			context.Background(),
			http.MethodDelete,
			"/proxies/"+proxyName+"/toxics/"+toxic.Name,
			nil,
		)
	})
}

func removeToxic(t *testing.T, proxyName string, toxicName string) {
	t.Helper()
	status, body, err := toxiproxyRequest(
		t.Context(),
		http.MethodDelete,
		"/proxies/"+proxyName+"/toxics/"+toxicName,
		nil,
	)
	if err != nil {
		t.Fatalf("remove Toxiproxy toxic %q: %v", toxicName, err)
	}
	if status != http.StatusNoContent {
		t.Fatalf("remove Toxiproxy toxic %q: status %d: %s", toxicName, status, body)
	}
}

func waitForToxiproxy(t *testing.T) {
	t.Helper()
	assertEventuallySucceeds(t, "connect to Toxiproxy API", func() error {
		status, body, err := toxiproxyRequest(t.Context(), http.MethodGet, "/version", nil)
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("status %d: %s", status, body)
		}
		return nil
	})
}

func toxiproxyRequest(
	ctx context.Context,
	method string,
	path string,
	payload any,
) (int, string, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, "", fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx,
		method,
		strings.TrimRight(envOrDefault(toxiproxyAPIAddressEnv, defaultToxiproxyAPIAddress), "/")+path,
		body,
	)
	if err != nil {
		return 0, "", fmt.Errorf("create request: %w", err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = response.Body.Close() }()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, "", fmt.Errorf("read response: %w", err)
	}
	return response.StatusCode, strings.TrimSpace(string(responseBody)), nil
}

func assertPingSucceeds(t *testing.T, ping func(context.Context) error) {
	t.Helper()
	assertEventuallySucceeds(t, "ping dependency", func() error {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		return ping(ctx)
	})
}

func assertPingFails(t *testing.T, ping func(context.Context) error) {
	t.Helper()
	assertEventuallyFails(t, "ping interrupted dependency", func() error {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		return ping(ctx)
	})
}

func assertOperationSucceeds(t *testing.T, operation func(context.Context) error) {
	t.Helper()
	assertEventuallySucceeds(t, "complete dependency operation", func() error {
		ctx, cancel := context.WithTimeout(t.Context(), dependencyOperationTimeout)
		defer cancel()
		return operation(ctx)
	})
}

func assertEventuallySucceeds(t *testing.T, description string, operation func() error) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := operation(); err == nil {
			return
		} else {
			lastErr = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting to %s: %v", description, lastErr)
}

func assertEventuallyFails(t *testing.T, description string, operation func() error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := operation(); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting to %s", description)
}

func envOrDefault(name string, defaultValue string) string {
	value, found := os.LookupEnv(name)
	if found {
		return value
	}
	return defaultValue
}
