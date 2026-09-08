package interceptors

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/go42-dev/go42/internal/tools"
)

type requestIDTestContextKey struct{}

func TestClientRequestIDInterceptorsPreserveMetadataAndCallContext(t *testing.T) {
	upstreamError := errors.New("upstream unavailable")
	for _, transport := range []string{"unary", "stream"} {
		t.Run(transport, func(t *testing.T) {
			for _, test := range []struct {
				name      string
				requestID string
				outgoing  metadata.MD
				want      metadata.MD
				err       error
			}{
				{name: "no context ID"},
				{
					name: "context ID", requestID: "request-42",
					want: metadata.Pairs("x-request-id", "request-42"),
				},
				{
					name: "existing metadata", requestID: "request-42",
					outgoing: metadata.Pairs("authorization", "Bearer token", "custom", "value"),
					want:     metadata.Pairs("authorization", "Bearer token", "custom", "value", "x-request-id", "request-42"),
				},
				{
					name: "existing request IDs", requestID: "request-42",
					outgoing: metadata.Pairs("x-request-id", "first", "x-request-id", "second"),
					want:     metadata.Pairs("x-request-id", "first", "x-request-id", "second", "x-request-id", "request-42"),
					err:      upstreamError,
				},
				{
					name:     "empty context ID preserves outgoing ID",
					outgoing: metadata.Pairs("x-request-id", "existing"),
					want:     metadata.Pairs("x-request-id", "existing"), err: upstreamError,
				},
			} {
				t.Run(test.name, func(t *testing.T) {
					ctx := context.WithValue(t.Context(), requestIDTestContextKey{}, "preserved")
					incoming := metadata.Pairs("x-request-id", "incoming-only")
					ctx = metadata.NewIncomingContext(ctx, incoming)
					if test.outgoing != nil {
						ctx = metadata.NewOutgoingContext(ctx, test.outgoing)
					}
					if test.requestID != "" {
						ctx = tools.SetRequestIDToContext(ctx, test.requestID)
					}
					ctx, cancel := context.WithTimeout(ctx, time.Hour)
					defer cancel()
					outgoingBefore, _ := metadata.FromOutgoingContext(ctx)
					conn := newTestClientConn(t, "dns:///payments.example")
					opts := []grpc.CallOption{grpc.WaitForReady(true), grpc.MaxCallRecvMsgSize(1024)}
					var calledCtx context.Context
					calls := 0
					checkContext := func(callCtx context.Context) {
						calls++
						calledCtx = callCtx
						assert.Equal(t, "preserved", callCtx.Value(requestIDTestContextKey{}))
						assert.Equal(t, test.requestID, tools.GetRequestIDFromContext(callCtx))
						assert.Equal(t, ctx.Done(), callCtx.Done())
						deadline, hasDeadline := callCtx.Deadline()
						wantDeadline, _ := ctx.Deadline()
						assert.True(t, hasDeadline)
						assert.Equal(t, wantDeadline, deadline)
						outgoing, _ := metadata.FromOutgoingContext(callCtx)
						assert.Equal(t, test.want, outgoing)
						gotIncoming, _ := metadata.FromIncomingContext(callCtx)
						assert.Equal(t, incoming, gotIncoming)
						if test.requestID == "" {
							assert.Same(t, ctx, callCtx)
						}
					}
					if transport == "unary" {
						request, reply := new(int), new(int)
						err := UnaryClientRequestIDInterceptor()(
							ctx,
							testClientMethod,
							request,
							reply,
							conn,
							func(callCtx context.Context, method string, req, resp any, cc *grpc.ClientConn, callOpts ...grpc.CallOption) error {
								checkContext(callCtx)
								assert.Equal(t, testClientMethod, method)
								assert.Same(t, request, req)
								assert.Same(t, reply, resp)
								assert.Same(t, conn, cc)
								assert.Equal(t, opts, callOpts)
								return test.err
							},
							opts...)
						assert.ErrorIs(t, err, test.err)
					} else {
						desc := &grpc.StreamDesc{StreamName: "watch", ServerStreams: true}
						wantStream := &struct{ grpc.ClientStream }{}
						stream, err := StreamClientRequestIDInterceptor()(
							ctx,
							desc,
							conn,
							testClientMethod,
							func(callCtx context.Context, streamDesc *grpc.StreamDesc, cc *grpc.ClientConn, method string, callOpts ...grpc.CallOption) (grpc.ClientStream, error) {
								checkContext(callCtx)
								assert.Same(t, desc, streamDesc)
								assert.Same(t, conn, cc)
								assert.Equal(t, testClientMethod, method)
								assert.Equal(t, opts, callOpts)
								return wantStream, test.err
							},
							opts...)
						assert.Same(t, wantStream, stream)
						assert.ErrorIs(t, err, test.err)
					}
					require.Equal(t, 1, calls)
					cancel()
					assert.ErrorIs(t, calledCtx.Err(), context.Canceled)
					outgoingAfter, _ := metadata.FromOutgoingContext(ctx)
					assert.Equal(t, outgoingBefore, outgoingAfter)
				})
			}
		})
	}
}

