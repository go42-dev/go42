package memcached

import (
	"math"
	"testing"
	"time"
)

func TestRateLimitExpiration(t *testing.T) {
	const nowSeconds int64 = 1_700_000_000

	tests := []struct {
		name      string
		nowMicros int64
		ttl       time.Duration
		want      int32
		wantErr   bool
	}{
		{
			name:      "subsecond TTL rounds up with clock padding",
			nowMicros: nowSeconds * 1_000_000,
			ttl:       time.Nanosecond,
			want:      2,
		},
		{
			name:      "whole second TTL includes clock padding",
			nowMicros: nowSeconds * 1_000_000,
			ttl:       time.Second,
			want:      2,
		},
		{
			name:      "fractional TTL rounds up with clock padding",
			nowMicros: nowSeconds * 1_000_000,
			ttl:       1500 * time.Millisecond,
			want:      3,
		},
		{
			name:      "largest relative expiration",
			nowMicros: nowSeconds * 1_000_000,
			ttl:       30*24*time.Hour - time.Second,
			want:      2_592_000,
		},
		{
			name:      "rounding crosses absolute expiration threshold",
			nowMicros: nowSeconds * 1_000_000,
			ttl:       30*24*time.Hour - time.Second + time.Nanosecond,
			want:      1_702_592_001,
		},
		{
			name:      "clock padding crosses absolute expiration threshold",
			nowMicros: nowSeconds * 1_000_000,
			ttl:       30 * 24 * time.Hour,
			want:      1_702_592_001,
		},
		{
			name:      "largest absolute expiration",
			nowMicros: nowSeconds * 1_000_000,
			ttl:       time.Duration(math.MaxInt32-nowSeconds-1) * time.Second,
			want:      math.MaxInt32,
		},
		{
			name:      "absolute expiration overflows int32",
			nowMicros: nowSeconds * 1_000_000,
			ttl:       time.Duration(math.MaxInt32-nowSeconds) * time.Second,
			wantErr:   true,
		},
		{
			name:      "maximum TTL exceeds expiration range",
			nowMicros: nowSeconds * 1_000_000,
			ttl:       time.Duration(math.MaxInt64),
			wantErr:   true,
		},
		{
			name:      "relative expiration underflows int32",
			nowMicros: nowSeconds * 1_000_000,
			ttl:       time.Duration(math.MinInt64),
			wantErr:   true,
		},
		{
			name:      "absolute expiration underflows int32",
			nowMicros: math.MinInt64,
			ttl:       31 * 24 * time.Hour,
			wantErr:   true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := rateLimitExpiration(test.nowMicros, test.ttl)
			if (err != nil) != test.wantErr {
				t.Fatalf("rateLimitExpiration() error = %v, wantErr %v", err, test.wantErr)
			}
			if got != test.want {
				t.Errorf("rateLimitExpiration() = %d, want %d", got, test.want)
			}
		})
	}
}
