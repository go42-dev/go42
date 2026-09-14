package tools

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizePagination(t *testing.T) {
	type testCase struct {
		name    string
		limit   int
		offset  int
		want    Pagination
		wantErr error
	}

	tests := []testCase{
		{name: "defaults", want: Pagination{Limit: 10}},
		{name: "default limit with offset", offset: 7, want: Pagination{Limit: 10, Offset: 7}},
		{name: "minimum limit", limit: 1, want: Pagination{Limit: 1}},
		{name: "explicit page", limit: 20, offset: 40, want: Pagination{Limit: 20, Offset: 40}},
		{name: "maximum limit", limit: 100, want: Pagination{Limit: 100}},
		{
			name: "maximum offset", limit: 1, offset: 2147483647,
			want: Pagination{Limit: 1, Offset: 2147483647},
		},
		{name: "negative limit", limit: -1, wantErr: ErrInvalidPagination},
		{name: "limit above maximum", limit: 101, wantErr: ErrInvalidPagination},
		{name: "negative offset", offset: -1, wantErr: ErrInvalidPagination},
	}
	// An int wider than 32 bits can also represent values above the public offset bound.
	if math.MaxInt > PaginationMaximumOffset {
		aboveMaximumOffset := PaginationMaximumOffset
		aboveMaximumOffset++
		tests = append(tests, testCase{
			name: "offset above maximum", limit: 1, offset: aboveMaximumOffset, wantErr: ErrInvalidPagination,
		})
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizePagination(test.limit, test.offset)

			require.ErrorIs(t, err, test.wantErr)
			assert.Equal(t, test.want, got)
		})
	}
}
