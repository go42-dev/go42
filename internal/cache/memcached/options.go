package memcached

import (
	"log/slog"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
)

type Option func(*Wrapper, *memcache.Client)

func WithLogger(logger *slog.Logger) Option {
	return func(w *Wrapper, client *memcache.Client) {
		w.logger = logger
	}
}

func WithConnectRetryTimeout(timeout time.Duration) Option {
	return func(w *Wrapper, client *memcache.Client) {
		w.connectRetry.Timeout = timeout
	}
}

func WithConnectRetryBackoff(initial time.Duration, max time.Duration) Option {
	return func(w *Wrapper, client *memcache.Client) {
		w.connectRetry.InitialBackoff = initial
		w.connectRetry.MaxBackoff = max
	}
}

func WithTimeout(timeout time.Duration) Option {
	return func(w *Wrapper, client *memcache.Client) {
		client.Timeout = timeout
	}
}

func WithMaxIdleConns(maxIdleConns int) Option {
	return func(w *Wrapper, client *memcache.Client) {
		client.MaxIdleConns = maxIdleConns
	}
}
