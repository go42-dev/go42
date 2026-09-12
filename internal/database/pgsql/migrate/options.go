package migrate

import (
	"log/slog"
	"time"

	"github.com/go42-dev/go42/internal/tools"
)

type options struct {
	logger       *slog.Logger
	connectRetry tools.StartupRetryPolicy
}

type Option func(opts *options)

func WithLogger(logger *slog.Logger) Option {
	return func(opts *options) {
		opts.logger = logger
	}
}

func WithConnectRetryTimeout(timeout time.Duration) Option {
	return func(opts *options) {
		opts.connectRetry.Timeout = timeout
	}
}

func WithConnectRetryBackoff(initial time.Duration, max time.Duration) Option {
	return func(opts *options) {
		opts.connectRetry.InitialBackoff = initial
		opts.connectRetry.MaxBackoff = max
	}
}

func defaultOptions() options {
	return options{
		connectRetry: tools.DefaultStartupRetryPolicy(),
	}
}
