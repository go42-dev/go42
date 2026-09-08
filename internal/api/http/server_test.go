package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/mock/gomock"

	oapi "github.com/go42-dev/go42/api/gen/sdk/http/v1/auth/oapi-codegen"
	ogen "github.com/go42-dev/go42/api/gen/sdk/http/v1/auth/ogen"
	"github.com/go42-dev/go42/internal/api/http/mocks"
	"github.com/go42-dev/go42/internal/metrics"
	"github.com/go42-dev/go42/internal/tools"
)

type httpRequestLoggingTest struct {
	name         string
	requestID    string
	body         string
	limited      bool
	limiterError error
	panic        bool
	status       int
}

func TestHTTPLogsRequestIDsBeforeRejections(t *testing.T) {
	for _, test := range []httpRequestLoggingTest{
		{name: "provided ID", requestID: "request-42", status: nethttp.StatusNoContent},
		{name: "generated ID", status: nethttp.StatusNoContent},
		{name: "body limit", requestID: "request-42", body: strings.Repeat("x", 2048), status: 413},
		{name: "rate limit", requestID: "request-42", limited: true, status: 429},
		{name: "limiter error", requestID: "request-42", limiterError: errors.New("unavailable"), status: 500},
		{name: "panic", requestID: "request-42", panic: true, status: 500},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := slog.New(tools.SlogContextWrapper(slog.NewJSONHandler(&output, &slog.HandlerOptions{
				Level: slog.LevelDebug,
			})))
			opts := []Option{WithBodyLimit(1024), WithLogger(logger)}
			if test.limited || test.limiterError != nil {
				limiter := mocks.NewMockrateLimiterAccessor(gomock.NewController(t))
				limiter.EXPECT().Limit(gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, _ string) (bool, error) {
						logger.InfoContext(ctx, "limiter")
						return !test.limited, test.limiterError
					})
				opts = append(opts, func(s *Server) { s.rateLimiter = limiter })
			}
			server := newTestServer(t, opts...)
			server.root.POST("/context", func(c *echo.Context) error {
				logger.InfoContext(c.Request().Context(), "handler")
				if test.panic {
					panic("handler failure")
				}
				return c.NoContent(nethttp.StatusNoContent)
			})
			output.Reset()
			request := httptest.NewRequestWithContext(
				t.Context(),
				nethttp.MethodPost,
				"/context",
				strings.NewReader(test.body),
			)
			request.Header.Set("x-request-id", test.requestID)
			response := httptest.NewRecorder()
			server.e.ServeHTTP(response, request)
			assert.Equal(t, test.status, response.Code)
			requestID := response.Header().Get("x-request-id")
			if len(test.requestID) > 0 {
				assert.Equal(t, test.requestID, requestID)
			} else {
				_, err := uuid.Parse(requestID)
				require.NoError(t, err)
			}
			decoder := json.NewDecoder(&output)
			logged := false
			for {
				var entry map[string]any
				err := decoder.Decode(&entry)
				if errors.Is(err, io.EOF) {
					break
				}
				require.NoError(t, err)
				assert.Equal(t, requestID, entry["request_id"], entry["msg"])
				logged = true
			}
			assert.True(t, logged)
		})
	}
}

func TestReadyReturnsDependencyStatus(t *testing.T) {
	tests := []struct {
		name       string
		check      func(context.Context) error
		wantStatus int
	}{
		{
			name: "healthy",
			check: func(context.Context) error {
				return nil
			},
			wantStatus: nethttp.StatusOK,
		},
		{
			name: "unhealthy",
			check: func(context.Context) error {
				return errors.New("dependency unavailable")
			},
			wantStatus: nethttp.StatusServiceUnavailable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServer(t, WithReadinessCheck(test.check))
			server.readyStatus.Store(ReadyStatusServing)

			if got := getReadyStatus(server); got != test.wantStatus {
				t.Errorf("GET /ready status = %d, want %d", got, test.wantStatus)
			}
		})
	}
}

func TestReadyReturnsServiceUnavailableWhenCheckTimesOut(t *testing.T) {
	checkCanceled := make(chan struct{})
	server := newTestServer(
		t,
		WithReadinessCheckTimeout(25*time.Millisecond),
		WithReadinessCheck(func(ctx context.Context) error {
			<-ctx.Done()
			close(checkCanceled)
			return ctx.Err()
		}),
	)
	server.readyStatus.Store(ReadyStatusServing)

	if got := getReadyStatus(server); got != nethttp.StatusServiceUnavailable {
		t.Errorf("GET /ready status = %d, want %d", got, nethttp.StatusServiceUnavailable)
	}
	waitForSignal(t, checkCanceled, "readiness check cancellation")
}

