package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
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
	err := r.GetTx(ctx).Create(msg).Error
	if err != nil {
		return fmt.Errorf("error saving message: %w", err)
	}
	return nil
}

func (r *Repository) GetUnprocessedMessages(ctx context.Context, limit int) ([]models.Message, error) {
	var messages []models.Message
	result := r.
		GetTx(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
		Where("status = ?", models.MessageStatusPending).
		Limit(limit).Find(&messages)
	if result.Error != nil {
		return nil, fmt.Errorf("error fetching messages: %w", result.Error)
	}
	return messages, nil
}

func (r *Repository) SaveProcessedMessages(ctx context.Context, messages []models.Message) error {
	var ids []uuid.UUID
	for _, message := range messages {
		ids = append(ids, message.ID)
	}
	result := r.
		GetTx(ctx).
		Model(&models.Message{}).
		Where("id IN ?", ids).
		Updates(map[string]interface{}{
			"status":       models.MessageStatusProcessed,
			"processed_at": time.Now().UTC(),
		})
	if result.Error != nil {
		return fmt.Errorf("error updating processed messages: %w", result.Error)
	}
	return nil
}

// DeleteProcessedMessages removes one bounded batch, oldest first. Recheck both
// status and processing time when deleting in case a message changed after selection.
func (r *Repository) DeleteProcessedMessages(ctx context.Context, before time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, errors.New("outbox cleanup batch size must be positive")
	}
	before = before.UTC()
	var ids []uuid.UUID
	err := r.GetTx(ctx).Model(&models.Message{}).
		Where("status = ? AND processed_at < ?", models.MessageStatusProcessed, before).
		Order("processed_at ASC").Order("id ASC").Limit(limit).
		Pluck("id", &ids).Error
	if err != nil {
		return 0, fmt.Errorf("error selecting processed messages for cleanup: %w", err)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	result := r.GetTx(ctx).
		Where("id IN ? AND status = ? AND processed_at < ?", ids, models.MessageStatusProcessed, before).
		Delete(&models.Message{})
	if result.Error != nil {
		return 0, fmt.Errorf("error deleting processed messages: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (r *Repository) SaveFailedMessages(ctx context.Context, messages []models.Message) error {
	for _, message := range messages {
		result := r.GetTx(ctx).Save(&message)
		if result.Error != nil {
			return fmt.Errorf("error saving message with ID %s: %w", message.ID, result.Error)
		}
	}
	return nil
}
