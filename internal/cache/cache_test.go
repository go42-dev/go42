package cache_test

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go42-dev/go42/internal/cache"
)

type cacheTestRecord struct {
	ID    int
	Name  string
	Roles []string
}

func TestCacheCodecRoundTrip(t *testing.T) {
	t.Run("record", func(t *testing.T) {
		assertCacheRoundTrip(t, cacheTestRecord{42, "review 数据", []string{"read", "write"}}, 1500*time.Millisecond)
	})
	t.Run("empty record", func(t *testing.T) {
		assertCacheRoundTrip(t, cacheTestRecord{}, cache.NoCache)
	})
	t.Run("slice", func(t *testing.T) {
		assertCacheRoundTrip(t, []int{-1, 0, 42}, time.Hour)
	})
	t.Run("map", func(t *testing.T) {
		assertCacheRoundTrip(t, map[string]string{"first": "one", "second": "two"}, cache.NoCache)
	})
	t.Run("binary", func(t *testing.T) {
		assertCacheRoundTrip(t, []byte{0, 1, 127, 255}, 250*time.Millisecond)
	})
	t.Run("empty string", func(t *testing.T) {
		assertCacheRoundTrip(t, "", time.Minute)
	})
	t.Run("zero number", func(t *testing.T) {
		assertCacheRoundTrip(t, 0, time.Minute)
	})
}

func TestGetDecodeDistinguishesMissingFromEmpty(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		found bool
		want  *cacheTestRecord
	}{
		{name: "missing", value: "ignored invalid gob"},
		{name: "empty", value: cacheGobString(t, cacheTestRecord{}), found: true, want: &cacheTestRecord{}},
		{
			name: "populated", value: cacheGobString(t, cacheTestRecord{ID: 42}), found: true,
			want: &cacheTestRecord{ID: 42},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			getter := cacheGetterFunc(func(gotCtx context.Context, key string) (string, bool, error) {
				assert.Same(t, ctx, gotCtx)
				assert.Equal(t, "record", key)
				return test.value, test.found, nil
			})
			value, err := cache.GetDecode[*cacheTestRecord](ctx, getter, "record")
			require.NoError(t, err)
			assert.Equal(t, test.want, value)
		})
	}
}

func TestGetDecodeReturnsZeroForMissingValue(t *testing.T) {
	getter := cacheGetterFunc(func(context.Context, string) (string, bool, error) {
		return "ignored invalid gob", false, nil
	})
	value, err := cache.GetDecode[cacheTestRecord](t.Context(), getter, "missing")
	require.NoError(t, err)
	assert.Equal(t, cacheTestRecord{}, value)
}

func TestGetDecodeRejectsInvalidData(t *testing.T) {
	encoded := cacheGobString(t, cacheTestRecord{ID: 42})
	for _, test := range []struct {
		name  string
		value string
		cause error
	}{
		{name: "empty", cause: io.EOF},
		{name: "corrupt", value: "invalid gob"},
		{name: "truncated", value: encoded[:len(encoded)-1], cause: io.ErrUnexpectedEOF},
		{name: "incompatible type", value: cacheGobString(t, "a string instead of a record")},
	} {
		t.Run(test.name, func(t *testing.T) {
			getter := cacheGetterFunc(func(context.Context, string) (string, bool, error) {
				return test.value, true, nil
			})
			_, err := cache.GetDecode[cacheTestRecord](t.Context(), getter, "record")
			require.ErrorContains(t, err, "gob decode failed:")
			if test.cause != nil {
				assert.ErrorIs(t, err, test.cause)
			}
		})
	}
}