func TestReadyReturnsServiceUnavailableAfterShutdown(t *testing.T) {
	server := newTestServer(t)
	_, serveResult := startTestServer(t, server)

	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := <-serveResult; err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if got := getReadyStatus(server); got != nethttp.StatusServiceUnavailable {
		t.Errorf("GET /ready status = %d, want %d", got, nethttp.StatusServiceUnavailable)
	}
}

func TestShutdownWaitsForActiveHTTPRequest(t *testing.T) {
	server := newTestServer(t)
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	server.root.GET("/block", func(c *echo.Context) error {
		close(requestStarted)
		<-releaseRequest
		return c.NoContent(nethttp.StatusNoContent)
	})

	address, serveResult := startTestServer(t, server)
	requestResult := make(chan error, 1)
	go func() {
		response, err := nethttp.Get("http://" + address + "/block") //nolint:gosec,noctx
		if response != nil {
			_ = response.Body.Close()
		}
		requestResult <- err
	}()
	waitForSignal(t, requestStarted, "HTTP request to start")

	shutdownResult := make(chan error, 1)
	go func() {
		shutdownResult <- server.Shutdown(context.Background())
	}()
	select {
	case err := <-shutdownResult:
		t.Fatalf("Shutdown() returned before request completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseRequest)
	if err := <-shutdownResult; err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := <-requestResult; err != nil {
		t.Fatalf("HTTP request error = %v", err)
	}
	if err := <-serveResult; err != nil {
		t.Fatalf("Start() error = %v", err)
	}
}

func TestShutdownReturnsAfterEchoGracefulTimeout(t *testing.T) {
	server := newTestServer(t, WithGracefulTimeout(50*time.Millisecond))
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	server.root.GET("/block", func(c *echo.Context) error {
		close(requestStarted)
		<-releaseRequest
		return c.NoContent(nethttp.StatusNoContent)
	})

	address, serveResult := startTestServer(t, server)
	requestResult := make(chan error, 1)
	go func() {
		response, err := nethttp.Get("http://" + address + "/block") //nolint:gosec,noctx
		if response != nil {
			_ = response.Body.Close()
		}
		requestResult <- err
	}()
	waitForSignal(t, requestStarted, "HTTP request to start")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	shutdownStarted := time.Now()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if elapsed := time.Since(shutdownStarted); elapsed < 25*time.Millisecond {
		t.Errorf("Shutdown() returned after %s, before Echo's graceful timeout", elapsed)
	}
	if err := <-serveResult; err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	close(releaseRequest)
	if err := <-requestResult; err != nil {
		t.Fatalf("HTTP request error = %v", err)
	}
}

func TestCORSAllowsAPIKeyHeader(t *testing.T) {
	server := newTestServer(t, WithCORSAllowOrigins([]string{"https://example.com"}))
	server.root.GET("/protected", func(c *echo.Context) error {
		return c.NoContent(nethttp.StatusNoContent)
	})

	address, serveResult := startTestServer(t, server)
	req, err := nethttp.NewRequest(
		nethttp.MethodOptions,
		"http://"+address+"/protected",
		strings.NewReader(""),
	)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", nethttp.MethodGet)
	req.Header.Set("Access-Control-Request-Headers", "x-api-key")

	resp, err := nethttp.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("preflight request error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != nethttp.StatusNoContent {
		t.Errorf("preflight status = %d, want %d", resp.StatusCode, nethttp.StatusNoContent)
	}
	allowedHeaders := resp.Header.Get("Access-Control-Allow-Headers")
	if !strings.Contains(strings.ToLower(allowedHeaders), "x-api-key") {
		t.Errorf("Access-Control-Allow-Headers = %q, want x-api-key", allowedHeaders)
	}

	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := <-serveResult; err != nil {
		t.Fatalf("Start() error = %v", err)
	}
}

func TestClientIPUsesDirectPeerByDefault(t *testing.T) {
	server := newTestServer(t)
	request := httptest.NewRequest(nethttp.MethodGet, "/", nil)
	request.RemoteAddr = "198.51.100.10:1234"
	request.Header.Set(echo.HeaderXForwardedFor, "203.0.113.10")

	if got := server.e.IPExtractor(request); got != "198.51.100.10" {
		t.Errorf("IPExtractor() = %q, want direct peer IP", got)
	}
}

func TestClientIPUsesForwardedAddressFromTrustedProxy(t *testing.T) {
	server := newTestServer(t, WithTrustedProxyCIDRs([]string{"192.0.2.0/24"}))
	request := httptest.NewRequest(nethttp.MethodGet, "/", nil)
	request.RemoteAddr = "192.0.2.10:1234"
	request.Header.Set(echo.HeaderXForwardedFor, "203.0.113.10")

	if got := server.e.IPExtractor(request); got != "203.0.113.10" {
		t.Errorf("IPExtractor() = %q, want forwarded client IP", got)
	}
}

func TestClientIPIgnoresForwardedAddressFromUntrustedPeer(t *testing.T) {
	server := newTestServer(t, WithTrustedProxyCIDRs([]string{"192.0.2.0/24"}))
	request := httptest.NewRequest(nethttp.MethodGet, "/", nil)
	request.RemoteAddr = "198.51.100.10:1234"
	request.Header.Set(echo.HeaderXForwardedFor, "203.0.113.10")

	if got := server.e.IPExtractor(request); got != "198.51.100.10" {
		t.Errorf("IPExtractor() = %q, want direct peer IP", got)
	}
}

func TestHTTPClientsDecodeServerErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
	}{
		{name: "global rate limit", status: nethttp.StatusTooManyRequests},
		{name: "handler failure", status: nethttp.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := []Option{WithBodyLimit(1024)}
			if test.status == nethttp.StatusTooManyRequests {
				limiter := mocks.NewMockrateLimiterAccessor(gomock.NewController(t))
				limiter.EXPECT().Limit(gomock.Any(), "127.0.0.1").Return(false, nil).Times(2)
				options = append(options, func(s *Server) { s.rateLimiter = limiter })
			}
			server := newTestServer(t, options...)
			t.Cleanup(server.shutdownCancel)
			server.v1.POST("/auth/login", func(*echo.Context) error {
				if test.status == nethttp.StatusTooManyRequests {
					t.Error("rate-limited request reached the handler")
				}
				return echo.ErrInternalServerError.Wrap(errors.New("private storage failure"))
			})
			endpoint := httptest.NewServer(server.e)
			t.Cleanup(endpoint.Close)

			t.Run("ogen", func(t *testing.T) {
				client, err := ogen.NewClient(endpoint.URL+"/api/v1", nil)
				if err != nil {
					t.Fatal(err)
				}
				response, err := client.Login(t.Context(), &ogen.LoginRequest{
					Email: "user@example.com", Password: "TestPassword123!",
				})
				var problem *ogen.Error
				if test.status == nethttp.StatusTooManyRequests {
					if err != nil {
						t.Fatalf("decode rate-limit response: %v", err)
					}
					rateLimited, ok := response.(*ogen.LoginTooManyRequests)
					if !ok {
						t.Fatalf("unexpected rate-limit response: %T", response)
					}
					problem = (*ogen.Error)(rateLimited)
				} else {
					var unexpected *ogen.UnexpectedResponseStatusCode
					if !errors.As(err, &unexpected) || unexpected.StatusCode != test.status {
						t.Fatalf("login error = %v, want typed HTTP %d error", err, test.status)
					}
					problem = &unexpected.Response
				}
				if int(problem.Status) != test.status || problem.Title != nethttp.StatusText(test.status) ||
					problem.Type != "/api/v1/auth/login" || problem.Detail.IsSet() {
					t.Fatalf("login response = %+v, want problem details for HTTP %d", problem, test.status)
				}
			})

			t.Run("oapi-codegen", func(t *testing.T) {
				client, err := oapi.NewClientWithResponses(endpoint.URL + "/api/v1")
				if err != nil {
					t.Fatal(err)
				}
				response, err := client.LoginWithResponse(t.Context(), oapi.LoginJSONRequestBody{
					Email: "user@example.com", Password: "TestPassword123!",
				})
				if err != nil {
					t.Fatalf("decode login response: %v", err)
				}
				problem := response.ApplicationproblemJSONDefault
				if test.status == nethttp.StatusTooManyRequests {
					problem = response.ApplicationproblemJSON429
				}
				if response.StatusCode() != test.status {
					t.Fatalf("login status = %d, want %d", response.StatusCode(), test.status)
				}
				if response.ContentType() != MIMEApplicationProblemJSON {
					t.Fatalf("login content type = %q, want problem+json", response.ContentType())
				}
				if problem == nil || int(problem.Status) != test.status ||
					problem.Title != nethttp.StatusText(test.status) ||
					problem.Type != "/api/v1/auth/login" || problem.Detail != nil {
					t.Fatalf("login problem = %+v, want problem details for HTTP %d", problem, test.status)
				}
			})
		})
	}
}

