package grpc

import (
	"context"
	"log/slog"
	"net"
	"runtime/debug"
	"time"

	"buf.build/go/protovalidate"
	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	protovalidateInterceptor "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/protovalidate"
	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/recovery"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	"github.com/go42-dev/go42/internal/api/grpc/interceptors"
	"github.com/go42-dev/go42/internal/metrics"
	"github.com/go42-dev/go42/internal/tools"
)

const (
	InterceptorPriorityPreprocess     = 1
	InterceptorPriorityObservability  = 2
	InterceptorPriorityAuthentication = 3
	InterceptorPriorityBusinessLogic  = 4
)

const (
	defaultReadinessCheckTimeout  = 2 * time.Second
	defaultReadinessCheckInterval = 5 * time.Second
)

//go:generate mockgen -source $GOFILE -package mocks -destination mocks/mocks.go

type adapterAccessor interface {
	Register(*grpc.Server)
}

type rateLimiterAccessor interface {
	Limit(ctx context.Context, key string) (bool, error)
}

type Server struct {
	logger     *slog.Logger
	grpcServer *grpc.Server

	maxRecvMsgSize int
	maxSendMsgSize int
	tracingEnabled bool
	withReflection bool
	healthServer   *health.Server
	healthCheckCtx context.Context

	readyCheck          func(context.Context) error
	readyCheckTimeout   time.Duration
	readyCheckInterval  time.Duration
	healthMonitorCancel context.CancelFunc

	rateLimiter rateLimiterAccessor

	extraUnaryInterceptors  map[int][]grpc.UnaryServerInterceptor
	extraStreamInterceptors map[int][]grpc.StreamServerInterceptor
}

func New(opts ...Option) *Server {
	s := &Server{
		extraUnaryInterceptors:  make(map[int][]grpc.UnaryServerInterceptor),
		extraStreamInterceptors: make(map[int][]grpc.StreamServerInterceptor),
		readyCheckTimeout:       defaultReadinessCheckTimeout,
		readyCheckInterval:      defaultReadinessCheckInterval,
	}
	for _, o := range opts {
		o(s)
	}
	if s.logger == nil {
		s.logger = slog.New(slog.DiscardHandler)
	}

	unaryPriorityQueue := tools.NewPriorityQueue[grpc.UnaryServerInterceptor]()
	unaryPriorityQueue.Enqueue(
		InterceptorPriorityPreprocess, interceptors.UnaryRequestIDInterceptor())
	unaryPriorityQueue.Enqueue(
		InterceptorPriorityPreprocess,
		logging.UnaryServerInterceptor(interceptorLogger(s.logger)))
	unaryPriorityQueue.Enqueue(
		InterceptorPriorityPreprocess,
		recovery.UnaryServerInterceptor(recovery.WithRecoveryHandlerContext(s.handlePanic)))
	unaryPriorityQueue.Enqueue(
		InterceptorPriorityPreprocess, interceptors.UnaryServerRateLimiterInterceptor(s.rateLimiter))
	unaryPriorityQueue.Enqueue(
		InterceptorPriorityObservability,
		interceptors.UnaryMetricsInterceptor())
	unaryPriorityQueue.Enqueue(
		InterceptorPriorityBusinessLogic,
		protovalidateInterceptor.UnaryServerInterceptor(protovalidate.GlobalValidator))

	streamPriorityQueue := tools.NewPriorityQueue[grpc.StreamServerInterceptor]()
	streamPriorityQueue.Enqueue(
		InterceptorPriorityPreprocess,
		interceptors.StreamRequestIDInterceptor())
	streamPriorityQueue.Enqueue(
		InterceptorPriorityPreprocess,
		logging.StreamServerInterceptor(interceptorLogger(s.logger)))
	streamPriorityQueue.Enqueue(
		InterceptorPriorityPreprocess,
		recovery.StreamServerInterceptor(recovery.WithRecoveryHandlerContext(s.handlePanic)))
	streamPriorityQueue.Enqueue(
		InterceptorPriorityPreprocess,
		interceptors.StreamServerRateLimiterInterceptor(s.rateLimiter))
	streamPriorityQueue.Enqueue(
		InterceptorPriorityObservability,
		interceptors.StreamMetricsInterceptor())
	streamPriorityQueue.Enqueue(
		InterceptorPriorityBusinessLogic,
		protovalidateInterceptor.StreamServerInterceptor(protovalidate.GlobalValidator))

	// add extra interceptors from options
	for priority, items := range s.extraUnaryInterceptors {
		for _, interceptor := range items {
			unaryPriorityQueue.Enqueue(priority, interceptor)
		}
	}
	for priority, items := range s.extraStreamInterceptors {
		for _, interceptor := range items {
			streamPriorityQueue.Enqueue(priority, interceptor)
		}
	}

	serverOptions := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(unaryPriorityQueue.Extract()...),
		grpc.ChainStreamInterceptor(streamPriorityQueue.Extract()...),
	}

	if s.maxRecvMsgSize > 0 {
		serverOptions = append(serverOptions, grpc.MaxRecvMsgSize(s.maxRecvMsgSize))
	}
	if s.maxSendMsgSize > 0 {
		serverOptions = append(serverOptions, grpc.MaxSendMsgSize(s.maxSendMsgSize))
	}
	if s.tracingEnabled {
		serverOptions = append(serverOptions, grpc.StatsHandler(otelgrpc.NewServerHandler()))
	}

	s.grpcServer = grpc.NewServer(serverOptions...)

	if s.withReflection {
		reflection.Register(s.grpcServer)
	}

	s.healthServer = health.NewServer()
	s.healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(s.grpcServer, s.healthServer)
	s.startHealthMonitor()

	return s
}

