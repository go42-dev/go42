package database_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go42-dev/go42/internal/database"
	"github.com/go42-dev/go42/internal/database/sqlite"
	"github.com/go42-dev/go42/internal/tools"
	"github.com/go42-dev/go42/tests/integration"
)

func TestTransactionQueriesUseDerivedContext(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(tools.SlogContextWrapper(slog.NewJSONHandler(&output, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "context.db"),
		sqlite.WithLogger(logger), sqlite.WithQueryLogging(true))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Shutdown(context.Background())) })
	repository := database.NewBaseRepository(db)
	require.NoError(t, db.Master().WithContext(t.Context()).Exec("create table entries (value text)").Error)
	output.Reset()
	rollback := errors.New("rollback test transaction")
	err = repository.WithTransaction(t.Context(), func(ctx context.Context) error {
		ctx = tools.SetRequestIDToContext(ctx, "request-42")
		ctx = tools.WithLogAttrs(ctx, slog.String("event_id", "event-42"))
		if err := repository.GetTx(ctx).Exec("insert into entries values ('test')").Error; err != nil {
			return err
		}
		var count int64
		if err := repository.GetReadDB(ctx).Table("entries").Count(&count).Error; err != nil {
			return err
		}
		assert.Equal(t, int64(1), count)
		return rollback
	})
	assert.ErrorIs(t, err, rollback)
	decoder := json.NewDecoder(&output)
	count := 0
	for {
		var entry map[string]any
		err := decoder.Decode(&entry)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		assert.Equal(t, "request-42", entry["request_id"])
		assert.Equal(t, "event-42", entry["event_id"])
		count++
	}
	assert.Equal(t, 2, count)
	var remaining int64
	require.NoError(t, db.Master().WithContext(t.Context()).Table("entries").Count(&remaining).Error)
	assert.Zero(t, remaining)
}

func TestTransactionCommitAndRollback(t *testing.T) {
	for _, commit := range []bool{true, false} {
		name := "commit"
		if !commit {
			name = "rollback"
		}
		t.Run(name, func(t *testing.T) {
			repository, db := newTransactionRepository(t)
			ctx, err := repository.Begin(t.Context(), sql.LevelDefault)
			require.NoError(t, err)
			assert.True(t, repository.InTransaction(ctx))
			assert.False(t, repository.InTransaction(t.Context()))
			require.NoError(t, repository.GetTx(ctx).Exec("INSERT INTO entries (value) VALUES (?)", "pending").Error)
			var values []string
			require.NoError(t, repository.GetReadDB(ctx).Table("entries").Pluck("value", &values).Error)
			assert.Equal(t, []string{"pending"}, values, "transaction reads must see its uncommitted writes")
			if commit {
				require.NoError(t, repository.Commit(ctx))
				assertTransactionValues(t, db, "pending")
			} else {
				require.NoError(t, repository.Rollback(ctx))
				assertTransactionValues(t, db)
			}
		})
	}
}

func TestWithTransactionCompletion(t *testing.T) {
	cause := errors.New("work failed")
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "success commits"},
		{name: "error rolls back", err: cause},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, db := newTransactionRepository(t)
			calls := 0
			err := repository.WithTransaction(t.Context(), func(ctx context.Context) error {
				calls++
				if err := repository.GetTx(ctx).
					Exec("INSERT INTO entries (value) VALUES (?)", "pending").
					Error; err != nil {
					return err
				}
				return test.err
			})
			assert.Equal(t, 1, calls)
			if test.err == nil {
				require.NoError(t, err)
				assertTransactionValues(t, db, "pending")
			} else {
				require.ErrorIs(t, err, cause)
				assertTransactionValues(t, db)
			}
		})
	}
}