func TestHTTPMetricsAndTracingRecordFinalResponses(t *testing.T) {
	previousProvider := otel.GetTracerProvider()
	spans := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})

	for _, test := range []struct {
		name             string
		handler          echo.HandlerFunc
		wantStatus       int
		wantError        bool
		wantHandlerCalls int
	}{
		{
			name: "success", wantStatus: nethttp.StatusCreated,
			handler: func(c *echo.Context) error { return c.NoContent(nethttp.StatusCreated) },
		},
		{
			name: "explicit-client-error", wantStatus: nethttp.StatusBadRequest, wantError: true,
			handler: func(c *echo.Context) error { return c.NoContent(nethttp.StatusBadRequest) },
		},
		{
			name: "returned-client-error", wantStatus: nethttp.StatusBadRequest, wantError: true, wantHandlerCalls: 1,
			handler: func(*echo.Context) error { return echo.ErrBadRequest },
		},
		{
			name: "returned-server-error", wantStatus: nethttp.StatusInternalServerError, wantError: true, wantHandlerCalls: 1,
			handler: func(*echo.Context) error { return errors.New("storage failure") },
		},
		{
			name: "panic", wantStatus: nethttp.StatusInternalServerError, wantError: true, wantHandlerCalls: 1,
			handler: func(*echo.Context) error { panic("handler failure") },
		},
		{
			name: "panic-with-http-error", wantStatus: nethttp.StatusInternalServerError, wantError: true, wantHandlerCalls: 1,
			handler: func(*echo.Context) error { panic(echo.ErrBadRequest) },
		},
		{
			name: "error-after-response", wantStatus: nethttp.StatusAccepted, wantHandlerCalls: 1,
			handler: func(c *echo.Context) error {
				if err := c.String(nethttp.StatusAccepted, "already sent"); err != nil {
					return err
				}
				return errors.New("failure after response")
			},
		},
		{
			name: "panic-after-response", wantStatus: nethttp.StatusAccepted, wantHandlerCalls: 1,
			handler: func(c *echo.Context) error {
				if err := c.String(nethttp.StatusAccepted, "already sent"); err != nil {
					return err
				}
				panic("failure after response")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServer(t, WithTracing(true))
			t.Cleanup(server.shutdownCancel)
			path := "/metrics-test/" + test.name
			server.root.GET(path, test.handler)
			handleError := server.e.HTTPErrorHandler
			handlerCalls := 0
			server.e.HTTPErrorHandler = func(c *echo.Context, err error) {
				handlerCalls++
				handleError(c, err)
			}

			labels := map[string]any{"method": nethttp.MethodGet, "path": path}
			requests := metrics.Counter("application_http_requests_count", labels)
			requestsBefore := requests.Get()
			labels["status"] = strconv.Itoa(test.wantStatus)
			labels["is_error"] = "no"
			if test.wantError {
				labels["is_error"] = "yes"
			}
			responses := metrics.Counter("application_http_responses_count", labels)
			responsesBefore := responses.Get()
			histogram := metrics.Histogram("application_http_latency_sec", labels)
			latencyCount := func() uint64 {
				var count uint64
				histogram.VisitNonZeroBuckets(func(_ string, n uint64) { count += n })
				return count
			}
			latenciesBefore := latencyCount()
			spansBefore := len(spans.Ended())

			response := httptest.NewRecorder()
			server.e.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), nethttp.MethodGet, path, nil))
			assert.Equal(t, test.wantStatus, response.Code)
			assert.Equal(t, test.wantHandlerCalls, handlerCalls, "each error must be handled once")
			assert.Equal(t, requestsBefore+1, requests.Get())
			assert.Equal(t, responsesBefore+1, responses.Get(), "count the final response even after a panic")
			assert.Equal(t, latenciesBefore+1, latencyCount(), "record latency even after a panic")

			ended := spans.Ended()
			require.Len(t, ended, spansBefore+1)
			traceStatus := 0
			for _, attribute := range ended[spansBefore].Attributes() {
				switch string(attribute.Key) {
				case "http.status_code", "http.response.status_code":
					traceStatus = int(attribute.Value.AsInt64())
				}
			}
			assert.Equal(t, response.Code, traceStatus, "tracing must observe the final HTTP response")
		})
	}
}

