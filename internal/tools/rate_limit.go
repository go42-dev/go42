package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"time"
)

const rateLimitCacheKeyPrefix = "rate_limit:"

//go:generate mockgen -source $GOFILE -package mocks -destination mocks/rate_limit.go

type cacheAccessor interface {
	AllowRateLimit(
		ctx context.Context,
		key string,
		interval time.Duration,
		burst int,
		ttl time.Duration,
	) (allowed bool, err error)
}

// RateLimiter applies a cache-backed rate limit to hashed, namespaced client keys.
type RateLimiter struct {
	cache     cacheAccessor
	namespace string
	rate      int
	window    time.Duration
	burst     int
	ttl       time.Duration
}

type RateLimiterOption func(*RateLimiter)

// WithRateLimitWindow sets the period over which the configured rate refills.
// The default window is one second.
func WithRateLimitWindow(window time.Duration) RateLimiterOption {
	return func(m *RateLimiter) { m.window = window }
}

func NewRateLimiter(
	cache cacheAccessor,
	namespace string,
	rate int,
	burst int,
	ttl time.Duration,
	opts ...RateLimiterOption,
) *RateLimiter {
	if namespace == "" {
		namespace = "default"
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	m := &RateLimiter{
		cache:     cache,
		namespace: namespace,
		rate:      rate,
		window:    time.Second,
		burst:     burst,
		ttl:       ttl,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func (m *RateLimiter) Limit(ctx context.Context, key string) (bool, error) {
	if key == "" {
		// forbid empty keys - treat them as mistake
		return false, nil
	}
	if m.cache == nil {
		return false, fmt.Errorf("rate limiter cache is nil")
	}
	if m.rate <= 0 {
		return false, fmt.Errorf("rate must be positive")
	}
	if m.burst <= 0 {
		return false, fmt.Errorf("burst must be positive")
	}
	if m.window <= 0 {
		return false, fmt.Errorf("rate limit window must be positive")
	}
	interval := m.window / time.Duration(m.rate)
	if m.window%time.Duration(m.rate) != 0 {
		interval++
	}
	ttl, err := RateLimitTTL(interval, m.burst, m.ttl)
	if err != nil {
		return false, err
	}

	sum := sha256.Sum256([]byte(key))
	cacheKey := rateLimitCacheKeyPrefix + m.namespace + ":" + hex.EncodeToString(sum[:])
	return m.cache.AllowRateLimit(ctx, cacheKey, interval, m.burst, ttl)
}

// RateLimitTTL retains a bucket until it could have refilled completely.
func RateLimitTTL(interval time.Duration, burst int, ttl time.Duration) (time.Duration, error) {
	if interval <= 0 || burst <= 0 || ttl <= 0 {
		return 0, fmt.Errorf("rate limit interval, burst and TTL must be positive")
	}
	if int64(burst) > math.MaxInt64/int64(interval) {
		return 0, fmt.Errorf("rate limit refill duration overflows")
	}
	return max(ttl, interval*time.Duration(burst)), nil
}
