package models

import (
	"database/sql"
	"time"

	"github.com/google/uuid"
)

const (
	MessageStatusPending   string = "pending"
	MessageStatusProcessed string = "processed"
	MessageStatusFailed    string = "failed"
)

type Message struct {
	ID            uuid.UUID
	AggregateID   int
	AggregateType string
	Topic         string
	Payload       []byte
	CreatedAt     time.Time
	ProcessedAt   sql.NullTime
	Status        string
	RetryCount    int
	MaxRetries    int
	LastError     string
	Metadata      map[string]string `gorm:"serializer:json"`
}

func (m *Message) TableName() string {
	return "transactional_outbox"
}