func TestTransactionRejectsNestedBegin(t *testing.T) {
	for _, name := range []string{"Begin", "WithTransaction"} {
		t.Run(name, func(t *testing.T) {
			repository, db := newTransactionRepository(t)
			ctx, err := repository.Begin(t.Context(), sql.LevelDefault)
			require.NoError(t, err)
			if name == "Begin" {
				var nested context.Context
				nested, err = repository.Begin(ctx, sql.LevelDefault)
				assert.Same(t, ctx, nested, "rejected nesting must preserve the outer transaction context")
			} else {
				err = repository.WithTransaction(ctx, func(context.Context) error {
					t.Error("nested transaction invoked its callback")
					return nil
				})
			}
			require.ErrorContains(t, err, "transaction already exists")
			require.NoError(t, repository.GetTx(ctx).Exec("INSERT INTO entries (value) VALUES (?)", "outer").Error)
			require.NoError(t, repository.Commit(ctx), "the outer transaction must remain usable")
			assertTransactionValues(t, db, "outer")
		})
	}
}

func TestTransactionRequiresContext(t *testing.T) {
	for _, name := range []string{"commit", "rollback"} {
		t.Run(name, func(t *testing.T) {
			repository, db := newTransactionRepository(t)
			var err error
			if name == "commit" {
				err = repository.Commit(t.Context())
			} else {
				err = repository.Rollback(t.Context())
			}
			require.ErrorContains(t, err, "no transaction found in context")
			assertTransactionValues(t, db)
		})
	}
}

func TestTransactionRejectsCompletedOperations(t *testing.T) {
	for _, test := range []struct {
		name        string
		commitFirst bool
		commitAgain bool
	}{
		{name: "commit after commit", commitFirst: true, commitAgain: true},
		{name: "rollback after commit", commitFirst: true},
		{name: "commit after rollback", commitAgain: true},
		{name: "rollback after rollback"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, db := newTransactionRepository(t)
			ctx, err := repository.Begin(t.Context(), sql.LevelDefault)
			require.NoError(t, err)
			require.NoError(t, repository.GetTx(ctx).Exec("INSERT INTO entries (value) VALUES (?)", "completed").Error)
			if test.commitFirst {
				require.NoError(t, repository.Commit(ctx))
			} else {
				require.NoError(t, repository.Rollback(ctx))
			}
			if test.commitAgain {
				err = repository.Commit(ctx)
			} else {
				err = repository.Rollback(ctx)
			}
			require.ErrorIs(t, err, sql.ErrTxDone)
			if test.commitFirst {
				assertTransactionValues(t, db, "completed")
			} else {
				assertTransactionValues(t, db)
			}
		})
	}
}

func TestWithTransactionRollsBackPanics(t *testing.T) {
	for _, rollbackFirst := range []bool{false, true} {
		name := "active transaction"
		if rollbackFirst {
			name = "rollback already completed"
		}
		t.Run(name, func(t *testing.T) {
			repository, db := newTransactionRepository(t)
			cause := errors.New("transaction panicked")
			require.PanicsWithValue(t, cause, func() {
				_ = repository.WithTransaction(t.Context(), func(ctx context.Context) error {
					require.NoError(
						t,
						repository.GetTx(ctx).Exec("INSERT INTO entries (value) VALUES (?)", "pending").Error,
					)
					if rollbackFirst {
						require.NoError(t, repository.Rollback(ctx))
					}
					panic(cause)
				})
			})
			assertTransactionValues(t, db)
			require.NoError(t, repository.WithTransaction(t.Context(), func(ctx context.Context) error {
				return repository.GetTx(ctx).Exec("INSERT INTO entries (value) VALUES (?)", "after panic").Error
			}))
			assertTransactionValues(t, db, "after panic")
		})
	}
}

func TestWithTransactionRejectsCanceledContext(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name := "canceled"
		if expired {
			name = "expired"
		}
		t.Run(name, func(t *testing.T) {
			repository, db := newTransactionRepository(t)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			cause := context.Canceled
			if expired {
				ctx, cancel = context.WithDeadline(t.Context(), time.Unix(0, 0))
				defer cancel()
				cause = context.DeadlineExceeded
			}
			err := repository.WithTransaction(ctx, func(context.Context) error {
				t.Error("canceled transaction invoked its callback")
				return nil
			})
			require.ErrorIs(t, err, cause)
			assertTransactionValues(t, db)
		})
	}
}

