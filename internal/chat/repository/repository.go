package repository

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/go42-dev/go42/internal/chat/domain"
	"github.com/go42-dev/go42/internal/chat/models"
	"github.com/go42-dev/go42/internal/database"
)

type Repository struct {
	*database.BaseRepository
}

func New(baseRepository *database.BaseRepository) *Repository {
	return &Repository{BaseRepository: baseRepository}
}

func (r *Repository) CreateChannel(ctx context.Context, channel *models.Channel) error {
	if err := gorm.G[models.Channel](r.GetTx(ctx)).Create(ctx, channel); err != nil {
		return fmt.Errorf("create channel: %w", err)
	}
	return nil
}

func (r *Repository) ListChannels(ctx context.Context, limit, offset int) ([]*models.Channel, error) {
	channels, err := gorm.G[*models.Channel](r.GetReadDB(ctx)).
		Limit(limit).Offset(offset).Order("id ASC").Find(ctx)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	return channels, nil
}

func (r *Repository) GetChannelByUUID(ctx context.Context, channelUUID string) (*models.Channel, error) {
	channel, err := gorm.G[models.Channel](r.GetReadDB(ctx)).
		Where("uuid = ?", channelUUID).First(ctx)
	if r.IsNotFoundError(err) {
		return nil, domain.ErrEntityNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get channel: %w", err)
	}
	return &channel, nil
}

func (r *Repository) CreateMessage(ctx context.Context, message *models.Message) error {
	if err := gorm.G[models.Message](r.GetTx(ctx)).Create(ctx, message); err != nil {
		return fmt.Errorf("create message: %w", err)
	}
	return nil
}

type ChannelMessage struct {
	ID          int
	UUID        string
	ChannelUUID string
	SenderUUID  string
	Content     string
	CreatedAt   time.Time
}

func (r *Repository) ListChannelMessages(
	ctx context.Context, channelID int, afterID int, limit int,
) ([]ChannelMessage, error) {
	rows, err := gorm.G[ChannelMessage](r.GetReadDB(ctx)).Raw(`
		SELECT m.id, m.uuid, c.uuid AS channel_uuid, u.uuid AS sender_uuid, m.content, m.created_at
		FROM chat_messages AS m
		JOIN chat_channels AS c ON c.id = m.channel_id
		JOIN auth_users AS u ON u.id = m.sender_user_id
		WHERE m.channel_id = ? AND m.id > ?
		ORDER BY m.id ASC
		LIMIT ?
	`, channelID, afterID, limit).Find(ctx)
	if err != nil {
		return nil, fmt.Errorf("list channel messages: %w", err)
	}
	return rows, nil
}

type PrivateMessage struct {
	ID            int
	UUID          string
	SenderUUID    string
	RecipientUUID string
	Content       string
	CreatedAt     time.Time
}

func (r *Repository) ListPrivateMessages(
	ctx context.Context, userID int, peerUserID int, afterID int, limit int,
) ([]PrivateMessage, error) {
	rows, err := gorm.G[PrivateMessage](r.GetReadDB(ctx)).Raw(`
		SELECT m.id, m.uuid, su.uuid AS sender_uuid, ru.uuid AS recipient_uuid, m.content, m.created_at
		FROM chat_messages AS m
		JOIN auth_users AS su ON su.id = m.sender_user_id
		JOIN auth_users AS ru ON ru.id = m.recipient_user_id
		WHERE m.channel_id IS NULL
			AND m.id > ?
			AND (
				(m.sender_user_id = ? AND m.recipient_user_id = ?)
				OR (m.sender_user_id = ? AND m.recipient_user_id = ?)
			)
		ORDER BY m.id ASC
		LIMIT ?
	`, afterID, userID, peerUserID, peerUserID, userID, limit).Find(ctx)
	if err != nil {
		return nil, fmt.Errorf("list private messages: %w", err)
	}
	return rows, nil
}

type LiveUpdate struct {
	ID            int
	UUID          string
	Type          string
	ChannelUUID   *string
	SenderUUID    string
	RecipientUUID *string
	Content       string
	CreatedAt     time.Time
}

func (r *Repository) ListLiveUpdates(
	ctx context.Context, userID int, afterID int, limit int,
) ([]LiveUpdate, error) {
	rows, err := gorm.G[LiveUpdate](r.GetReadDB(ctx)).Raw(`
		SELECT
			m.id,
			m.uuid,
			CASE WHEN m.channel_id IS NULL THEN 'private' ELSE 'channel' END AS type,
			c.uuid AS channel_uuid,
			su.uuid AS sender_uuid,
			ru.uuid AS recipient_uuid,
			m.content,
			m.created_at
		FROM chat_messages AS m
		JOIN auth_users AS su ON su.id = m.sender_user_id
		LEFT JOIN auth_users AS ru ON ru.id = m.recipient_user_id
		LEFT JOIN chat_channels AS c ON c.id = m.channel_id
		WHERE m.id > ?
			AND (
				m.channel_id IS NOT NULL
				OR m.sender_user_id = ?
				OR m.recipient_user_id = ?
			)
		ORDER BY m.id ASC
		LIMIT ?
	`, afterID, userID, userID, limit).Find(ctx)
	if err != nil {
		return nil, fmt.Errorf("list live updates: %w", err)
	}
	return rows, nil
}

type User struct {
	ID   int
	UUID string
}

func (r *Repository) GetUserByUUID(ctx context.Context, userUUID string) (*User, error) {
	user, err := gorm.G[User](r.GetReadDB(ctx)).Raw(`
		SELECT id, uuid
		FROM auth_users
		WHERE uuid = ? AND deleted_at IS NULL
		LIMIT 1
	`, userUUID).First(ctx)
	if r.IsNotFoundError(err) {
		return nil, domain.ErrEntityNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	return &user, nil
}
