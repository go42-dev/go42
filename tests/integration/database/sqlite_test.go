package database_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/go42-dev/go42/internal/database/sqlite"
	"github.com/go42-dev/go42/internal/database/sqlite/migrate"
)

func TestMigratePreservesSchema(t *testing.T) {
	for _, test := range []struct {
		name       string
		path       string
		options    []sqlite.Option
		persistent bool
	}{
		{
			name:       "file",
			path:       filepath.Join(t.TempDir(), "app.db"),
			options:    []sqlite.Option{sqlite.WithMode("rwc"), sqlite.WithCacheMode("shared")},
			persistent: true,
		},
		{
			name:    "default_memory",
			path:    "file::memory:",
			options: []sqlite.Option{sqlite.WithMode("memory"), sqlite.WithCacheMode("shared")},
		},
		{
			name:    "named_memory",
			path:    "file:" + filepath.Join(t.TempDir(), "app.db"),
			options: []sqlite.Option{sqlite.WithMode("memory"), sqlite.WithCacheMode("shared")},
		},
		{
			name: "named_shared_memory_uri",
			path: "file:" + filepath.Join(t.TempDir(), "app.db") + "?mode=memory&cache=shared",
		},
		{
			name: "named_private_memory_uri",
			path: "file:" + filepath.Join(t.TempDir(), "app.db") + "?mode=memory&cache=private",
		},
		{name: "private_memory", path: ":memory:"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, err := sqlite.Open(test.path, test.options...)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Shutdown(context.Background())) })
			sqlDB, err := db.Master().DB()
			require.NoError(t, err)
			schemaPath := "../../../migrate/sqlite"
			require.NoError(t, migrate.Migrate(t.Context(), sqlDB, schemaPath))

			var users int64
			require.NoError(t, db.Master().WithContext(t.Context()).Table("auth_users").Count(&users).Error)
			require.Positive(t, users, "the migrated seed data must remain available")

			const userUUID = "00000000-0000-0000-0000-000000000042"
			require.NoError(t, db.Master().WithContext(t.Context()).Exec(
				"INSERT INTO auth_users (uuid, email) VALUES (?, ?)", userUUID, "migration-test@example.com").Error)
			require.NoError(t, migrate.Migrate(t.Context(), sqlDB, schemaPath))
			require.NoError(t, db.Slave().WithContext(t.Context()).Table("auth_users").
				Where("uuid = ?", userUUID).Count(&users).Error)
			require.Equal(t, int64(1), users, "application writes must survive another migration run")

			require.NoError(t, db.Shutdown(t.Context()))
			reopened, err := sqlite.Open(test.path, test.options...)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reopened.Shutdown(context.Background())) })
			var tables int64
			require.NoError(t, reopened.Master().WithContext(t.Context()).Table("sqlite_master").
				Where("type = ? AND name = ?", "table", "auth_users").Count(&tables).Error)
			if test.persistent {
				require.Equal(t, int64(1), tables, "file databases must survive application shutdown")
			} else {
				require.Zero(t, tables, "application shutdown must release the in-memory database")
			}
		})
	}
}

func TestMigrateLeavesCallerPoolOpenOnError(t *testing.T) {
	for _, test := range []struct {
		name string
		sql  string
		err  string
	}{
		{name: "missing_schema", err: "failed to create goose provider"},
		{name: "invalid_sql", sql: "-- +goose Up\nINSERT INTO missing_table VALUES (1);\n", err: "migration failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, err := sqlite.Open(":memory:")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Shutdown(context.Background())) })
			sqlDB, err := db.Master().DB()
			require.NoError(t, err)
			require.NoError(
				t,
				db.Master().WithContext(t.Context()).Exec("CREATE TABLE retained_data (value INTEGER)").Error,
			)
			require.NoError(t, db.Master().WithContext(t.Context()).Exec("INSERT INTO retained_data VALUES (42)").Error)

			schemaPath := t.TempDir()
			if test.sql == "" {
				schemaPath = filepath.Join(schemaPath, "missing")
			} else {
				require.NoError(
					t,
					os.WriteFile(filepath.Join(schemaPath, "00001_invalid.sql"), []byte(test.sql), 0o600),
				)
			}
			require.ErrorContains(t, migrate.Migrate(t.Context(), sqlDB, schemaPath), test.err)
			var value int
			require.NoError(
				t,
				db.Master().WithContext(t.Context()).Raw("SELECT value FROM retained_data").Scan(&value).Error,
			)
			require.Equal(t, 42, value, "migration errors must leave the caller's database open and intact")
		})
	}
}
