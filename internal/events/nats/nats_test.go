package nats

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNATSRejectsInvalidOptionsBeforeConnecting(t *testing.T) {
	for _, option := range []Option{
		WithConnectTimeout(0), WithPublishAckTimeout(0), WithSubAckTimeout(0), WithSubWorkerCount(0),
		WithConnectRetryTimeout(0), WithConnectRetryBackoff(time.Second, time.Millisecond),
		WithConsumerBindings(map[string]ConsumerBinding{"auth": {Stream: "events"}}),
		WithTLSConfig("", "client.pem", "", ""),
	} {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		backend, err := New(ctx, "nats://127.0.0.1:1", option)
		require.Error(t, err)
		require.NotErrorIs(t, err, context.Canceled)
		require.Nil(t, backend)
	}
}

func TestNATSStartupDeadlineSurvivesStalledHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	backend, err := New(ctx, "nats://"+listener.Addr().String())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, backend)
	require.Less(t, time.Since(started), time.Second)
	select {
	case conn := <-accepted:
		_ = conn.Close()
	case <-time.After(time.Second):
		t.Fatal("test server did not accept the connection")
	}
}
