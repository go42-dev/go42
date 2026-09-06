package memcached

import (
	"math"
	"testing"
	"time"
)

func FuzzRateLimitExpiration(f *testing.F) {
	const nowMicros int64 = 1_700_000_000_000_000
	for _, ttl := range []time.Duration{
		time.Nanosecond,
		time.Second,
		1500 * time.Millisecond,
		30*24*time.Hour - time.Second,
		30*24*time.Hour - time.Second + time.Nanosecond,
		30 * 24 * time.Hour,
		time.Duration(math.MaxInt32-1_700_000_000-1) * time.Second,
		time.Duration(math.MaxInt32-1_700_000_000) * time.Second,
		time.Duration(math.MaxInt64),
	} {
		f.Add(nowMicros, int64(ttl))
	}
	f.Add(int64(math.MinInt64), int64(31*24*time.Hour))
	f.Add(int64(math.MaxInt64), int64(31*24*time.Hour))

	f.Fuzz(func(t *testing.T, nowMicros, ttlNanos int64) {
		// The caller validates that the TTL is positive before encoding it.
		if ttlNanos <= 0 {
			t.Skip()
		}
		ttl := time.Duration(ttlNanos)
		expiration, err := rateLimitExpiration(nowMicros, ttl)

		// Decode the protocol value into a time window. time.Time arithmetic
		// keeps the oracle independent of duration and int32 overflow checks.
		anchor := time.Unix(0, 0)
		if ttl > 30*24*time.Hour-time.Second {
			anchor = time.Unix(nowMicros/1_000_000, 0)
		}
		earliest := anchor.Add(ttl).Add(time.Second)
		latest := earliest.Add(time.Second)
		outOfRange := earliest.After(time.Unix(math.MaxInt32, 0)) ||
			!latest.After(time.Unix(math.MinInt32, 0))
		if (err != nil) != outOfRange {
			t.Fatalf("expiration error = %v, unrepresentable time window = %v", err, outOfRange)
		}
		if err != nil {
			return
		}
		decoded := time.Unix(int64(expiration), 0)
		if decoded.Before(earliest) || !decoded.Before(latest) {
			t.Fatalf("expiration %v outside retention window [%v, %v)", decoded, earliest, latest)
		}
	})
}
