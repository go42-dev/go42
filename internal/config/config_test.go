package config_test

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go42-dev/go42/internal/config"
	"github.com/go42-dev/go42/internal/tools"
)

func TestPgsqlDSNRoundTrip(t *testing.T) {
	for _, test := range []struct {
		name, host, user, password, database string
		port                                 int
	}{
		{"ordinary", "localhost", "review", "plain-password", "review", 5433},
		{"reserved characters", "127.0.0.1", "u:@%/?#", "p:@%/?#", "review/%?#", 5432},
		{"percent sequences", "127.0.0.1", "u%41", "p%2Fss", "review%23name", 5432},
		{"spaces and unicode", "localhost", "review user", "päss word", "review 数据", 5432},
		{"quotes and backslashes", "localhost", "u'\\ser", "p'\\\"ss", "review'\\name", 5432},
		{"IPv6", "::1", "review", "plain-password", "review", 6543},
		{"IPv6 zone", "fe80::1%eth0", "review", "plain-password", "review", 6543},
	} {
		t.Run(test.name, func(t *testing.T) {
			master := config.PgsqlMaster{
				Host: test.host, Port: test.port, User: test.user, Password: test.password, Name: test.database,
			}
			slave := config.PgsqlSlave{
				Host: test.host, Port: test.port, User: test.user, Password: test.password, Name: test.database,
			}
			for role, dsn := range map[string]string{"master": master.DSN(), "slave": slave.DSN()} {
				t.Run(role, func(t *testing.T) {
					parsed, err := pgx.ParseConfig(dsn)
					require.NoError(t, err)
					assert.Equal(t, test.user, parsed.User)
					assert.Equal(t, test.password, parsed.Password)
					assert.Equal(t, test.database, parsed.Database)
					assert.Equal(t, test.host, parsed.Host)
					assert.Equal(t, test.port, int(parsed.Port))
				})
			}
		})
	}
}

func TestMysqlDSNRoundTrip(t *testing.T) {
	for _, test := range []struct {
		name, host, user, password, database, charset string
		port                                          int
	}{
		{"ordinary", "localhost", "review", "plain-password", "review", "utf8mb4", 3307},
		{"raw passwords", "127.0.0.1", "review", "a%2F/b#c?d@e:f", "review", "utf8mb4", 3306},
		{"reserved characters", "127.0.0.1", "u%@?#", "plain-password", "review%name?#", "utf8mb4", 3306},
		{"percent sequences", "localhost", "u%41", "p%2Fss", "review%41", "utf8mb4", 3306},
		{"spaces and unicode", "localhost", "review user", "päss word", "review 数据", "utf8mb4", 3306},
		{"IPv6", "::1", "review", "plain-password", "review", "utf8mb4", 63306},
		{"IPv6 zone", "fe80::1%eth0", "review", "plain-password", "review", "utf8mb4", 63306},
		{"charset fallback", "localhost", "review", "plain-password", "review", "utf8mb4,utf8", 3306},
	} {
		t.Run(test.name, func(t *testing.T) {
			master := config.MysqlMaster{
				Host: test.host, Port: test.port, User: test.user, Password: test.password,
				Name: test.database, Charset: test.charset,
			}
			slave := config.MysqlSlave{
				Host: test.host, Port: test.port, User: test.user, Password: test.password,
				Name: test.database, Charset: test.charset,
			}
			for role, dsn := range map[string]string{"master": master.DSN(), "slave": slave.DSN()} {
				t.Run(role, func(t *testing.T) {
					parsed, err := mysql.ParseDSN(dsn)
					require.NoError(t, err)
					assert.Equal(t, test.user, parsed.User)
					assert.Equal(t, test.password, parsed.Passwd)
					assert.Equal(t, test.database, parsed.DBName)
					assert.Equal(t, "tcp", parsed.Net)
					host, port, err := net.SplitHostPort(parsed.Addr)
					require.NoError(t, err)
					assert.Equal(t, test.host, host)
					assert.Equal(t, strconv.Itoa(test.port), port)
					assert.True(t, parsed.ParseTime)
					assert.Equal(t, time.UTC, parsed.Loc)
					assert.True(t, parsed.AllowNativePasswords)
					assert.True(t, parsed.CheckConnLiveness)

					// Read the parsed charset list through the driver's canonical DSN.
					canonical := parsed.FormatDSN()
					query := canonical[strings.LastIndexByte(canonical, '?')+1:]
					assert.Contains(t, strings.Split(query, "&"), "charset="+test.charset)
				})
			}
		})
	}
}

func TestDatabaseDSNWithEmptyHost(t *testing.T) {
	assert.Empty(t, (config.PgsqlMaster{}).DSN())
	assert.Empty(t, (config.PgsqlSlave{}).DSN())
	assert.Empty(t, (config.MysqlSlave{}).DSN())

	master := config.MysqlMaster{Port: 3306, User: "review", Password: "password", Name: "review", Charset: "utf8mb4"}
	require.NotEmpty(t, master.DSN())
	parsed, err := mysql.ParseDSN(master.DSN())
	require.NoError(t, err)
	assert.Equal(t, ":3306", parsed.Addr)
}

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
