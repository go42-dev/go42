package adapter

import (
	"time"

	"github.com/go42-dev/go42/internal/chat/models"
	"github.com/go42-dev/go42/internal/chat/repository"
)

type channelResponse struct {
	UUID      string    `json:"uuid"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

func channelResponseFromModel(channel *models.Channel) channelResponse {
	return channelResponse{
		UUID:      channel.UUID.String(),
		Name:      channel.Name,
		CreatedAt: channel.CreatedAt,
	}
}

type messageResponse struct {
	UUID      string    `json:"uuid"`
	CreatedAt time.Time `json:"created_at"`
}

func messageResponseFromModel(message *models.Message) messageResponse {
	return messageResponse{
		UUID:      message.UUID.String(),
		CreatedAt: message.CreatedAt,
	}
}

type channelMessageResponse struct {
	ID          int       `json:"id"`
	UUID        string    `json:"uuid"`
	ChannelUUID string    `json:"channel_uuid"`
	SenderUUID  string    `json:"sender_uuid"`
	Content     string    `json:"content"`
	CreatedAt   time.Time `json:"created_at"`
}

func channelMessageResponseFromModel(message repository.ChannelMessage) channelMessageResponse {
	return channelMessageResponse{
		ID:          message.ID,
		UUID:        message.UUID,
		ChannelUUID: message.ChannelUUID,
		SenderUUID:  message.SenderUUID,
		Content:     message.Content,
		CreatedAt:   message.CreatedAt,
	}
}

type privateMessageResponse struct {
	ID            int       `json:"id"`
	UUID          string    `json:"uuid"`
	SenderUUID    string    `json:"sender_uuid"`
	RecipientUUID string    `json:"recipient_uuid"`
	Content       string    `json:"content"`
	CreatedAt     time.Time `json:"created_at"`
}

func privateMessageResponseFromModel(message repository.PrivateMessage) privateMessageResponse {
	return privateMessageResponse{
		ID:            message.ID,
		UUID:          message.UUID,
		SenderUUID:    message.SenderUUID,
		RecipientUUID: message.RecipientUUID,
		Content:       message.Content,
		CreatedAt:     message.CreatedAt,
	}
}

type liveUpdateResponse struct {
	ID            int       `json:"id"`
	UUID          string    `json:"uuid"`
	Type          string    `json:"type"`
	ChannelUUID   *string   `json:"channel_uuid,omitempty"`
	SenderUUID    string    `json:"sender_uuid"`
	RecipientUUID *string   `json:"recipient_uuid,omitempty"`
	Content       string    `json:"content"`
	CreatedAt     time.Time `json:"created_at"`
}

func liveUpdateResponseFromModel(update repository.LiveUpdate) liveUpdateResponse {
	return liveUpdateResponse{
		ID:            update.ID,
		UUID:          update.UUID,
		Type:          update.Type,
		ChannelUUID:   update.ChannelUUID,
		SenderUUID:    update.SenderUUID,
		RecipientUUID: update.RecipientUUID,
		Content:       update.Content,
		CreatedAt:     update.CreatedAt,
	}
}

type liveUpdatesResponse struct {
	Updates     []liveUpdateResponse `json:"updates"`
	NextAfterID int                  `json:"next_after_id"`
}
