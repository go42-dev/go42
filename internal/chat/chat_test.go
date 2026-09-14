package chat

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/go42-dev/go42/internal/chat/domain"
	"github.com/go42-dev/go42/internal/chat/models"
	"github.com/go42-dev/go42/internal/chat/repository"
)

type stubRepository struct {
	channels []*models.Channel
	messages []*models.Message
	users    map[string]*repository.User
}

func (s *stubRepository) CreateChannel(_ context.Context, channel *models.Channel) error {
	channel.ID = len(s.channels) + 1
	s.channels = append(s.channels, channel)
	return nil
}

func (s *stubRepository) ListChannels(_ context.Context, _, _ int) ([]*models.Channel, error) {
	return s.channels, nil
}

func (s *stubRepository) GetChannelByUUID(_ context.Context, channelUUID string) (*models.Channel, error) {
	for _, channel := range s.channels {
		if channel.UUID.String() == channelUUID {
			return channel, nil
		}
	}
	return nil, domain.ErrEntityNotFound
}

func (s *stubRepository) CreateMessage(_ context.Context, message *models.Message) error {
	message.ID = len(s.messages) + 1
	s.messages = append(s.messages, message)
	return nil
}

func (s *stubRepository) ListChannelMessages(_ context.Context, _, _, _ int) ([]repository.ChannelMessage, error) {
	return nil, nil
}

func (s *stubRepository) ListPrivateMessages(_ context.Context, _, _, _, _ int) ([]repository.PrivateMessage, error) {
	return nil, nil
}

func (s *stubRepository) ListLiveUpdates(_ context.Context, _, _, _ int) ([]repository.LiveUpdate, error) {
	return nil, nil
}

func (s *stubRepository) GetUserByUUID(_ context.Context, userUUID string) (*repository.User, error) {
	user, ok := s.users[userUUID]
	if !ok {
		return nil, domain.ErrEntityNotFound
	}
	return user, nil
}

func TestCreateChannel(t *testing.T) {
	repo := &stubRepository{}
	service := NewService(repo)

	channel, err := service.CreateChannel(context.Background(), 12, "  general  ")
	require.NoError(t, err)
	require.Equal(t, "general", channel.Name)
	require.NotEqual(t, uuid.Nil, channel.UUID)
	require.Equal(t, 1, channel.ID)
}

func TestCreateChannelRejectsInvalidName(t *testing.T) {
	repo := &stubRepository{}
	service := NewService(repo)

	_, err := service.CreateChannel(context.Background(), 12, "   ")
	require.ErrorIs(t, err, domain.ErrInvalidInput)
}

func TestCreatePrivateMessage(t *testing.T) {
	recipientUUID := uuid.NewString()
	repo := &stubRepository{
		users: map[string]*repository.User{
			recipientUUID: {ID: 77, UUID: recipientUUID},
		},
	}
	service := NewService(repo)

	message, err := service.CreatePrivateMessage(
		context.Background(),
		10,
		uuid.NewString(),
		recipientUUID,
		" hello ",
	)
	require.NoError(t, err)
	require.Equal(t, "hello", message.Content)
	require.NotNil(t, message.RecipientUserID)
	require.Equal(t, 77, *message.RecipientUserID)
}

func TestCreatePrivateMessageRejectsSelfTarget(t *testing.T) {
	repo := &stubRepository{users: map[string]*repository.User{}}
	service := NewService(repo)
	userUUID := uuid.NewString()

	_, err := service.CreatePrivateMessage(context.Background(), 10, userUUID, userUUID, "hello")
	require.ErrorIs(t, err, domain.ErrInvalidInput)
}