func TestServerRequestIDInterceptorsRecordAndPropagateIdentity(t *testing.T) {
	for _, transport := range []string{"unary", "stream"} {
		t.Run(transport, func(t *testing.T) {
			for _, test := range []struct {
				name string
				ids  []string
				want string
			}{
				{name: "missing ID"},
				{name: "empty ID", ids: []string{""}},
				{name: "provided ID", ids: []string{"request-42"}, want: "request-42"},
				{name: "first ID wins", ids: []string{"first", "second"}, want: "first"},
				{name: "empty first ID", ids: []string{"", "second"}},
			} {
				t.Run(test.name, func(t *testing.T) {
					recorder := tracetest.NewSpanRecorder()
					provider := sdktrace.NewTracerProvider(
						sdktrace.WithSpanProcessor(recorder),
						sdktrace.WithSampler(sdktrace.AlwaysSample()),
					)
					t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
					ctx := context.WithValue(t.Context(), requestIDTestContextKey{}, "preserved")
					ctx, cancel := context.WithCancel(ctx)
					defer cancel()
					ctx, span := provider.Tracer("request-id-test").Start(ctx, "request")
					defer span.End()
					incoming := metadata.Pairs("authorization", "Bearer token")
					if test.ids != nil {
						incoming["x-request-id"] = test.ids
					}
					ctx = metadata.NewIncomingContext(ctx, incoming)
					ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("custom", "value"))
					incomingBefore := incoming.Copy()
					var requestID string
					var handlerCtx context.Context
					calls := 0
					checkContext := func(callCtx context.Context) {
						calls++
						handlerCtx = callCtx
						requestID = tools.GetRequestIDFromContext(callCtx)
						if test.want == "" {
							assert.NoError(t, uuid.Validate(requestID))
						} else {
							assert.Equal(t, test.want, requestID)
						}
						assert.Equal(t, "preserved", callCtx.Value(requestIDTestContextKey{}))
						assert.Equal(t, ctx.Done(), callCtx.Done())
						assert.Same(t, span, trace.SpanFromContext(callCtx))
						outgoing, _ := metadata.FromOutgoingContext(callCtx)
						assert.Equal(t, metadata.Pairs("custom", "value", "x-request-id", requestID), outgoing)
						gotIncoming, _ := metadata.FromIncomingContext(callCtx)
						assert.Equal(t, incomingBefore, gotIncoming)
					}
					wantErr := errors.New("handler failed")
					if transport == "unary" {
						request, response := new(int), new(int)
						result, err := UnaryRequestIDInterceptor()(
							ctx,
							request,
							&grpc.UnaryServerInfo{FullMethod: testClientMethod},
							func(callCtx context.Context, req any) (any, error) {
								checkContext(callCtx)
								assert.Same(t, request, req)
								return response, wantErr
							},
						)
						assert.Same(t, response, result)
						assert.ErrorIs(t, err, wantErr)
					} else {
						server := new(int)
						stream := &requestIDTestServerStream{ctx: ctx}
						err := StreamRequestIDInterceptor()(
							server,
							stream,
							&grpc.StreamServerInfo{FullMethod: testClientMethod},
							func(srv any, callStream grpc.ServerStream) error {
								assert.Same(t, server, srv)
								checkContext(callStream.Context())
								return wantErr
							},
						)
						assert.ErrorIs(t, err, wantErr)
					}
					require.Equal(t, 1, calls)
					span.End()
					spans := recorder.Ended()
					require.Len(t, spans, 1)
					assert.Contains(t, spans[0].Attributes(), attribute.String("rpc.request_id", requestID))
					assert.Empty(t, tools.GetRequestIDFromContext(ctx))
					assert.Equal(t, incomingBefore, incoming)
					outgoing, _ := metadata.FromOutgoingContext(ctx)
					assert.Equal(t, metadata.Pairs("custom", "value"), outgoing)
					cancel()
					assert.ErrorIs(t, handlerCtx.Err(), context.Canceled)
				})
			}
		})
	}
}

type requestIDTestServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *requestIDTestServerStream) Context() context.Context { return s.ctx }
