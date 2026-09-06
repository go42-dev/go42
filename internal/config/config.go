package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/go42-dev/go42/internal/tools"
)

type Config struct {
	sync.RWMutex
	Core     Core
	Limits   Limits
	Logger   Logger
	Tracing  Tracing
	Sentry   Sentry
	Vault    Vault
	Etcd     Etcd
	Database Database
	Cache    Cache
	Events   Events
	Pprof    Pprof
	Server   Server
	Outbox   Outbox
	Auth     Auth
}

// ╭──────────────────────────────╮
// │             CORE             │
// ╰──────────────────────────────╯

type Core struct {
	ServiceName                string        `env:"SERVICE_NAME"                  default:"go42"`
	Environment                string        `env:"ENVIRONMENT"                   default:"default"`
	StartupConnectTimeout      time.Duration `env:"STARTUP_CONNECT_TIMEOUT"       default:"1m"      v:"gt=0"`
	StartupRetryInitialBackoff time.Duration `env:"STARTUP_RETRY_INITIAL_BACKOFF" default:"500ms"   v:"gt=0"`
	StartupRetryMaxBackoff     time.Duration `env:"STARTUP_RETRY_MAX_BACKOFF"     default:"5s"      v:"gt=0"`
	ReadinessCheckTimeout      time.Duration `env:"READINESS_CHECK_TIMEOUT"       default:"2s"      v:"gt=0"`
	ReadinessCheckInterval     time.Duration `env:"READINESS_CHECK_INTERVAL"      default:"5s"      v:"gt=0"`
	ShutdownGracePeriod        time.Duration `env:"SHUTDOWN_GRACE_PERIOD"         default:"10s"`
	ShutdownWaitForProbe       time.Duration `env:"SHUTDOWN_WAIT_FOR_PROBE"       default:"2s"`
	ShutdownComponentTimeout   time.Duration `env:"SHUTDOWN_COMPONENT_TIMEOUT"    default:"3s"      v:"gt=0"`
}

// ╭──────────────────────────────╮
// │            LIMITS            │
// ╰──────────────────────────────╯

type Limits struct {
	AutoMemLimitEnabled bool    `env:"AUTOMEMLIMIT_ENABLED" default:"false"`
	MemLimitRatio       float64 `env:"MEMLIMIT_RATIO"       default:"0.9"   v:"gte=0.2,lte=1.0"`
}

// ╭──────────────────────────────╮
// │            LOGGER            │
// ╰──────────────────────────────╯

type Logger struct {
	LogLevel  string `env:"LOG_LEVEL"      default:"info"   v:"oneof=debug info warn error"`
	LogOutput string `env:"LOG_OUTPUT"     default:"stdout" v:"oneof=none stdout stderr file"`
	LogFormat string `env:"LOG_FORMAT"     default:"json"   v:"oneof=json text tint"`
	AddSource bool   `env:"LOG_ADD_SOURCE" default:"true"`
}

func (l *Logger) Level() slog.Level {
	logLevel := strings.ToLower(l.LogLevel)

	modifierIndex := -1
	modifierSign := 0

	if idx := strings.IndexByte(logLevel, '+'); idx != -1 {
		modifierIndex = idx
		modifierSign = 1
	} else if idx := strings.IndexByte(logLevel, '-'); idx != -1 {
		modifierIndex = idx
		modifierSign = -1
	}

	var baseLevel slog.Level
	var extraLevel int

	if modifierIndex != -1 {
		if val, err := strconv.Atoi(logLevel[modifierIndex+1:]); err == nil {
			extraLevel = val
		}
		logLevel = logLevel[:modifierIndex]
	}

	// Get base log level
	switch logLevel {
	case "debug":
		baseLevel = slog.LevelDebug
	case "info":
		baseLevel = slog.LevelInfo
	case "warn":
		baseLevel = slog.LevelWarn
	case "error":
		baseLevel = slog.LevelError
	default:
		return slog.LevelInfo
	}

	if modifierSign != 0 {
		return baseLevel + slog.Level(modifierSign*extraLevel)
	}

	return baseLevel
}

// ╭──────────────────────────────╮
// │           TRACING            │
// ╰──────────────────────────────╯

