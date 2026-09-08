package workers

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
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

func TestTokenLastUsedUpdaterContinuesAfterStorageFailures(t *testing.T) {
	for _, stage := range []string{"begin", "update", "commit"} {
		t.Run(stage, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				ctrl := gomock.NewController(t)
				repository := mocks.NewMockrepository(ctrl)
				service := mocks.NewMockauthService(ctrl)
				events := make(chan domain.TokenWasUsed)
				service.EXPECT().RecentlyUsedTokensChan().Return((<-chan domain.TokenWasUsed)(events))
				var logs bytes.Buffer
				updater := NewTokenLastUsedUpdater(repository, service, TokenLastUsedUpdaterWithLogger(
					slog.New(slog.NewJSONHandler(&logs, nil)),
				))
				storageError := errors.New("token usage storage unavailable")
				transactions := 0
				var transactionCtx context.Context
				var currentToken domain.TokenWasUsed
				var attempts, committed []domain.TokenWasUsed
				repository.EXPECT().WithTransaction(ctx, gomock.Any()).
					DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error {
						transactions++
						if transactions == 1 && stage == "begin" {
							return storageError
						}
						txCtx, txCancel := context.WithCancel(ctx)
						defer txCancel()
						transactionCtx = txCtx
						err := fn(txCtx)
						if transactions == 1 && stage == "commit" {
							assert.NoError(t, err)
							return storageError
						}
						if err == nil {
							committed = append(committed, currentToken)
						}
						return err
					}).Times(3)
				wantAttempts := 3
				if stage == "begin" {
					wantAttempts--
				}
				repository.EXPECT().UpdateTokenLastUsed(gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, id int, when time.Time) error {
						assert.Same(t, transactionCtx, ctx)
						currentToken = domain.TokenWasUsed{ID: id, When: when}
						attempts = append(attempts, currentToken)
						if transactions == 1 && stage == "update" {
							return storageError
						}
						return nil
					}).Times(wantAttempts)
				done := make(chan struct{})
				go func() {
					defer close(done)
					updater.Run(ctx, time.Hour)
				}()
				base := time.Now()
				initial := []domain.TokenWasUsed{{ID: 1, When: base}, {ID: 2, When: base.Add(time.Minute)}}
				for _, event := range initial {
					events <- event
				}
				synctest.Wait()
				time.Sleep(time.Hour)
				synctest.Wait()
				assert.Equal(t, 2, transactions)
				require.Len(t, committed, 1)
				assert.Contains(t, initial, committed[0])
				if stage == "begin" {
					assert.Equal(t, committed, attempts)
				} else {
					assert.ElementsMatch(t, initial, attempts)
				}
				assert.Contains(t, logs.String(), storageError.Error())
				assert.Contains(t, logs.String(), "failed to update token last used time")
				next := domain.TokenWasUsed{ID: 3, When: base.Add(2 * time.Minute)}
				events <- next
				synctest.Wait()
				time.Sleep(time.Hour)
				synctest.Wait()
				require.Len(t, committed, 2)
				assert.Equal(t, next, committed[1])
				time.Sleep(time.Hour)
				synctest.Wait()
				assert.Equal(t, 3, transactions, "completed buffers must not be flushed again")
				cancel()
				synctest.Wait()
				<-done
			})
		})
	}
}

func TestTokenLastUsedUpdaterStopsWhileIdle(t *testing.T) {
	for _, cancelBeforeRun := range []bool{false, true} {
		name := "waiting for events"
		if cancelBeforeRun {
			name = "before start"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				ctrl := gomock.NewController(t)
				repository := mocks.NewMockrepository(ctrl)
				service := mocks.NewMockauthService(ctrl)
				service.EXPECT().
					RecentlyUsedTokensChan().
					Return((<-chan domain.TokenWasUsed)(make(chan domain.TokenWasUsed)))
				updater := NewTokenLastUsedUpdater(repository, service)
				if cancelBeforeRun {
					cancel()
				}
				done := make(chan struct{})
				go func() {
					defer close(done)
					updater.Run(ctx, time.Hour)
				}()
				synctest.Wait()
				cancel()
				synctest.Wait()
				<-done
			})
		})
	}
}

func TestTokenLastUsedUpdaterCancelsPendingUpdate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		ctrl := gomock.NewController(t)
		repository := mocks.NewMockrepository(ctrl)
		service := mocks.NewMockauthService(ctrl)
		events := make(chan domain.TokenWasUsed)
		service.EXPECT().RecentlyUsedTokensChan().Return((<-chan domain.TokenWasUsed)(events))
		var logs bytes.Buffer
		updater := NewTokenLastUsedUpdater(repository, service, TokenLastUsedUpdaterWithLogger(
			slog.New(slog.NewJSONHandler(&logs, nil)),
		))
		var transactionErr error
		repository.EXPECT().WithTransaction(ctx, gomock.Any()).
			DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error {
				transactionErr = fn(ctx)
				return transactionErr
			})
		when := time.Now()
		started := make(chan struct{}, 1)
		repository.EXPECT().UpdateTokenLastUsed(ctx, 42, when).
			DoAndReturn(func(ctx context.Context, _ int, _ time.Time) error {
				started <- struct{}{}
				<-ctx.Done()
				return ctx.Err()
			})
		done := make(chan struct{})
		go func() {
			defer close(done)
			updater.Run(ctx, time.Hour)
		}()
		events <- domain.TokenWasUsed{ID: 42, When: when}
		synctest.Wait()
		time.Sleep(time.Hour)
		synctest.Wait()
		require.Len(t, started, 1)
		cancel()
		synctest.Wait()
		<-done
		assert.ErrorIs(t, transactionErr, context.Canceled)
		assert.Contains(t, logs.String(), context.Canceled.Error())
	})
}
