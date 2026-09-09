//go:build resilience

package resilience

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

const (
	resilienceAppBinaryEnv   = "RESILIENCE_APP_BINARY"
	resilienceAppCoverDirEnv = "RESILIENCE_APP_COVERDIR"
)

type testApplication struct {
	command *exec.Cmd
	done    chan struct{}
	logFile *os.File

	waitLock sync.RWMutex
	waitErr  error
}

func TestApplicationReadinessTracksCriticalDependencies(t *testing.T) {
	resetProxy(t, proxyConfig{
		Name:     postgresProxyName,
		Listen:   "0.0.0.0:15432",
		Upstream: "pgsql:5432",
		Enabled:  true,
	})
	resetProxy(t, proxyConfig{
		Name:     redisProxyName,
		Listen:   "0.0.0.0:16379",
		Upstream: "redis:6379",
		Enabled:  true,
	})
	resetProxy(t, proxyConfig{
		Name:     natsProxyName,
		Listen:   "0.0.0.0:14222",
		Upstream: "nats:4222",
		Enabled:  true,
	})

	httpAddress := availableAddress(t)
	grpcAddress := availableAddress(t)
	app := startTestApplication(t, httpAddress, grpcAddress)
	defer stopTestApplication(t, app)

	assertHTTPStatus(t, app, "http://"+httpAddress+"/health", http.StatusOK)
	assertHTTPStatus(t, app, "http://"+httpAddress+"/ready", http.StatusOK)
	assertGRPCHealthStatus(t, app, grpcAddress, healthpb.HealthCheckResponse_SERVING)

	setProxyEnabled(t, postgresProxyName, false)
	assertHTTPStatus(t, app, "http://"+httpAddress+"/ready", http.StatusServiceUnavailable)
	assertGRPCHealthStatus(t, app, grpcAddress, healthpb.HealthCheckResponse_NOT_SERVING)
	setProxyEnabled(t, postgresProxyName, true)
	assertHTTPStatus(t, app, "http://"+httpAddress+"/ready", http.StatusOK)
	assertGRPCHealthStatus(t, app, grpcAddress, healthpb.HealthCheckResponse_SERVING)

	setProxyEnabled(t, redisProxyName, false)
	assertHTTPStatus(t, app, "http://"+httpAddress+"/ready", http.StatusServiceUnavailable)
	assertGRPCHealthStatus(t, app, grpcAddress, healthpb.HealthCheckResponse_NOT_SERVING)
	setProxyEnabled(t, redisProxyName, true)
	assertHTTPStatus(t, app, "http://"+httpAddress+"/ready", http.StatusOK)
	assertGRPCHealthStatus(t, app, grpcAddress, healthpb.HealthCheckResponse_SERVING)

	setProxyEnabled(t, natsProxyName, false)
	assertReadinessRemainsHealthy(t, app, "http://"+httpAddress+"/ready", grpcAddress)
	setProxyEnabled(t, natsProxyName, true)
}

