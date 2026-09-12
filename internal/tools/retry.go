package tools

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/avast/retry-go/v4"

	"github.com/go42-dev/go42/internal/metrics"
)

// StartupRetryPolicy configures retries during dependency initialization.
type StartupRetryPolicy struct {
	Timeout        time.Duration
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

func DefaultStartupRetryPolicy() StartupRetryPolicy {
	return StartupRetryPolicy{
		Timeout:        time.Minute,
		InitialBackoff: 500 * time.Millisecond,
		MaxBackoff:     5 * time.Second,
	}
}

// Do retries dependency initialization until the policy or parent context expires.
// Each attempt must honor the supplied context or bound its own I/O, and clean up
// failed connections and any connections acquired after cancellation.
func (p StartupRetryPolicy) Do(
	ctx context.Context,
	backend string,
	logger *slog.Logger,
	connect func(context.Context) error,
) error {
	if p.Timeout <= 0 || p.InitialBackoff <= 0 || p.MaxBackoff < p.InitialBackoff {
		return errors.New("startup timeout and retry backoff must be positive and ordered")
	}

	retryCtx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()

	return retry.Do(func() error {
		err := connect(retryCtx)
		result := "success"
		if err != nil {
			result = "failure"
		}
		metrics.Counter("application_startup_connection_attempts_total", map[string]any{
			"backend": backend,
			"result":  result,
		}).Inc()
		return err
	},
		retry.Context(retryCtx),
		retry.Attempts(0),
		retry.Delay(p.InitialBackoff),
		retry.MaxDelay(p.MaxBackoff),
		retry.DelayType(retry.FullJitterBackoffDelay),
		retry.WrapContextErrorWithLastError(true),
		retry.OnRetry(func(n uint, err error) {
			if retryCtx.Err() == nil {
				logger.WarnContext(ctx, "connection attempt failed, retrying...",
					slog.String("backend", backend),
					slog.Any("attempt", n+1),
					slog.Any("error", err),
				)
			}
		}),
	)
}
