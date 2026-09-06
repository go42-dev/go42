package http

import (
	"context"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	customMiddleware "github.com/go42-dev/go42/internal/api/http/middleware"
)

const (
	ReadyStatusStarting int32 = iota
	ReadyStatusServing
	ReadyStatusShuttingDown
)

const defaultReadinessCheckTimeout = 2 * time.Second

//go:generate mockgen -source $GOFILE -package mocks -destination mocks/mocks.go

type adapterAccessor interface {
	Register(*echo.Group)
}

type rateLimiterAccessor interface {
	Limit(ctx context.Context, key string) (bool, error)
}

type Server struct {
	l    *slog.Logger
	e    *echo.Echo
	root *echo.Group
	v1   *echo.Group

	staticRoot  string
	swaggerRoot string

	rateLimiter rateLimiterAccessor

	readyStatus       atomic.Int32
	readyCheck        func(context.Context) error
	readyCheckTimeout time.Duration

	serveDone   chan struct{}
	shutdownCtx context.Context

	shutdownCancel   context.CancelFunc
	tracingEnabled   bool
	swaggerDarkStyle bool
	bodyLimit        int64
	readTimeout      time.Duration
	writeTimeout     time.Duration
	gracefulTimeout  time.Duration
	allowOrigins     []string
}

func New(opts ...Option) *Server {
	ctx, cancel := context.WithCancel(context.Background())

	echoServer := echo.New()
	echoServer.IPExtractor = echo.ExtractIPDirect()
	echoServer.Validator = NewValidator()

	s := &Server{
		e:                 echoServer,
		allowOrigins:      make([]string, 0),
		serveDone:         make(chan struct{}),
		shutdownCtx:       ctx,
		shutdownCancel:    cancel,
		readyCheckTimeout: defaultReadinessCheckTimeout,
	}

	for _, opt := range opts {
		opt(s)
	}

	// route echo's internal logs through the project's slog logger
	s.e.Logger = s.l.With(slog.String("who", "echo.Logger"))

	// all panics and explicit errors are handled here
	s.e.HTTPErrorHandler = NewErrorHandler(s.l)

	// panics are handled and passed to the HTTPErrorHandler
	// this middleware should be always the first one in the chain
	s.e.Use(middleware.Recover())

	s.e.Use(middleware.RemoveTrailingSlash())

	if s.tracingEnabled {
		s.e.Use(echo.WrapMiddleware(otelhttp.NewMiddleware(
			"http-server",
			otelhttp.WithFilter(func(r *http.Request) bool {
				return r.URL.Path != "/health" &&
					r.URL.Path != "/ready" &&
					r.URL.Path != "/metrics"
			}),
		)))
	}

	s.e.Use(customMiddleware.NewRequestID())

	s.e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		Skipper:      customMiddleware.DefaultSkipper,
		HandleError:  true,
		LogStatus:    true,
		LogMethod:    true,
		LogURI:       true,
		LogRequestID: true,
		LogValuesFunc: func(c *echo.Context, v middleware.RequestLoggerValues) error {
			// log any request with status code < 500 as normal INFO level
			if code := echo.StatusCode(v.Error); v.Error == nil || (code != 0 && code < 500) {
				s.l.DebugContext(
					c.Request().Context(),
					"request",
					slog.Int("status", v.Status),
					slog.String("method", v.Method),
					slog.String("uri", v.URI),
					slog.String("who", "echo.RequestLogger"),
				)
			}
			return nil
		},
	}))

	s.e.Use(customMiddleware.NewMetricsCollector())
	s.e.Use(customMiddleware.NewRateLimiter(s.rateLimiter))
	s.e.Use(middleware.BodyLimitWithConfig(middleware.BodyLimitConfig{
		Skipper:    customMiddleware.DefaultSkipper,
		LimitBytes: s.bodyLimit,
	}))

	s.e.Use(middleware.SecureWithConfig(middleware.SecureConfig{
		Skipper:            customMiddleware.DefaultSkipper,
		XSSProtection:      "1; mode=block",
		ContentTypeNosniff: "nosniff",
		XFrameOptions:      "DENY",
		HSTSMaxAge:         31536000,
		HSTSPreloadEnabled: true,
	}))

	// AllowOrigins: ["*"] with AllowCredentials: true is not allowed by CORS spec.
	allowCredentials := true
	if len(s.allowOrigins) > 0 && s.allowOrigins[0] == "*" {
		allowCredentials = false
		s.l.Warn("CORS is configured to allow all origins")
	}

	s.e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: s.allowOrigins,
		// allow all methods for CORS requests
		AllowMethods: []string{
			http.MethodOptions,
			http.MethodGet,
			http.MethodPost,
			http.MethodPut,
			http.MethodPatch,
			http.MethodDelete,
		},
		// allowed headers for CORS requests
		AllowHeaders: []string{"Authorization", "Content-Type", "X-API-Key"},
		// allow javascript to read extra response headers
		ExposeHeaders: []string{"Content-Length", "x-request-id"},
		// allow JWT to be sent by cross-origin requests
		AllowCredentials: allowCredentials,
		// caching of OPTIONS requests
		MaxAge: 3600,
	}))

	s.root = s.e.Group("")
	s.root.Static("/static", s.staticRoot)

	s.root.GET("/health", s.health)
	s.root.GET("/ready", s.ready)

	{
		s.v1 = s.e.Group("/api/v1")

		// serve openapi specification files
		s.v1.Static("", s.swaggerRoot+"/v1")

		// embed swagger html template itself
		tmpl := template.Must(template.New("swagger").Parse(swaggerTemplate))
		s.v1.GET("/", func(c *echo.Context) error {
			return tmpl.Execute(c.Response(), swaggerTemplateData{
				SpecURLs:  s.parseSpecDir(s.swaggerRoot+"/v1", "/api/v1/"),
				DarkTheme: s.swaggerDarkStyle,
			})
		})
	}

	s.readyStatus.Store(ReadyStatusStarting)

	return s
}