type Tracing struct {
	Enable             bool          `env:"TRACING_ENABLED"               default:"false"`
	Provider           string        `env:"TRACING_PROVIDER"              default:"otel"  v:"oneof=otel"`
	SamplingRate       float64       `env:"TRACING_SAMPLING_RATE"         default:"1.0"   v:"gte=0.0,lte=1.0"`
	Timeout            time.Duration `env:"TRACING_TIMEOUT"               default:"5s"`
	MaxExportBatchSize int           `env:"TRACING_MAX_EXPORT_BATCH_SIZE" default:"100"`
	MaxQueueSize       int           `env:"TRACING_MAX_QUEUE_SIZE"        default:"1000"`
	Otel               Otel
}

type Otel struct {
	GrpcHost string `env:"TRACING_OTEL_GRPC_HOST" default:"localhost:4317"`
}

// ╭──────────────────────────────╮
// │            SENTRY            │
// ╰──────────────────────────────╯

type Sentry struct {
	Enabled    bool    `env:"SENTRY_ENABLED"     default:"false"`
	DSN        string  `env:"SENTRY_DSN"         default:""`
	Debug      bool    `env:"SENTRY_DEBUG"       default:"false"`
	AddSource  bool    `env:"SENTRY_ADD_SOURCE"  default:"true"`
	Stacktrace bool    `env:"SENTRY_STACKTRACE"  default:"false"`
	SampleRate float64 `env:"SENTRY_SAMPLE_RATE" default:"1.0"   v:"gte=0.0,lte=1.0"`
}

// ╭──────────────────────────────╮
// │             VAULT            │
// ╰──────────────────────────────╯

type Vault struct {
	Enabled    bool          `env:"VAULT_ENABLED"     default:"false"`
	Host       string        `env:"VAULT_HOST"        default:"http://localhost:8200"`
	Timeout    time.Duration `env:"VAULT_TIMEOUT"     default:"5s"`
	AuthType   string        `env:"VAULT_AUTH_TYPE"   default:"token"`
	Token      string        `env:"VAULT_TOKEN"       default:"qwerty"`
	SecretPath string        `env:"VAULT_SECRET_PATH" default:"/secret/data/github.com/go42-dev/go42"`
	MountPath  string        `env:"VAULT_MOUNT_PATH"  default:"secret"`
}

// ╭──────────────────────────────╮
// │             ETCD             │
// ╰──────────────────────────────╯

type Etcd struct {
	Enabled      bool          `env:"ETCD_ENABLED"       default:"false"`
	Hosts        []string      `env:"ETCD_HOST"          default:"localhost:2379"`
	Timeout      time.Duration `env:"ETCD_TIMEOUT"       default:"5s"`
	Method       string        `env:"ETCD_METHOD"        default:"bind"`
	SyncInterval time.Duration `env:"ETCD_SYNC_INTERVAL" default:"5m"`
}

// ╭──────────────────────────────╮
// │           DATABASE           │
// ╰──────────────────────────────╯

type Database struct {
	Engine      string `env:"DATABASE_ENGINE"       default:"sqlite"   v:"oneof=sqlite pgsql mysql"`
	MigratePath string `env:"DATABASE_MIGRATE_PATH" default:"/migrate"`
	LogQueries  bool   `env:"DATABASE_LOG_QUERIES"  default:"false"`
	Sqlite      Sqlite
	Pgsql       Pgsql
	Mysql       Mysql
}

func (db Database) FullMigratePath() string {
	return fmt.Sprintf(
		"%s/%s",
		strings.TrimRight(db.MigratePath, "/"),
		db.Engine,
	)
}

type Sqlite struct {
	Mode       string `env:"DATABASE_SQLITE_MODE"       default:"memory"`
	SqliteFile string `env:"DATABASE_SQLITE_PATH"       default:"file::memory:"`
	CacheMode  string `env:"DATABASE_SQLITE_CACHE_MODE" default:"shared"`
}

type Mysql struct {
	Master          MysqlMaster
	Slave           MysqlSlave
	ConnMaxIdleTime time.Duration `env:"DATABASE_MYSQL_CONN_MAX_IDLE_TIME" default:"10m"`
	ConnMaxLifetime time.Duration `env:"DATABASE_MYSQL_CONN_MAX_LIFETIME"  default:"30m"`
	MaxIdleConns    int           `env:"DATABASE_MYSQL_MAX_IDLE_CONNS"     default:"10"`
	MaxOpenConns    int           `env:"DATABASE_MYSQL_MAX_OPEN_CONNS"     default:"100"`
	QueryTimeout    time.Duration `env:"DATABASE_MYSQL_QUERY_TIMEOUT"      default:"10s"`
}

