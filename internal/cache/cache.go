package cache

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"time"
)

// NoCache is passed as TTL to indicate that no expiration should be set.
const NoCache = time.Duration(0)

type Getter interface {
	Get(ctx context.Context, key string) (value string, found bool, err error)
}

type Setter interface {
	Set(ctx context.Context, key string, value string, ttl time.Duration) error
}

type Cache interface {
	Getter
	Setter
	SetIfAbsent(ctx context.Context, key string, value string, ttl time.Duration) (stored bool, err error)
	// AllowRateLimit consumes one token, refilling one token per interval up to burst.
	AllowRateLimit(
		ctx context.Context, key string, interval time.Duration, burst int, ttl time.Duration,
	) (allowed bool, err error)
	Invalidate(ctx context.Context, key string) error
}

type Engine interface {
	Cache
	Ping(ctx context.Context) error
	Shutdown(ctx context.Context) error
}

// ----

var ErrSetIfAbsentUnsupported = errors.New("cache does not support atomic set-if-absent")

// NoopCache is a no-op implementation of Cache intended for tests.
type NoopCache struct{}

func NewNoop() *NoopCache {
	return &NoopCache{}
}

func (NoopCache) Get(_ context.Context, _ string) (string, bool, error) {
	return "", false, nil
}

func (NoopCache) Set(_ context.Context, _, _ string, _ time.Duration) error {
	return nil
}

func (NoopCache) SetIfAbsent(
	_ context.Context,
	_, _ string,
	_ time.Duration,
) (bool, error) {
	return false, ErrSetIfAbsentUnsupported
}

func (NoopCache) AllowRateLimit(
	_ context.Context,
	_ string,
	_ time.Duration,
	_ int,
	_ time.Duration,
) (bool, error) {
	return true, nil
}

func (NoopCache) Invalidate(_ context.Context, _ string) error {
	return nil
}

func (NoopCache) Shutdown(_ context.Context) error {
	return nil
}

func (NoopCache) Ping(ctx context.Context) error {
	return ctx.Err()
}

// ----

// SetEncode serializes a value of type T using gob encoding and stores it in the cache.
// Value should be passed by value, not by pointer.
func SetEncode[T any](ctx context.Context, cache Setter, key string, value T, ttl time.Duration) error {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(value); err != nil {
		return fmt.Errorf("gob encode failed: %w", err)
	}
	return cache.Set(ctx, key, buf.String(), ttl)
}

// GetDecode retrieves a value of type T from the cache, decoding it from gob format.
// If the value is not found, it returns the zero value of T and no error.
// Best to use pointer to T, to distinguish between "not found" and "found but empty" cases.
func GetDecode[T any](ctx context.Context, cache Getter, key string) (T, error) {
	var out T
	str, found, err := cache.Get(ctx, key)
	if err != nil {
		return out, err
	}
	if !found {
		return out, nil
	}
	err = gob.NewDecoder(bytes.NewBufferString(str)).Decode(&out)
	if err != nil {
		return out, fmt.Errorf("gob decode failed: %w", err)
	}
	return out, nil
}
