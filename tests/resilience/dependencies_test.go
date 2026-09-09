//go:build resilience

package resilience

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"

	"github.com/go42-dev/go42/internal/cache/memcached"
	"github.com/go42-dev/go42/internal/cache/redis"
	"github.com/go42-dev/go42/internal/database/mysql"
	"github.com/go42-dev/go42/internal/database/pgsql"
	"github.com/go42-dev/go42/internal/events"
	kafkaengine "github.com/go42-dev/go42/internal/events/kafka"
	natsengine "github.com/go42-dev/go42/internal/events/nats"
	rabbitmqengine "github.com/go42-dev/go42/internal/events/rabbitmq"
)

const dependencyOperationTimeout = time.Second

type resilienceClient struct {
	operation func(context.Context) error
	shutdown  func(context.Context) error
}

type dependencyFactory struct {
	name  string
	proxy proxyConfig
	open  func(context.Context, time.Duration) (*resilienceClient, error)
}

type brokerFactory struct {
	name  string
	proxy proxyConfig
	open  func(context.Context, time.Duration) (events.Backend, error)
}

func dataDependencyFactories() []dependencyFactory {
	return []dependencyFactory{
		{
			name: "PostgreSQL",
			proxy: proxyConfig{
				Name:     postgresProxyName,
				Listen:   "0.0.0.0:15432",
				Upstream: "pgsql:5432",
				Enabled:  true,
			},
			open: openPostgresClient,
		},
		{
			name: "MySQL",
			proxy: proxyConfig{
				Name:     mysqlProxyName,
				Listen:   "0.0.0.0:13306",
				Upstream: "mysql:3306",
				Enabled:  true,
			},
			open: openMySQLClient,
		},
		{
			name: "Redis",
			proxy: proxyConfig{
				Name:     redisProxyName,
				Listen:   "0.0.0.0:16379",
				Upstream: "redis:6379",
				Enabled:  true,
			},
			open: openRedisClient,
		},
		{
			name: "Memcached",
			proxy: proxyConfig{
				Name:     memcachedProxyName,
				Listen:   "0.0.0.0:11212",
				Upstream: "memcached:11211",
				Enabled:  true,
			},
			open: openMemcachedClient,
		},
	}
}

func brokerDependencyFactories() []brokerFactory {
	return []brokerFactory{
		{
			name: "NATS",
			proxy: proxyConfig{
				Name:     natsProxyName,
				Listen:   "0.0.0.0:14222",
				Upstream: "nats:4222",
				Enabled:  true,
			},
			open: openNATSBackend,
		},
		{
			name: "Kafka",
			proxy: proxyConfig{
				Name:     kafkaProxyName,
				Listen:   "0.0.0.0:19092",
				Upstream: "kafka:19093",
				Enabled:  true,
			},
			open: openKafkaBackend,
		},
		{
			name: "RabbitMQ",
			proxy: proxyConfig{
				Name:     rabbitmqProxyName,
				Listen:   "0.0.0.0:15673",
				Upstream: "rabbitmq:5672",
				Enabled:  true,
			},
			open: openRabbitMQBackend,
		},
	}
}

func allDependencyFactories() []dependencyFactory {
	factories := dataDependencyFactories()
	for _, broker := range brokerDependencyFactories() {
		broker := broker
		factories = append(factories, dependencyFactory{
			name:  broker.name,
			proxy: broker.proxy,
			open: func(ctx context.Context, retryTimeout time.Duration) (*resilienceClient, error) {
				backend, err := broker.open(ctx, retryTimeout)
				if err != nil {
					return nil, err
				}
				topic := uniqueTopic("startup")
				return &resilienceClient{
					operation: func(ctx context.Context) error {
						if initializer, ok := backend.(events.TopicInitializer); ok {
							if err := initializer.InitializeTopic(topic); err != nil {
								return err
							}
						}
						msg := message.NewMessage(watermill.NewUUID(), []byte("resilience check"))
						msg.SetContext(ctx)
						return backend.Publisher().Publish(topic, msg)
					},
					shutdown: backend.Shutdown,
				}, nil
			},
		})
	}
	return factories
}

