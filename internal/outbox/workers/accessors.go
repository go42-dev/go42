package workers

import (
	"context"
	"database/sql"
	"time"

	"github.com/go42-dev/go42/internal/outbox/models"
)

//go:generate mockgen -source $GOFILE -package mocks -destination mocks/mocks.go

type repository interface {
	WithTransactionIsolation(
		ctx context.Context, isolationLvl sql.IsolationLevel, fn func(txCtx context.Context) error,
	) error
	GetUnprocessedMessages(ctx context.Context, limit int) ([]models.Message, error)
	SaveProcessedMessages(ctx context.Context, messages []models.Message) error
	SaveFailedMessages(ctx context.Context, messages []models.Message) error
}

type publisher interface {
	Publish(ctx context.Context, topic string, id string, event []byte) error
}

type cleanupRepository interface {
	DeleteProcessedMessages(ctx context.Context, before time.Time, limit int) (int64, error)
	GetOldestProcessedMessageTime(ctx context.Context, before time.Time) (time.Time, bool, error)
}
