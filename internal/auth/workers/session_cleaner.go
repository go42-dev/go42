package workers

import (
	"context"
	"log/slog"
	"time"
)

type sessionRepository interface {
	DeleteExpiredSessions(ctx context.Context) (int64, error)
}

type SessionCleaner struct {
	repository sessionRepository
	logger     *slog.Logger
}

func NewSessionCleaner(repository sessionRepository, logger *slog.Logger) *SessionCleaner {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &SessionCleaner{repository: repository, logger: logger}
}

func (c *SessionCleaner) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		for ctx.Err() == nil {
			deleted, err := c.repository.DeleteExpiredSessions(ctx)
			if err != nil {
				if ctx.Err() == nil {
					c.logger.ErrorContext(ctx, "failed to clean up expired sessions", slog.Any("error", err))
				}
				break
			}
			if deleted == 0 {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
