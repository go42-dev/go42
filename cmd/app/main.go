package main

import (
	"context"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/KimMachineGun/automemlimit/memlimit"
	vmetrics "github.com/VictoriaMetrics/metrics"
	"github.com/getsentry/sentry-go"
	sentryslog "github.com/getsentry/sentry-go/slog"
	"github.com/hasansino/etcd2cfg"
	"github.com/hasansino/vault2cfg"
	"github.com/hashicorp/vault-client-go"
	"github.com/lmittmann/tint"
	slogmulti "github.com/samber/slog-multi"
	etcdClient "go.etcd.io/etcd/client/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.12.0"
	"google.golang.org/grpc"

	grpcAPI "github.com/go42-dev/go42/internal/api/grpc"
	httpAPI "github.com/go42-dev/go42/internal/api/http"
	"github.com/go42-dev/go42/internal/auth"
	authGrpcAdapterV1 "github.com/go42-dev/go42/internal/auth/adapters/grpc/v1"
	authHttpAdapterV1 "github.com/go42-dev/go42/internal/auth/adapters/http/v1"
	authInterceptors "github.com/go42-dev/go42/internal/auth/interceptors"
	authRepositoryPkg "github.com/go42-dev/go42/internal/auth/repository"
	authWorkers "github.com/go42-dev/go42/internal/auth/workers"
	"github.com/go42-dev/go42/internal/cache"
	"github.com/go42-dev/go42/internal/cache/local"
	"github.com/go42-dev/go42/internal/cache/memcached"
	"github.com/go42-dev/go42/internal/cache/redis"
	"github.com/go42-dev/go42/internal/config"
	"github.com/go42-dev/go42/internal/database"
	"github.com/go42-dev/go42/internal/database/mysql"
	mysqlMigrate "github.com/go42-dev/go42/internal/database/mysql/migrate"
	"github.com/go42-dev/go42/internal/database/pgsql"
	pgsqlMigrate "github.com/go42-dev/go42/internal/database/pgsql/migrate"
	"github.com/go42-dev/go42/internal/database/sqlite"
	sqliteMigrate "github.com/go42-dev/go42/internal/database/sqlite/migrate"
	"github.com/go42-dev/go42/internal/events"
	"github.com/go42-dev/go42/internal/events/gochan"
	"github.com/go42-dev/go42/internal/events/kafka"
	"github.com/go42-dev/go42/internal/events/nats"
	"github.com/go42-dev/go42/internal/events/rabbitmq"
	"github.com/go42-dev/go42/internal/metrics"
	metricsAdapterV1 "github.com/go42-dev/go42/internal/metrics/adapters/http"
	"github.com/go42-dev/go42/internal/metrics/observers"
	"github.com/go42-dev/go42/internal/outbox"
	outboxRepositoryPkg "github.com/go42-dev/go42/internal/outbox/repository"
	outboxWorkers "github.com/go42-dev/go42/internal/outbox/workers"
	"github.com/go42-dev/go42/internal/tools"
)

// These variables are passed as arguments to compiler.
var (
	xBuildTag    string
	xBuildCommit string
)

func init() {
	if len(xBuildTag) == 0 {
		xBuildTag = "dev"
	}
	if len(xBuildCommit) == 0 {
		xBuildCommit = "dev"
	}
}

