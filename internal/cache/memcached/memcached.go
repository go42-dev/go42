package memcached

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/bradfitz/gomemcache/memcache"

	"github.com/go42-dev/go42/internal/tools"
)

type Wrapper struct {
	logger *slog.Logger
	client *memcache.Client

	connectRetryTimeout        time.Duration
	connectRetryInitialBackoff time.Duration
	connectRetryMaxBackoff     time.Duration
}

const (
	defaultConnectRetryTimeout        = time.Minute
	defaultConnectRetryInitialBackoff = 500 * time.Millisecond
	defaultConnectRetryMaxBackoff     = 5 * time.Second
)

func Open(ctx context.Context, hosts []string, opts ...Option) (*Wrapper, error) {
	w := &Wrapper{
		connectRetryTimeout:        defaultConnectRetryTimeout,
		connectRetryInitialBackoff: defaultConnectRetryInitialBackoff,
		connectRetryMaxBackoff:     defaultConnectRetryMaxBackoff,
	}
	client := memcache.New(hosts...)
	for _, opt := range opts {
		opt(w, client)
	}
	if w.logger == nil {
		w.logger = slog.New(slog.DiscardHandler)
	}

	retryCtx, cancel := context.WithTimeout(ctx, w.connectRetryTimeout)
	defer cancel()

	err := retry.Do(func() error {
		if err := client.Ping(); err != nil {
			pingErr := fmt.Errorf("failed to ping memcached: %w", err)
			if closeErr := client.Close(); closeErr != nil {
				return errors.Join(
					pingErr,
					fmt.Errorf("failed to close memcached client: %w", closeErr),
				)
			}
			return pingErr
		}
		return nil
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
	w.client = client
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
	done := make(chan error, 1)
	go func() {
		done <- w.client.Ping()
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		if err != nil {
			return fmt.Errorf("memcached ping failed: %w", err)
		}
		return nil
	}
}

func (w *Wrapper) Get(_ context.Context, key string) (string, bool, error) {
	item, err := w.client.Get(key)
	if err != nil {
		if errors.Is(err, memcache.ErrCacheMiss) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(item.Value), true, nil
}

func (w *Wrapper) Set(_ context.Context, key string, value string, ttl time.Duration) error {
	return w.client.Set(newItem(key, value, ttl))
}

func (w *Wrapper) SetIfAbsent(
	_ context.Context,
	key string,
	value string,
	ttl time.Duration,
) (bool, error) {
	err := w.client.Add(newItem(key, value, ttl))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, memcache.ErrNotStored) {
		return false, nil
	}
	return false, err
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
	// Memcached updates the same GCRA state with compare-and-swap.
	intervalMicros := max(int64(1), interval.Microseconds())
	burstTolerance := int64(burst-1) * intervalMicros

	const maxCASAttempts = 16
	for range maxCASAttempts {
		if err := ctx.Err(); err != nil {
			return false, err
		}

		now := time.Now().UnixMicro()
		expiration, err := rateLimitExpiration(now, ttl)
		if err != nil {
			return false, err
		}
		item, err := w.client.Get(key)
		if errors.Is(err, memcache.ErrCacheMiss) {
			item = newItem(key, strconv.FormatInt(now+intervalMicros, 10), ttl)
			item.Expiration = expiration
			err = w.client.Add(item)
			if err == nil {
				return true, nil
			}
			if errors.Is(err, memcache.ErrNotStored) {
				continue
			}
			return false, err
		}
		if err != nil {
			return false, err
		}

		tat, err := strconv.ParseInt(string(item.Value), 10, 64)
		if err != nil {
			return false, fmt.Errorf("decode rate limiter state: %w", err)
		}
		if tat < now {
			tat = now
		}
		if now < tat-burstTolerance {
			return false, nil
		}

		item.Value = []byte(strconv.FormatInt(tat+intervalMicros, 10))
		item.Expiration = expiration
		err = w.client.CompareAndSwap(item)
		if err == nil {
			return true, nil
		}
		if errors.Is(err, memcache.ErrCASConflict) || errors.Is(err, memcache.ErrNotStored) {
			continue
		}
		return false, err
	}

	return false, fmt.Errorf("rate limiter state remained contended after %d attempts", maxCASAttempts)
}

func rateLimitExpiration(nowMicros int64, ttl time.Duration) (int32, error) {
	seconds := int64(ttl / time.Second)
	if ttl%time.Second != 0 {
		seconds++
	}
	// Memcached's relative clock has second precision. Retain the bucket for
	// an extra second so clock rounding cannot restore its burst too early.
	seconds++
	if seconds > 30*24*60*60 {
		// Values above 30 days are interpreted as absolute Unix timestamps.
		seconds += nowMicros / 1_000_000
	}
	if seconds < math.MinInt32 || seconds > math.MaxInt32 {
		return 0, fmt.Errorf("rate limit window exceeds memcached expiration range")
	}
	return int32(seconds), nil
}

func (w *Wrapper) Invalidate(_ context.Context, key string) error {
	err := w.client.Delete(key)
	if errors.Is(err, memcache.ErrCacheMiss) {
		return nil
	}
	return err
}

func newItem(key string, value string, ttl time.Duration) *memcache.Item {
	item := &memcache.Item{Key: key, Value: []byte(value)}
	if ttl > 0 {
		item.Expiration = expirationSeconds(ttl)
	}
	return item
}

func expirationSeconds(ttl time.Duration) int32 {
	if ttl <= 0 {
		return 0
	}

	seconds := ttl / time.Second
	if ttl%time.Second != 0 {
		seconds++
	}
	if seconds > time.Duration(1<<31-1) {
		return 1<<31 - 1
	}

	return int32(seconds)
}
