package workers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionCleanerDrainsBatchesImmediatelyAndPolls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		started := time.Now()
		batches := []int64{2, 1, 0, 3, 0}
		var calls []time.Time
		repository := sessionRepositoryFunc(func(repoCtx context.Context) (int64, error) {
			assert.Same(t, ctx, repoCtx)
			calls = append(calls, time.Now())
			if len(calls) > len(batches) {
				t.Error("unexpected cleanup call")
				cancel()
				return 0, nil
			}
			return batches[len(calls)-1], nil
		})
		cleaner := NewSessionCleaner(repository, nil)
		done := make(chan struct{})
		go func() {
			defer close(done)
			cleaner.Run(ctx, time.Hour)
		}()

		synctest.Wait()
		require.Equal(t, []time.Time{started, started, started}, calls)
		time.Sleep(time.Hour - time.Nanosecond)
		synctest.Wait()
		assert.Len(t, calls, 3)
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		assert.Equal(t, []time.Time{
			started, started, started, started.Add(time.Hour), started.Add(time.Hour),
		}, calls)

		cancel()
		synctest.Wait()
		<-done
		time.Sleep(time.Hour)
		synctest.Wait()
		assert.Len(t, calls, 5)
	})
}

func TestSessionCleanerRetriesRepositoryErrorsAtNextInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		calls := 0
		repository := sessionRepositoryFunc(func(repoCtx context.Context) (int64, error) {
			assert.Same(t, ctx, repoCtx)
			calls++
			switch calls {
			case 1:
				return 0, errors.New("database unavailable")
			case 2:
				return 2, nil
			case 3:
				return 0, nil
			default:
				t.Error("unexpected cleanup call")
				cancel()
				return 0, nil
			}
		})
		var output bytes.Buffer
		cleaner := NewSessionCleaner(repository, slog.New(slog.NewJSONHandler(&output, nil)))
		done := make(chan struct{})
		go func() {
			defer close(done)
			cleaner.Run(ctx, time.Hour)
		}()

		synctest.Wait()
		require.Equal(t, 1, calls)
		var record map[string]any
		require.NoError(t, json.Unmarshal(output.Bytes(), &record))
		assert.Equal(t, "ERROR", record["level"])
		assert.Equal(t, "failed to clean up expired sessions", record["msg"])
		assert.Equal(t, "database unavailable", record["error"])
		logged := output.String()

		time.Sleep(time.Hour - time.Nanosecond)
		synctest.Wait()
		assert.Equal(t, 1, calls)
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		assert.Equal(t, 3, calls)
		assert.Equal(t, logged, output.String())
		cancel()
		synctest.Wait()
		<-done
	})
}

func TestSessionCleanerSkipsCanceledContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		calls := 0
		repository := sessionRepositoryFunc(func(context.Context) (int64, error) {
			calls++
			return 0, nil
		})

		NewSessionCleaner(repository, nil).Run(ctx, time.Hour)

		assert.Zero(t, calls)
	})
}

func TestSessionCleanerStopsBetweenBatches(t *testing.T) {
	for _, test := range []struct {
		name      string
		stopAfter int
	}{
		{name: "first batch", stopAfter: 1},
		{name: "later batch", stopAfter: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				calls := 0
				repository := sessionRepositoryFunc(func(repoCtx context.Context) (int64, error) {
					assert.Same(t, ctx, repoCtx)
					calls++
					if calls >= test.stopAfter {
						cancel()
					}
					if calls > test.stopAfter {
						return 0, nil
					}
					return 1, nil
				})

				NewSessionCleaner(repository, nil).Run(ctx, time.Hour)

				assert.Equal(t, test.stopAfter, calls)
			})
		})
	}
}

func TestSessionCleanerSuppressesErrorsDuringShutdown(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "canceled operation", err: context.Canceled},
		{name: "repository failure during cancellation", err: errors.New("database unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				calls := 0
				repository := sessionRepositoryFunc(func(repoCtx context.Context) (int64, error) {
					assert.Same(t, ctx, repoCtx)
					calls++
					<-repoCtx.Done()
					return 0, test.err
				})
				var output bytes.Buffer
				cleaner := NewSessionCleaner(repository, slog.New(slog.NewJSONHandler(&output, nil)))
				done := make(chan struct{})
				go func() {
					defer close(done)
					cleaner.Run(ctx, time.Hour)
				}()

				synctest.Wait()
				require.Equal(t, 1, calls)
				cancel()
				synctest.Wait()
				<-done
				assert.Equal(t, 1, calls)
				assert.Empty(t, output.String())
			})
		})
	}
}

type sessionRepositoryFunc func(context.Context) (int64, error)

func (fn sessionRepositoryFunc) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	return fn(ctx)
}
