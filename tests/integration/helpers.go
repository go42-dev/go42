// #nosec

package integration

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go42-dev/go42/internal/config"
	"github.com/go42-dev/go42/internal/database"
	"github.com/go42-dev/go42/internal/database/mysql"
	"github.com/go42-dev/go42/internal/database/pgsql"
	"github.com/go42-dev/go42/internal/database/sqlite"
)

const (
	httpServerAddressEnvVarName = "HTTP_SERVER_ADDRESS"
	httpAPIKeyEnvVarName        = "HTTP_API_KEY"
	grpcServerAddressEnvVarName = "GRPC_SERVER_ADDRESS"
	grpcAPIKeyEnvVarName        = "GRPC_API_KEY"
)

const (
	defaultHttpServerAddress = "http://localhost:8080"
	defaultGrpcServerAddress = "localhost:50051"
	defaultGrpcAPIKey        = "api_kXqdf2uQ7hmOARp-pZrhA6_IsZSeKCmSEM4YFKBGIzA"
)

var (
	customHttpServerAddress string
	customGrpcServerAddress string
)

func init() {
	value, found := os.LookupEnv(httpServerAddressEnvVarName)
	if found {
		customHttpServerAddress = strings.TrimRight(value, "/")
	}
	value, found = os.LookupEnv(grpcServerAddressEnvVarName)
	if found {
		customGrpcServerAddress = strings.TrimRight(value, "/")
	}
}

func HTTPServerAddress() string {
	if customHttpServerAddress != "" {
		return customHttpServerAddress
	}
	return defaultHttpServerAddress
}

func HTTPAPIKey() string {
	return os.Getenv(httpAPIKeyEnvVarName)
}

func GRPCServerAddress() string {
	if customGrpcServerAddress != "" {
		return customGrpcServerAddress
	}
	return defaultGrpcServerAddress
}

func GRPCAPIKey() string {
	if apiKey := os.Getenv(grpcAPIKeyEnvVarName); apiKey != "" {
		return apiKey
	}
	return defaultGrpcAPIKey
}

// ---

const randomStringDefaultLength = 8

// GenerateRandomString returns a string with given prefix.
func GenerateRandomString(prefix string) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, randomStringDefaultLength)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return fmt.Sprintf("%s-%s", prefix, string(b))
}

// NewDatabase creates an empty database and closes and removes it during test cleanup.
// It uses the application's DATABASE_* settings. The configured MySQL/PostgreSQL
// user must have permission to create and drop databases.
// The returned DSN connects to the new database for tests that open additional connections.
func NewDatabase(t *testing.T) (database.Database, string) {
	t.Helper()
	var cfg config.Database
	require.NoError(t, env.ParseWithOptions(&cfg, env.Options{
		TagName: config.TagNameEnvVarName, DefaultValueTagName: config.TagNameDefaultValue,
	}))
	open := func(dsn string) database.Database {
		t.Helper()
		var db database.Database
		var err error
		switch cfg.Engine {
		case "sqlite":
			db, err = sqlite.Open(dsn)
		case "mysql":
			db, err = mysql.Open(t.Context(), dsn, "", mysql.WithConnectRetryTimeout(5*time.Second))
		case "pgsql":
			db, err = pgsql.Open(t.Context(), dsn, "", pgsql.WithConnectRetryTimeout(5*time.Second))
		default:
			t.Fatalf("unsupported DATABASE_ENGINE: %s", cfg.Engine)
		}
		require.NoError(t, err)
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			assert.NoError(t, db.Shutdown(ctx))
		})
		return db
	}
	if cfg.Engine == "sqlite" {
		dsn := filepath.Join(t.TempDir(), "test.db")
		return open(dsn), dsn
	}
	name := "go42_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	var adminDSN, dsn, identifier string
	switch cfg.Engine {
	case "mysql":
		adminDSN = cfg.Mysql.Master.DSN()
		cfg.Mysql.Master.Name = name
		dsn, identifier = cfg.Mysql.Master.DSN(), "`"+name+"`"
	case "pgsql":
		adminDSN = cfg.Pgsql.Master.DSN()
		cfg.Pgsql.Master.Name = name
		dsn, identifier = cfg.Pgsql.Master.DSN(), `"`+name+`"`
	default:
		t.Fatalf("unsupported DATABASE_ENGINE: %s", cfg.Engine)
	}
	require.NotEmpty(t, adminDSN, "database master connection is not configured")
	admin := open(adminDSN)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, admin.Master().WithContext(ctx).Exec("CREATE DATABASE "+identifier).Error,
		"the configured database user must have permission to create test databases")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		assert.NoError(t, admin.Master().WithContext(ctx).Exec("DROP DATABASE "+identifier).Error)
	})
	return open(dsn), dsn
}