func (s *Server) handlePanic(ctx context.Context, p any) error {
	metrics.Counter("application_errors", map[string]interface{}{
		"type": "grpc_panic",
	}).Inc()
	s.logger.ErrorContext(ctx, "grpc panic",
		slog.Any("panic", p),
		slog.Any("stack", debug.Stack()),
	)
	return status.Error(codes.Internal, "internal server error")
}

func (s *Server) Serve(listen string) error {
	lis, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	return s.grpcServer.Serve(lis)
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.healthMonitorCancel != nil {
		s.healthMonitorCancel()
	}
	s.healthServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)

	stopped := make(chan struct{})

	go func() {
		s.grpcServer.GracefulStop()
		close(stopped)
	}()

	// wait until graceful shutdown is done (stopped channel is closed)
	// or we reach the context deadline (ctx.Done() is closed)
	select {
	case <-stopped:
		return nil
	case <-ctx.Done():
		s.grpcServer.Stop()
		<-stopped
		return ctx.Err()
	}
}

func (s *Server) startHealthMonitor() {
	if s.healthCheckCtx == nil && s.readyCheck == nil {
		return
	}

	ctx, cancel := context.WithCancel(s.healthCheckCtx)
	s.healthMonitorCancel = cancel

	go s.monitorHealth(ctx)
}

func (s *Server) monitorHealth(ctx context.Context) {
	if s.readyCheck == nil {
		<-ctx.Done()
		s.healthServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
		return
	}

	ticker := time.NewTicker(s.readyCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.updateHealthStatus(ctx)
		case <-ctx.Done():
			s.healthServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
			return
		}
	}
}

func (s *Server) updateHealthStatus(ctx context.Context) {
	checkCtx, cancel := context.WithTimeout(ctx, s.readyCheckTimeout)
	err := s.readyCheck(checkCtx)
	cancel()

	if ctx.Err() != nil {
		s.healthServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
		return
	}

	if err != nil {
		s.healthServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
		s.logger.DebugContext(ctx, "readiness check failed", slog.Any("error", err))
		return
	}

	s.healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
}

func (s *Server) Register(adapters ...adapterAccessor) {
	for _, p := range adapters {
		p.Register(s.grpcServer)
	}
}

func interceptorLogger(l *slog.Logger) logging.Logger {
	return logging.LoggerFunc(func(ctx context.Context, lvl logging.Level, msg string, fields ...any) {
		l.Log(ctx, slog.Level(lvl), msg, fields...)
	})
}
