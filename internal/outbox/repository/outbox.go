package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/go42-dev/go42/internal/database"
	"github.com/go42-dev/go42/internal/outbox/models"
)

type Repository struct {
	*database.BaseRepository
}

func New(baseRepository *database.BaseRepository) *Repository {
	return &Repository{baseRepository}
}

func (r *Repository) NewOutboxMessage(ctx context.Context, msg *models.Message) error {
	normalizeMessageTimes(msg)
	err := gorm.G[models.Message](r.GetTx(ctx)).Create(ctx, msg)
	if err != nil {
		return fmt.Errorf("error saving message: %w", err)
	}
	return nil
}

func (r *Repository) GetUnprocessedMessages(ctx context.Context, limit int) ([]models.Message, error) {
	messages, err := gorm.G[models.Message](r.GetTx(ctx),
		clause.Locking{
			Strength: clause.LockingStrengthUpdate,
			Options:  clause.LockingOptionsSkipLocked,
		},
	).
		Where("status = ?", models.MessageStatusPending).
		Where("next_attempt_at IS NULL OR next_attempt_at <= ?", time.Now().UTC()).
		Order("created_at ASC").
		Order("id ASC").
		Limit(limit).
		Find(ctx)
	if err != nil {
		return nil, fmt.Errorf("error fetching messages: %w", err)
	}
	return messages, nil
}

func (r *Repository) SaveProcessedMessages(ctx context.Context, messages []models.Message) error {
	ids := make([]uuid.UUID, len(messages))
	for i, message := range messages {
		ids[i] = message.ID
	}
	_, err := gorm.G[models.Message](r.GetTx(ctx)).
		Where("id IN ?", ids).
		Select("status", "processed_at", "next_attempt_at").
		Updates(ctx, models.Message{
			Status:      models.MessageStatusProcessed,
			ProcessedAt: sql.NullTime{Time: time.Now().UTC(), Valid: true},
		})
	if err != nil {
		return fmt.Errorf("error updating processed messages: %w", err)
	}
	return nil
}

// DeleteProcessedMessages removes one bounded batch, oldest first. Recheck both
// status and processing time when deleting in case a message changed after selection.
func (r *Repository) DeleteProcessedMessages(ctx context.Context, before time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, errors.New("outbox cleanup batch size must be positive")
	}

	var (
		ids          []uuid.UUID
		deleteBefore = before.UTC()
	)

	db := r.GetTx(ctx)
	err := gorm.G[models.Message](db).Select("id").
		Where("status = ? AND processed_at < ?", models.MessageStatusProcessed, deleteBefore).
		Order("processed_at ASC").
		Order("id ASC").
		Limit(limit).
		Scan(ctx, &ids)
	if err != nil {
		return 0, fmt.Errorf("error selecting processed messages for cleanup: %w", err)
	}
	if len(ids) == 0 {
		return 0, nil
	}

	rows, err := gorm.G[models.Message](db).
		Where("id IN ?", ids).
		Where("status = ? AND processed_at < ?", models.MessageStatusProcessed, deleteBefore).
		Delete(ctx)
	if err != nil {
		return 0, fmt.Errorf("error deleting processed messages: %w", err)
	}

	return int64(rows), nil
}

func (r *Repository) SaveFailedMessages(ctx context.Context, messages []models.Message) error {
	db := r.GetTx(ctx)
	// Select all fields so zero values also overwrite stored values.
	for _, message := range messages {
		normalizeMessageTimes(&message)
		_, err := gorm.G[*models.Message](db).
			Where("id = ?", message.ID).
			Select("*").
			Updates(ctx, &message)
		if err != nil {
			return fmt.Errorf("error updating message with ID %s: %w", message.ID, err)
		}
	}
	return nil
}

func normalizeMessageTimes(message *models.Message) {
	message.CreatedAt = message.CreatedAt.UTC()
	if message.ProcessedAt.Valid {
		message.ProcessedAt.Time = message.ProcessedAt.Time.UTC()
	}
	if message.NextAttemptAt.Valid {
		message.NextAttemptAt.Time = message.NextAttemptAt.Time.UTC()
	}
}
