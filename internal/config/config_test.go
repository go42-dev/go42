package config_test

import (
	"testing"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go42-dev/go42/internal/config"
	"github.com/go42-dev/go42/internal/tools"
)

func TestOutboxCleanupDefaults(t *testing.T) {
	var cfg config.Outbox
	require.NoError(t, env.ParseWithOptions(&cfg, env.Options{
		TagName: config.TagNameEnvVarName, DefaultValueTagName: config.TagNameDefaultValue,
		Environment: map[string]string{},
	}))
	require.NoError(t, tools.ValidateStructCompact(cfg))
	assert.Equal(t, time.Hour, cfg.CleanupInterval)
	assert.Equal(t, 7*24*time.Hour, cfg.CleanupRetention)
	assert.Equal(t, 1000, cfg.CleanupBatchSize)
}

func TestOutboxCleanupConfigurationRejectsNonpositiveValues(t *testing.T) {
	t.Setenv("OUTBOX_CLEANUP_INTERVAL", "1h")
	t.Setenv("OUTBOX_CLEANUP_RETENTION", "168h")
	t.Setenv("OUTBOX_CLEANUP_BATCH_SIZE", "1000")
	for name, values := range map[string][]string{
		"OUTBOX_CLEANUP_INTERVAL":   {"0s", "-1s"},
		"OUTBOX_CLEANUP_RETENTION":  {"0s", "-1s"},
		"OUTBOX_CLEANUP_BATCH_SIZE": {"0", "-1"},
	} {
		for _, value := range values {
			t.Run(name+"="+value, func(t *testing.T) {
				t.Setenv(name, value)
				_, err := config.New()
				require.ErrorContains(t, err, "must be greater than 0")
			})
		}
	}
}
