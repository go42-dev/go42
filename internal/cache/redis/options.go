package redis

import (
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

type Option func(*Wrapper, *redis.Options)

func WithLogger(logger *slog.Logger) Option {
	return func(w *Wrapper, opts *redis.Options) {
		w.logger = logger
	}
}

func WithConnectRetryTimeout(timeout time.Duration) Option {
	return func(w *Wrapper, opts *redis.Options) {
		w.connectRetry.Timeout = timeout
	}
}

func WithConnectRetryBackoff(initial time.Duration, max time.Duration) Option {
	return func(w *Wrapper, opts *redis.Options) {
		w.connectRetry.InitialBackoff = initial
		w.connectRetry.MaxBackoff = max
	}
}

func WithClientName(name string) Option {
	return func(w *Wrapper, opts *redis.Options) {
		opts.ClientName = name
	}
}

func WithUserName(username string) Option {
	return func(w *Wrapper, opts *redis.Options) {
		opts.Username = username
	}
}

func WithPassword(password string) Option {
	return func(w *Wrapper, opts *redis.Options) {
		opts.Password = password
	}
}

func WithMaxRetries(maxRetries int) Option {
	return func(w *Wrapper, opts *redis.Options) {
		opts.MaxRetries = maxRetries
	}
}

func WithMinRetryBackoff(minRetryBackoff time.Duration) Option {
	return func(w *Wrapper, opts *redis.Options) {
		opts.MinRetryBackoff = minRetryBackoff
	}
}

func WithMaxRetryBackoff(maxRetryBackoff time.Duration) Option {
	return func(w *Wrapper, opts *redis.Options) {
		opts.MaxRetryBackoff = maxRetryBackoff
	}
}

func WithDialTimeout(dialTimeout time.Duration) Option {
	return func(w *Wrapper, opts *redis.Options) {
		opts.DialTimeout = dialTimeout
	}
}

func WithReadTimeout(readTimeout time.Duration) Option {
	return func(w *Wrapper, opts *redis.Options) {
		opts.ReadTimeout = readTimeout
	}
}

func WithWriteTimeout(writeTimeout time.Duration) Option {
	return func(w *Wrapper, opts *redis.Options) {
		opts.WriteTimeout = writeTimeout
	}
}

func WithContextTimeoutEnabled(enabled bool) Option {
	return func(w *Wrapper, opts *redis.Options) {
		opts.ContextTimeoutEnabled = enabled
	}
}

func WithPoolSize(poolSize int) Option {
	return func(w *Wrapper, opts *redis.Options) {
		opts.PoolSize = poolSize
	}
}

func WithPoolTimeout(poolTimeout time.Duration) Option {
	return func(w *Wrapper, opts *redis.Options) {
		opts.PoolTimeout = poolTimeout
	}
}

func WithMinIdleConns(minIdleConns int) Option {
	return func(w *Wrapper, opts *redis.Options) {
		opts.MinIdleConns = minIdleConns
	}
}

func WithMaxIdleConns(maxIdleConns int) Option {
	return func(w *Wrapper, opts *redis.Options) {
		opts.MaxIdleConns = maxIdleConns
	}
}

func WithMaxActiveConns(maxActiveConns int) Option {
	return func(w *Wrapper, opts *redis.Options) {
		opts.MaxActiveConns = maxActiveConns
	}
}

func WithConnMaxIdleTime(connMaxIdleTime time.Duration) Option {
	return func(w *Wrapper, opts *redis.Options) {
		opts.ConnMaxIdleTime = connMaxIdleTime
	}
}

func WithConnMaxLifetime(connMaxLifetime time.Duration) Option {
	return func(w *Wrapper, opts *redis.Options) {
		opts.ConnMaxLifetime = connMaxLifetime
	}
}
