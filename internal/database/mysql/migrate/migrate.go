package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

const (
	// Keep the lock table and ID stable so different application versions coordinate.
	lockTableName          = "goose_lock"
	lockID                 = 4097083626
	lockRetryIntervalSec   = 5
	lockRetryCount         = 90 // 7.5 minutes before query time and retry jitter.
	unlockRetryIntervalSec = 2
	unlockRetryCount       = 30 // 1 minute before query time and retry jitter.
	lockLeaseDuration      = 30 * time.Second
	lockHeartbeatInterval  = 5 * time.Second
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

	slog2mysql := &slog2mysql{logger, slog.LevelWarn}
	if err := mysql.SetLogger(slog2mysql); err != nil {
		return fmt.Errorf("failed to set MySQL slog2mysql: %w", err)
	}

	var db *sql.DB
	err := config.connectRetry.Do(ctx, "mysql", logger, func(attemptCtx context.Context) error {
		conn, err := sql.Open("mysql", uri)
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
	locker, err := lock.NewMySQLTableLocker(
		lock.WithTableName(lockTableName),
		lock.WithTableLockID(lockID),
		lock.WithTableLockTimeout(lockRetryIntervalSec*time.Second, lockRetryCount),
		lock.WithTableUnlockTimeout(unlockRetryIntervalSec*time.Second, unlockRetryCount),
		lock.WithTableLeaseDuration(lockLeaseDuration),
		lock.WithTableHeartbeatInterval(lockHeartbeatInterval),
		lock.WithTableLogger(logger),
	)
	if err != nil {
		return fmt.Errorf("failed to create migration locker: %w", err)
	}

	provider, err := goose.NewProvider(
		goose.DialectMySQL,
		db,
		os.DirFS(schemaPath),
		goose.WithSlog(logger),
		goose.WithVerbose(true),
		goose.WithLocker(locker),
	)
	if err != nil {
		return fmt.Errorf("failed to create goose provider: %w", err)
	}

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migration failed: %w", err)
	}

	return nil
}

// slog2mysql is a wrapper to adapt slog logger to the MySQL logger interface.
type slog2mysql struct {
	logger *slog.Logger
	level  slog.Level
}

func (l *slog2mysql) Print(v ...any) {
	l.logger.Log(context.Background(), l.level, fmt.Sprint(v...))
}
