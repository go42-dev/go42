package database_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go42-dev/go42/internal/database"
	"github.com/go42-dev/go42/internal/database/sqlite"
	"github.com/go42-dev/go42/internal/tools"
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