func TestWithTransactionCancellationRollsBackWrites(t *testing.T) {
	repository, db := newTransactionRepository(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := repository.WithTransaction(ctx, func(txCtx context.Context) error {
		if err := repository.GetTx(txCtx).Exec("INSERT INTO entries (value) VALUES (?)", "pending").Error; err != nil {
			return err
		}
		cancel()
		return txCtx.Err()
	})
	require.ErrorIs(t, err, context.Canceled)
	assertTransactionValues(t, db)
	require.NoError(t, repository.WithTransaction(t.Context(), func(ctx context.Context) error {
		return repository.GetTx(ctx).Exec("INSERT INTO entries (value) VALUES (?)", "after cancellation").Error
	}))
	assertTransactionValues(t, db, "after cancellation")
}

func TestWithTransactionPreservesRollbackErrors(t *testing.T) {
	cause := errors.New("work failed after rollback")
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "commit after callback rollback"},
		{name: "callback error after rollback", err: cause},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, db := newTransactionRepository(t)
			err := repository.WithTransaction(t.Context(), func(ctx context.Context) error {
				if err := repository.GetTx(ctx).
					Exec("INSERT INTO entries (value) VALUES (?)", "pending").
					Error; err != nil {
					return err
				}
				if err := repository.Rollback(ctx); err != nil {
					return err
				}
				return test.err
			})
			require.ErrorIs(t, err, sql.ErrTxDone)
			if test.err != nil {
				assert.ErrorIs(t, err, cause, "cleanup failure must preserve the callback's error")
			} else {
				assert.ErrorContains(t, err, "error committing transaction")
			}
			assertTransactionValues(t, db)
		})
	}
}

func TestWithTransactionRollsBackConstraintFailures(t *testing.T) {
	repository, db := newTransactionRepository(t)
	require.NoError(t, db.Master().WithContext(t.Context()).Exec(
		"CREATE UNIQUE INDEX entries_unique_value ON entries (value)",
	).Error)
	err := repository.WithTransaction(t.Context(), func(ctx context.Context) error {
		for range 2 {
			if err := repository.GetTx(ctx).
				Exec("INSERT INTO entries (value) VALUES (?)", "duplicate").
				Error; err != nil {
				return err
			}
		}
		return nil
	})
	require.Error(t, err)
	assert.True(t, repository.IsDuplicateKeyError(err))
	assertTransactionValues(t, db)
	require.NoError(t, repository.WithTransaction(t.Context(), func(ctx context.Context) error {
		return repository.GetTx(ctx).Exec("INSERT INTO entries (value) VALUES (?)", "recovered").Error
	}))
	assertTransactionValues(t, db, "recovered")
}

func newTransactionRepository(t *testing.T) (*database.BaseRepository, database.Database) {
	t.Helper()
	db, _ := integration.NewDatabase(t)
	identifier := "INTEGER PRIMARY KEY"
	switch db.Master().Name() {
	case "postgres":
		identifier = "BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY"
	case "mysql":
		identifier = "BIGINT AUTO_INCREMENT PRIMARY KEY"
	}
	require.NoError(t, db.Master().WithContext(t.Context()).Exec(
		"CREATE TABLE entries (id "+identifier+", value VARCHAR(100) NOT NULL)",
	).Error)
	return database.NewBaseRepository(db), db
}

func assertTransactionValues(t *testing.T, db database.Database, want ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	values := []string{}
	require.NoError(t, db.Master().WithContext(ctx).Table("entries").Order("id").Pluck("value", &values).Error)
	assert.Equal(t, append([]string{}, want...), values)
}
