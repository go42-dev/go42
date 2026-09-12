package pgsql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	slogGorm "github.com/orandin/slog-gorm"
	"go.opentelemetry.io/otel/attribute"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/plugin/opentelemetry/tracing"

	"github.com/go42-dev/go42/internal/tools"
)

type Postgres struct {
	logger *slog.Logger

	master     *gorm.DB
	masterConn *sql.DB
	slave      *gorm.DB
	slaveConn  *sql.DB

	connMaxIdleTime time.Duration
	connMaxLifetime time.Duration
	maxOpenConns    int
	maxIdleConns    int

	queryTimeout time.Duration
	connectRetry tools.StartupRetryPolicy

	queryLogging bool
}

func Open(ctx context.Context, masterDSN string, slaveDSN string, opts ...Option) (*Postgres, error) {
	w := &Postgres{
		connectRetry: tools.DefaultStartupRetryPolicy(),
	}

	for _, opt := range opts {
		opt(w)
	}
	if w.logger == nil {
		w.logger = slog.New(slog.DiscardHandler)
	}

	slogGormOpts := []slogGorm.Option{
		slogGorm.WithHandler(w.logger.Handler()),
		// log level translations: when gormDB sends X level -> slog handles it as Y level
		slogGorm.SetLogLevel(slogGorm.ErrorLogType, slog.LevelWarn),
		slogGorm.SetLogLevel(slogGorm.SlowQueryLogType, slog.LevelWarn),
		slogGorm.SetLogLevel(slogGorm.DefaultLogType, slog.LevelDebug),
	}

	if w.queryLogging {
		slogGormOpts = append(slogGormOpts, slogGorm.WithTraceAll())
	} else {
		slogGormOpts = append(slogGormOpts, slogGorm.WithIgnoreTrace())
	}

	// ---

	masterConn, err := w.connect(
		ctx, masterDSN,
		&gorm.Config{
			PrepareStmt: true,
			Logger:      slogGorm.New(slogGormOpts...),
		})
	if err != nil {
		return nil, err
	}
	masterConnDB, err := masterConn.DB()
	if err != nil {
		return nil, err
	}
	var slaveConnDB *sql.DB
	initializationComplete := false
	defer func() {
		if initializationComplete {
			return
		}
		if slaveConnDB != nil && slaveConnDB != masterConnDB {
			_ = slaveConnDB.Close()
		}
		_ = masterConnDB.Close()
	}()

	if err := masterConn.Use(tracing.NewPlugin(
		tracing.WithDBSystem("postgresql"),
		tracing.WithAttributes(attribute.String("db.role", "master")),
		tracing.WithoutServerAddress(),
		tracing.WithoutMetrics(),
	)); err != nil {
		return nil, err
	}

	masterConnDB.SetMaxOpenConns(w.maxOpenConns)
	masterConnDB.SetMaxIdleConns(w.maxIdleConns)
	masterConnDB.SetConnMaxLifetime(w.connMaxLifetime)
	masterConnDB.SetConnMaxIdleTime(w.connMaxIdleTime)

	w.master = masterConn
	w.masterConn = masterConnDB

	// ---

	if len(slaveDSN) > 0 {
		slaveConn, err := w.connect(
			ctx, slaveDSN,
			&gorm.Config{
				PrepareStmt: true,
				Logger:      slogGorm.New(slogGormOpts...),
			})
		if err != nil {
			return nil, err
		}

		slaveConnDB, err = slaveConn.DB()
		if err != nil {
			return nil, err
		}

		if err := slaveConn.Use(tracing.NewPlugin(
			tracing.WithDBSystem("postgresql"),
			tracing.WithAttributes(attribute.String("db.role", "slave")),
			tracing.WithoutServerAddress(),
			tracing.WithoutMetrics(),
		)); err != nil {
			return nil, err
		}

		slaveConnDB.SetMaxOpenConns(w.maxOpenConns)
		slaveConnDB.SetMaxIdleConns(w.maxIdleConns)
		slaveConnDB.SetConnMaxLifetime(w.connMaxLifetime)
		slaveConnDB.SetConnMaxIdleTime(w.connMaxIdleTime)

		w.slave = slaveConn
		w.slaveConn = slaveConnDB
	} else {
		w.slave = masterConn
		w.slaveConn = masterConnDB
	}

	initializationComplete = true
	return w, nil
}

func (w *Postgres) connect(ctx context.Context, dsn string, config *gorm.Config) (*gorm.DB, error) {
	dsn, err := withQueryTimeout(dsn, w.queryTimeout)
	if err != nil {
		return nil, err
	}

	config.NowFunc = func() time.Time { return time.Now().UTC() }

	// this affects only the initial connection ping
	config.DisableAutomaticPing = true

	var db *gorm.DB
	err = w.connectRetry.Do(ctx, "pgsql", w.logger, func(attemptCtx context.Context) error {
		conn, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn}), config)
		if err != nil {
			return fmt.Errorf("failed to open database connection: %w", err)
		}
		connDB, err := conn.DB()
		if err != nil {
			return fmt.Errorf("failed to get database instance: %w", err)
		}
		if err := connDB.PingContext(attemptCtx); err != nil {
			pingErr := fmt.Errorf("failed to ping database: %w", err)
			if closeErr := connDB.Close(); closeErr != nil {
				return errors.Join(
					pingErr,
					fmt.Errorf("failed to close database after ping failure: %w", closeErr),
				)
			}
			return pingErr
		}
		db = conn
		return nil
	})
	if err != nil {
		return nil, err
	}
	return db, nil
}

func withQueryTimeout(dsn string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		return dsn, nil
	}

	timeoutMilliseconds := int64((timeout + time.Millisecond - 1) / time.Millisecond)
	timeoutValue := strconv.FormatInt(timeoutMilliseconds, 10)

	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsedDSN, err := url.Parse(dsn)
		if err != nil {
			return "", fmt.Errorf("failed to configure query timeout: %w", err)
		}
		query := parsedDSN.Query()
		query.Set("statement_timeout", timeoutValue)
		parsedDSN.RawQuery = query.Encode()
		return parsedDSN.String(), nil
	}

	return strings.TrimSpace(dsn) + " statement_timeout=" + timeoutValue, nil
}

func (w *Postgres) Shutdown(ctx context.Context) error {
	doneChan := make(chan error, 1)
	go func() {
		masterErr := w.masterConn.Close()
		slaveErr := w.slaveConn.Close()
		doneChan <- errors.Join(masterErr, slaveErr)
	}()
	select {
	case <-ctx.Done():
		return errors.New("timeout")
	case err := <-doneChan:
		return err
	}
}

func (w *Postgres) Master() *gorm.DB {
	return w.master
}

func (w *Postgres) Slave() *gorm.DB {
	return w.slave
}

func (w *Postgres) Ping(ctx context.Context) error {
	if err := w.masterConn.PingContext(ctx); err != nil {
		return fmt.Errorf("master database ping failed: %w", err)
	}
	if w.slaveConn != w.masterConn {
		if err := w.slaveConn.PingContext(ctx); err != nil {
			return fmt.Errorf("slave database ping failed: %w", err)
		}
	}
	return nil
}

func (w *Postgres) IsNotFoundError(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

func (w *Postgres) IsDuplicateKeyError(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}