func main() {
	// main context of the application
	ctx, cancel := context.WithCancel(context.Background())

	// configuration
	cfg, err := config.New()
	if err != nil {
		log.Fatalf("failed to initialize config: %v\n", err)
	}

	// core systems
	loggerFactory := initLogging(ctx, cfg)
	initVault(ctx, cfg)
	etcdCloser := initEtcd(ctx, cfg)
	initLimits(ctx, cfg)
	sentryCloser := initSentry(ctx, cfg, loggerFactory)
	pprofCloser := initProfiling(ctx, cfg)
	metricsHandler := initMetrics(ctx, cfg)
	tracingCloser := initTracing(ctx, cfg)

	// database engine
	var (
		dbEngine database.Database
	)
	switch cfg.Database.Engine {
	case "sqlite":
		// run database migrations
		slog.Info("running database migrations...")
		err = sqliteMigrate.Migrate(
			ctx,
			cfg.Database.Sqlite.SqliteFile,
			cfg.Database.FullMigratePath(),
			sqlite.ConnectionOption{Key: "mode", Value: cfg.Database.Sqlite.Mode},
			sqlite.ConnectionOption{Key: "cache", Value: cfg.Database.Sqlite.CacheMode},
		)
		if err != nil {
			log.Fatalf("failed to execute migrations: %v\n", err)
		}

		// connect to database
		slog.Info("Connecting to sqlite...")
		var sqliteConnErr error
		dbEngine, sqliteConnErr = sqlite.Open(
			cfg.Database.Sqlite.SqliteFile,
			sqlite.WithMode(cfg.Database.Sqlite.Mode),
			sqlite.WithCacheMode(cfg.Database.Sqlite.CacheMode),
			sqlite.WithLogger(slog.Default().With(slog.String("component", "gorm-sqlite"))),
			sqlite.WithQueryLogging(cfg.Database.LogQueries),
		)
		if sqliteConnErr != nil {
			log.Fatalf("failed to connect to sqlite: %v\n", sqliteConnErr)
		}

		slog.Info("connected to sqlite")
	case "mysql":
		// run database migrations
		slog.Info("running database migrations...")
		err = mysqlMigrate.Migrate(
			ctx,
			cfg.Database.Mysql.Master.DSN(),
			cfg.Database.FullMigratePath(),
			mysqlMigrate.WithLogger(
				slog.Default().With(slog.String("component", "migrate-mysql")),
			),
			mysqlMigrate.WithConnectRetryTimeout(cfg.Core.StartupConnectTimeout),
			mysqlMigrate.WithConnectRetryBackoff(
				cfg.Core.StartupRetryInitialBackoff,
				cfg.Core.StartupRetryMaxBackoff,
			),
		)
		if err != nil {
			log.Fatalf("failed to execute migrations: %v\n", err)
		}

		// connect to database
		slog.Info("connecting to MySQL...")
		var mysqlConnErr error
		dbEngine, mysqlConnErr = mysql.Open(
			ctx,
			cfg.Database.Mysql.Master.DSN(),
			cfg.Database.Mysql.Slave.DSN(),
			mysql.WithLogger(slog.Default().With(slog.String("component", "gorm-mysql"))),
			mysql.WithQueryLogging(cfg.Database.LogQueries),
			mysql.WithConnMaxIdleTime(cfg.Database.Mysql.ConnMaxIdleTime),
			mysql.WithConnMaxLifetime(cfg.Database.Mysql.ConnMaxLifetime),
			mysql.WithMaxOpenConns(cfg.Database.Mysql.MaxOpenConns),
			mysql.WithMaxIdleConns(cfg.Database.Mysql.MaxIdleConns),
			mysql.WithQueryTimeout(cfg.Database.Mysql.QueryTimeout),
			mysql.WithConnectRetryTimeout(cfg.Core.StartupConnectTimeout),
			mysql.WithConnectRetryBackoff(
				cfg.Core.StartupRetryInitialBackoff,
				cfg.Core.StartupRetryMaxBackoff,
			),
		)
		if mysqlConnErr != nil {
			log.Fatalf("failed to connect to mysql: %v\n", mysqlConnErr)
		}

		slog.Info("connected to mysql")
	case "pgsql":
		// run database migrations
		slog.Info("running database migrations...")
		err = pgsqlMigrate.Migrate(
			ctx,
			cfg.Database.Pgsql.Master.DSN(),
			cfg.Database.FullMigratePath(),
			pgsqlMigrate.WithLogger(
				slog.Default().With(slog.String("component", "migrate-pgsql")),
			),
			pgsqlMigrate.WithConnectRetryTimeout(cfg.Core.StartupConnectTimeout),
			pgsqlMigrate.WithConnectRetryBackoff(
				cfg.Core.StartupRetryInitialBackoff,
				cfg.Core.StartupRetryMaxBackoff,
			),
		)
		if err != nil {
			log.Fatalf("failed to execute migrations: %v\n", err)
		}

		// connect to database
		slog.Info("connecting to PostgreSQL...")
		var pgsqlConnErr error
		dbEngine, pgsqlConnErr = pgsql.Open(
			ctx,
			cfg.Database.Pgsql.Master.DSN(),
			cfg.Database.Pgsql.Slave.DSN(),
			pgsql.WithLogger(slog.Default().With(slog.String("component", "gorm-pgsql"))),
			pgsql.WithQueryLogging(cfg.Database.LogQueries),
			pgsql.WithConnMaxIdleTime(cfg.Database.Pgsql.ConnMaxIdleTime),
			pgsql.WithConnMaxLifetime(cfg.Database.Pgsql.ConnMaxLifetime),
			pgsql.WithMaxOpenConns(cfg.Database.Pgsql.MaxOpenConns),
			pgsql.WithMaxIdleConns(cfg.Database.Pgsql.MaxIdleConns),
			pgsql.WithQueryTimeout(cfg.Database.Pgsql.QueryTimeout),
			pgsql.WithConnectRetryTimeout(cfg.Core.StartupConnectTimeout),
			pgsql.WithConnectRetryBackoff(
				cfg.Core.StartupRetryInitialBackoff,
				cfg.Core.StartupRetryMaxBackoff,
			),
		)
		if pgsqlConnErr != nil {
			log.Fatalf("failed to connect to pgsql: %v\n", pgsqlConnErr)
		}

		slog.Info("connected to pgsql")
	default:
		log.Fatalf("empty or not supported database engine: %v\n", cfg.Database.Engine)
	}

	// database metrics
	{
		masterDB, err := dbEngine.Master().DB()
		if err != nil {
			log.Fatalf("failed to retrieve master db: %v\n", err)
		}
		dbObserverMaster, err := observers.NewDatabaseObserver(
			masterDB,
			observers.WithName("gorm-master"),
		)
		if err != nil {
			log.Fatalf("failed to initialize database metrics: %v\n", err)
		}
		go dbObserverMaster.Observe(ctx)

		slaveDB, err := dbEngine.Slave().DB()
		if err != nil {
			log.Fatalf("failed to retrieve slave db: %v\n", err)
		}
		dbObserverSlave, err := observers.NewDatabaseObserver(
			slaveDB,
			observers.WithName("gorm-slave"),
		)
		if err != nil {
			log.Fatalf("failed to initialize database metrics: %v\n", err)
		}
		go dbObserverSlave.Observe(ctx)
	}

	// cache engine
	var (
		cacheEngine cache.Engine
	)
	switch cfg.Cache.Engine {
	case "local":
		cacheEngine = local.New(
			local.WithCapacity(cfg.Cache.Local.Capacity),
			local.WithMaxCostBytes(cfg.Cache.Local.MaxCostBytes),
		)
		slog.Info("local cache engine initialized")
	case "memcached":
		var err error
		cacheEngine, err = memcached.Open(
			ctx,
			cfg.Cache.Memcached.Hosts,
			memcached.WithLogger(
				slog.Default().With(slog.String("component", "cache-memcached")),
			),
			memcached.WithConnectRetryTimeout(cfg.Core.StartupConnectTimeout),
			memcached.WithConnectRetryBackoff(
				cfg.Core.StartupRetryInitialBackoff,
				cfg.Core.StartupRetryMaxBackoff,
			),
			memcached.WithTimeout(cfg.Cache.Memcached.Timeout),
			memcached.WithMaxIdleConns(cfg.Cache.Memcached.MaxIdleConns),
		)
		if err != nil {
			log.Fatalf("failed to initialize memcached cache: %v\n", err)
		}
		slog.Info("memcached cache initialized")
	case "redis":
		var err error
		cacheEngine, err = redis.Open(
			ctx,
			cfg.Cache.Redis.Host, cfg.Cache.Redis.DB,
			redis.WithLogger(slog.Default().With(slog.String("component", "cache-redis"))),
			redis.WithConnectRetryTimeout(cfg.Core.StartupConnectTimeout),
			redis.WithConnectRetryBackoff(
				cfg.Core.StartupRetryInitialBackoff,
				cfg.Core.StartupRetryMaxBackoff,
			),
			redis.WithClientName(cfg.Core.ServiceName),
			redis.WithUserName(cfg.Cache.Redis.Username),
			redis.WithPassword(cfg.Cache.Redis.Password),
			redis.WithMaxRetries(cfg.Cache.Redis.MaxRetries),
			redis.WithMinRetryBackoff(cfg.Cache.Redis.MinRetryBackoff),
			redis.WithMaxRetryBackoff(cfg.Cache.Redis.MaxRetryBackoff),
			redis.WithDialTimeout(cfg.Cache.Redis.DialTimeout),
			redis.WithReadTimeout(cfg.Cache.Redis.ReadTimeout),
			redis.WithWriteTimeout(cfg.Cache.Redis.WriteTimeout),
			redis.WithContextTimeoutEnabled(cfg.Cache.Redis.ContextTimeoutEnabled),
			redis.WithPoolSize(cfg.Cache.Redis.PoolSize),
			redis.WithPoolTimeout(cfg.Cache.Redis.PoolTimeout),
			redis.WithMinIdleConns(cfg.Cache.Redis.MinIdleConns),
			redis.WithMaxIdleConns(cfg.Cache.Redis.MaxIdleConns),
			redis.WithMaxActiveConns(cfg.Cache.Redis.MaxActiveConns),
			redis.WithConnMaxIdleTime(cfg.Cache.Redis.ConnMaxIdleTime),
			redis.WithConnMaxLifetime(cfg.Cache.Redis.ConnMaxLifetime),
		)
		if err != nil {
			log.Fatalf("failed to initialize redis cache: %v\n", err)
		}
		slog.Info("redis cache initialized")
	default:
		log.Fatalf("unknown cache engine: %s", cfg.Cache.Engine)
	}

	// events engine
	var (
		eventsBackend events.Backend
	)
	switch cfg.Events.Engine {
	case "gochan":
		eventsBackend = gochan.New(
			gochan.WithLogger(slog.Default().With(slog.String("component", "events-gochan"))),
		)
		slog.Info("gochan event engine initialized")
	case "nats":
		eventsBackend, err = nats.New(
			ctx,
			cfg.Events.NATS.DSN,
			nats.WithLogger(slog.Default().With(slog.String("component", "events-nats"))),
			nats.WithConnectRetryTimeout(cfg.Core.StartupConnectTimeout),
			nats.WithConnectRetryBackoff(
				cfg.Core.StartupRetryInitialBackoff,
				cfg.Core.StartupRetryMaxBackoff,
			),
			nats.WithClientName(cfg.Events.NATS.ClientName),
			nats.WithClientToken(cfg.Events.NATS.Token),
			nats.WithConnectTimeout(cfg.Events.NATS.ConnTimeout),
			nats.WithConnectionRetry(cfg.Events.NATS.ConnRetry),
			nats.WithMaxReconnects(cfg.Events.NATS.MaxRetry),
			nats.WithReconnectDelay(cfg.Events.NATS.RetryDelay),
			nats.WithJetStreamAutoProvision(cfg.Events.NATS.JetStream.AutoProvision),
			nats.WithSubGroupPrefix(cfg.Events.Consumer.Group),
			nats.WithSubWorkerCount(cfg.Events.NATS.Subscriber.WorkerCount),
			nats.WithSubTimeout(cfg.Events.NATS.Subscriber.Timeout),
			nats.WithSubAckTimeout(cfg.Events.NATS.Subscriber.AckTimeout),
			nats.WithSubCloseTimeout(cfg.Events.NATS.Subscriber.CloseTimeout),
		)
		if err != nil {
			log.Fatalf("failed to initialize nats event engine: %v\n", err)
		}
		slog.Info("nats event engine initialized")
	case "rabbitmq":
		eventsBackend, err = rabbitmq.New(
			ctx,
			cfg.Events.RabbitMQ.DSN,
			cfg.Events.Consumer.Group,
			rabbitmq.WithLogger(slog.Default().With(slog.String("component", "events-rabbitmq"))),
			rabbitmq.WithConnectRetryTimeout(cfg.Core.StartupConnectTimeout),
			rabbitmq.WithConnectRetryBackoff(
				cfg.Core.StartupRetryInitialBackoff,
				cfg.Core.StartupRetryMaxBackoff,
			),
			rabbitmq.WithReconnectBackoffInitialInterval(
				cfg.Events.RabbitMQ.ReconnectInitialInterval,
			),
			rabbitmq.WithReconnectBackoffMultiplier(cfg.Events.RabbitMQ.ReconnectMultiplier),
			rabbitmq.WithReconnectBackoffMaxInterval(cfg.Events.RabbitMQ.ReconnectMaxInterval),
			rabbitmq.WithPublishMandatory(cfg.Events.RabbitMQ.PublishMandatory),
			rabbitmq.WithConsumeConsumerName(cfg.Events.RabbitMQ.ConsumeConsumerName),
			rabbitmq.WithConsumeNoRequeueOnNack(cfg.Events.RabbitMQ.ConsumeNoRequeue),
			rabbitmq.WithConsumeExclusive(cfg.Events.RabbitMQ.ConsumeExclusive),
			rabbitmq.WithConsumeQosPrefetchCount(cfg.Events.RabbitMQ.ConsumePrefetchCount),
			rabbitmq.WithConsumeQosPrefetchSize(cfg.Events.RabbitMQ.ConsumePrefetchSize),
			rabbitmq.WithConsumeQosGlobal(cfg.Events.RabbitMQ.ConsumeQosGlobal),
			rabbitmq.WithMessageUUIDHeaderKey(cfg.Events.RabbitMQ.MessageUUIDHeader),
		)
		if err != nil {
			log.Fatalf("failed to initialize rabbitmq event engine: %v\n", err)
		}
		slog.Info("rabbitmq event engine initialized")
	case "kafka":
		eventsBackend, err = kafka.New(
			ctx,
			cfg.Events.Kafka.Brokers,
			cfg.Events.Consumer.Group,
			kafka.WithLogger(slog.Default().With(slog.String("component", "events-kafka"))),
			kafka.WithConnectRetryTimeout(cfg.Core.StartupConnectTimeout),
			kafka.WithConnectRetryBackoff(
				cfg.Core.StartupRetryInitialBackoff,
				cfg.Core.StartupRetryMaxBackoff,
			),
			kafka.WithClientID(cfg.Events.Kafka.ClientID),
			kafka.WithKafkaVersion(cfg.Events.Kafka.Version),
			kafka.WithDialTimeout(cfg.Events.Kafka.DialTimeout),
			kafka.WithReadTimeout(cfg.Events.Kafka.ReadTimeout),
			kafka.WithWriteTimeout(cfg.Events.Kafka.WriteTimeout),
			kafka.WithKeepAlive(cfg.Events.Kafka.KeepAlive),
			kafka.WithProducerRetryBackoff(cfg.Events.Kafka.ProducerRetryBackoff),
			kafka.WithProducerMaxMessageBytes(cfg.Events.Kafka.ProducerMaxMessageBytes),
			kafka.WithProducerCompression(cfg.Events.Kafka.ProducerCompression),
			kafka.WithConsumerRetryBackoff(cfg.Events.Kafka.ConsumerRetryBackoff),
			kafka.WithConsumerMaxWaitTime(cfg.Events.Kafka.ConsumerMaxWaitTime),
			kafka.WithConsumerMaxProcessingTime(cfg.Events.Kafka.ConsumerMaxProcessingTime),
			kafka.WithConsumerOffsetInitial(cfg.Events.Kafka.ConsumerOffsetInitial),
			kafka.WithConsumerGroupSessionTimeout(cfg.Events.Kafka.ConsumerSessionTimeout),
			kafka.WithConsumerGroupHeartbeatInterval(cfg.Events.Kafka.ConsumerHeartbeatInterval),
			kafka.WithConsumerGroupRebalanceStrategy(cfg.Events.Kafka.ConsumerRebalanceStrategy),
			kafka.WithMetadataRefreshFrequency(cfg.Events.Kafka.MetadataRefreshFrequency),
		)
		if err != nil {
			log.Fatalf("failed to initialize kafka event engine: %v\n", err)
		}
		slog.Info("kafka event engine initialized")
	default:
		eventsBackend = events.NewNoop()
		slog.Info("no event engine initialized")
	}

	eventsEngine, err := events.NewRouter(
		eventsBackend,
		events.DeliveryPolicy{
			MaxRetries:            cfg.Events.Consumer.MaxRetries,
			InitialBackoff:        cfg.Events.Consumer.InitialBackoff,
			MaxBackoff:            cfg.Events.Consumer.MaxBackoff,
			DeadLetterTopicSuffix: cfg.Events.Consumer.DeadLetterTopicSuffix,
			CloseTimeout:          cfg.Core.ShutdownComponentTimeout,
		},
		events.WithLogger(slog.Default().With(slog.String("component", "events-router"))),
	)
	if err != nil {
		log.Fatalf("failed to initialize event delivery policy: %v\n", err)
	}

	// service layer

	var (
		outboxService *outbox.Service
		authService   *auth.Service
	)
	{
		// outbox domain
		outboxLogger := slog.Default().With(slog.String("component", "outbox-service"))
		outboxRepository := outboxRepositoryPkg.New(database.NewBaseRepository(dbEngine))
		outboxService = outbox.NewService(
			outboxRepository,
			outbox.WithLogger(outboxLogger),
		)

		outboxPublisher := outboxWorkers.NewOutboxMessagePublisher(
			outboxRepository,
			eventsEngine,
			outboxWorkers.OutboxMessagePublisherWithLogger(
				slog.Default().With(slog.String("component", "outbox-publisher")),
			),
		)

		// auth domain
		authLogger := slog.Default().With(slog.String("component", "auth-service"))
		authRepository := authRepositoryPkg.New(
			database.NewBaseRepository(dbEngine),
			cacheEngine,
			cfg.Auth.Cache.Repository.Secrets,
		)
		authService = auth.NewService(
			authRepository,
			outboxService,
			cacheEngine,
			auth.WithLogger(authLogger),
			auth.WithJWTSecrets(cfg.Auth.JWT.InitialSecrets),
			auth.WithJWTAccessTokenTTL(cfg.Auth.JWT.AccessTokenTTL),
			auth.WithJWTRefreshTokenTTL(cfg.Auth.JWT.RefreshTokenTTL),
			auth.WithJWTIssuer(cfg.Auth.JWT.Issuer),
			auth.WithJWTAudience(cfg.Auth.JWT.Audience),
			auth.WithMinPasswordEntropyBits(cfg.Auth.MinPasswordEntropyBits),
			auth.WithRateLimiterEnabled(cfg.Auth.RateLimiter.Enabled),
			auth.WithLoginAccountRequests(cfg.Auth.RateLimiter.LoginAccountRequests),
			auth.WithLoginIPRequests(cfg.Auth.RateLimiter.LoginIPRequests),
			auth.WithLoginWindow(cfg.Auth.RateLimiter.LoginWindow),
			auth.WithSignupIPRequests(cfg.Auth.RateLimiter.SignupIPRequests),
			auth.WithSignupWindow(cfg.Auth.RateLimiter.SignupWindow),
			auth.WithRefreshSessionRequests(cfg.Auth.RateLimiter.RefreshSessionRequests),
			auth.WithRefreshWindow(cfg.Auth.RateLimiter.RefreshWindow),
		)

		authTokenLastUsedUpdater := authWorkers.NewTokenLastUsedUpdater(
			authRepository,
			authService,
			authWorkers.TokenLastUsedUpdaterWithLogger(
				slog.Default().With(slog.String("component", "auth-token-updater")),
			),
		)
		go authTokenLastUsedUpdater.Run(ctx, cfg.Auth.TokenUpdaterInterval)

		authSessionCleaner := authWorkers.NewSessionCleaner(
			authRepository,
			slog.Default().With(slog.String("component", "auth-session-cleaner")),
		)
		go authSessionCleaner.Run(ctx, cfg.Auth.SessionCleanupInterval)

		authEventsSubscriber := authWorkers.NewAuthEventSubscriber(
			authRepository,
			authWorkers.AuthEventSubscriberWithLogger(
				slog.Default().With(slog.String("component", "auth-events-subscriber")),
			),
		)
		err := authEventsSubscriber.Subscribe(eventsEngine)
		if err != nil {
			log.Fatalf("failed to subscribe to events: %v\n", err)
		}

		// no more subscriber registrations after this point

		if err := eventsEngine.Start(ctx); err != nil {
			log.Fatalf("failed to start event router: %v\n", err)
		}

		// register publishers

		go outboxPublisher.Run(ctx, cfg.Outbox.WorkerRunInterval, cfg.Outbox.WorkerBatchSize)
	}

	// readinessCancel used to dynamically detect failures and disable probe
	readinessCtx, readinessCancel := context.WithCancelCause(ctx)
	readinessCheck := func(ctx context.Context) error {
		if err := context.Cause(readinessCtx); err != nil {
			return err
		}
		return errors.Join(
			dbEngine.Ping(ctx),
			cacheEngine.Ping(ctx),
		)
	}

	// http server
	httpServerOpts := []httpAPI.Option{
		httpAPI.WithLogger(slog.Default().With(slog.String("component", "http-server"))),
		httpAPI.WitHealthCheckCtx(ctx),
		httpAPI.WithTracing(cfg.Tracing.Enable),
		httpAPI.WithReadTimeout(cfg.Server.HTTP.ReadTimeout),
		httpAPI.WithWriteTimeout(cfg.Server.HTTP.WriteTimeout),
		httpAPI.WithStaticRoot(cfg.Server.HTTP.StaticRoot),
		httpAPI.WithSwaggerRoot(cfg.Server.HTTP.SwaggerRoot),
		httpAPI.WithBodyLimit(cfg.Server.HTTP.BodyLimitKB * 1024),
		httpAPI.WithSwaggerDarkStyle(cfg.Server.HTTP.SwaggerDark),
		httpAPI.WithCORSAllowOrigins(cfg.Server.HTTP.CORSAllowOrigins),
		httpAPI.WithTrustedProxyCIDRs(cfg.Server.HTTP.TrustedProxyCIDRs),
		httpAPI.WithGracefulTimeout(cfg.Core.ShutdownComponentTimeout),
		httpAPI.WithReadinessCheckTimeout(cfg.Core.ReadinessCheckTimeout),
		httpAPI.WithReadinessCheck(readinessCheck),
	}

	if cfg.Server.HTTP.RateLimiter.Enabled {
		httpServerOpts = append(httpServerOpts, httpAPI.WithRateLimiter(
			cacheEngine,
			cfg.Server.HTTP.RateLimiter.Rate,
			cfg.Server.HTTP.RateLimiter.Burst,
			cfg.Server.HTTP.RateLimiter.TTL,
		))
	}

	// register http services
	httpServer := httpAPI.New(httpServerOpts...)
	httpServer.Register(metricsAdapterV1.New(metricsHandler))

	authHttpAdapter := authHttpAdapterV1.New(authService)
	httpServer.RegisterV1(authHttpAdapter)

	// run server

	go func() {
		slog.Info("starting http server...", slog.String("port", cfg.Server.HTTP.Listen))
		if err := httpServer.Start(cfg.Server.HTTP.Listen); err != nil &&
			!errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("failed to start http server: %v\n", err)
		}
	}()

	// grpc server

	grpcServerOpts := []grpcAPI.Option{
		grpcAPI.WithLogger(slog.Default().With(slog.String("component", "grpc-server"))),
		grpcAPI.WitHealthCheckCtx(ctx),
		grpcAPI.WithReadinessCheck(readinessCheck),
		grpcAPI.WithReadinessCheckTimeout(cfg.Core.ReadinessCheckTimeout),
		grpcAPI.WithReadinessCheckInterval(cfg.Core.ReadinessCheckInterval),
		grpcAPI.WithTracing(cfg.Tracing.Enable),
		grpcAPI.WithMaxRecvMsgSize(cfg.Server.GRPC.MaxRecvMsgSize),
		grpcAPI.WithMaxSendMsgSize(cfg.Server.GRPC.MaxSendMsgSize),
		grpcAPI.WithReflection(cfg.Server.GRPC.ReflectionEnabled),
	}

	if cfg.Server.GRPC.RateLimiter.Enabled {
		grpcServerOpts = append(grpcServerOpts, grpcAPI.WithRateLimiter(
			cacheEngine,
			cfg.Server.GRPC.RateLimiter.Rate,
			cfg.Server.GRPC.RateLimiter.Burst,
			cfg.Server.GRPC.RateLimiter.TTL,
		))
	}

	grpcPermissionRegistry := grpcAPI.NewPermissionRegistry()

	if cfg.Server.GRPC.AuthorisationEnabled {
		grpcServerOpts = append(grpcServerOpts,
			grpcAPI.WithUnaryInterceptor(
				grpcAPI.InterceptorPriorityAuthentication,
				authInterceptors.NewUnaryAuthInterceptor(authService)),
			grpcAPI.WithUnaryInterceptor(
				grpcAPI.InterceptorPriorityAuthentication,
				authInterceptors.NewUnaryAccessInterceptor(grpcPermissionRegistry)),
			grpcAPI.WithStreamInterceptor(
				grpcAPI.InterceptorPriorityAuthentication,
				authInterceptors.NewStreamAuthInterceptor(authService)),
			grpcAPI.WithStreamInterceptor(
				grpcAPI.InterceptorPriorityAuthentication,
				authInterceptors.NewStreamAccessInterceptor(grpcPermissionRegistry)),
		)
	}

	grpcServer := grpcAPI.New(grpcServerOpts...)

	// register grpc services

	authGrpc := authGrpcAdapterV1.New(
		authService,
		authGrpcAdapterV1.WithPermissionRegistry(grpcPermissionRegistry),
	)
	grpcServer.Register(authGrpc)

	// run server

	go func() {
		slog.Info("starting grpc server...", slog.String("port", cfg.Server.GRPC.Listen))
		if err := grpcServer.Serve(cfg.Server.GRPC.Listen); err != nil &&
			!errors.Is(err, grpc.ErrServerStopped) {
			log.Fatalf("failed to start grpc server: %v\n", err)
		}
	}()

	go watchReadiness(
		ctx, readinessCancel,
		eventsEngine.Errors(),
	)

	// entities passed into shutdown are processed in the same order
	shutdown(
		cfg,
		cancel,
		etcdCloser, pprofCloser,
		httpServer, grpcServer, eventsEngine,
		cacheEngine, dbEngine, tracingCloser,
		sentryCloser,
	)
}

