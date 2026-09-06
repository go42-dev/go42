package tools_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/go42-dev/go42/internal/cache/local"
	"github.com/go42-dev/go42/internal/tools"
	toolsMocks "github.com/go42-dev/go42/internal/tools/mocks"
)

func TestRateLimiter_AllowsWithinLimit(t *testing.T) {
	rl := newRateLimiter(t, 2, 2, time.Minute, 100) // 2 req/sec, burst 2
	key := "user1"
	allowed := 0
	for i := 0; i < 2; i++ {
		if allow(t, rl, key) {
			allowed++
		}
	}
	if allowed != 2 {
		t.Errorf("expected 2 allowed, got %d", allowed)
	}
}

func TestRateLimiter_ExceedsLimit(t *testing.T) {
	rl := newRateLimiter(t, 1, 1, time.Minute, 100) // 1 req/sec, burst 1
	key := "user2"
	if !allow(t, rl, key) {
		t.Error("first request should be allowed")
	}
	if allow(t, rl, key) {
		t.Error("second request should be rate limited")
	}
}

func TestRateLimiter_ResetsAfterTime(t *testing.T) {
	rl := newRateLimiter(t, 1, 1, time.Minute, 100)
	key := "user3"
	if !allow(t, rl, key) {
		t.Error("first request should be allowed")
	}
	if allow(t, rl, key) {
		t.Error("second request should be rate limited")
	}
	time.Sleep(time.Second)
	if !allow(t, rl, key) {
		t.Error("should allow after 1 second")
	}
}

func TestRateLimiter_EmptyKey(t *testing.T) {
	rl := newRateLimiter(t, 1, 1, time.Minute, 100)
	if allow(t, rl, "") {
		t.Error("empty string key should not be allowed")
	}
}

func TestRateLimiter_ConcurrentDistinctKeys(t *testing.T) {
	const (
		workers        = 32
		callsPerWorker = 100
	)

	rl := newRateLimiter(t, 1, 1, time.Minute, workers*callsPerWorker)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(workers)

	for worker := 0; worker < workers; worker++ {
		go func() {
			defer wg.Done()
			<-start

			for call := 0; call < callsPerWorker; call++ {
				key := fmt.Sprintf("worker-%d-call-%d", worker, call)
				allowed, err := rl.Limit(context.Background(), key)
				if err != nil {
					t.Errorf("Limit(%q) error = %v", key, err)
					continue
				}
				if !allowed {
					t.Errorf("first request for key %q was rejected", key)
				}
			}
		}()
	}

	close(start)
	wg.Wait()
}

func TestRateLimiter_RetainsKeysUntilRefill(t *testing.T) {
	const idleTTL = 20 * time.Millisecond
	cache := newLocalCache(t, 100)
	ctx := context.Background()

	allowed, err := cache.AllowRateLimit(ctx, "user", time.Second, 1, idleTTL)
	if err != nil {
		t.Fatalf("first AllowRateLimit() error = %v", err)
	}
	if !allowed {
		t.Fatal("first request should be allowed")
	}
	allowed, err = cache.AllowRateLimit(ctx, "user", time.Second, 1, idleTTL)
	if err != nil {
		t.Fatalf("second AllowRateLimit() error = %v", err)
	}
	if allowed {
		t.Fatal("second request should be rate limited")
	}

	time.Sleep(2 * idleTTL)
	allowed, err = cache.AllowRateLimit(ctx, "user", time.Second, 1, idleTTL)
	if err != nil {
		t.Fatalf("AllowRateLimit() after expiry error = %v", err)
	}
	if allowed {
		t.Error("short idle TTL must not reset a bucket before it refills")
	}
}

func TestRateLimiter_EvictsLeastRecentlyUsedKeyAtCapacity(t *testing.T) {
	rl := newRateLimiter(t, 1, 1, time.Hour, 2)

	if !allow(t, rl, "user-a") || !allow(t, rl, "user-b") {
		t.Fatal("first request for each key should be allowed")
	}
	if allow(t, rl, "user-a") {
		t.Fatal("second request for user-a should be rate limited")
	}
	if !allow(t, rl, "user-c") {
		t.Fatal("first request for user-c should be allowed")
	}
	if allow(t, rl, "user-a") {
		t.Error("recently used user-a should retain its limiter state")
	}
	if !allow(t, rl, "user-b") {
		t.Error("least recently used user-b should receive a fresh limiter")
	}
}

