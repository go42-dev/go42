package tools

import (
	"errors"
	"math"
)

// Pagination defaults and bounds apply to paginated API operations across the project.
const (
	PaginationDefaultLimit  = 10
	PaginationMaximumLimit  = 100
	PaginationMaximumOffset = math.MaxInt32
)

var ErrInvalidPagination = errors.New("invalid pagination")

type Pagination struct {
	Limit  int
	Offset int
}

// NormalizePagination applies the default limit for zero and rejects values outside
// the shared bounds. Adapters call it after decoding input and before invoking services.
func NormalizePagination(limit, offset int) (Pagination, error) {
	if limit == 0 {
		limit = PaginationDefaultLimit
	}
	if limit < 0 || limit > PaginationMaximumLimit || offset < 0 || offset > PaginationMaximumOffset {
		return Pagination{}, ErrInvalidPagination
	}
	return Pagination{Limit: limit, Offset: offset}, nil
}