func initLogging(_ context.Context, cfg *config.Config) func(...slog.Handler) *slog.Logger {
	var slogOutput io.Writer
	switch cfg.Logger.LogOutput {
	case "none":
		slogOutput = io.Discard
	case "stdout":
		slogOutput = os.Stdout
	case "stderr":
		slogOutput = os.Stderr
	case "file":
		file, err := os.OpenFile("app.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			log.Fatalf("failed to open log file: %s", err)
		}
		slogOutput = file
	}

	var slogHandler slog.Handler
	switch cfg.Logger.LogFormat {
	case "text":
		loggerOpts := &slog.HandlerOptions{
			AddSource: cfg.Logger.AddSource,
			Level:     cfg.Logger.Level(),
		}
		slogHandler = slog.NewTextHandler(slogOutput, loggerOpts)
	case "json":
		loggerOpts := &slog.HandlerOptions{
			AddSource: cfg.Logger.AddSource,
			Level:     cfg.Logger.Level(),
		}
		slogHandler = slog.NewJSONHandler(slogOutput, loggerOpts)
	case "tint":
		loggerOpts := &tint.Options{
			AddSource:  cfg.Logger.AddSource,
			Level:      cfg.Logger.Level(),
			TimeFormat: time.Kitchen,
		}
		slogHandler = tint.NewTextHandler(slogOutput, loggerOpts)
	default:
		log.Fatalf("unsupported logging format: %s", cfg.Logger.LogFormat)
	}

	hostname, _ := os.Hostname()

	loggerFactory := func(handlers ...slog.Handler) *slog.Logger {
		handlers = append([]slog.Handler{slogHandler}, handlers...)
		return slog.New(
			tools.SlogContextWrapper(
				slogmulti.Fanout(handlers...))).With(
			slog.String("service", cfg.Core.ServiceName),
			slog.String("environment", cfg.Core.Environment),
			slog.String("hostname", hostname),
			slog.String("build_tag", xBuildTag),
			slog.String("build_commit", xBuildCommit),
		)
	}

	// Any call to log.* will be redirected to slog.Error.
	// Because of that, we need to agree to use `log` package only for errors.
	slog.SetLogLoggerLevel(slog.LevelError)
	// for both 'log' and 'slog'
	slog.SetDefault(loggerFactory())

	slog.Info("logging initialized", slog.String("log_level", cfg.Logger.Level().String()))

	return loggerFactory
}

