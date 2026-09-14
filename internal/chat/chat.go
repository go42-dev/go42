package chat

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/go42-dev/go42/internal/chat/domain"
	"github.com/go42-dev/go42/internal/chat/models"
	"github.com/go42-dev/go42/internal/chat/repository"
	"github.com/go42-dev/go42/internal/tools"
)

const (
	maxChannelNameLength = 120
	maxMessageLength     = 4000
	defaultLimit         = 50
	maxLimit             = 200
)

//go:generate mockgen -source $GOFILE -package mocks -destination mocks/mocks.go

type repositoryAccessor interface {
	CreateChannel(ctx context.Context, channel *models.Channel) error
	ListChannels(ctx context.Context, limit, offset int) ([]*models.Channel, error)
	GetChannelByUUID(ctx context.Context, channelUUID string) (*models.Channel, error)
	CreateMessage(ctx context.Context, message *models.Message) error
	ListChannelMessages(ctx context.Context, channelID int, afterID int, limit int) ([]repository.ChannelMessage, error)
	ListPrivateMessages(ctx context.Context, userID int, peerUserID int, afterID int, limit int) ([]repository.PrivateMessage, error)
	ListLiveUpdates(ctx context.Context, userID int, afterID int, limit int) ([]repository.LiveUpdate, error)
	GetUserByUUID(ctx context.Context, userUUID string) (*repository.User, error)
}

type Service struct {
	repository repositoryAccessor
}

func NewService(repository repositoryAccessor) *Service {
	return &Service{repository: repository}
}

func (s *Service) CreateChannel(ctx context.Context, createdBy int, name string) (*models.Channel, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxChannelNameLength {
		return nil, domain.ErrInvalidInput
	}
	channel := &models.Channel{
		UUID:            uuid.New(),
		Name:            name,
		CreatedByUserID: createdBy,
	}
	if err := s.repository.CreateChannel(ctx, channel); err != nil {
		return nil, err
	}
	return channel, nil
}

func (s *Service) ListChannels(ctx context.Context, limit, offset int) ([]*models.Channel, error) {
	page, err := tools.NormalizePagination(s.normalizeLimit(limit), offset)
	if err != nil {
		return nil, domain.ErrInvalidInput
	}
	return s.repository.ListChannels(ctx, page.Limit, page.Offset)
}

func (s *Service) CreateChannelMessage(
	ctx context.Context, channelUUID string, senderID int, content string,
) (*models.Message, error) {
	content = strings.TrimSpace(content)
	if content == "" || len(content) > maxMessageLength {
		return nil, domain.ErrInvalidInput
	}
	if _, err := uuid.Parse(channelUUID); err != nil {
		return nil, domain.ErrInvalidInput
	}
	channel, err := s.repository.GetChannelByUUID(ctx, channelUUID)
	if err != nil {
		return nil, err
	}
	message := &models.Message{
		UUID:         uuid.New(),
		ChannelID:    &channel.ID,
		SenderUserID: senderID,
		Content:      content,
	}
	if err := s.repository.CreateMessage(ctx, message); err != nil {
		return nil, err
	}
	return message, nil
}

func (s *Service) ListChannelMessages(
	ctx context.Context, channelUUID string, afterID int, limit int,
) ([]repository.ChannelMessage, error) {
	if afterID < 0 {
		return nil, domain.ErrInvalidInput
	}
	channel, err := s.repository.GetChannelByUUID(ctx, channelUUID)
	if err != nil {
		return nil, err
	}
	return s.repository.ListChannelMessages(ctx, channel.ID, afterID, s.normalizeLimit(limit))
}

func (s *Service) CreatePrivateMessage(
	ctx context.Context, senderID int, senderUUID string, recipientUUID string, content string,
) (*models.Message, error) {
	content = strings.TrimSpace(content)
	if content == "" || len(content) > maxMessageLength {
		return nil, domain.ErrInvalidInput
	}
	if _, err := uuid.Parse(recipientUUID); err != nil {
		return nil, domain.ErrInvalidInput
	}
	if senderUUID == recipientUUID {
		return nil, domain.ErrInvalidInput
	}
	recipient, err := s.repository.GetUserByUUID(ctx, recipientUUID)
	if err != nil {
		return nil, err
	}
	message := &models.Message{
		UUID:            uuid.New(),
		SenderUserID:    senderID,
		RecipientUserID: &recipient.ID,
		Content:         content,
	}
	if err := s.repository.CreateMessage(ctx, message); err != nil {
		return nil, err
	}
	return message, nil
}

func (s *Service) ListPrivateMessages(
	ctx context.Context, userID int, peerUUID string, afterID int, limit int,
) ([]repository.PrivateMessage, error) {
	if afterID < 0 {
		return nil, domain.ErrInvalidInput
	}
	peer, err := s.repository.GetUserByUUID(ctx, peerUUID)
	if err != nil {
		return nil, err
	}
	return s.repository.ListPrivateMessages(ctx, userID, peer.ID, afterID, s.normalizeLimit(limit))
}

func (s *Service) ListLiveUpdates(
	ctx context.Context, userID int, afterID int, limit int,
) ([]repository.LiveUpdate, error) {
	if afterID < 0 {
		return nil, domain.ErrInvalidInput
	}
	return s.repository.ListLiveUpdates(ctx, userID, afterID, s.normalizeLimit(limit))
}

func (s *Service) normalizeLimit(limit int) int {
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}