func openPostgresClient(ctx context.Context, retryTimeout time.Duration) (*resilienceClient, error) {
	dsn := fmt.Sprintf(
		"postgres://user:qwerty@%s/go42?sslmode=disable",
		envOrDefault(postgresAddressEnv, defaultPostgresAddress),
	)
	database, err := pgsql.Open(
		ctx,
		dsn,
		"",
		pgsql.WithLogger(slog.New(slog.DiscardHandler)),
		pgsql.WithConnectRetryTimeout(retryTimeout),
		pgsql.WithConnectRetryBackoff(50*time.Millisecond, 200*time.Millisecond),
		pgsql.WithMaxIdleConns(2),
		pgsql.WithMaxOpenConns(8),
	)
	if err != nil {
		return nil, err
	}
	return &resilienceClient{
		operation: func(ctx context.Context) error {
			if err := database.Ping(ctx); err != nil {
				return err
			}
			return database.Master().WithContext(ctx).Exec("select 1").Error
		},
		shutdown: database.Shutdown,
	}, nil
}

func openMySQLClient(ctx context.Context, retryTimeout time.Duration) (*resilienceClient, error) {
	dsn := fmt.Sprintf(
		"user:qwerty@tcp(%s)/go42?parseTime=true",
		envOrDefault(mysqlAddressEnv, defaultMySQLAddress),
	)
	database, err := mysql.Open(
		ctx,
		dsn,
		"",
		mysql.WithLogger(slog.New(slog.DiscardHandler)),
		mysql.WithConnectRetryTimeout(retryTimeout),
		mysql.WithConnectRetryBackoff(50*time.Millisecond, 200*time.Millisecond),
		mysql.WithMaxIdleConns(2),
		mysql.WithMaxOpenConns(8),
	)
	if err != nil {
		return nil, err
	}
	return &resilienceClient{
		operation: func(ctx context.Context) error {
			if err := database.Ping(ctx); err != nil {
				return err
			}
			return database.Master().WithContext(ctx).Exec("select 1").Error
		},
		shutdown: database.Shutdown,
	}, nil
}

func openRedisClient(ctx context.Context, retryTimeout time.Duration) (*resilienceClient, error) {
	cache, err := redis.Open(
		ctx,
		envOrDefault(redisAddressEnv, defaultRedisAddress),
		0,
		redis.WithLogger(slog.New(slog.DiscardHandler)),
		redis.WithConnectRetryTimeout(retryTimeout),
		redis.WithConnectRetryBackoff(50*time.Millisecond, 200*time.Millisecond),
		redis.WithDialTimeout(200*time.Millisecond),
		redis.WithReadTimeout(200*time.Millisecond),
		redis.WithWriteTimeout(200*time.Millisecond),
		redis.WithPoolSize(8),
	)
	if err != nil {
		return nil, err
	}
	key := "resilience:" + watermill.NewUUID()
	return &resilienceClient{
		operation: func(ctx context.Context) error {
			if err := cache.Set(ctx, key, "value", time.Minute); err != nil {
				return err
			}
			value, found, err := cache.Get(ctx, key)
			if err != nil {
				return err
			}
			if !found || value != "value" {
				return fmt.Errorf("unexpected cached value %q, found=%t", value, found)
			}
			return nil
		},
		shutdown: cache.Shutdown,
	}, nil
}

func openMemcachedClient(ctx context.Context, retryTimeout time.Duration) (*resilienceClient, error) {
	cache, err := memcached.Open(
		ctx,
		[]string{envOrDefault(memcachedAddressEnv, defaultMemcachedAddress)},
		memcached.WithLogger(slog.New(slog.DiscardHandler)),
		memcached.WithConnectRetryTimeout(retryTimeout),
		memcached.WithConnectRetryBackoff(50*time.Millisecond, 200*time.Millisecond),
		memcached.WithTimeout(200*time.Millisecond),
		memcached.WithMaxIdleConns(8),
	)
	if err != nil {
		return nil, err
	}
	key := "resilience:" + watermill.NewUUID()
	return &resilienceClient{
		operation: func(ctx context.Context) error {
			if err := cache.Ping(ctx); err != nil {
				return err
			}
			if err := cache.Set(ctx, key, "value", time.Minute); err != nil {
				return err
			}
			value, found, err := cache.Get(ctx, key)
			if err != nil {
				return err
			}
			if !found || value != "value" {
				return fmt.Errorf("unexpected cached value %q, found=%t", value, found)
			}
			return nil
		},
		shutdown: cache.Shutdown,
	}, nil
}