func initVault(ctx context.Context, cfg *config.Config) {
	if !cfg.Vault.Enabled {
		return
	}

	client, err := vault.New(
		vault.WithAddress(cfg.Vault.Host),
		vault.WithRequestTimeout(cfg.Vault.Timeout),
	)
	if err != nil {
		log.Fatalf("failed to initialise vault client: %v", err)
	}

	switch cfg.Vault.AuthType {
	case "token":
		err = client.SetToken(cfg.Vault.Token)
		if err != nil {
			log.Fatalf("failed to authenticate in vault: %v", err)
		}
	default:
		log.Fatalf("unknown vault auth type: %s", cfg.Vault.AuthType)
	}

	slog.Info("connected and authenticated @ vault")

	reqCtx, cancel := context.WithTimeout(ctx, cfg.Vault.Timeout)
	defer cancel()

	data, err := client.Secrets.KvV2Read(
		reqCtx, cfg.Vault.SecretPath, vault.WithMountPath(cfg.Vault.MountPath),
	)
	if err != nil {
		log.Fatalf("failed to read vault secrets: %v", err)
	}

	if err := vault2cfg.Bind(cfg, data.Data.Data); err != nil {
		log.Fatalf("failed to bind vault secrets: %v", err)
	}
}

func initEtcd(ctx context.Context, cfg *config.Config) ShutMeDown {
	if !cfg.Etcd.Enabled {
		slog.Warn("etcd is disabled")
		return nil
	}

	// Connect to etcd
	client, err := etcdClient.New(etcdClient.Config{
		Endpoints:   cfg.Etcd.Hosts,
		DialTimeout: cfg.Etcd.Timeout,
		// forces to use log.Default()
		Logger:    nil,
		LogConfig: nil,
	})
	if err != nil {
		log.Fatalf("Failed to connect to etcd: %v", err)
	}

	syncCtx, cancel := context.WithTimeout(ctx, cfg.Etcd.Timeout)
	defer cancel()

	err = client.Sync(syncCtx)
	if err != nil {
		log.Fatalf("Failed to connect to etcd: %v", err)
	}

	etcdLogger := slog.Default().With(slog.String("component", "etcd"))

	switch cfg.Etcd.Method {
	case "bind":
		err = etcd2cfg.Bind(
			cfg, client,
			etcd2cfg.WithLogger(etcdLogger),
			etcd2cfg.WithClientTimeout(cfg.Etcd.Timeout),
		)
		if err != nil {
			log.Fatalf("Failed to bind config: %v", err)
		}
	case "sync":
		err = etcd2cfg.Sync(
			ctx, cfg, client,
			etcd2cfg.WithLogger(etcdLogger),
			etcd2cfg.WithClientTimeout(cfg.Etcd.Timeout),
			etcd2cfg.WithRunInterval(cfg.Etcd.SyncInterval),
		)
		if err != nil {
			log.Fatalf("Failed to bind config: %v", err)
		}
	}

	slog.Info("connected to etcd")

	return &ShutMeDownWrap{closer: client}
}

