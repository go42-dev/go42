//go:build resilience

package resilience

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/go42-dev/go42/internal/cache"
	"github.com/go42-dev/go42/internal/cache/local"
	"github.com/go42-dev/go42/internal/cache/memcached"
	"github.com/go42-dev/go42/internal/cache/redis"
)

const (
	cacheContractTimeout = 15 * time.Second
	cacheEntryTTL        = time.Second
)

type cacheContractFactory struct {
	name string
	open func(t *testing.T) cache.Engine
}

func TestCacheEngineContract(t *testing.T) {
	for _, factory := range cacheContractFactories() {
		t.Run(factory.name, func(t *testing.T) {
			engine := factory.open(t)
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := engine.Shutdown(ctx); err != nil {
					t.Errorf("shutdown cache: %v", err)
				}
			})
			assertCacheReadWriteContract(t, engine)
			assertCacheExpirationContract(t, engine)
			assertCacheSetIfAbsentContract(t, engine)
			assertCacheRateLimitContract(t, engine)
		})
	}
}

func cacheContractFactories() []cacheContractFactory {
	return []cacheContractFactory{
		{
			name: "local",
			open: func(t *testing.T) cache.Engine {
				t.Helper()
				return local.New()
			},
		},
		{
			name: "redis",
			open: func(t *testing.T) cache.Engine {
				t.Helper()
				resetProxy(t, proxyConfig{
					Name: redisProxyName, Listen: "0.0.0.0:16379", Upstream: "redis:6379", Enabled: true,
				})
				ctx, cancel := context.WithTimeout(t.Context(), cacheContractTimeout)
				defer cancel()
				engine, err := redis.Open(
					ctx,
					envOrDefault(redisAddressEnv, defaultRedisAddress),
					0,
					redis.WithLogger(slog.New(slog.DiscardHandler)),
					redis.WithConnectRetryTimeout(cacheContractTimeout),
					redis.WithConnectRetryBackoff(50*time.Millisecond, 200*time.Millisecond),
				)
				if err != nil {
					t.Fatalf("open Redis: %v", err)
				}
				return engine
			},
		},
		{
			name: "memcached",
			open: func(t *testing.T) cache.Engine {
				t.Helper()
				resetProxy(t, proxyConfig{
					Name: memcachedProxyName, Listen: "0.0.0.0:11212", Upstream: "memcached:11211", Enabled: true,
				})
				ctx, cancel := context.WithTimeout(t.Context(), cacheContractTimeout)
				defer cancel()
				engine, err := memcached.Open(
					ctx,
					[]string{envOrDefault(memcachedAddressEnv, defaultMemcachedAddress)},
					memcached.WithLogger(slog.New(slog.DiscardHandler)),
					memcached.WithConnectRetryTimeout(cacheContractTimeout),
					memcached.WithConnectRetryBackoff(50*time.Millisecond, 200*time.Millisecond),
					memcached.WithTimeout(time.Second),
				)
				if err != nil {
					t.Fatalf("open Memcached: %v", err)
				}
				return engine
			},
		},
	}
}

func assertCacheReadWriteContract(t *testing.T, engine cache.Engine) {
	t.Helper()
	key := uniqueCacheKey("read-write")
	ctx := t.Context()

	if value, found, err := engine.Get(ctx, key); err != nil || found || value != "" {
		t.Fatalf("Get(missing) = (%q, %t, %v), want (empty, false, nil)", value, found, err)
	}
	if err := engine.Set(ctx, key, "value", cache.NoCache); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	assertCachedValue(t, engine, key, "value")
	if err := engine.Invalidate(ctx, key); err != nil {
		t.Fatalf("Invalidate() error = %v", err)
	}
	if err := engine.Invalidate(ctx, key); err != nil {
		t.Fatalf("Invalidate(missing) error = %v", err)
	}
	assertCacheMiss(t, engine, key)
}

func assertCacheExpirationContract(t *testing.T, engine cache.Engine) {
	t.Helper()
	key := uniqueCacheKey("expiration")
	if err := engine.Set(t.Context(), key, "value", cacheEntryTTL); err != nil {
		t.Fatalf("Set(TTL) error = %v", err)
	}
	assertCachedValue(t, engine, key, "value")

	deadline := time.Now().Add(3 * cacheEntryTTL)
	for time.Now().Before(deadline) {
		_, found, err := engine.Get(t.Context(), key)
		if err != nil {
			t.Fatalf("Get(expiring) error = %v", err)
		}
		if !found {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("cache entry remained available after %s", cacheEntryTTL)
}

func assertCacheSetIfAbsentContract(t *testing.T, engine cache.Engine) {
	t.Helper()
	key := uniqueCacheKey("set-if-absent")
	const contenders = 16
	start := make(chan struct{})
	winners := make(chan string, contenders)
	var waitGroup sync.WaitGroup
	waitGroup.Add(contenders)

	for index := range contenders {
		go func() {
			defer waitGroup.Done()
			<-start
			value := fmt.Sprintf("value-%d", index)
			ok, err := engine.SetIfAbsent(
				t.Context(), key, value, cacheEntryTTL,
			)
			if err != nil {
				t.Errorf("SetIfAbsent() error = %v", err)
				return
			}
			if ok {
				winners <- value
			}
		}()
	}
	close(start)
	waitGroup.Wait()
	close(winners)

	var winningValues []string
	for value := range winners {
		winningValues = append(winningValues, value)
	}
	if len(winningValues) != 1 {
		t.Fatalf("SetIfAbsent() stored %d values, want 1", len(winningValues))
	}
	assertCachedValue(t, engine, key, winningValues[0])

	stored, err := engine.SetIfAbsent(t.Context(), key, "replacement", cacheEntryTTL)
	if err != nil {
		t.Fatalf("SetIfAbsent(existing) error = %v", err)
	}
	if stored {
		t.Error("SetIfAbsent(existing) = true, want false")
	}
	assertCachedValue(t, engine, key, winningValues[0])
}

func assertCacheRateLimitContract(t *testing.T, engine cache.Engine) {
	t.Helper()
	key := uniqueCacheKey("rate-limit")

	for request := range 3 {
		allowed, err := engine.AllowRateLimit(t.Context(), key, time.Second, 2, 5*time.Second)
		if err != nil {
			t.Fatalf("AllowRateLimit(%d) error = %v", request, err)
		}
		wantAllowed := request < 2
		if allowed != wantAllowed {
			t.Errorf("AllowRateLimit(%d) = %t, want %t", request, allowed, wantAllowed)
		}
	}
}

func assertCachedValue(t *testing.T, engine cache.Engine, key, want string) {
	t.Helper()
	value, found, err := engine.Get(t.Context(), key)
	if err != nil || !found || value != want {
		t.Fatalf("Get() = (%q, %t, %v), want (%q, true, nil)", value, found, err, want)
	}
}

func assertCacheMiss(t *testing.T, engine cache.Engine, key string) {
	t.Helper()
	value, found, err := engine.Get(t.Context(), key)
	if err != nil || found || value != "" {
		t.Fatalf("Get() = (%q, %t, %v), want (empty, false, nil)", value, found, err)
	}
}

func uniqueCacheKey(prefix string) string {
	return fmt.Sprintf("resilience:%s:%d", prefix, time.Now().UnixNano())
}