func openNATSBackend(ctx context.Context, retryTimeout time.Duration) (events.Backend, error) {
	return natsengine.New(
		ctx,
		envOrDefault(natsAddressEnv, defaultNATSAddress),
		natsengine.WithJetStreamAutoProvision(true),
		natsengine.WithLogger(slog.New(slog.DiscardHandler)),
		natsengine.WithConnectTimeout(200*time.Millisecond),
		natsengine.WithConnectRetryTimeout(retryTimeout),
		natsengine.WithConnectRetryBackoff(50*time.Millisecond, 200*time.Millisecond),
		natsengine.WithMaxReconnects(-1),
		natsengine.WithReconnectDelay(50*time.Millisecond),
		natsengine.WithSubGroupPrefix("resilience_"+watermill.NewUUID()),
		natsengine.WithSubTimeout(2*time.Second),
		natsengine.WithSubAckTimeout(2*time.Second),
		natsengine.WithSubCloseTimeout(2*time.Second),
	)
}

func openKafkaBackend(ctx context.Context, retryTimeout time.Duration) (events.Backend, error) {
	return kafkaengine.New(
		ctx,
		[]string{envOrDefault(kafkaAddressEnv, defaultKafkaAddress)},
		"resilience_"+watermill.NewUUID(),
		kafkaengine.WithTopicAutoCreation(true),
		kafkaengine.WithLogger(slog.New(slog.DiscardHandler)),
		kafkaengine.WithConnectRetryTimeout(retryTimeout),
		kafkaengine.WithConnectRetryBackoff(50*time.Millisecond, 200*time.Millisecond),
		kafkaengine.WithDialTimeout(200*time.Millisecond),
		kafkaengine.WithReadTimeout(500*time.Millisecond),
		kafkaengine.WithWriteTimeout(500*time.Millisecond),
		kafkaengine.WithProducerRetryBackoff(50*time.Millisecond),
		kafkaengine.WithConsumerRetryBackoff(50*time.Millisecond),
		kafkaengine.WithConsumerOffsetInitial(-2),
		kafkaengine.WithConsumerGroupSessionTimeout(10*time.Second),
		kafkaengine.WithConsumerGroupHeartbeatInterval(time.Second),
		kafkaengine.WithMetadataRefreshFrequency(100*time.Millisecond),
	)
}

func openRabbitMQBackend(ctx context.Context, retryTimeout time.Duration) (events.Backend, error) {
	return rabbitmqengine.New(
		ctx,
		envOrDefault(rabbitmqAddressEnv, defaultRabbitMQAddress),
		"resilience_"+watermill.NewUUID(),
		rabbitmqengine.WithAutoProvision(true),
		rabbitmqengine.WithLogger(slog.New(slog.DiscardHandler)),
		rabbitmqengine.WithConnectRetryTimeout(retryTimeout),
		rabbitmqengine.WithConnectRetryBackoff(50*time.Millisecond, 200*time.Millisecond),
		rabbitmqengine.WithReconnectBackoffInitialInterval(50*time.Millisecond),
		rabbitmqengine.WithReconnectBackoffMultiplier(1.2),
		rabbitmqengine.WithReconnectBackoffMaxInterval(200*time.Millisecond),
	)
}

func uniqueTopic(prefix string) string {
	return prefix + "_" + watermill.NewUUID()
}

func shutdownClient(t *testing.T, client *resilienceClient) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.shutdown(ctx); err != nil {
		t.Errorf("shutdown dependency: %v", err)
	}
}