type MysqlMaster struct {
	Host     string `env:"DATABASE_MYSQL_MASTER_HOST"     default:"localhost"`
	Port     int    `env:"DATABASE_MYSQL_MASTER_PORT"     default:"3306"`
	User     string `env:"DATABASE_MYSQL_MASTER_USER"     default:"user"`
	Password string `env:"DATABASE_MYSQL_MASTER_PASSWORD" default:"qwerty"`
	Charset  string `env:"DATABASE_MYSQL_MASTER_CHARSET"  default:"utf8mb4"`
	Name     string `env:"DATABASE_MYSQL_MASTER_NAME"     default:"go42"`
}

func (db MysqlMaster) DSN() string {
	return fmt.Sprintf(
		"%s:%s@tcp(%s:%d)/%s?charset=%s&parseTime=True&loc=UTC",
		db.User, db.Password, db.Host, db.Port, db.Name, db.Charset,
	)
}

type MysqlSlave struct {
	Host     string `env:"DATABASE_MYSQL_SLAVE_HOST"     default:""`
	Port     int    `env:"DATABASE_MYSQL_SLAVE_PORT"     default:"3306"`
	User     string `env:"DATABASE_MYSQL_SLAVE_USER"     default:"user"`
	Password string `env:"DATABASE_MYSQL_SLAVE_PASSWORD" default:"qwerty"`
	Charset  string `env:"DATABASE_MYSQL_SLAVE_CHARSET"  default:"utf8mb4"`
	Name     string `env:"DATABASE_MYSQL_SLAVE_NAME"     default:"go42"`
}

func (db MysqlSlave) DSN() string {
	if db.Host == "" {
		return ""
	}
	return fmt.Sprintf(
		"%s:%s@tcp(%s:%d)/%s?charset=%s&parseTime=True&loc=UTC",
		db.User, db.Password, db.Host, db.Port, db.Name, db.Charset,
	)
}

type Pgsql struct {
	Master          PgsqlMaster
	Slave           PgsqlSlave
	ConnMaxIdleTime time.Duration `env:"DATABASE_PGSQL_CONN_MAX_IDLE_TIME" default:"10m"`
	ConnMaxLifetime time.Duration `env:"DATABASE_PGSQL_CONN_MAX_LIFETIME"  default:"30m"`
	MaxIdleConns    int           `env:"DATABASE_PGSQL_MAX_IDLE_CONNS"     default:"10"`
	MaxOpenConns    int           `env:"DATABASE_PGSQL_MAX_OPEN_CONNS"     default:"100"`
	QueryTimeout    time.Duration `env:"DATABASE_PGSQL_QUERY_TIMEOUT"      default:"10s"`
}

type PgsqlMaster struct {
	Host     string `env:"DATABASE_PGSQL_MASTER_HOST"     default:"localhost"`
	Port     int    `env:"DATABASE_PGSQL_MASTER_PORT"     default:"5432"`
	User     string `env:"DATABASE_PGSQL_MASTER_USER"     default:"user"`
	Password string `env:"DATABASE_PGSQL_MASTER_PASSWORD" default:"qwerty"`
	Name     string `env:"DATABASE_PGSQL_MASTER_NAME"     default:"go42"`
}

func (db PgsqlMaster) DSN() string {
	if db.Host == "" {
		return ""
	}
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s",
		db.User, db.Password, db.Host, db.Port, db.Name,
	)
}

type PgsqlSlave struct {
	Host     string `env:"DATABASE_PGSQL_SLAVE_HOST"     default:""`
	Port     int    `env:"DATABASE_PGSQL_SLAVE_PORT"     default:"5432"`
	User     string `env:"DATABASE_PGSQL_SLAVE_USER"     default:"user"`
	Password string `env:"DATABASE_PGSQL_SLAVE_PASSWORD" default:"qwerty"`
	Name     string `env:"DATABASE_PGSQL_SLAVE_NAME"     default:"go42"`
}

func (db PgsqlSlave) DSN() string {
	if db.Host == "" {
		return ""
	}
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s",
		db.User, db.Password, db.Host, db.Port, db.Name,
	)
}

