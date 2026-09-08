package local_test

import (
	"context"
	"errors"
	"math"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go42-dev/go42/internal/cache/local"
)

func TestCacheReadsDoNotExtendExpiration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := newLocalTestCache(t)
		require.NoError(t, cache.Set(t.Context(), "item", "value", time.Hour))
		time.Sleep(30 * time.Minute)
		assertLocalEntry(t, cache, "item", "value", true)
		time.Sleep(30*time.Minute - time.Nanosecond)
		assertLocalEntry(t, cache, "item", "value", true)
		time.Sleep(2 * time.Nanosecond)
		assertLocalEntry(t, cache, "item", "", false)
	})
}

func TestCacheZeroTTLPreservesEmptyValues(t *testing.T) {
	for _, operation := range []string{"set", "set if absent"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cache := newLocalTestCache(t)
				assertLocalEntry(t, cache, "item", "", false)
				if operation == "set" {
					require.NoError(t, cache.Set(t.Context(), "item", "", 0))
				} else {
					stored, err := cache.SetIfAbsent(t.Context(), "item", "", 0)
					require.NoError(t, err)
					require.True(t, stored)
				}
				time.Sleep(24 * time.Hour)
				assertLocalEntry(t, cache, "item", "", true)
				stored, err := cache.SetIfAbsent(t.Context(), "item", "replacement", time.Hour)
				require.NoError(t, err)
				assert.False(t, stored)
				time.Sleep(2 * time.Hour)
				assertLocalEntry(t, cache, "item", "", true)
			})
		})
	}
}

func TestCacheOverwriteReplacesValueAndExpiration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := newLocalTestCache(t)
		require.NoError(t, cache.Set(t.Context(), "item", "first", time.Hour))
		time.Sleep(30 * time.Minute)
		require.NoError(t, cache.Set(t.Context(), "item", "second", 2*time.Hour))
		time.Sleep(time.Hour)
		assertLocalEntry(t, cache, "item", "second", true)
		time.Sleep(time.Hour + time.Nanosecond)
		assertLocalEntry(t, cache, "item", "", false)
	})
}

func TestCacheInvalidationOnlyRemovesMatchingKey(t *testing.T) {
	cache := newLocalTestCache(t)
	require.NoError(t, cache.Set(t.Context(), "first", "one", 0))
	require.NoError(t, cache.Set(t.Context(), "second", "two", 0))
	require.NoError(t, cache.Invalidate(t.Context(), "first"))
	require.NoError(t, cache.Invalidate(t.Context(), "first"))
	require.NoError(t, cache.Invalidate(t.Context(), "missing"))
	assertLocalEntry(t, cache, "first", "", false)
	assertLocalEntry(t, cache, "second", "two", true)
	stored, err := cache.SetIfAbsent(t.Context(), "first", "replacement", 0)
	require.NoError(t, err)
	assert.True(t, stored)
	assertLocalEntry(t, cache, "first", "replacement", true)
}

func TestCacheSetIfAbsentPreservesExistingExpiration(t *testing.T) {
	for _, replacementTTL := range []time.Duration{0, 3 * time.Hour} {
		t.Run(replacementTTL.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cache := newLocalTestCache(t)
				stored, err := cache.SetIfAbsent(t.Context(), "item", "first", time.Hour)
				require.NoError(t, err)
				require.True(t, stored)
				time.Sleep(30 * time.Minute)
				stored, err = cache.SetIfAbsent(t.Context(), "item", "replacement", replacementTTL)
				require.NoError(t, err)
				assert.False(t, stored)
				assertLocalEntry(t, cache, "item", "first", true)
				time.Sleep(30*time.Minute + time.Nanosecond)
				stored, err = cache.SetIfAbsent(t.Context(), "item", "after expiry", time.Hour)
				require.NoError(t, err)
				assert.True(t, stored)
				assertLocalEntry(t, cache, "item", "after expiry", true)
				time.Sleep(time.Hour + time.Nanosecond)
				assertLocalEntry(t, cache, "item", "", false)
			})
		})
	}
}

func TestCacheSetIfAbsentHasOneConcurrentWinner(t *testing.T) {
	cache := newLocalTestCache(t)
	const workers = 32
	type result struct {
		value  string
		stored bool
		err    error
	}
	results := make(chan result, workers)
	start := make(chan struct{})
	ctx := t.Context()
	for worker := range workers {
		go func() {
			<-start
			value := strconv.Itoa(worker)
			stored, err := cache.SetIfAbsent(ctx, "shared", value, 0)
			results <- result{value: value, stored: stored, err: err}
		}()
	}
	close(start)
	var winners []string
	for range workers {
		result := <-results
		assert.NoError(t, result.err)
		if result.stored {
			winners = append(winners, result.value)
		}
	}
	require.Len(t, winners, 1)
	assertLocalEntry(t, cache, "shared", winners[0], true)
}

func TestCacheCapacityEvictsLeastRecentlyUsedEntry(t *testing.T) {
	cache := newLocalTestCache(t, local.WithCapacity(2))
	require.NoError(t, cache.Set(t.Context(), "a", "one", 0))
	require.NoError(t, cache.Set(t.Context(), "b", "two", 0))
	assertLocalEntry(t, cache, "a", "one", true)
	require.NoError(t, cache.Set(t.Context(), "c", "three", 0))
	assertLocalEntry(t, cache, "a", "one", true)
	assertLocalEntry(t, cache, "b", "", false)
	assertLocalEntry(t, cache, "c", "three", true)
}