func initLimits(_ context.Context, cfg *config.Config) {
	if cfg.Limits.AutoMemLimitEnabled {
		_, err := memlimit.Set(
			memlimit.WithLogger(slog.Default().With(slog.String("component", "memlimit"))),
			memlimit.WithRatio(cfg.Limits.MemLimitRatio),
			memlimit.WithProvider(
				memlimit.ApplyFallback(
					memlimit.FromCgroup,
					memlimit.FromSystem,
				),
			),
		)
		if err != nil {
			slog.Error("failed to set memory limits", slog.Any("error", err))
		}
	} else {
		slog.Warn("package `automemlimit` is disabled")
	}
}

func initSentry(
	ctx context.Context, cfg *config.Config, loggerFactory func(...slog.Handler) *slog.Logger,
) ShutMeDown {
	if !cfg.Sentry.Enabled {
		slog.Warn("sentry is disabled")
		return nil
	}

	hostname, _ := os.Hostname()

	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:              cfg.Sentry.DSN,
		ServerName:       hostname,
		Environment:      cfg.Core.Environment,
		Release:          xBuildTag,
		SampleRate:       cfg.Sentry.SampleRate,
		Debug:            cfg.Sentry.Debug,
		AttachStacktrace: cfg.Sentry.Stacktrace,
		Tags: map[string]string{
			"service":      cfg.Core.ServiceName,
			"hostname":     hostname,
			"build_tag":    xBuildTag,
			"build_commit": xBuildCommit,
		},
	})
	sentry.CurrentHub().BindClient(client)

	if err != nil {
		log.Fatalf("failed to initialize sentry: %s", err)
	}

	sentryHandler := sentryslog.Option{
		LogLevel:  []slog.Level{slog.LevelError},
		AddSource: cfg.Sentry.AddSource,
	}.NewSentryHandler(ctx)

	slog.SetDefault(loggerFactory(sentryHandler))
	slog.Info("sentry initialized")

	return &ShutMeDownWrap{
		fn: func(ctx context.Context) error {
			left, ok := ctx.Deadline()
			if ok {
				client.Flush(time.Until(left))
			} else {
				client.Flush(time.Second)
			}
			client.Close()
			return nil
		},
	}
}

