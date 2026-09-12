package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	// Keep the lock ID stable so different application versions coordinate.
	lockID                 = 1288990
	lockRetryIntervalSec   = 5
	lockRetryCount         = 90 // 7.5 minutes before query time.
	unlockRetryIntervalSec = 2
	unlockRetryCount       = 30 // 1 minute before query time.
)

func Migrate(
	ctx context.Context,
	uri string,
	schemaPath string,
	opts ...Option,
) (returnErr error) {
	config := defaultOptions()

	for _, opt := range opts {
		opt(&config)
	}

	logger := config.logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	var db *sql.DB
	err := config.connectRetry.Do(ctx, "pgsql", logger, func(attemptCtx context.Context) error {
		conn, err := sql.Open("pgx", uri)
		if err != nil {
			return fmt.Errorf("failed to open database connection: %w", err)
		}
		if err := conn.PingContext(attemptCtx); err != nil {
			pingErr := fmt.Errorf("failed to ping database: %w", err)
			if closeErr := conn.Close(); closeErr != nil {
				return errors.Join(
					pingErr,
					fmt.Errorf("failed to close migration database: %w", closeErr),
				)
			}
			return pingErr
		}
		db = conn
		return nil
	})
	if err != nil {
		return err
	}

	// migrations have independent connections, so we can close the connection after migration
	defer func() {
		if err := db.Close(); err != nil {
			returnErr = errors.Join(
				returnErr,
				fmt.Errorf("failed to close migration database: %w", err),
			)
		}
	}()

	// locker is used to ensure that only one migration process runs at a time
	// this is required to prevent concurrent migrations that could lead to database inconsistencies
	locker, err := lock.NewPostgresSessionLocker(
		lock.WithLockID(lockID),
		lock.WithLockTimeout(lockRetryIntervalSec, lockRetryCount),
		lock.WithUnlockTimeout(unlockRetryIntervalSec, unlockRetryCount),
	)
	if err != nil {
		return fmt.Errorf("failed to create session locker: %w", err)
	}

	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		os.DirFS(schemaPath),
		goose.WithSlog(logger),
		goose.WithVerbose(true),
		goose.WithSessionLocker(locker),
	)
	if err != nil {
		return fmt.Errorf("failed to create goose provider: %w", err)
	}

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migration failed: %w", err)
	}

	return nil
}
