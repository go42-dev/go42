package interceptors

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	grpcmocks "github.com/go42-dev/go42/internal/api/grpc/mocks"
)

const testClientMethod = "/payments.v1.PaymentService/Charge"

func TestExtractRateLimitKeyFromCtx(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		peer     *peer.Peer
		expected string
	}{
		{
			name:     "missing peer",
			expected: "",
		},
		{
			name:     "nil peer address",
			peer:     &peer.Peer{},
			expected: "",
		},
		{
			name: "IPv4 address",
			peer: &peer.Peer{
				Addr: &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 54321},
			},
			expected: "192.0.2.10",
		},
		{
			name: "same IPv4 address with another port",
			peer: &peer.Peer{
				Addr: &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 61482},
			},
			expected: "192.0.2.10",
		},
		{
			name: "IPv6 address",
			peer: &peer.Peer{
				Addr: &net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 54321},
			},
			expected: "2001:db8::1",
		},
		{
			name: "same IPv6 address with another port",
			peer: &peer.Peer{
				Addr: &net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 61482},
			},
			expected: "2001:db8::1",
		},
		{
			name: "non-TCP address",
			peer: &peer.Peer{
				Addr: &net.UnixAddr{Name: "/run/go42.sock", Net: "unix"},
			},
			expected: "/run/go42.sock",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			if tt.peer != nil {
				ctx = peer.NewContext(ctx, tt.peer)
			}

			assert.Equal(t, tt.expected, extractRateLimitKeyFromCtx(ctx))
		})
	}
}

func TestDefaultClientRateLimitKey(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	key := defaultClientRateLimitKey(ctx, "dns:///payments.example", testClientMethod)

	assert.Equal(
		t,
		"grpc-client|dns:///payments.example|/payments.v1.PaymentService/Charge",
		key,
	)
	assert.Equal(t, key, defaultClientRateLimitKey(ctx, "dns:///payments.example", testClientMethod))
	assert.NotEqual(t, key, defaultClientRateLimitKey(ctx, "dns:///email.example", testClientMethod))
	assert.NotEqual(
		t,
		key,
		defaultClientRateLimitKey(ctx, "dns:///payments.example", "/payments.v1.PaymentService/Refund"),
	)
}

func TestUnaryClientRateLimiterInterceptor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		allowed      bool
		limiterError error
		expectedCode codes.Code
		invoked      bool
	}{
		{
			name:    "allowed",
			allowed: true,
			invoked: true,
		},
		{
			name:         "denied",
			expectedCode: codes.ResourceExhausted,
		},
		{
			name:         "limiter unavailable",
			limiterError: errors.New("cache unavailable"),
			expectedCode: codes.Unavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			limiter := grpcmocks.NewMockrateLimiterAccessor(ctrl)
			limiter.EXPECT().
				Limit(gomock.Any(), "grpc-client|payments|"+testClientMethod).
				Return(tt.allowed, tt.limiterError)

			conn := newTestClientConn(t, "dns:///payments.example")
			interceptor := UnaryClientRateLimiterInterceptor(
				limiter,
				WithClientRateLimitScope("payments"),
			)

			invoked := false
			invoker := func(
				context.Context,
				string,
				interface{},
				interface{},
				*grpc.ClientConn,
				...grpc.CallOption,
			) error {
				invoked = true
				return nil
			}

			err := interceptor(
				context.Background(),
				testClientMethod,
				nil,
				nil,
				conn,
				invoker,
			)

			assert.Equal(t, tt.invoked, invoked)
			assert.Equal(t, tt.expectedCode, status.Code(err))
		})
	}
}

func TestStreamClientRateLimiterInterceptor(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	limiter := grpcmocks.NewMockrateLimiterAccessor(ctrl)
	limiter.EXPECT().
		Limit(gomock.Any(), "grpc-client|dns:///payments.example|"+testClientMethod).
		Return(false, nil)

	conn := newTestClientConn(t, "dns:///payments.example")
	interceptor := StreamClientRateLimiterInterceptor(limiter)

	invoked := false
	streamer := func(
		context.Context,
		*grpc.StreamDesc,
		*grpc.ClientConn,
		string,
		...grpc.CallOption,
	) (grpc.ClientStream, error) {
		invoked = true
		return nil, nil
	}

	stream, err := interceptor(
		context.Background(),
		&grpc.StreamDesc{},
		conn,
		testClientMethod,
		streamer,
	)

	assert.Nil(t, stream)
	assert.False(t, invoked)
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))
}