func TestCacheByteLimitIncludesKeysAndUpdatedValues(t *testing.T) {
	cache := newLocalTestCache(t, local.WithCapacity(0), local.WithMaxCostBytes(6))
	require.NoError(t, cache.Set(t.Context(), "a", "12", 0))
	require.NoError(t, cache.Set(t.Context(), "bb", "3", 0))
	assertLocalEntry(t, cache, "bb", "3", true)
	assertLocalEntry(t, cache, "a", "12", true)
	require.NoError(t, cache.Set(t.Context(), "c", "4", 0))
	assertLocalEntry(t, cache, "bb", "", false)
	assertLocalEntry(t, cache, "c", "4", true)
	assertLocalEntry(t, cache, "a", "12", true)
	require.NoError(t, cache.Set(t.Context(), "a", "12345", 0))
	assertLocalEntry(t, cache, "c", "", false)
	assertLocalEntry(t, cache, "a", "12345", true)
}

func TestCacheDoesNotRetainAnEntryLargerThanByteLimit(t *testing.T) {
	cache := newLocalTestCache(t, local.WithMaxCostBytes(3))
	require.NoError(t, cache.Set(t.Context(), "é", "12", 0))
	assertLocalEntry(t, cache, "é", "", false)
}

func TestLocalRateLimitRejectsInvalidSettings(t *testing.T) {
	for _, test := range []struct {
		name     string
		interval time.Duration
		burst    int
		ttl      time.Duration
	}{
		{name: "zero interval", burst: 1, ttl: time.Hour},
		{name: "negative interval", interval: -time.Second, burst: 1, ttl: time.Hour},
		{name: "zero burst", interval: time.Hour, ttl: time.Hour},
		{name: "negative burst", interval: time.Hour, burst: -1, ttl: time.Hour},
		{name: "zero TTL", interval: time.Hour, burst: 1},
		{name: "negative TTL", interval: time.Hour, burst: 1, ttl: -time.Second},
		{name: "refill overflow", interval: time.Duration(math.MaxInt64), burst: 2, ttl: time.Hour},
	} {
		t.Run(test.name, func(t *testing.T) {
			cache := newLocalTestCache(t)
			allowed, err := cache.AllowRateLimit(t.Context(), "client", test.interval, test.burst, test.ttl)
			require.Error(t, err)
			assert.False(t, allowed)
			allowed, err = cache.AllowRateLimit(t.Context(), "client", time.Hour, 1, time.Hour)
			require.NoError(t, err)
			assert.True(t, allowed, "invalid settings must not create a bucket")
		})
	}
}

func TestLocalRateLimitCancellationDoesNotConsumeBudget(t *testing.T) {
	for _, wantErr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(wantErr.Error(), func(t *testing.T) {
			cache := newLocalTestCache(t)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if errors.Is(wantErr, context.DeadlineExceeded) {
				ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
				defer cancel()
			}
			allowed, err := cache.AllowRateLimit(ctx, "client", time.Hour, 1, time.Hour)
			require.ErrorIs(t, err, wantErr)
			assert.False(t, allowed)
			allowed, err = cache.AllowRateLimit(t.Context(), "client", time.Hour, 1, time.Hour)
			require.NoError(t, err)
			assert.True(t, allowed)
		})
	}
}

func TestLocalRateLimitSharesBurstAcrossConcurrentCalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := newLocalTestCache(t)
		const workers, burst = 32, 4
		type result struct {
			allowed bool
			err     error
		}
		results := make(chan result, workers)
		start := make(chan struct{})
		ctx := t.Context()
		for range workers {
			go func() {
				<-start
				allowed, err := cache.AllowRateLimit(ctx, "shared", time.Hour, burst, time.Minute)
				results <- result{allowed: allowed, err: err}
			}()
		}
		close(start)
		allowedCount := 0
		for range workers {
			result := <-results
			assert.NoError(t, result.err)
			if result.allowed {
				allowedCount++
			}
		}
		assert.Equal(t, burst, allowedCount)
		time.Sleep(time.Hour - time.Nanosecond)
		allowed, err := cache.AllowRateLimit(ctx, "shared", time.Hour, burst, time.Minute)
		require.NoError(t, err)
		assert.False(t, allowed)
		time.Sleep(time.Nanosecond)
		allowed, err = cache.AllowRateLimit(ctx, "shared", time.Hour, burst, time.Minute)
		require.NoError(t, err)
		assert.True(t, allowed)
		allowed, err = cache.AllowRateLimit(ctx, "shared", time.Hour, burst, time.Minute)
		require.NoError(t, err)
		assert.False(t, allowed)
		allowed, err = cache.AllowRateLimit(ctx, "independent", time.Hour, burst, time.Minute)
		require.NoError(t, err)
		assert.True(t, allowed)
	})
}

func newLocalTestCache(t *testing.T, opts ...local.Option) *local.Wrapper {
	t.Helper()
	cache := local.New(opts...)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, cache.Shutdown(ctx))
	})
	return cache
}

func assertLocalEntry(t *testing.T, cache *local.Wrapper, key, value string, found bool) {
	t.Helper()
	got, exists, err := cache.Get(t.Context(), key)
	require.NoError(t, err)
	assert.Equal(t, found, exists, "key %q", key)
	assert.Equal(t, value, got, "key %q", key)
}