func (s *Server) Start(addr string) error {
	cfg := echo.StartConfig{Address: addr}
	return s.start(cfg)
}

func (s *Server) start(sc echo.StartConfig) error {
	sc.HideBanner = true
	sc.HidePort = true
	sc.GracefulTimeout = s.gracefulTimeout
	sc.BeforeServeFunc = func(hs *http.Server) error {
		if s.readTimeout > 0 {
			hs.ReadTimeout = s.readTimeout
		}
		if s.writeTimeout > 0 {
			hs.WriteTimeout = s.writeTimeout
		}
		// route low-level connection/tls errors through the project's slog
		hs.ErrorLog = slog.NewLogLogger(
			s.l.Handler().WithAttrs([]slog.Attr{
				slog.String("who", "echo.StdLogger"),
			}),
			slog.LevelError,
		)
		// mark the server as ready to serve requests just before launch
		s.readyStatus.CompareAndSwap(
			ReadyStatusStarting,
			ReadyStatusServing,
		)
		return nil
	}

	// By closing this channel we are confident that sc.Start() exited,
	// which means that the server is no longer serving requests.
	// This is important for graceful shutdown, because we want to wait until all requests are finished before exiting the process.
	defer close(s.serveDone)

	return sc.Start(s.shutdownCtx, s.e)
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.readyStatus.Store(ReadyStatusShuttingDown)

	s.shutdownCancel()

	select {
	case <-s.serveDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Register adapters for /
func (s *Server) Register(adapters ...adapterAccessor) {
	for _, p := range adapters {
		p.Register(s.root)
	}
}

// RegisterV1 adapters for /api/v1/
func (s *Server) RegisterV1(adapters ...adapterAccessor) {
	for _, p := range adapters {
		p.Register(s.v1)
	}
}

func (s *Server) health(ctx *echo.Context) error {
	return ctx.NoContent(http.StatusOK)
}

func (s *Server) ready(echoCtx *echo.Context) error {
	if s.readyStatus.Load() != ReadyStatusServing {
		return echoCtx.NoContent(http.StatusServiceUnavailable)
	}
	if s.readyCheck == nil {
		return echoCtx.NoContent(http.StatusOK)
	}

	ctx, cancel := context.WithTimeout(echoCtx.Request().Context(), s.readyCheckTimeout)
	defer cancel()

	if err := s.readyCheck(ctx); err != nil {
		s.l.DebugContext(ctx, "readiness check failed", slog.Any("error", err))
		return echoCtx.NoContent(http.StatusServiceUnavailable)
	}

	return echoCtx.NoContent(http.StatusOK)
}

// parseSpecDir Reads the directory with OpenAPI spec files and returns a map.
// Ignores `.combined.yaml`"` file, which should be generated.
func (s *Server) parseSpecDir(dir string, prefix string) map[string]string {
	specURLs := make(map[string]string)
	specDir, err := os.ReadDir(dir)
	if err != nil {
		s.l.Error(
			"failed to read spec directory",
			slog.String("dir", dir),
			slog.Any("error", err),
		)
		return specURLs
	}
	for _, file := range specDir {
		// ignore .combined.yaml - it should be generated with `make generate`
		if file.IsDir() || file.Name() == ".combined.yaml" {
			continue
		}
		parts := strings.Split(file.Name(), ".")
		if len(parts) != 2 {
			s.l.Warn(
				"unexpected spec file name format",
				slog.String("file", file.Name()),
			)
			continue
		}
		specURLs[parts[0]] = prefix + file.Name()
	}
	return specURLs
}
