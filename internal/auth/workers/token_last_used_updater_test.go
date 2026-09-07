package workers

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/go42-dev/go42/internal/auth/domain"
	"github.com/go42-dev/go42/internal/auth/workers/mocks"
)

func TestTokenLastUsedUpdaterKeepsLatestWhileFlushing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		ctrl := gomock.NewController(t)
		repository := mocks.NewMockrepository(ctrl)
		service := mocks.NewMockauthService(ctrl)
		events := make(chan domain.TokenWasUsed)
		service.EXPECT().RecentlyUsedTokensChan().Return((<-chan domain.TokenWasUsed)(events))
		repository.EXPECT().WithTransaction(gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }).Times(3)

		flushing := make(chan struct{})
		finishFlush := make(chan struct{})
		var writes []domain.TokenWasUsed
		repository.EXPECT().UpdateTokenLastUsed(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, id int, when time.Time) error {
				writes = append(writes, domain.TokenWasUsed{ID: id, When: when})
				if len(writes) == 1 {
					close(flushing)
					select {
					case <-finishFlush:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				return nil
			}).Times(3)

		updater := NewTokenLastUsedUpdater(repository, service)
		go updater.Run(ctx, time.Hour)
		base := time.Now()
		latest := base.Add(time.Minute)
		for _, event := range []domain.TokenWasUsed{
			{ID: 1, When: base},
			{ID: 1, When: latest},
			{ID: 2, When: base},
			{ID: 1, When: latest},
			{ID: 1, When: base.Add(time.Second)},
		} {
			events <- event
		}
		synctest.Wait()
		time.Sleep(time.Hour)
		<-flushing

		// Events received during a flush belong to the next buffer.
		nextLatest := latest.Add(time.Minute)
		events <- domain.TokenWasUsed{ID: 1, When: nextLatest}
		events <- domain.TokenWasUsed{ID: 1, When: latest}
		synctest.Wait()
		close(finishFlush)
		synctest.Wait()
		require.Len(t, writes, 2)
		assert.ElementsMatch(t, []domain.TokenWasUsed{
			{ID: 1, When: latest}, {ID: 2, When: base},
		}, writes)

		time.Sleep(time.Hour)
		synctest.Wait()
		require.Len(t, writes, 3)
		assert.Equal(t, domain.TokenWasUsed{ID: 1, When: nextLatest}, writes[2])

		// Reusing an empty buffer must not flush the old events again.
		time.Sleep(time.Hour)
		synctest.Wait()
		assert.Len(t, writes, 3)
		cancel()
		synctest.Wait()
	})
}