// ╭──────────────────────────────╮
// │            CACHE             │
// ╰──────────────────────────────╯

type Cache struct {
	Engine    string `env:"CACHE_ENGINE" default:"local" v:"oneof=local memcached redis"`
	Local     Local
	Redis     Redis
	Memcached Memcached
}

type Local struct {
	Capacity     uint64 `env:"CACHE_LOCAL_CAPACITY"       default:"10000"`
	MaxCostBytes uint64 `env:"CACHE_LOCAL_MAX_COST_BYTES" default:"1073741824"`
}

type Redis struct {
	Host                  string        `env:"CACHE_REDIS_HOST"                    default:"localhost:6379"`
	DB                    int           `env:"CACHE_REDIS_DB"                      default:"0"`
	Username              string        `env:"CACHE_REDIS_USERNAME"                default:""`
	Password              string        `env:"CACHE_REDIS_PASSWORD"                default:""`
	MaxRetries            int           `env:"CACHE_REDIS_MAX_RETRIES"             default:"3"`
	MinRetryBackoff       time.Duration `env:"CACHE_REDIS_MIN_RETRY_BACKOFF"       default:"8ms"`
	MaxRetryBackoff       time.Duration `env:"CACHE_REDIS_MAX_RETRY_BACKOFF"       default:"512ms"`
	DialTimeout           time.Duration `env:"CACHE_REDIS_DIAL_TIMEOUT"            default:"5s"`
	ReadTimeout           time.Duration `env:"CACHE_REDIS_READ_TIMEOUT"            default:"3s"`
	WriteTimeout          time.Duration `env:"CACHE_REDIS_WRITE_TIMEOUT"           default:"3s"`
	ContextTimeoutEnabled bool          `env:"CACHE_REDIS_CONTEXT_TIMEOUT_ENABLED" default:"true"`
	PoolSize              int           `env:"CACHE_REDIS_POOL_SIZE"               default:"10"`
	PoolTimeout           time.Duration `env:"CACHE_REDIS_POOL_TIMEOUT"            default:"4s"`
	MinIdleConns          int           `env:"CACHE_REDIS_MIN_IDLE_CONNS"          default:"0"`
	MaxIdleConns          int           `env:"CACHE_REDIS_MAX_IDLE_CONNS"          default:"0"`
	MaxActiveConns        int           `env:"CACHE_REDIS_MAX_ACTIVE_CONNS"        default:"0"`
	ConnMaxIdleTime       time.Duration `env:"CACHE_REDIS_CONN_MAX_IDLE_TIME"      default:"0s"`
	ConnMaxLifetime       time.Duration `env:"CACHE_REDIS_CONN_MAX_LIFETIME"       default:"0s"`
}

type Memcached struct {
	Hosts        []string      `env:"CACHE_MEMCACHED_HOSTS"          default:"localhost:11211"`
	Timeout      time.Duration `env:"CACHE_MEMCACHED_TIMEOUT"        default:"1s"`
	MaxIdleConns int           `env:"CACHE_MEMCACHED_MAX_IDLE_CONNS" default:"100"`
}

// ╭──────────────────────────────╮
// │           EVENTS             │
// ╰──────────────────────────────╯

type Events struct {
	Engine   string `env:"EVENTS_ENGINE" default:"gochan" v:"oneof=none gochan nats rabbitmq kafka"`
	Consumer EventsConsumer
	NATS     EventsNATS
	RabbitMQ EventsRabbitMQ
	Kafka    EventsKafka
}

type EventsConsumer struct {
	Group                 string        `env:"EVENTS_CONSUMER_GROUP"           default:"default" v:"required"`
	MaxRetries            int           `env:"EVENTS_CONSUMER_MAX_RETRIES"     default:"3"       v:"gte=0,lte=100"`
	InitialBackoff        time.Duration `env:"EVENTS_CONSUMER_INITIAL_BACKOFF" default:"250ms"   v:"gt=0"`
	MaxBackoff            time.Duration `env:"EVENTS_CONSUMER_MAX_BACKOFF"     default:"5s"      v:"gt=0"`
	DeadLetterTopicSuffix string        `env:"EVENTS_CONSUMER_DLQ_SUFFIX"      default:"_dlq"    v:"required"`
}

