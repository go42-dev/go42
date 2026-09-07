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

func TestOutboxPublishTimeoutConfiguration(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  time.Duration
		valid bool
	}{
		{name: "default", want: 10 * time.Second, valid: true},
		{name: "override", value: "250ms", want: 250 * time.Millisecond, valid: true},
		{name: "zero", value: "0s"},
		{name: "negative", value: "-1s"},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment := map[string]string{}
			if test.value != "" {
				environment["OUTBOX_PUBLISH_TIMEOUT"] = test.value
			}
			var cfg config.Outbox
			require.NoError(t, env.ParseWithOptions(&cfg, env.Options{
				TagName: config.TagNameEnvVarName, DefaultValueTagName: config.TagNameDefaultValue,
				Environment: environment,
			}))
			err := tools.ValidateStructCompact(cfg)
			if !test.valid {
				require.ErrorContains(t, err, "must be greater than 0")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, cfg.PublishTimeout)
		})
	}
}

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
