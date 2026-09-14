package models

import (
	"time"

	"github.com/google/uuid"
)

type Channel struct {
	ID              int
	UUID            uuid.UUID
	Name            string
	CreatedByUserID int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (*Channel) TableName() string { return "chat_channels" }

type Message struct {
	ID              int
	UUID            uuid.UUID
	ChannelID       *int
	SenderUserID    int
	RecipientUserID *int
	Content         string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (*Message) TableName() string { return "chat_messages" }
