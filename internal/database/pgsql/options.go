package pgsql

import (
	"log/slog"
	"time"
)

type Option func(w *Postgres)

func WithLogger(logger *slog.Logger) Option {
	return func(w *Postgres) {
		w.logger = logger
	}
}

func WithQueryLogging(do bool) Option {
	return func(w *Postgres) {
		w.queryLogging = do
	}
}

func WithConnMaxIdleTime(d time.Duration) Option {
	return func(w *Postgres) {
		w.connMaxIdleTime = d
	}
}

func WithConnMaxLifetime(d time.Duration) Option {
	return func(w *Postgres) {
		w.connMaxLifetime = d
	}
}

func WithMaxIdleConns(n int) Option {
	return func(w *Postgres) {
		w.maxIdleConns = n
	}
}

func WithMaxOpenConns(n int) Option {
	return func(w *Postgres) {
		w.maxOpenConns = n
	}
}

func WithQueryTimeout(timeout time.Duration) Option {
	return func(w *Postgres) {
		w.queryTimeout = timeout
	}
}

func WithConnectRetryTimeout(timeout time.Duration) Option {
	return func(w *Postgres) {
		w.connectRetry.Timeout = timeout
	}
}

func WithConnectRetryBackoff(initial time.Duration, max time.Duration) Option {
	return func(w *Postgres) {
		w.connectRetry.InitialBackoff = initial
		w.connectRetry.MaxBackoff = max
	}
}
