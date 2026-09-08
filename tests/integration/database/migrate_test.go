package database_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/go42-dev/go42/internal/database"
	mysqlMigrate "github.com/go42-dev/go42/internal/database/mysql/migrate"
	pgsqlMigrate "github.com/go42-dev/go42/internal/database/pgsql/migrate"
	"github.com/go42-dev/go42/tests/integration"
)

func TestDatabaseMigrationsPreserveWritesOnRepeatedRuns(t *testing.T) {
	h := newMigrationHarness(t)
	h.write(t, "INSERT INTO migration_entries VALUES (2, 'migrated');")
	require.NoError(t, h.run(t.Context(), h.uri, h.path))
	require.NoError(t, h.db.Master().WithContext(t.Context()).Exec(
		"INSERT INTO migration_entries VALUES (3, 'application write')",
	).Error)
	require.NoError(t, h.run(t.Context(), h.uri, h.path))
	h.assertValues(t, "original", "migrated", "application write")
	h.assertAppliedOnce(t)
}

func TestDatabaseMigrationsRollBackFailuresAndRecover(t *testing.T) {
	for _, name := range []string{"missing directory", "invalid SQL", "invalid migration"} {
		t.Run(name, func(t *testing.T) {
			h := newMigrationHarness(t)
			path := h.path
			want := "migration failed"
			switch name {
			case "missing directory":
				path = filepath.Join(path, "missing")
				want = "failed to create goose provider"
			case "invalid SQL":
				// DML rolls back on both engines; MySQL DDL has implicit commits.
				h.write(t, "INSERT INTO migration_entries VALUES (2, 'rolled back');\n"+
					"INSERT INTO missing_migration_table VALUES (1);")
			case "invalid migration":
				require.NoError(t, os.WriteFile(filepath.Join(path, "00001_test.sql"),
					[]byte("INSERT INTO migration_entries VALUES (2, 'missing goose directive');"), 0o600))
			}
			require.ErrorContains(t, h.run(t.Context(), h.uri, path), want)
			h.assertValues(t, "original")
			h.write(t, "INSERT INTO migration_entries VALUES (2, 'recovered');")
			require.NoError(t, h.run(t.Context(), h.uri, h.path), "failure must release the migration lock")
			h.assertValues(t, "original", "recovered")
			h.assertAppliedOnce(t)
		})
	}
}

func TestDatabaseMigrationsSerializeConcurrentRunners(t *testing.T) {
	for _, completion := range []string{"success", "cancellation", "deadline"} {
		t.Run(completion, func(t *testing.T) {
			h := newMigrationHarness(t)
			h.write(t, "UPDATE migration_entries SET value = 'migrated' WHERE id = 1;\n"+
				"INSERT INTO migration_entries VALUES (2, 'applied once');")
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			pool, err := h.db.Master().DB()
			require.NoError(t, err)
			blocker, err := pool.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = blocker.Rollback() }()
			_, err = blocker.ExecContext(ctx, "UPDATE migration_entries SET value = 'held' WHERE id = 1")
			require.NoError(t, err)
			first := h.start(t, ctx)
			h.waitForLock(t, ctx)

			secondCtx, stopSecond := context.WithCancel(ctx)
			defer stopSecond()
			if completion == "deadline" {
				secondCtx, stopSecond = context.WithTimeout(ctx, 150*time.Millisecond)
				defer stopSecond()
			}
			second := h.start(t, secondCtx)
			if completion != "deadline" {
				select {
				case err := <-second:
					t.Fatalf("second migrator returned while the first held the lock: %v", err)
				case <-time.After(150 * time.Millisecond):
				}
			}
			switch completion {
			case "cancellation":
				stopSecond()
				err := waitForMigration(t, second)
				require.ErrorIs(t, err, context.Canceled)
			case "deadline":
				err := waitForMigration(t, second)
				require.ErrorIs(t, err, context.DeadlineExceeded)
			}
			h.assertValues(t, "original")
			require.NoError(t, blocker.Rollback())
			require.NoError(t, waitForMigration(t, first))
			if completion == "success" {
				require.NoError(t, waitForMigration(t, second))
			} else {
				require.NoError(t, h.run(ctx, h.uri, h.path), "a canceled waiter must be able to retry")
			}
			h.assertValues(t, "migrated", "applied once")
			h.assertAppliedOnce(t)
		})
	}
}