type EventsNATS struct {
	DSN         string        `env:"NATS_DSN"          default:"nats://localhost:4222"`
	ClientName  string        `env:"NATS_CLIENT_NAME"  default:""`
	Token       string        `env:"NATS_TOKEN"        default:""`
	ConnTimeout time.Duration `env:"NATS_CONN_TIMEOUT" default:"5s"`
	ConnRetry   bool          `env:"NATS_CONN_RETRY"   default:"false"`
	MaxRetry    int           `env:"NATS_MAX_RETRY"    default:"-1"`
	RetryDelay  time.Duration `env:"NATS_RETRY_DELAY"  default:"1s"`
	JetStream   EventsNATSJetStream
	Subscriber  EventsNATSSubscriber
}

type EventsNATSJetStream struct {
	AutoProvision bool `env:"NATS_JETSTREAM_AUTO_PROVISION" default:"true"`
}

type EventsNATSSubscriber struct {
	WorkerCount  int           `env:"NATS_SUB_WORKER_COUNT"  default:"1"`
	Timeout      time.Duration `env:"NATS_SUB_TIMEOUT"       default:"30s"`
	AckTimeout   time.Duration `env:"NATS_SUB_ACK_TIMEOUT"   default:"30s"`
	CloseTimeout time.Duration `env:"NATS_SUB_CLOSE_TIMEOUT" default:"30s"`
}

type EventsRabbitMQ struct {
	DSN                      string        `env:"RABBITMQ_DSN"                        default:"amqp://guest:guest@localhost:5672/"`
	ReconnectInitialInterval time.Duration `env:"RABBITMQ_RECONNECT_INITIAL_INTERVAL" default:"500ms"`
	ReconnectMultiplier      float64       `env:"RABBITMQ_RECONNECT_MULTIPLIER"       default:"1.5"`
	ReconnectMaxInterval     time.Duration `env:"RABBITMQ_RECONNECT_MAX_INTERVAL"     default:"30s"`
	PublishMandatory         bool          `env:"RABBITMQ_PUBLISH_MANDATORY"          default:"false"`
	ConsumeNoRequeue         bool          `env:"RABBITMQ_CONSUME_NO_REQUEUE"         default:"false"`
	ConsumeConsumerName      string        `env:"RABBITMQ_CONSUME_CONSUMER_NAME"      default:""`
	ConsumeExclusive         bool          `env:"RABBITMQ_CONSUME_EXCLUSIVE"          default:"false"`
	ConsumePrefetchCount     int           `env:"RABBITMQ_CONSUME_PREFETCH_COUNT"     default:"1"`
	ConsumePrefetchSize      int           `env:"RABBITMQ_CONSUME_PREFETCH_SIZE"      default:"0"`
	ConsumeQosGlobal         bool          `env:"RABBITMQ_CONSUME_QOS_GLOBAL"         default:"false"`
	MessageUUIDHeader        string        `env:"RABBITMQ_MESSAGE_UUID_HEADER"        default:""`
}

type EventsKafka struct {
	Brokers                   []string      `env:"KAFKA_BROKERS"                      default:"localhost:9092"`
	ClientID                  string        `env:"KAFKA_CLIENT_ID"                    default:""`
	Version                   string        `env:"KAFKA_VERSION"                      default:"4.0.0"`
	DialTimeout               time.Duration `env:"KAFKA_DIAL_TIMEOUT"                 default:"30s"`
	ReadTimeout               time.Duration `env:"KAFKA_READ_TIMEOUT"                 default:"30s"`
	WriteTimeout              time.Duration `env:"KAFKA_WRITE_TIMEOUT"                default:"30s"`
	KeepAlive                 time.Duration `env:"KAFKA_KEEP_ALIVE"                   default:"0s"`
	ProducerRetryBackoff      time.Duration `env:"KAFKA_PRODUCER_RETRY_BACKOFF"       default:"100ms"`
	ProducerMaxMessageBytes   int           `env:"KAFKA_PRODUCER_MAX_MESSAGE_BYTES"   default:"1000000"`
	ProducerCompression       string        `env:"KAFKA_PRODUCER_COMPRESSION"         default:"none"`
	ConsumerRetryBackoff      time.Duration `env:"KAFKA_CONSUMER_RETRY_BACKOFF"       default:"2s"`
	ConsumerMaxWaitTime       time.Duration `env:"KAFKA_CONSUMER_MAX_WAIT_TIME"       default:"250ms"`
	ConsumerMaxProcessingTime time.Duration `env:"KAFKA_CONSUMER_MAX_PROCESSING_TIME" default:"100ms"`
	ConsumerOffsetInitial     int64         `env:"KAFKA_CONSUMER_OFFSET_INITIAL"      default:"-1"`
	ConsumerSessionTimeout    time.Duration `env:"KAFKA_CONSUMER_SESSION_TIMEOUT"     default:"10s"`
	ConsumerHeartbeatInterval time.Duration `env:"KAFKA_CONSUMER_HEARTBEAT_INTERVAL"  default:"3s"`
	ConsumerRebalanceStrategy string        `env:"KAFKA_CONSUMER_REBALANCE_STRATEGY"  default:"range"`
	MetadataRefreshFrequency  time.Duration `env:"KAFKA_METADATA_REFRESH_FREQUENCY"   default:"10m"`
}