func TestHTTPMetricsWaitForErrorHandlerResponse(t *testing.T) {
	const path = "/metrics-test/rendered-error"
	server := newTestServer(t)
	t.Cleanup(server.shutdownCancel)
	server.root.GET(path, func(*echo.Context) error { return echo.ErrBadRequest })
	labels := map[string]any{
		"method": nethttp.MethodGet, "path": path, "status": "503", "is_error": "yes",
	}
	rendered := metrics.Counter("application_http_responses_count", labels)
	renderedBefore := rendered.Get()
	labels["status"] = "400"
	inferred := metrics.Counter("application_http_responses_count", labels)
	inferredBefore := inferred.Get()
	handlerCalls := 0
	server.e.HTTPErrorHandler = func(c *echo.Context, err error) {
		handlerCalls++
		if !errors.Is(err, echo.ErrBadRequest) {
			t.Errorf("error handler received %v, want the original error", err)
		}
		if rendered.Get() != renderedBefore || inferred.Get() != inferredBefore {
			t.Error("response metrics were recorded before error rendering finished")
		}
		if err := c.NoContent(nethttp.StatusServiceUnavailable); err != nil {
			t.Error(err)
		}
	}
	response := httptest.NewRecorder()
	server.e.ServeHTTP(response, httptest.NewRequest(nethttp.MethodGet, path, nil))
	if response.Code != nethttp.StatusServiceUnavailable || handlerCalls != 1 {
		t.Errorf("status = %d, handler calls = %d; want 503 and one call", response.Code, handlerCalls)
	}
	if rendered.Get() != renderedBefore+1 || inferred.Get() != inferredBefore {
		t.Error("metrics must record the status chosen by the error handler")
	}
}

