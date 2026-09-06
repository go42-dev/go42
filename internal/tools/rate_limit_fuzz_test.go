package tools_test

import (
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/go42-dev/go42/internal/tools"
)

func FuzzRateLimitTTL(f *testing.F) {
	f.Add(int64(time.Second), 10, int64(time.Minute))
	f.Add(int64(time.Second), 100, int64(time.Minute))
	f.Add(int64(1), 1, int64(1))
	f.Add(int64(math.MaxInt64), 1, int64(1))
	f.Add(int64(math.MaxInt64/2), 2, int64(1))
	f.Add(int64(math.MaxInt64/2+1), 2, int64(1))
	f.Add(int64(0), 1, int64(1))
	f.Add(int64(1), 0, int64(1))
	f.Add(int64(1), 1, int64(0))
	f.Add(int64(math.MinInt64), -1, int64(math.MaxInt64))

	f.Fuzz(func(t *testing.T, intervalNanos int64, burst int, ttlNanos int64) {
		got, err := tools.RateLimitTTL(time.Duration(intervalNanos), burst, time.Duration(ttlNanos))
		// Arbitrary precision multiplication catches wraparound independently
		// of the division guard used by RateLimitTTL.
		refill := new(big.Int).Mul(big.NewInt(intervalNanos), big.NewInt(int64(burst)))
		invalid := intervalNanos <= 0 || burst <= 0 || ttlNanos <= 0 || !refill.IsInt64()
		if (err != nil) != invalid {
			t.Fatalf("RateLimitTTL() error = %v, invalid input = %v", err, invalid)
		}
		if err != nil {
			return
		}
		if int64(got) < ttlNanos || int64(got) < refill.Int64() {
			t.Fatalf("TTL %d expires before requested TTL %d or refill %v", got, ttlNanos, refill)
		}
		if int64(got) != ttlNanos && int64(got) != refill.Int64() {
			t.Fatalf("TTL %d exceeds both requested TTL %d and refill %v", got, ttlNanos, refill)
		}
	})
}