func initProfiling(_ context.Context, cfg *config.Config) ShutMeDown {
	if !cfg.Pprof.Enabled {
		slog.Warn("pprof is disabled")
		return nil
	}

	slog.Info("starting pprof http server...", slog.String("port", cfg.Pprof.Listen))

	prefix := strings.TrimRight(cfg.Pprof.Prefix, " /")

	pprofMux := http.NewServeMux()
	pprofMux.HandleFunc(prefix+"/", pprof.Index)
	pprofMux.HandleFunc(prefix+"/cmdline", pprof.Cmdline)
	pprofMux.HandleFunc(prefix+"/profile", pprof.Profile)
	pprofMux.HandleFunc(prefix+"/symbol", pprof.Symbol)
	pprofMux.HandleFunc(prefix+"/trace", pprof.Trace)

	server := &http.Server{
		Addr:         cfg.Pprof.Listen,
		ReadTimeout:  cfg.Pprof.ReadTimeout,
		WriteTimeout: cfg.Pprof.WriteTimeout,
		Handler:      pprofMux,
		ErrorLog: slog.NewLogLogger(
			slog.Default().With(
				slog.String("component", "pprof"),
			).Handler(), slog.LevelError),
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("failed to start pprof http server", slog.Any("error", err))
		}
	}()

	return server
}

