package tools_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/go42-dev/go42/internal/tools"
)

func TestContextFieldsHandlerEnrichesDerivedLoggers(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(tools.SlogContextWrapper(slog.NewJSONHandler(&output, nil))).
		With(slog.String("component", "test")).WithGroup("operation")
	span := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2},
	})
	ctx := trace.ContextWithSpanContext(t.Context(), span)
	ctx = tools.SetRequestIDToContext(ctx, "request-42")
	ctx = tools.WithLogAttrs(ctx, slog.String("event_id", "event-42"))
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	logger.ErrorContext(ctx, "failed", slog.String("reason", "test"))

	var record map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &record))
	assert.Equal(t, "test", record["component"])
	operation, ok := record["operation"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "request-42", operation["request_id"])
	assert.Equal(t, "event-42", operation["event_id"])
	assert.Equal(t, span.TraceID().String(), operation["trace_id"])
	assert.Equal(t, span.SpanID().String(), operation["span_id"])
	assert.Equal(t, "test", operation["reason"])
}

func TestContextFieldsHandlerOmitsMissingCorrelation(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(tools.SlogContextWrapper(slog.NewJSONHandler(&output, nil)))
	logger.InfoContext(t.Context(), "background work")
	var record map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &record))
	assert.NotContains(t, record, "request_id")
	assert.NotContains(t, record, "trace_id")
	assert.NotContains(t, record, "span_id")
}

func TestLogAttrsDoNotLeakBetweenConcurrentContexts(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(tools.SlogContextWrapper(slog.NewJSONHandler(&output, nil)))
	parent := tools.WithLogAttrs(t.Context(), slog.String("step", "parent"))
	var workers sync.WaitGroup
	for i := range 32 {
		workers.Go(func() {
			id := fmt.Sprintf("request-%d", i)
			ctx := tools.WithLogAttrs(parent, slog.String("step", id))
			ctx = tools.SetRequestIDToContext(ctx, id)
			logger.With(slog.Int("worker", i)).InfoContext(ctx, "work")
		})
	}
	workers.Wait()

	decoder := json.NewDecoder(&output)
	seen := make(map[string]bool)
	for range 32 {
		var record map[string]any
		require.NoError(t, decoder.Decode(&record))
		id := fmt.Sprintf("request-%.0f", record["worker"])
		assert.Equal(t, id, record["request_id"])
		assert.Equal(t, id, record["step"])
		seen[id] = true
	}
	assert.Len(t, seen, 32)
	attrs := tools.LogAttrsFromContext(parent)
	require.Len(t, attrs, 1)
	assert.Equal(t, "parent", attrs[0].Value.String())
	attrs[0] = slog.String("step", "changed")
	assert.Equal(t, "parent", tools.LogAttrsFromContext(parent)[0].Value.String())
}