func newTestServer(t *testing.T, extraOptions ...Option) *Server {
	t.Helper()
	options := []Option{
		WithLogger(slog.New(slog.DiscardHandler)),
		WithStaticRoot(t.TempDir()),
		WithSwaggerRoot(t.TempDir()),
		WithCORSAllowOrigins([]string{"*"}),
		WithGracefulTimeout(time.Second),
	}
	options = append(options, extraOptions...)
	return New(options...)
}

func startTestServer(t *testing.T, server *Server) (string, <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.start(echo.StartConfig{Listener: listener})
	}()
	return listener.Addr().String(), serveResult
}

func waitForSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func getReadyStatus(server *Server) int {
	request := httptest.NewRequest(nethttp.MethodGet, "/ready", nil)
	response := httptest.NewRecorder()
	server.e.ServeHTTP(response, request)
	return response.Code
}

func TestSwaggerSpecDiscovery(t *testing.T) {
	for _, test := range []struct {
		name     string
		prefix   string
		files    []string
		want     map[string]string
		warnings []string
	}{
		{
			name: "specification files", prefix: "/api/v1/",
			files: []string{"auth.yaml", "users.json"},
			want:  map[string]string{"auth": "/api/v1/auth.yaml", "users": "/api/v1/users.json"},
		},
		{
			name: "generated file and directories", prefix: "/api/v1/",
			files: []string{"auth.yaml", ".combined.yaml", "directory.yaml/nested.yaml"},
			want:  map[string]string{"auth": "/api/v1/auth.yaml"},
		},
		{
			name: "unexpected file names", prefix: "/api/v1/",
			files: []string{"users.yaml", "README", "auth.v1.yaml"},
			want:  map[string]string{"users": "/api/v1/users.yaml"}, warnings: []string{"README", "auth.v1.yaml"},
		},
		{name: "empty directory", prefix: "/api/v1/", want: map[string]string{}},
		{name: "empty prefix", files: []string{"auth.yaml"}, want: map[string]string{"auth": "auth.yaml"}},
		{
			name: "custom prefix", prefix: "https://docs.example/specs/", files: []string{"auth.yaml"},
			want: map[string]string{"auth": "https://docs.example/specs/auth.yaml"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			writeSwaggerTestFiles(t, dir, test.files...)
			var output bytes.Buffer
			server := newTestServer(t, WithLogger(slog.New(slog.NewJSONHandler(&output, nil))))
			output.Reset()
			assert.Equal(t, test.want, server.parseSpecDir(dir, test.prefix))

			lines := strings.FieldsFunc(output.String(), func(r rune) bool { return r == '\n' })
			require.Len(t, lines, len(test.warnings))
			for index, line := range lines {
				var entry struct{ Level, Msg, File string }
				require.NoError(t, json.Unmarshal([]byte(line), &entry))
				assert.Equal(t, "WARN", entry.Level)
				assert.Equal(t, "unexpected spec file name format", entry.Msg)
				assert.Equal(t, test.warnings[index], entry.File)
			}
		})
	}
}