func TestClientRateLimitBypassPreservesCalls(t *testing.T) {
	for _, transport := range []string{"unary", "stream"} {
		t.Run(transport, func(t *testing.T) {
			ctx := t.Context()
			wantErr := errors.New("upstream unavailable")
			opts := []grpc.CallOption{grpc.WaitForReady(true), grpc.MaxCallRecvMsgSize(2048)}
			calls := 0
			if transport == "unary" {
				request, reply := new(int), new(int)
				err := UnaryClientRateLimiterInterceptor(nil)(
					ctx,
					testClientMethod,
					request,
					reply,
					nil,
					func(callCtx context.Context, method string, req, resp any, conn *grpc.ClientConn, callOpts ...grpc.CallOption) error {
						calls++
						assert.Same(t, ctx, callCtx)
						assert.Equal(t, testClientMethod, method)
						assert.Same(t, request, req)
						assert.Same(t, reply, resp)
						assert.Nil(t, conn)
						assert.Equal(t, opts, callOpts)
						return wantErr
					},
					opts...)
				assert.ErrorIs(t, err, wantErr)
			} else {
				desc := &grpc.StreamDesc{StreamName: "watch", ServerStreams: true}
				wantStream := &struct{ grpc.ClientStream }{}
				stream, err := StreamClientRateLimiterInterceptor(nil)(
					ctx,
					desc,
					nil,
					testClientMethod,
					func(callCtx context.Context, streamDesc *grpc.StreamDesc, conn *grpc.ClientConn, method string, callOpts ...grpc.CallOption) (grpc.ClientStream, error) {
						calls++
						assert.Same(t, ctx, callCtx)
						assert.Same(t, desc, streamDesc)
						assert.Nil(t, conn)
						assert.Equal(t, testClientMethod, method)
						assert.Equal(t, opts, callOpts)
						return wantStream, wantErr
					},
					opts...)
				assert.Same(t, wantStream, stream)
				assert.ErrorIs(t, err, wantErr)
			}
			assert.Equal(t, 1, calls)
		})
	}
}

func TestStreamClientRateLimitPreservesCallsAndHandlesFailures(t *testing.T) {
	upstreamError := errors.New("upstream unavailable")
	for _, test := range []struct {
		name         string
		limiterError error
		streamError  error
	}{
		{name: "allowed"},
		{name: "upstream failure", streamError: upstreamError},
		{name: "limiter failure", limiterError: errors.New("private cache failure")},
		{name: "limiter cancellation", limiterError: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			conn := newTestClientConn(t, "dns:///payments.example")
			limiter := grpcmocks.NewMockrateLimiterAccessor(gomock.NewController(t))
			limiter.EXPECT().Limit(ctx, "context-aware-key").Return(true, test.limiterError)
			keyCalls := 0
			interceptor := StreamClientRateLimiterInterceptor(limiter, WithClientRateLimitKeyFunc(
				func(callCtx context.Context, target, method string) string {
					keyCalls++
					assert.Same(t, ctx, callCtx)
					assert.Equal(t, "dns:///payments.example", target)
					assert.Equal(t, testClientMethod, method)
					return "context-aware-key"
				},
			))
			desc := &grpc.StreamDesc{StreamName: "watch", ServerStreams: true}
			wantStream := &struct{ grpc.ClientStream }{}
			opts := []grpc.CallOption{grpc.WaitForReady(true)}
			calls := 0
			stream, err := interceptor(
				ctx,
				desc,
				conn,
				testClientMethod,
				func(callCtx context.Context, streamDesc *grpc.StreamDesc, cc *grpc.ClientConn, method string, callOpts ...grpc.CallOption) (grpc.ClientStream, error) {
					calls++
					assert.Same(t, ctx, callCtx)
					assert.Same(t, desc, streamDesc)
					assert.Same(t, conn, cc)
					assert.Equal(t, testClientMethod, method)
					assert.Equal(t, opts, callOpts)
					return wantStream, test.streamError
				},
				opts...)
			assert.Equal(t, 1, keyCalls)
			if test.limiterError != nil {
				assert.Equal(t, 0, calls)
				assert.Nil(t, stream)
				require.Equal(t, codes.Unavailable, status.Code(err))
				assert.Equal(t, "rate limiter unavailable", status.Convert(err).Message())
			} else {
				assert.Equal(t, 1, calls)
				assert.Same(t, wantStream, stream)
				assert.ErrorIs(t, err, test.streamError)
			}
		})
	}
}

func newTestClientConn(t *testing.T, target string) *grpc.ClientConn {
	t.Helper()

	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, conn.Close())
	})
	return conn
}