func TestRateLimiter_PropagatesCacheError(t *testing.T) {
	wantErr := errors.New("cache unavailable")
	expectedKey := fmt.Sprintf("rate_limit:test:%x", sha256.Sum256([]byte("user")))
	ctrl := gomock.NewController(t)
	cache := toolsMocks.NewMockcacheAccessor(ctrl)
	cache.EXPECT().
		AllowRateLimit(gomock.Any(), expectedKey, time.Second, 1, time.Minute).
		Return(false, wantErr)

	rl := tools.NewRateLimiter(cache, "test", 1, 1, time.Minute)
	allowed, err := rl.Limit(context.Background(), "user")
	if allowed {
		t.Error("Limit() allowed request on cache failure")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("Limit() error = %v, want %v", err, wantErr)
	}
}

func TestRateLimiter_CustomWindow(t *testing.T) {
	for _, window := range []time.Duration{time.Minute, time.Hour} {
		t.Run(window.String(), func(t *testing.T) {
			limiter := tools.NewRateLimiter(
				newLocalCache(t, 100), "test", 5, 5, time.Millisecond, tools.WithRateLimitWindow(window),
			)
			for range 5 {
				if !allow(t, limiter, "user") {
					t.Fatal("request within window budget should be allowed")
				}
			}
			if allow(t, limiter, "user") {
				t.Fatal("request exceeding window budget should be limited")
			}
		})
	}
}

func TestRateLimiter_NormalizesIntervalAndTTL(t *testing.T) {
	for _, test := range []struct {
		name     string
		rate     int
		burst    int
		window   time.Duration
		interval time.Duration
		ttl      time.Duration
	}{
		{"fractional interval", 3, 3, time.Second, 333333334 * time.Nanosecond, time.Second + 2*time.Nanosecond},
		{"full burst refill", 5, 10, time.Minute, 12 * time.Second, 2 * time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			cache := toolsMocks.NewMockcacheAccessor(gomock.NewController(t))
			cache.EXPECT().
				AllowRateLimit(gomock.Any(), gomock.Any(), test.interval, test.burst, test.ttl).
				Return(true, nil)
			limiter := tools.NewRateLimiter(
				cache, "test", test.rate, test.burst, time.Second, tools.WithRateLimitWindow(test.window),
			)
			if !allow(t, limiter, "user") {
				t.Fatal("first request should be allowed")
			}
		})
	}
}

func TestRateLimiter_InvalidConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		rate   int
		burst  int
		window time.Duration
	}{
		{"zero rate", 0, 1, time.Second},
		{"negative rate", -1, 1, time.Second},
		{"zero burst", 1, 0, time.Second},
		{"negative burst", 1, -1, time.Second},
		{"zero window", 1, 1, 0},
		{"negative window", 1, 1, -time.Second},
		{"refill overflow", 1, 2, time.Duration(math.MaxInt64)},
	} {
		t.Run(test.name, func(t *testing.T) {
			cache := toolsMocks.NewMockcacheAccessor(gomock.NewController(t))
			limiter := tools.NewRateLimiter(
				cache, "test", test.rate, test.burst, time.Minute, tools.WithRateLimitWindow(test.window),
			)
			if allowed, err := limiter.Limit(t.Context(), "user"); allowed || err == nil {
				t.Fatalf("invalid configuration: allowed=%v, error=%v", allowed, err)
			}
		})
	}
}

func TestRateLimitTTLRejectsInvalidConfiguration(t *testing.T) {
	for _, test := range []struct {
		interval time.Duration
		burst    int
		ttl      time.Duration
	}{
		{0, 1, time.Second}, {time.Second, 0, time.Second}, {time.Second, 1, 0},
		{-time.Second, 1, time.Second}, {time.Second, -1, time.Second}, {time.Second, 1, -time.Second},
		{time.Duration(math.MaxInt64), 2, time.Second},
	} {
		if _, err := tools.RateLimitTTL(test.interval, test.burst, test.ttl); err == nil {
			t.Errorf("invalid configuration accepted: %+v", test)
		}
	}
}

func newRateLimiter(
	t *testing.T,
	rate int,
	burst int,
	ttl time.Duration,
	capacity int,
) *tools.RateLimiter {
	t.Helper()

	cache := newLocalCache(t, capacity)
	return tools.NewRateLimiter(cache, "test", rate, burst, ttl)
}

func newLocalCache(t *testing.T, capacity int) *local.Wrapper {
	t.Helper()

	cache := local.New(local.WithCapacity(uint64(capacity)))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := cache.Shutdown(ctx); err != nil {
			t.Errorf("shutdown local cache: %v", err)
		}
	})
	return cache
}

func allow(t *testing.T, limiter *tools.RateLimiter, key string) bool {
	t.Helper()
	allowed, err := limiter.Limit(context.Background(), key)
	if err != nil {
		t.Fatalf("Limit(%q) error = %v", key, err)
	}
	return allowed
}