func TestSwaggerSpecDiscoveryReportsDirectoryErrors(t *testing.T) {
	for _, name := range []string{"missing directory", "file instead of directory"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "specs")
			if name == "file instead of directory" {
				require.NoError(t, os.WriteFile(dir, []byte("not a directory"), 0o600))
			}
			var output bytes.Buffer
			server := newTestServer(t, WithLogger(slog.New(slog.NewJSONHandler(&output, nil))))
			output.Reset()
			assert.Equal(t, map[string]string{}, server.parseSpecDir(dir, "/api/v1/"))
			var entry struct{ Level, Msg, Dir, Error string }
			require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
			assert.Equal(t, "ERROR", entry.Level)
			assert.Equal(t, "failed to read spec directory", entry.Msg)
			assert.Equal(t, dir, entry.Dir)
			assert.NotEmpty(t, entry.Error)
		})
	}
}

func TestSwaggerRendersAvailableSpecsAndTheme(t *testing.T) {
	for _, dark := range []bool{false, true} {
		t.Run("dark="+strconv.FormatBool(dark), func(t *testing.T) {
			root := t.TempDir()
			writeSwaggerTestFiles(t, filepath.Join(root, "v1"),
				"auth.yaml", "users.yaml", ".combined.yaml", "README", "directory.yaml/nested.yaml",
			)
			server := newTestServer(t, WithSwaggerRoot(root), WithSwaggerDarkStyle(dark))
			response := httptest.NewRecorder()
			server.e.ServeHTTP(
				response,
				httptest.NewRequestWithContext(t.Context(), nethttp.MethodGet, "/api/v1/", nil),
			)
			require.Equal(t, nethttp.StatusOK, response.Code)
			body := response.Body.String()
			var specURLs []string
			for _, line := range strings.Split(body, "\n") {
				if value, ok := strings.CutPrefix(strings.TrimSpace(line), "url: "); ok {
					var specURL string
					require.NoError(t, json.Unmarshal([]byte(strings.TrimSuffix(value, ",")), &specURL))
					specURLs = append(specURLs, specURL)
				}
			}
			assert.ElementsMatch(t, []string{"/api/v1/auth.yaml", "/api/v1/users.yaml"}, specURLs)
			assert.Contains(t, body, `name: "auth"`)
			assert.Contains(t, body, `name: "users"`)
			assert.NotContains(t, body, ".combined.yaml")
			assert.NotContains(t, body, "README")
			assert.NotContains(t, body, "directory.yaml")
			assert.Contains(t, body, `href="/static/swagger/swagger-ui.css"`)
			assert.Equal(t, dark, strings.Contains(body, `href="/static/swagger/dark.min.css"`))
			assert.Equal(t, dark, strings.Contains(body, `href="/static/swagger/one-dark.min.css"`))

			for _, path := range []string{"/api/v1/auth.yaml", "/api/v1/users.yaml"} {
				spec := httptest.NewRecorder()
				server.e.ServeHTTP(spec, httptest.NewRequestWithContext(t.Context(), nethttp.MethodGet, path, nil))
				assert.Equal(t, nethttp.StatusOK, spec.Code)
				assert.Equal(t, "openapi: 3.0.3\n", spec.Body.String())
			}
		})
	}
}

func TestSwaggerRendersWithMissingSpecDirectory(t *testing.T) {
	server := newTestServer(t)
	response := httptest.NewRecorder()
	server.e.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), nethttp.MethodGet, "/api/v1/", nil))
	require.Equal(t, nethttp.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), "SwaggerUIBundle({")
	assert.Contains(t, response.Body.String(), "urls: [")
	assert.NotContains(t, response.Body.String(), `url: "`)
}

func writeSwaggerTestFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte("openapi: 3.0.3\n"), 0o600))
	}
}
