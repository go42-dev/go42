package memcached

import (
	"math"
	"testing"
	"time"
)

func TestExpirationSeconds(t *testing.T) {
	const nowSeconds int64 = 1_700_000_000
	for _, test := range []struct {
		name    string
		ttl     time.Duration
		want    int32
		wantErr bool
	}{
		{name: "subsecond rounds up", ttl: time.Nanosecond, want: 1},
		{name: "whole second", ttl: time.Second, want: 1},
		{name: "fractional second rounds up", ttl: 1500 * time.Millisecond, want: 2},
		{name: "rounds to thirty days", ttl: 30*24*time.Hour - time.Nanosecond, want: 2_592_000},
		{name: "exactly thirty days", ttl: 30 * 24 * time.Hour, want: 2_592_000},
		{name: "just over thirty days", ttl: 30*24*time.Hour + time.Nanosecond, want: 1_702_592_001},
		{name: "thirty-one days", ttl: 31 * 24 * time.Hour, want: 1_702_678_400},
		{
			name: "largest absolute expiration",
			ttl:  time.Duration(math.MaxInt32-nowSeconds) * time.Second,
			want: math.MaxInt32,
		},
		{
			name:    "rounding overflows absolute expiration",
			ttl:     time.Duration(math.MaxInt32-nowSeconds)*time.Second + time.Nanosecond,
			wantErr: true,
		},
		{name: "maximum duration exceeds expiration range", ttl: time.Duration(math.MaxInt64), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := expirationSeconds(nowSeconds*1_000_000, test.ttl, 0)
			if (err != nil) != test.wantErr {
				t.Fatalf("expirationSeconds() error = %v, wantErr %v", err, test.wantErr)
			}
			if got != test.want {
				t.Errorf("expirationSeconds() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestNewItemWithoutExpiration(t *testing.T) {
	for _, ttl := range []time.Duration{0, -time.Second, time.Duration(math.MinInt64)} {
		t.Run(ttl.String(), func(t *testing.T) {
			item, err := newItem("key", "value", ttl)
			if err != nil {
				t.Fatal(err)
			}
			if item.Expiration != 0 {
				t.Errorf("Expiration = %d, want 0", item.Expiration)
			}
		})
	}
}

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
