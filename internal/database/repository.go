package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"gorm.io/gorm"
)

type BaseRepository struct {
	db Database
}

func NewBaseRepository(db Database) *BaseRepository {
	return &BaseRepository{db: db}
}

type ctxKey string

var ctxKeyTx ctxKey = "transaction"

func (r *BaseRepository) GetTx(ctx context.Context) *gorm.DB {
	if tx, ok := ctx.Value(ctxKeyTx).(*gorm.DB); ok {
		return tx
	}
	return r.db.Master().WithContext(ctx)
}

func (r *BaseRepository) GetReadDB(ctx context.Context) *gorm.DB {
	if tx, ok := ctx.Value(ctxKeyTx).(*gorm.DB); ok {
		return tx
	}
	return r.db.Slave().WithContext(ctx)
}

func (r *BaseRepository) InTransaction(ctx context.Context) bool {
	_, ok := ctx.Value(ctxKeyTx).(*gorm.DB)
	return ok
}

func (r *BaseRepository) Begin(
	ctx context.Context, isolationLvl sql.IsolationLevel,
) (context.Context, error) {
	if _, ok := ctx.Value(ctxKeyTx).(*gorm.DB); ok {
		return ctx, errors.New("transaction already exists in context")
	}
	tx := r.db.Master().WithContext(ctx).Begin(&sql.TxOptions{Isolation: isolationLvl})
	if tx.Error != nil {
		return ctx, fmt.Errorf("failed to begin transaction: %w", tx.Error)
	}
	return context.WithValue(ctx, ctxKeyTx, tx), nil
}

func (r *BaseRepository) Commit(ctx context.Context) error {
	tx, ok := ctx.Value(ctxKeyTx).(*gorm.DB)
	if !ok {
		return errors.New("no transaction found in context")
	}
	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}

func (r *BaseRepository) Rollback(ctx context.Context) error {
	tx, ok := ctx.Value(ctxKeyTx).(*gorm.DB)
	if !ok {
		return errors.New("no transaction found in context")
	}
	if err := tx.Rollback().Error; err != nil {
		return fmt.Errorf("failed to rollback transaction: %w", err)
	}
	return nil
}

func (r *BaseRepository) WithTransaction(ctx context.Context, fn func(txCtx context.Context) error) error {
	txCtx, err := r.Begin(ctx, sql.LevelDefault)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	defer func() {
		if rec := recover(); rec != nil {
			if rbErr := r.Rollback(txCtx); rbErr != nil {
				slog.Default().
					With(slog.String("component", "db-repository")).
					Error("panic: rollback failed", slog.Any("err", rbErr))
			}
			// we handled panic only to roll back the transaction
			// actual handling of panic should be done by the caller
			panic(rec)
		}
	}()

	if err := fn(txCtx); err != nil {
		if rbErr := r.Rollback(txCtx); rbErr != nil {
			return fmt.Errorf("error executing transaction (rollback failed: %w): %w", rbErr, err)
		}
		return err
	}

	if err := r.Commit(txCtx); err != nil {
		rbErr := r.Rollback(txCtx)
		if rbErr != nil {
			return fmt.Errorf("error committing transaction (rollback failed: %w): %w", rbErr, err)
		}
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

func (r *BaseRepository) IsNotFoundError(err error) bool {
	return r.db.IsNotFoundError(err)
}

func (r *BaseRepository) IsDuplicateKeyError(err error) bool {
	return r.db.IsDuplicateKeyError(err)
}
