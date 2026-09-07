package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"

	"github.com/pressly/goose/v3"
)

// Migrate applies migrations using the application's pool so in-memory databases
// stay alive. The caller owns the pool and is responsible for closing it.
func Migrate(ctx context.Context, db *sql.DB, schemaPath string) error {
	provider, err := goose.NewProvider(
		goose.DialectSQLite3,
		db,
		os.DirFS(schemaPath),
		goose.WithLogger(
			slog.NewLogLogger(
				slog.Default().Handler().WithAttrs(
					[]slog.Attr{slog.String("component", "migrate")},
				),
				slog.LevelInfo,
			),
		),
		goose.WithVerbose(true),
	)
	if err != nil {
		return fmt.Errorf("failed to create goose provider: %w", err)
	}

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migration failed: %w", err)
	}

	return nil
}
