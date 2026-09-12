package mysql

import (
	"log/slog"
	"time"
)

type Option func(w *Mysql)

func WithLogger(logger *slog.Logger) Option {
	return func(w *Mysql) {
		w.logger = logger
	}
}

func WithQueryLogging(do bool) Option {
	return func(w *Mysql) {
		w.queryLogging = do
	}
}

func WithConnMaxIdleTime(d time.Duration) Option {
	return func(w *Mysql) {
		w.connMaxIdleTime = d
	}
}

func WithConnMaxLifetime(d time.Duration) Option {
	return func(w *Mysql) {
		w.connMaxLifetime = d
	}
}

func WithMaxIdleConns(n int) Option {
	return func(w *Mysql) {
		w.maxIdleConns = n
	}
}

func WithMaxOpenConns(n int) Option {
	return func(w *Mysql) {
		w.maxOpenConns = n
	}
}

func WithQueryTimeout(timeout time.Duration) Option {
	return func(w *Mysql) {
		w.queryTimeout = timeout
	}
}

func WithConnectRetryTimeout(timeout time.Duration) Option {
	return func(w *Mysql) {
		w.connectRetry.Timeout = timeout
	}
}

func WithConnectRetryBackoff(initial time.Duration, max time.Duration) Option {
	return func(w *Mysql) {
		w.connectRetry.InitialBackoff = initial
		w.connectRetry.MaxBackoff = max
	}
}