func TestDatabaseMigrationsBoundConnectionRetries(t *testing.T) {
	h := newMigrationHarness(t)
	h.write(t, "INSERT INTO migration_entries VALUES (2, 'recovered');")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	uri := "postgres://user:qwerty@" + address + "/missing?sslmode=disable"
	if h.db.Master().Name() == "mysql" {
		uri = "user:qwerty@tcp(" + address + ")/missing?parseTime=true"
	}
	for _, expired := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
		want := context.DeadlineExceeded
		if !expired {
			cancel()
			want = context.Canceled
		}
		started := time.Now()
		err := h.run(ctx, uri, h.path)
		cancel()
		require.ErrorIs(t, err, want)
		require.Less(t, time.Since(started), 2*time.Second, "connection retries must respect the caller's context")
	}
	h.assertValues(t, "original")
	require.NoError(t, h.run(t.Context(), h.uri, h.path))
	h.assertValues(t, "original", "recovered")
	h.assertAppliedOnce(t)
}

type migrationHarness struct {
	db   database.Database
	uri  string
	path string
	run  func(context.Context, string, string) error
}

func newMigrationHarness(t *testing.T) *migrationHarness {
	t.Helper()
	db, uri := integration.NewDatabase(t)
	if db.Master().Name() == "sqlite" {
		t.Skip("PostgreSQL/MySQL migration tests require DATABASE_ENGINE=mysql or pgsql")
	}
	h := &migrationHarness{db: db, uri: uri, path: t.TempDir()}
	if db.Master().Name() == "postgres" {
		h.run = func(ctx context.Context, uri, path string) error {
			return pgsqlMigrate.Migrate(ctx, uri, path,
				pgsqlMigrate.WithConnectRetryTimeout(2*time.Second),
				pgsqlMigrate.WithConnectRetryBackoff(10*time.Millisecond, 20*time.Millisecond))
		}
	} else {
		h.run = func(ctx context.Context, uri, path string) error {
			return mysqlMigrate.Migrate(ctx, uri, path,
				mysqlMigrate.WithConnectRetryTimeout(2*time.Second),
				mysqlMigrate.WithConnectRetryBackoff(10*time.Millisecond, 20*time.Millisecond))
		}
	}
	require.NoError(t, db.Master().WithContext(t.Context()).Exec(
		"CREATE TABLE migration_entries (id INTEGER PRIMARY KEY, value VARCHAR(100) NOT NULL)",
	).Error)
	require.NoError(t, db.Master().WithContext(t.Context()).Exec(
		"INSERT INTO migration_entries VALUES (1, 'original')",
	).Error)
	return h
}

func (h *migrationHarness) write(t *testing.T, statements string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(h.path, "00001_test.sql"),
		[]byte("-- +goose Up\n"+statements+"\n"), 0o600))
}

func (h *migrationHarness) assertValues(t *testing.T, want ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var values []string
	require.NoError(
		t,
		h.db.Master().WithContext(ctx).Table("migration_entries").Order("id").Pluck("value", &values).Error,
	)
	require.Equal(t, want, values)
}

func (h *migrationHarness) assertAppliedOnce(t *testing.T) {
	t.Helper()
	var applied int64
	require.NoError(t, h.db.Master().WithContext(t.Context()).Table("goose_db_version").
		Where("version_id = ? AND is_applied = ?", 1, true).Count(&applied).Error)
	require.EqualValues(t, 1, applied)
}

func (h *migrationHarness) waitForLock(t *testing.T, ctx context.Context) {
	t.Helper()
	// Observe the stable lock identity used to coordinate different application versions.
	query := `SELECT COUNT(*) FROM pg_locks WHERE locktype = 'advisory' AND objid = 1288990
		AND database = (SELECT oid FROM pg_database WHERE datname = current_database()) AND granted`
	if h.db.Master().Name() == "mysql" {
		query = "SELECT COUNT(*) FROM goose_lock WHERE lock_id = 4097083626 AND locked = true"
	}
	require.Eventually(t, func() bool {
		var held int
		err := h.db.Master().WithContext(ctx).Raw(query).Scan(&held).Error
		return err == nil && held == 1
	}, 3*time.Second, 10*time.Millisecond, "first migrator must acquire its lock before the second starts")
}

func (h *migrationHarness) start(t *testing.T, ctx context.Context) <-chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	stopped := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(3 * time.Second):
			t.Error("migration did not stop after cancellation")
		}
	})
	go func() {
		defer close(stopped)
		done <- h.run(ctx, h.uri, h.path)
	}()
	return done
}

func waitForMigration(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("migration did not finish within its deadline")
		return nil
	}
}
