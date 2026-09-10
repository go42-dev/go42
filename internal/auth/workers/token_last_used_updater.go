package workers

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/go42-dev/go42/internal/tools"
)

type buffer struct {
	data map[int]time.Time
}

type TokenLastUsedUpdater struct {
	sync.Mutex
	logger        *slog.Logger
	repository    repository
	authService   authService
	activeBuffer  *buffer
	sleeperBuffer *buffer
}

func NewTokenLastUsedUpdater(
	repository repository,
	authService authService,
	opts ...TokenLastUsedUpdaterOption,
) *TokenLastUsedUpdater {
	updater := &TokenLastUsedUpdater{
		repository:  repository,
		authService: authService,
		activeBuffer: &buffer{
			data: make(map[int]time.Time, tools.BufferSize8192),
		},
		sleeperBuffer: &buffer{
			data: make(map[int]time.Time, tools.BufferSize8192),
		},
	}
	for _, o := range opts {
		o(updater)
	}
	if updater.logger == nil {
		updater.logger = slog.New(slog.DiscardHandler)
	}
	return updater
}

func (u *TokenLastUsedUpdater) Run(ctx context.Context, interval time.Duration) {
	u.logger.InfoContext(ctx, "starting api token last used updater")
	go func() {
		c := u.authService.RecentlyUsedTokensChan()
		for {
			select {
			case <-ctx.Done():
				return
			case e := <-c:
				u.Lock()
				if previous, found := u.activeBuffer.data[e.ID]; !found || e.When.After(previous) {
					u.activeBuffer.data[e.ID] = e.When
				}
				u.Unlock()
			}
		}
	}()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			u.run(ctx)
		}
	}
}

func (u *TokenLastUsedUpdater) run(ctx context.Context) {
	// swap buffers, so that we can process the active buffer
	// while new tokens are being added to the sleeper buffer
	u.Lock()
	u.activeBuffer, u.sleeperBuffer = u.sleeperBuffer, u.activeBuffer
	u.Unlock()

	// before finishing, clear the sleeper buffer
	defer clear(u.sleeperBuffer.data)

	for id, when := range u.sleeperBuffer.data {
		err := u.repository.WithTransaction(ctx, func(txCtx context.Context) error {
			return u.repository.UpdateTokenLastUsed(txCtx, id, when)
		})
		if err != nil {
			u.logger.ErrorContext(
				ctx, "failed to update token last used time",
				slog.Any("token", id),
				slog.Any("when", when),
				slog.Any("error", err),
			)
		}
	}
}

type TokenLastUsedUpdaterOption func(*TokenLastUsedUpdater)

func TokenLastUsedUpdaterWithLogger(logger *slog.Logger) TokenLastUsedUpdaterOption {
	return func(o *TokenLastUsedUpdater) {
		o.logger = logger
	}
}
