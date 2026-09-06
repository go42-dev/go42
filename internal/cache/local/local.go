package local

import (
	"context"
	"runtime"
	"time"

	"github.com/jellydator/ttlcache/v3"
	ratepkg "golang.org/x/time/rate"

	"github.com/go42-dev/go42/internal/tools"
)

type Wrapper struct {
	cache                *ttlcache.Cache[string, string]
	rateLimitCache       *ttlcache.Cache[string, *ratepkg.Limiter]
	cleanupDone          chan struct{}
	rateLimitCleanupDone chan struct{}
}

func New(opts ...Option) *Wrapper {
	cfg := config{
		capacity:     10_000,
		maxCostBytes: 1 << 30,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	cacheOpts := []ttlcache.Option[string, string]{
		ttlcache.WithDisableTouchOnHit[string, string](),
	}
	if cfg.capacity > 0 {
		cacheOpts = append(cacheOpts, ttlcache.WithCapacity[string, string](cfg.capacity))
	}
	if cfg.maxCostBytes > 0 {
		cacheOpts = append(cacheOpts, ttlcache.WithMaxCost[string, string](
			cfg.maxCostBytes,
			func(item ttlcache.CostItem[string, string]) uint64 {
				return uint64(len(item.Key) + len(item.Value))
			},
		))
	}

	c := ttlcache.New[string, string](cacheOpts...)
	rateLimitCacheOpts := make([]ttlcache.Option[string, *ratepkg.Limiter], 0, 1)
	if cfg.capacity > 0 {
		rateLimitCacheOpts = append(
			rateLimitCacheOpts,
			ttlcache.WithCapacity[string, *ratepkg.Limiter](cfg.capacity),
		)
	}
	rateLimitCache := ttlcache.New[string, *ratepkg.Limiter](rateLimitCacheOpts...)

	cleanupDone := make(chan struct{})
	rateLimitCleanupDone := make(chan struct{})

	go func() {
		c.Start()
		close(cleanupDone)
	}()
	go func() {
		rateLimitCache.Start()
		close(rateLimitCleanupDone)
	}()

	return &Wrapper{
		cache:                c,
		rateLimitCache:       rateLimitCache,
		cleanupDone:          cleanupDone,
		rateLimitCleanupDone: rateLimitCleanupDone,
	}
}

func (w *Wrapper) Shutdown(ctx context.Context) error {
	for {
		w.cache.Stop()
		w.rateLimitCache.Stop()

		cacheStopped := channelClosed(w.cleanupDone)
		rateLimitCacheStopped := channelClosed(w.rateLimitCleanupDone)
		if cacheStopped && rateLimitCacheStopped {
			w.cache.DeleteAll()
			w.rateLimitCache.DeleteAll()
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			runtime.Gosched()
		}
	}
}

func (w *Wrapper) Ping(ctx context.Context) error {
	return ctx.Err()
}

func channelClosed(channel <-chan struct{}) bool {
	select {
	case <-channel:
		return true
	default:
		return false
	}
}

func (w *Wrapper) Get(_ context.Context, key string) (string, bool, error) {
	item := w.cache.Get(key)
	if item == nil {
		return "", false, nil
	}
	return item.Value(), true, nil
}

func (w *Wrapper) Set(_ context.Context, key string, value string, ttl time.Duration) error {
	w.cache.Set(key, value, normalizedTTL(ttl))
	return nil
}

func (w *Wrapper) SetIfAbsent(
	_ context.Context,
	key string,
	value string,
	ttl time.Duration,
) (bool, error) {
	_, found := w.cache.GetOrSet(
		key,
		value,
		ttlcache.WithTTL[string, string](normalizedTTL(ttl)),
	)
	return !found, nil
}

func (w *Wrapper) AllowRateLimit(
	ctx context.Context,
	key string,
	interval time.Duration,
	burst int,
	ttl time.Duration,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	ttl, err := tools.RateLimitTTL(interval, burst, ttl)
	if err != nil {
		return false, err
	}
	item, _ := w.rateLimitCache.GetOrSetFunc(
		key,
		func() *ratepkg.Limiter {
			return ratepkg.NewLimiter(ratepkg.Every(interval), burst)
		},
		ttlcache.WithTTL[string, *ratepkg.Limiter](ttl),
	)
	return item.Value().Allow(), nil
}

func (w *Wrapper) Invalidate(_ context.Context, key string) error {
	w.cache.Delete(key)
	return nil
}

func normalizedTTL(ttl time.Duration) time.Duration {
	if ttl == 0 {
		return ttlcache.NoTTL
	}
	return ttl
}
