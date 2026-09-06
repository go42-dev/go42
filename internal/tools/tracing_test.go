package tools_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/go42-dev/go42/internal/tools"
)

type traceHelperTest struct {
	name string
	run  func(context.Context, func(context.Context))
}

func TestTraceHelpersPropagateUnsampledSpans(t *testing.T) {
	previousProvider := otel.GetTracerProvider()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	})
	tests := []traceHelperTest{
		{name: "value", run: func(ctx context.Context, fn func(context.Context)) {
			tools.TraceReturnT(ctx, "test", "operation", func(ctx context.Context) int { fn(ctx); return 42 })
		}},
		{name: "value and error", run: func(ctx context.Context, fn func(context.Context)) {
			_, _ = tools.TraceReturnTWithErr(ctx, "test", "operation", func(ctx context.Context) (int, error) {
				fn(ctx)
				return 42, nil
			})
		}},
		{name: "error", run: func(ctx context.Context, fn func(context.Context)) {
			_ = tools.TraceReturnErr(ctx, "test", "operation", func(ctx context.Context) error { fn(ctx); return nil })
		}},
		{name: "no return", run: func(ctx context.Context, fn func(context.Context)) {
			tools.TraceNoReturn(ctx, "test", "operation", fn)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			ctx := tools.SetRequestIDToContext(t.Context(), "request-42")
			test.run(ctx, func(ctx context.Context) {
				called = true
				span := trace.SpanFromContext(ctx)
				assert.True(t, span.SpanContext().IsValid())
				assert.False(t, span.IsRecording())
				assert.Equal(t, "request-42", tools.GetRequestIDFromContext(ctx))
			})
			assert.True(t, called)
		})
	}
}

func TestTraceReturnTWithErrSetsSpanStatus(t *testing.T) {
	recorder := installSpanRecorder(t)
	expectedErr := errors.New("operation failed")

	tests := []struct {
		name          string
		result        int
		err           error
		expectedCode  codes.Code
		expectedEvent bool
	}{
		{
			name:         "success",
			result:       42,
			expectedCode: codes.Ok,
		},
		{
			name:          "error",
			result:        7,
			err:           expectedErr,
			expectedCode:  codes.Error,
			expectedEvent: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder.Reset()

			result, err := tools.TraceReturnTWithErr(
				context.Background(),
				"test",
				"operation",
				func(context.Context) (int, error) {
					return tt.result, tt.err
				},
			)

			assert.Equal(t, tt.result, result)
			assert.ErrorIs(t, err, tt.err)
			span := requireSingleSpan(t, recorder)
			assert.Equal(t, tt.expectedCode, span.Status().Code)
			assert.Equal(t, tt.expectedEvent, hasExceptionEvent(span))
		})
	}
}

func TestTraceReturnErrSetsSpanStatus(t *testing.T) {
	recorder := installSpanRecorder(t)
	expectedErr := errors.New("operation failed")

	tests := []struct {
		name          string
		err           error
		expectedCode  codes.Code
		expectedEvent bool
	}{
		{
			name:         "success",
			expectedCode: codes.Ok,
		},
		{
			name:          "error",
			err:           expectedErr,
			expectedCode:  codes.Error,
			expectedEvent: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder.Reset()

			err := tools.TraceReturnErr(
				context.Background(),
				"test",
				"operation",
				func(context.Context) error {
					return tt.err
				},
			)

			assert.ErrorIs(t, err, tt.err)
			span := requireSingleSpan(t, recorder)
			assert.Equal(t, tt.expectedCode, span.Status().Code)
			assert.Equal(t, tt.expectedEvent, hasExceptionEvent(span))
		})
	}
}

func installSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()

	previousProvider := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	})
	return recorder
}

func requireSingleSpan(
	t *testing.T,
	recorder *tracetest.SpanRecorder,
) sdktrace.ReadOnlySpan {
	t.Helper()

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	return spans[0]
}

func hasExceptionEvent(span sdktrace.ReadOnlySpan) bool {
	for _, event := range span.Events() {
		if event.Name == "exception" {
			return true
		}
	}
	return false
}