func initMetrics(_ context.Context, cfg *config.Config) http.Handler {
	hostname, _ := os.Hostname()
	metrics.RegisterGlobalLabels(map[string]interface{}{
		"service":     cfg.Core.ServiceName,
		"environment": cfg.Core.Environment,
		"hostname":    hostname,
	})
	metrics.Gauge("application_build", map[string]interface{}{
		"build_tag":    xBuildTag,
		"build_commit": xBuildCommit,
	}).Set(1)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vmetrics.WritePrometheus(w, true)
		vmetrics.WriteFDMetrics(w)
	})
}

func initTracing(ctx context.Context, cfg *config.Config) ShutMeDown {
	if !cfg.Tracing.Enable {
		slog.Warn("tracing is disabled")
		return nil
	}

	res, err := resource.New(
		ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(cfg.Core.ServiceName),
			semconv.ServiceVersionKey.String(xBuildTag),
			semconv.DeploymentEnvironmentKey.String(cfg.Core.Environment),
		),
		resource.WithOS(),
		resource.WithProcess(),
		resource.WithContainer(),
		resource.WithHost(),
	)
	if err != nil {
		log.Fatalf("failed to create resource: %v", err)
	}

	var exporter sdktrace.SpanExporter

	switch cfg.Tracing.Provider {
	case "otel":
		exporter, err = otlptrace.New(
			ctx,
			otlptracegrpc.NewClient(
				otlptracegrpc.WithEndpoint(cfg.Tracing.Otel.GrpcHost),
				otlptracegrpc.WithTimeout(cfg.Tracing.Timeout),
				otlptracegrpc.WithInsecure(),
			),
		)
		if err != nil {
			log.Fatalf("failed to create OTLP exporter: %v", err)
		}
		slog.InfoContext(
			ctx, "initialized OTLP tracing exporter",
			slog.String("endpoint", cfg.Tracing.Otel.GrpcHost))
	default:
		log.Fatalf("unsupported tracing provider: %s", cfg.Tracing.Provider)
	}

	var sampler sdktrace.Sampler
	if cfg.Tracing.SamplingRate <= 0 {
		sampler = sdktrace.NeverSample()
	} else if cfg.Tracing.SamplingRate >= 1 {
		sampler = sdktrace.AlwaysSample()
	} else {
		sampler = sdktrace.TraceIDRatioBased(cfg.Tracing.SamplingRate)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sampler),
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(cfg.Tracing.Timeout),
			sdktrace.WithMaxExportBatchSize(cfg.Tracing.MaxExportBatchSize),
			sdktrace.WithMaxQueueSize(cfg.Tracing.MaxQueueSize),
		),
		sdktrace.WithResource(res),
	)

	otel.SetTracerProvider(tp)

	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.ErrorContext(ctx, "otel error", slog.Any("error", err))
	}))

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	slog.Info("tracing initialized",
		slog.String("provider", cfg.Tracing.Provider),
		slog.Float64("sampling_rate", cfg.Tracing.SamplingRate))

	return tp
}

