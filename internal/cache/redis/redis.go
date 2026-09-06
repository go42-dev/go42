package redis

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/redis/go-redis/v9"

	"github.com/go42-dev/go42/internal/tools"
)

type Wrapper struct {
	logger *slog.Logger
	client *redis.Client

	connectRetryTimeout        time.Duration
	connectRetryInitialBackoff time.Duration
	connectRetryMaxBackoff     time.Duration
}

const (
	defaultConnectRetryTimeout        = time.Minute
	defaultConnectRetryInitialBackoff = 500 * time.Millisecond
	defaultConnectRetryMaxBackoff     = 5 * time.Second
)

// allowRateLimitScript implements GCRA atomically inside Redis.
var allowRateLimitScript = redis.NewScript(`
local interval = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local ttl_ms = tonumber(ARGV[3])
local now_parts = redis.call('TIME')
local now = (tonumber(now_parts[1]) * 1000000) + tonumber(now_parts[2])
local tat = tonumber(redis.call('GET', KEYS[1]))

if not tat or tat < now then
	tat = now
end

local allow_at = tat - ((burst - 1) * interval)
if now < allow_at then
	redis.call('PEXPIRE', KEYS[1], ttl_ms)
	return 0
end

redis.call('SET', KEYS[1], string.format('%.0f', tat + interval), 'PX', ttl_ms)
return 1
`)

func Open(ctx context.Context, host string, db int, opts ...Option) (*Wrapper, error) {
	w := &Wrapper{
		connectRetryTimeout:        defaultConnectRetryTimeout,
		connectRetryInitialBackoff: defaultConnectRetryInitialBackoff,
		connectRetryMaxBackoff:     defaultConnectRetryMaxBackoff,
	}

	cfg := &redis.Options{
		Addr: host,
		DB:   db,
	}
	for _, opt := range opts {
		opt(w, cfg)
	}
	if w.logger == nil {
		w.logger = slog.New(slog.DiscardHandler)
	}

	retryCtx, cancel := context.WithTimeout(ctx, w.connectRetryTimeout)
	defer cancel()

	rdb, err := retry.DoWithData[*redis.Client](func() (*redis.Client, error) {
		rdb := redis.NewClient(cfg)
		if err := rdb.Ping(retryCtx).Err(); err != nil {
			pingErr := fmt.Errorf("failed to ping redis: %w", err)
			if closeErr := rdb.Close(); closeErr != nil {
				return nil, errors.Join(
					pingErr,
					fmt.Errorf("failed to close redis client: %w", closeErr),
				)
			}
			return nil, pingErr
		}
		return rdb, nil
	},
		retry.Context(retryCtx),
		retry.Attempts(0),
		retry.Delay(w.connectRetryInitialBackoff),
		retry.MaxDelay(w.connectRetryMaxBackoff),
		retry.DelayType(retry.FullJitterBackoffDelay),
		retry.WrapContextErrorWithLastError(true),
		retry.OnRetry(func(n uint, err error) {
			if retryCtx.Err() == nil {
				w.logger.WarnContext(
					ctx,
					"cache connection attempt failed, retrying...",
					slog.Any("attempt", n+1),
					slog.Any("error", err),
				)
			}
		}),
	)
	if err != nil {
		return nil, err
	}

	w.client = rdb
	return w, nil
}

func (w *Wrapper) Shutdown(ctx context.Context) error {
	done := make(chan error, 1)
	go func() {
		done <- w.client.Close()
	}()
	select {
	case <-ctx.Done():
		return errors.New("timeout")
	case err := <-done:
		return err
	}
}

func (w *Wrapper) Ping(ctx context.Context) error {
	if err := w.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis ping failed: %w", err)
	}
	return nil
}

func (w *Wrapper) Get(ctx context.Context, key string) (string, bool, error) {
	cmd := w.client.Get(ctx, key)
	if cmd.Err() != nil {
		if errors.Is(cmd.Err(), redis.Nil) {
			return "", false, nil
		}
		return "", false, cmd.Err()
	}
	return cmd.Val(), true, nil
}

func (w *Wrapper) Set(ctx context.Context, key string, value string, ttl time.Duration) error {
	cmd := w.client.Set(ctx, key, value, ttl)
	return cmd.Err()
}

func (w *Wrapper) SetIfAbsent(
	ctx context.Context,
	key string,
	value string,
	ttl time.Duration,
) (bool, error) {
	err := w.client.SetArgs(ctx, key, value, redis.SetArgs{
		Mode: "NX",
		TTL:  ttl,
	}).Err()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (w *Wrapper) AllowRateLimit(
	ctx context.Context,
	key string,
	interval time.Duration,
	burst int,
	ttl time.Duration,
) (bool, error) {
	ttl, err := tools.RateLimitTTL(interval, burst, ttl)
	if err != nil {
		return false, err
	}
	allowed, err := allowRateLimitScript.Run(
		ctx,
		w.client,
		[]string{key},
		max(int64(1), interval.Microseconds()),
		burst,
		max(int64(1), ttl.Milliseconds()),
	).Int()
	return allowed == 1 && err == nil, err
}

func (w *Wrapper) Invalidate(ctx context.Context, key string) error {
	cmd := w.client.Del(ctx, key)
	if cmd.Err() != nil {
		return cmd.Err()
	}
	return nil
}