func TestSetEncodeDoesNotWriteOnEncodingFailure(t *testing.T) {
	cause := errors.New("custom encoding failed")
	for _, test := range []struct {
		name  string
		value any
		cause error
	}{
		{name: "unsupported value", value: make(chan int)},
		{name: "custom encoder failure", value: cacheEncodingFailure{cause}, cause: cause},
	} {
		t.Run(test.name, func(t *testing.T) {
			setter := cacheSetterFunc(func(context.Context, string, string, time.Duration) error {
				t.Error("cache write attempted after encoding failed")
				return nil
			})
			err := cache.SetEncode(t.Context(), setter, "record", test.value, time.Minute)
			require.ErrorContains(t, err, "gob encode failed:")
			if test.cause != nil {
				assert.ErrorIs(t, err, test.cause)
			}
		})
	}
}

func TestCacheCodecPropagatesBackendErrors(t *testing.T) {
	for _, cause := range []error{errors.New("cache unavailable"), context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			ctx := t.Context()
			if errors.Is(cause, context.Canceled) {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			} else if errors.Is(cause, context.DeadlineExceeded) {
				expired, cancel := context.WithDeadline(ctx, time.Unix(0, 0))
				defer cancel()
				ctx = expired
			}
			t.Run("get", func(t *testing.T) {
				getter := cacheGetterFunc(func(gotCtx context.Context, key string) (string, bool, error) {
					assert.Same(t, ctx, gotCtx)
					assert.Equal(t, "record", key)
					return "invalid gob must not be decoded after a backend error", true, cause
				})
				value, err := cache.GetDecode[cacheTestRecord](ctx, getter, "record")
				assert.ErrorIs(t, err, cause)
				assert.Equal(t, cacheTestRecord{}, value)
			})
			t.Run("set", func(t *testing.T) {
				setter := cacheSetterFunc(func(gotCtx context.Context, key, value string, ttl time.Duration) error {
					assert.Same(t, ctx, gotCtx)
					assert.Equal(t, "record", key)
					assert.Equal(t, 1500*time.Millisecond, ttl)
					var decoded cacheTestRecord
					require.NoError(t, gob.NewDecoder(bytes.NewBufferString(value)).Decode(&decoded))
					assert.Equal(t, cacheTestRecord{ID: 42}, decoded)
					return cause
				})
				err := cache.SetEncode(ctx, setter, "record", cacheTestRecord{ID: 42}, 1500*time.Millisecond)
				assert.ErrorIs(t, err, cause)
			})
		})
	}
}

func assertCacheRoundTrip[T any](t *testing.T, value T, ttl time.Duration) {
	t.Helper()
	ctx := t.Context()
	var stored string
	writes := 0
	setter := cacheSetterFunc(func(gotCtx context.Context, key, encoded string, gotTTL time.Duration) error {
		writes++
		assert.Same(t, ctx, gotCtx)
		assert.Equal(t, "record", key)
		assert.Equal(t, ttl, gotTTL)
		stored = encoded
		var decoded T
		require.NoError(t, gob.NewDecoder(bytes.NewBufferString(encoded)).Decode(&decoded))
		assert.Equal(t, value, decoded)
		return nil
	})
	require.NoError(t, cache.SetEncode(ctx, setter, "record", value, ttl))
	assert.Equal(t, 1, writes)
	reads := 0
	getter := cacheGetterFunc(func(gotCtx context.Context, key string) (string, bool, error) {
		reads++
		assert.Same(t, ctx, gotCtx)
		assert.Equal(t, "record", key)
		return stored, true, nil
	})
	decoded, err := cache.GetDecode[T](ctx, getter, "record")
	require.NoError(t, err)
	assert.Equal(t, value, decoded)
	assert.Equal(t, 1, reads)
}

func cacheGobString(t *testing.T, value any) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, gob.NewEncoder(&buf).Encode(value))
	return buf.String()
}

type cacheEncodingFailure struct{ err error }

func (v cacheEncodingFailure) GobEncode() ([]byte, error) { return nil, v.err }

type cacheGetterFunc func(context.Context, string) (string, bool, error)

func (f cacheGetterFunc) Get(ctx context.Context, key string) (string, bool, error) {
	return f(ctx, key)
}

type cacheSetterFunc func(context.Context, string, string, time.Duration) error

func (f cacheSetterFunc) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return f(ctx, key, value, ttl)
}