// ╭──────────────────────────────╮
// │            PPROF             │
// ╰──────────────────────────────╯

type Pprof struct {
	Enabled      bool          `env:"PPROF_ENABLED"       default:"false"`
	Listen       string        `env:"PPROF_LISTEN"        default:":6060"`
	Prefix       string        `env:"PPROF_PREFIX"        default:"/debug/pprof"`
	ReadTimeout  time.Duration `env:"PPROF_READ_TIMEOUT"  default:"5s"`
	WriteTimeout time.Duration `env:"PPROF_WRITE_TIMEOUT" default:"60s"`
}

// ╭──────────────────────────────╮
// │            SERVER            │
// ╰──────────────────────────────╯

type Server struct {
	HTTP HTTP
	GRPC GRPC
}

type HTTP struct {
	Listen            string        `env:"SERVER_HTTP_LISTEN"              default:":8080"`
	ReadTimeout       time.Duration `env:"SERVER_HTTP_READ_TIMEOUT"        default:"5s"`
	WriteTimeout      time.Duration `env:"SERVER_HTTP_WRITE_TIMEOUT"       default:"5s"`
	StaticRoot        string        `env:"SERVER_HTTP_STATIC_ROOT"         default:"/usr/share/www"`
	SwaggerRoot       string        `env:"SERVER_HTTP_SWAGGER_ROOT"        default:"/usr/share/www/api"`
	SwaggerDark       bool          `env:"SERVER_HTTP_SWAGGER_DARK"        default:"true"`
	BodyLimitKB       int64         `env:"SERVER_HTTP_BODY_LIMIT_KB"       default:"1024"`
	CORSAllowOrigins  []string      `env:"SERVER_HTTP_CORS_ALLOW_ORIGINS"  default:"*"`
	TrustedProxyCIDRs []string      `env:"SERVER_HTTP_TRUSTED_PROXY_CIDRS"                              v:"omitempty,dive,cidr"`
	RateLimiter       HTTPRateLimiter
}

type HTTPRateLimiter struct {
	Enabled bool          `env:"SERVER_HTTP_RATE_LIMITER_ENABLED" default:"false"`
	Rate    int           `env:"SERVER_HTTP_RATE_LIMITER_RATE"    default:"100"`
	Burst   int           `env:"SERVER_HTTP_RATE_LIMITER_BURST"   default:"10"`
	TTL     time.Duration `env:"SERVER_HTTP_RATE_LIMITER_TTL"     default:"10m"`
}

type GRPC struct {
	Listen               string `env:"SERVER_GRPC_LISTEN"                  default:":50051"`
	MaxRecvMsgSize       int    `env:"SERVER_GRPC_MAX_RECV_MSG_SIZE_BYTES" default:"4194304"`
	MaxSendMsgSize       int    `env:"SERVER_GRPC_MAX_SEND_MSG_SIZE_BYTES" default:"4194304"`
	ReflectionEnabled    bool   `env:"SERVER_GRPC_REFLECTION_ENABLED"      default:"false"`
	AuthorisationEnabled bool   `env:"SERVER_GRPC_AUTHORIZATION_ENABLED"   default:"true"`
	RateLimiter          GRPCRateLimiter
}