func watchReadiness(
	ctx context.Context, readinessCancel context.CancelCauseFunc, observables ...<-chan error,
) {
	watchCtx, stopWatching := context.WithCancel(ctx)
	defer stopWatching()

	var watchers sync.WaitGroup
	var failure sync.Once

	for _, observable := range observables {
		if observable == nil {
			continue
		}
		watchers.Go(func() {
			for {
				select {
				// shutdown signal received, stop watching (parent context canceled)
				case <-watchCtx.Done():
					return
				// error received from observable, mark unready and stop watching
				case err, ok := <-observable:
					// if observable channel is closed or context canceled, stop watching
					if !ok || watchCtx.Err() != nil {
						return
					}
					// we received genuine error from observable channel
					if err != nil {
						failure.Do(func() {
							readinessCancel(err)
							// cancel watchCtx to stop all other watchers
							stopWatching()
						})
						return
					}
				}
			}
		})
	}

	watchers.Wait()
}

func shutdown(cfg *config.Config, mainCancel context.CancelFunc, closers ...ShutMeDown) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	// wait for signal
	sig := <-sigChan
	slog.Info("Received signal, shutting down...", slog.String("signal", sig.String()))

	// allows second signal to bypass graceful shutdown and terminate application immediately
	signal.Stop(sigChan)
	close(sigChan)

	// total timeout for graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Core.ShutdownGracePeriod)
	defer cancel()

	done := make(chan struct{})
	go func(ctx context.Context) {
		// Calling cancel() on main context disables health-checks for http and grpc servers.
		mainCancel()
		time.Sleep(cfg.Core.ShutdownWaitForProbe)
		for _, c := range closers {
			if c == nil {
				continue
			}
			cmpCtx, cmpCancel := context.WithTimeout(ctx, cfg.Core.ShutdownComponentTimeout)
			// We assume that Shutdown() is blocking and final operation -
			// that is, when Shutdown() returns, all resources are released
			// or operation timed out, and we should not wait for it anymore.
			if err := c.Shutdown(cmpCtx); err != nil {
				slog.Error("shutdown error", slog.Any("error", err))
			}
			cmpCancel()
		}
		close(done)
	}(ctx)

	select {
	case <-done:
		slog.Info("shutdown completed")
	case <-ctx.Done():
		slog.Info("shutdown timed out")
	}

	// When an application receives a signal (SIGINT/SIGTERM) and catches it using a signal handler,
	// the typical and expected behavior is exiting with a non-zero status.
	// Go runtime(?) will enforce exit code 1 even if os.Exit() is called with a different code.
}

// ShutMeDown implements graceful shutdown for specific component.
type ShutMeDown interface {
	Shutdown(context.Context) error
}

// ShutMeDownWrap wraps io.Closer or plan function and implements ShutMeDown interface.
// Only one of the fields is used: closer or fn.
// If both are nil, Shutdown() returns nil.
type ShutMeDownWrap struct {
	closer io.Closer
	fn     func(ctx context.Context) error
}

// Shutdown implements graceful shutdown for specific component.
// It should be blocking and final.
func (s *ShutMeDownWrap) Shutdown(ctx context.Context) error {
	done := make(chan error, 1)
	go func() {
		if s.closer != nil {
			done <- s.closer.Close()
		} else if s.fn != nil {
			done <- s.fn(ctx)
		} else {
			done <- nil
		}
	}()
	select {
	case <-ctx.Done():
		return errors.New("timeout")
	case err := <-done:
		return err
	}
}
