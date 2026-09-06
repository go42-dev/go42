package outbox_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/go42-dev/go42/internal/outbox"
	"github.com/go42-dev/go42/internal/outbox/domain"
	"github.com/go42-dev/go42/internal/outbox/mocks"
	"github.com/go42-dev/go42/internal/outbox/models"
	"github.com/go42-dev/go42/internal/tools"
)

func TestServiceCreatesPendingOutboxMessage(t *testing.T) {
	ctrl := gomock.NewController(t)
	repository := mocks.NewMockrepository(ctrl)
	service := outbox.NewService(repository)
	message := &domain.Message{
		AggregateID:   42,
		AggregateType: "user.created",
		Payload:       []byte(`{"uuid":"test"}`),
	}
	ctx := tools.SetRequestIDToContext(t.Context(), "request-42")

	repository.EXPECT().NewOutboxMessage(ctx, gomock.Any()).
		DoAndReturn(func(_ context.Context, stored *models.Message) error {
			if stored.ID == uuid.Nil {
				t.Error("stored message ID is nil")
			}
			if stored.AggregateID != message.AggregateID ||
				stored.AggregateType != message.AggregateType ||
				stored.Topic != "auth" ||
				string(stored.Payload) != string(message.Payload) {
				t.Errorf("stored message = %#v, want input fields", stored)
			}
			if stored.Metadata["request_id"] != "request-42" {
				t.Errorf("stored metadata = %#v, want request ID from context", stored.Metadata)
			}
			if stored.Status != models.MessageStatusPending {
				t.Errorf("stored status = %q, want %q", stored.Status, models.MessageStatusPending)
			}
			if stored.MaxRetries != domain.MaxRetries {
				t.Errorf("stored max retries = %d, want %d", stored.MaxRetries, domain.MaxRetries)
			}
			return nil
		})

	if err := service.NewOutboxMessage(ctx, "auth", message); err != nil {
		t.Fatalf("NewOutboxMessage() error = %v", err)
	}
}

func TestServiceRejectsInvalidOutboxMessage(t *testing.T) {
	ctrl := gomock.NewController(t)
	service := outbox.NewService(mocks.NewMockrepository(ctrl))

	err := service.NewOutboxMessage(t.Context(), "auth", &domain.Message{})
	if err == nil {
		t.Fatal("NewOutboxMessage() error = nil, want validation error")
	}
}

func TestServiceReturnsRepositoryError(t *testing.T) {
	ctrl := gomock.NewController(t)
	repository := mocks.NewMockrepository(ctrl)
	service := outbox.NewService(repository)
	wantErr := errors.New("repository unavailable")
	repository.EXPECT().NewOutboxMessage(gomock.Any(), gomock.Any()).Return(wantErr)

	err := service.NewOutboxMessage(t.Context(), "auth", &domain.Message{
		AggregateID:   42,
		AggregateType: "user.created",
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("NewOutboxMessage() error = %v, want %v", err, wantErr)
	}
}
