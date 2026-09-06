package interceptors

import (
	"context"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/go42-dev/go42/internal/tools"
)

const headerNameRequestID = "x-request-id"

func UnaryRequestIDInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		newCtx := extractOrGenerateRequestID(ctx)
		return handler(newCtx, req)
	}
}

type requestIDServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *requestIDServerStream) Context() context.Context {
	return w.ctx
}

func StreamRequestIDInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv interface{},
		stream grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		newCtx := extractOrGenerateRequestID(stream.Context())
		wrappedStream := &requestIDServerStream{
			ServerStream: stream,
			ctx:          newCtx,
		}
		return handler(srv, wrappedStream)
	}
}

func extractOrGenerateRequestID(ctx context.Context) context.Context {
	requestID := getRequestID(ctx)

	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() && span.IsRecording() {
		span.SetAttributes(attribute.String("rpc.request_id", requestID))
	}

	return metadata.AppendToOutgoingContext(
		tools.SetRequestIDToContext(ctx, requestID), headerNameRequestID, requestID)
}

func getRequestID(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if ids := md.Get(headerNameRequestID); len(ids) > 0 && len(ids[0]) > 0 {
			return ids[0]
		}
	}
	return uuid.New().String()
}

// ---

func UnaryClientRequestIDInterceptor() grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply interface{},
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		newCtx := propagateRequestID(ctx)
		return invoker(newCtx, method, req, reply, cc, opts...)
	}
}

func StreamClientRequestIDInterceptor() grpc.StreamClientInterceptor {
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		newCtx := propagateRequestID(ctx)
		return streamer(newCtx, desc, cc, method, opts...)
	}
}

// ---

func propagateRequestID(ctx context.Context) context.Context {
	if requestID := tools.GetRequestIDFromContext(ctx); requestID != "" {
		return metadata.AppendToOutgoingContext(ctx, headerNameRequestID, requestID)
	}
	return ctx
}
