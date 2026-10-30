package workers

import (
	"context"
	"time"

	"github.com/go42-dev/go42/internal/auth/domain"
	"github.com/go42-dev/go42/internal/auth/models"
)

//go:generate go tool mockgen -source $GOFILE -package mocks -destination mocks/mocks.go

type repository interface {
	WithTransaction(ctx context.Context, fn func(txCtx context.Context) error) error
	UpdateTokenLastUsed(ctx context.Context, tokenID int, when time.Time) error
	SaveUserHistoryRecord(ctx context.Context, record *models.UserHistoryRecord) error
}

type authService interface {
	RecentlyUsedTokensChan() <-chan domain.TokenWasUsed
}

type subscriber interface {
	Subscribe(
		topic string,
		handler func(ctx context.Context, event []byte) error,
	) error
}