type GRPCRateLimiter struct {
	Enabled bool          `env:"SERVER_GRPC_RATE_LIMITER_ENABLED" default:"false"`
	Rate    int           `env:"SERVER_GRPC_RATE_LIMITER_RATE"    default:"100"`
	Burst   int           `env:"SERVER_GRPC_RATE_LIMITER_BURST"   default:"10"`
	TTL     time.Duration `env:"SERVER_GRPC_RATE_LIMITER_TTL"     default:"10m"`
}

// ╭──────────────────────────────╮
// │            OUTBOX            │
// ╰──────────────────────────────╯

type Outbox struct {
	WorkerRunInterval time.Duration `env:"OUTBOX_WORKER_INTERVAL"   default:"5s"`
	WorkerBatchSize   int           `env:"OUTBOX_WORKER_BATCH_SIZE" default:"1000"`
}

// ╭──────────────────────────────╮
// │             AUTH             │
// ╰──────────────────────────────╯

type Auth struct {
	RateLimiter            AuthRateLimiter
	TokenUpdaterInterval   time.Duration `env:"AUTH_TOKEN_UPDATER_INTERVAL"    default:"5m"`
	SessionCleanupInterval time.Duration `env:"AUTH_SESSION_CLEANUP_INTERVAL"  default:"1h" v:"gt=0"`
	MinPasswordEntropyBits int           `env:"AUTH_MIN_PASSWORD_ENTROPY_BITS" default:"50"`
	Cache                  struct {
		Repository struct {
			Secrets time.Duration `env:"AUTH_CACHE_REPOSITORY_SECRETS" default:"1m"`
		}
	}
	JWT struct {
		InitialSecrets  []string      `env:"AUTH_JWT_SECRETS"               default:"0128899,9988210"`
		AccessTokenTTL  time.Duration `env:"AUTH_JWT_ACCESS_TOKEN_TTL"      default:"15m" v:"gt=0"`
		RefreshTokenTTL time.Duration `env:"AUTH_JWT_REFRESH_TOKEN_TTL"     default:"168h" v:"gt=0"`
		Issuer          string        `env:"AUTH_JWT_ISSUER"                default:"go42"`
		Audience        []string      `env:"AUTH_JWT_AUDIENCE"              default:"go42"`
	}
}

type AuthRateLimiter struct {
	Enabled                bool          `env:"AUTH_RATE_LIMITER_ENABLED"                  default:"false"`
	LoginAccountRequests   int           `env:"AUTH_RATE_LIMITER_LOGIN_ACCOUNT_REQUESTS"   default:"5"     v:"gt=0"`
	LoginIPRequests        int           `env:"AUTH_RATE_LIMITER_LOGIN_IP_REQUESTS"        default:"20"    v:"gt=0"`
	LoginWindow            time.Duration `env:"AUTH_RATE_LIMITER_LOGIN_WINDOW"             default:"1m"    v:"gt=0"`
	SignupIPRequests       int           `env:"AUTH_RATE_LIMITER_SIGNUP_IP_REQUESTS"       default:"5"     v:"gt=0"`
	SignupWindow           time.Duration `env:"AUTH_RATE_LIMITER_SIGNUP_WINDOW"            default:"1h"    v:"gt=0"`
	RefreshSessionRequests int           `env:"AUTH_RATE_LIMITER_REFRESH_SESSION_REQUESTS" default:"30"    v:"gt=0"`
	RefreshWindow          time.Duration `env:"AUTH_RATE_LIMITER_REFRESH_WINDOW"           default:"1m"    v:"gt=0"`
}

// ---

const (
	TagNameEnvVarName   = "env"
	TagNameDefaultValue = "default"
)

func New() (*Config, error) {
	cfg := new(Config)
	err := env.ParseWithOptions(cfg, env.Options{
		TagName:             TagNameEnvVarName,
		DefaultValueTagName: TagNameDefaultValue,
	})
	if err != nil {
		return nil, err
	}

	if err := tools.ValidateStructCompact(cfg); err != nil {
		return nil, err
	}
	if cfg.Core.StartupRetryMaxBackoff < cfg.Core.StartupRetryInitialBackoff {
		return nil, errors.New(
			"startup retry maximum backoff must not be less than initial backoff",
		)
	}

	return cfg, nil
}

func (c *Config) String() string {
	if out, err := json.MarshalIndent(&c, "", "  "); err == nil {
		return string(out)
	}
	return ""
}