func startTestApplication(t *testing.T, httpAddress string, grpcAddress string) *testApplication {
	t.Helper()
	binary := os.Getenv(resilienceAppBinaryEnv)
	if len(binary) == 0 {
		t.Fatalf("%s is not set; run this test through make test-resilience", resilienceAppBinaryEnv)
	}

	logFile, err := os.CreateTemp(t.TempDir(), "application-*.log")
	if err != nil {
		t.Fatalf("create application log: %v", err)
	}
	repositoryRoot := findRepositoryRoot(t)
	command := exec.Command(binary) //nolint:gosec
	command.Dir = repositoryRoot
	command.Stdout = logFile
	command.Stderr = logFile
	command.Env = []string{
		"SERVICE_NAME=resilience-test",
		"ENVIRONMENT=test",
		"LOG_OUTPUT=none",
		"STARTUP_CONNECT_TIMEOUT=10s",
		"STARTUP_RETRY_INITIAL_BACKOFF=50ms",
		"STARTUP_RETRY_MAX_BACKOFF=200ms",
		"READINESS_CHECK_TIMEOUT=200ms",
		"READINESS_CHECK_INTERVAL=100ms",
		"SHUTDOWN_GRACE_PERIOD=5s",
		"SHUTDOWN_WAIT_FOR_PROBE=0s",
		"SHUTDOWN_COMPONENT_TIMEOUT=1s",
		"DATABASE_ENGINE=pgsql",
		"DATABASE_MIGRATE_PATH=" + filepath.Join(repositoryRoot, "migrate"),
		"DATABASE_PGSQL_MASTER_HOST=127.0.0.1",
		"DATABASE_PGSQL_MASTER_PORT=15432",
		"DATABASE_PGSQL_QUERY_TIMEOUT=500ms",
		"CACHE_ENGINE=redis",
		"CACHE_REDIS_HOST=" + envOrDefault(redisAddressEnv, defaultRedisAddress),
		"CACHE_REDIS_DIAL_TIMEOUT=200ms",
		"CACHE_REDIS_READ_TIMEOUT=200ms",
		"CACHE_REDIS_WRITE_TIMEOUT=200ms",
		"EVENTS_ENGINE=nats",
		"NATS_JETSTREAM_AUTO_PROVISION=true",
		"NATS_DSN=" + envOrDefault(natsAddressEnv, defaultNATSAddress),
		"NATS_CONN_TIMEOUT=200ms",
		"NATS_MAX_RETRY=-1",
		"NATS_RETRY_DELAY=50ms",
		"NATS_SUB_TIMEOUT=2s",
		"NATS_SUB_ACK_TIMEOUT=2s",
		"NATS_SUB_CLOSE_TIMEOUT=2s",
		"SERVER_HTTP_LISTEN=" + httpAddress,
		"SERVER_HTTP_STATIC_ROOT=" + filepath.Join(repositoryRoot, "static"),
		"SERVER_HTTP_SWAGGER_ROOT=" + filepath.Join(repositoryRoot, "api", "openapi"),
		"SERVER_GRPC_LISTEN=" + grpcAddress,
	}
	// Keep app counters separate from the directory managed by go test.
	if coverDir := os.Getenv(resilienceAppCoverDirEnv); len(coverDir) > 0 {
		command.Env = append(command.Env, "GOCOVERDIR="+coverDir)
	}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start application: %v", err)
	}

	app := &testApplication{
		command: command,
		done:    make(chan struct{}),
		logFile: logFile,
	}
	go func() {
		err := command.Wait()
		app.waitLock.Lock()
		app.waitErr = err
		app.waitLock.Unlock()
		close(app.done)
	}()
	return app
}

func stopTestApplication(t *testing.T, app *testApplication) {
	t.Helper()
	if app.command.Process == nil {
		return
	}
	if err := app.command.Process.Signal(os.Interrupt); err != nil {
		t.Errorf("signal application shutdown: %v", err)
		_ = app.command.Process.Kill()
	}

	select {
	case <-app.done:
		err := app.waitError()
		if err != nil {
			t.Errorf("application shutdown: %v\n%s", err, applicationLogs(app))
		}
	case <-time.After(7 * time.Second):
		t.Errorf("application did not shut down within deadline\n%s", applicationLogs(app))
		_ = app.command.Process.Kill()
		<-app.done
	}
	_ = app.logFile.Close()
}

func assertHTTPStatus(t *testing.T, app *testApplication, url string, want int) {
	t.Helper()
	assertEventuallySucceeds(t, fmt.Sprintf("receive HTTP status %d from %s", want, url), func() error {
		if err := applicationExitError(app); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return err
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != want {
			return fmt.Errorf("status %d, want %d", response.StatusCode, want)
		}
		return nil
	})
}

func assertGRPCHealthStatus(
	t *testing.T,
	app *testApplication,
	address string,
	want healthpb.HealthCheckResponse_ServingStatus,
) {
	t.Helper()
	connection, err := grpc.NewClient(
		address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("create gRPC health client: %v", err)
	}
	defer func() { _ = connection.Close() }()
	client := healthpb.NewHealthClient(connection)
	assertEventuallySucceeds(t, "receive gRPC health status "+want.String(), func() error {
		if err := applicationExitError(app); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		response, err := client.Check(ctx, &healthpb.HealthCheckRequest{})
		if err != nil {
			return err
		}
		if response.Status != want {
			return fmt.Errorf("gRPC health status %s, want %s", response.Status, want)
		}
		return nil
	})
}

func assertReadinessRemainsHealthy(
	t *testing.T,
	app *testApplication,
	readyURL string,
	grpcAddress string,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		assertHTTPStatus(t, app, readyURL, http.StatusOK)
		assertGRPCHealthStatus(t, app, grpcAddress, healthpb.HealthCheckResponse_SERVING)
		time.Sleep(100 * time.Millisecond)
	}
}

func applicationExitError(app *testApplication) error {
	select {
	case <-app.done:
		err := app.waitError()
		return fmt.Errorf("application exited unexpectedly: %w\n%s", err, applicationLogs(app))
	default:
		return nil
	}
}

func applicationLogs(app *testApplication) string {
	data, err := os.ReadFile(app.logFile.Name())
	if err != nil {
		return fmt.Sprintf("read application logs: %v", err)
	}
	return string(data)
}

func (app *testApplication) waitError() error {
	app.waitLock.RLock()
	defer app.waitLock.RUnlock()
	return app.waitErr
}

func availableAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve local address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release local address: %v", err)
	}
	return address
}

func findRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve resilience test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}
