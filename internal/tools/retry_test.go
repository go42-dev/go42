package tools_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/go42-dev/go42/internal/metrics"
	"github.com/go42-dev/go42/internal/tools"
)

func TestStartupRetryPolicyRetriesConnectionsAndRecordsOutcomes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		policy := tools.DefaultStartupRetryPolicy()
		attempts := 0
		err := policy.Do(t.Context(), t.Name(), slog.New(slog.DiscardHandler), func(ctx context.Context) error {
			require.NoError(t, ctx.Err())
			attempts++
			if attempts < 3 {
				return errors.New("dependency unavailable")
			}
			return nil
		})
		require.NoError(t, err)
		require.Equal(t, 3, attempts)
		require.EqualValues(t, 2, metrics.Counter("application_startup_connection_attempts_total", map[string]any{
			"backend": t.Name(), "result": "failure",
		}).Get())
		require.EqualValues(t, 1, metrics.Counter("application_startup_connection_attempts_total", map[string]any{
			"backend": t.Name(), "result": "success",
		}).Get())
	})
}

func TestStartupRetryPolicyStopsAtTheEffectiveDeadlineAndPreservesLastError(t *testing.T) {
	for _, parentDeadline := range []bool{false, true} {
		name := "policy deadline"
		if parentDeadline {
			name = "parent deadline"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				policy := tools.DefaultStartupRetryPolicy()
				policy.Timeout = time.Second
				ctx := t.Context()
				budget := policy.Timeout
				if parentDeadline {
					budget /= 2
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, budget)
					defer cancel()
				}
				started := time.Now()
				cause := errors.New("dependency unavailable")
				attempts := 0
				err := policy.Do(ctx, t.Name(), slog.New(slog.DiscardHandler), func(attemptCtx context.Context) error {
					attempts++
					deadline, ok := attemptCtx.Deadline()
					require.True(t, ok)
					require.Equal(t, started.Add(budget), deadline)
					return cause
				})
				require.ErrorIs(t, err, context.DeadlineExceeded)
				require.ErrorIs(t, err, cause)
				require.Positive(t, attempts)
				require.Equal(t, budget, time.Since(started))
			})
		})
	}
}

func TestStartupRetryPolicyDoesNotConnectAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := tools.DefaultStartupRetryPolicy().
		Do(ctx, t.Name(), slog.New(slog.DiscardHandler), func(context.Context) error {
			t.Fatal("connection attempted after cancellation")
			return nil
		})
	require.ErrorIs(t, err, context.Canceled)
}

func TestStartupRetryPolicyRejectsInvalidSettingsBeforeConnecting(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*tools.StartupRetryPolicy)
	}{
		{"zero timeout", func(p *tools.StartupRetryPolicy) { p.Timeout = 0 }},
		{"negative timeout", func(p *tools.StartupRetryPolicy) { p.Timeout = -time.Second }},
		{"zero backoff", func(p *tools.StartupRetryPolicy) { p.InitialBackoff = 0 }},
		{"negative backoff", func(p *tools.StartupRetryPolicy) { p.InitialBackoff = -time.Second }},
		{"descending backoff", func(p *tools.StartupRetryPolicy) { p.MaxBackoff = p.InitialBackoff / 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := tools.DefaultStartupRetryPolicy()
			tc.configure(&policy)
			err := policy.Do(t.Context(), t.Name(), slog.New(slog.DiscardHandler), func(context.Context) error {
				t.Fatal("connection attempted with invalid retry settings")
				return nil
			})
			require.ErrorContains(t, err, "startup timeout and retry backoff must be positive and ordered")
		})
	}
}
