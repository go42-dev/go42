package tools

import (
	"context"
	"log/slog"
	"slices"

	"go.opentelemetry.io/otel/trace"
)

const contextKeyLogAttrs contextKey = "log_attrs"

// WithLogAttrs adds fields to logs made with the returned context.
// A derived context replaces fields with the same key without modifying its parent.
func WithLogAttrs(ctx context.Context, attrs ...slog.Attr) context.Context {
	current, _ := ctx.Value(contextKeyLogAttrs).([]slog.Attr)
	return context.WithValue(ctx, contextKeyLogAttrs, mergeLogAttrs(slices.Clone(current), attrs...))
}

// LogAttrsFromContext returns a fresh slice of correlation and application fields.
func LogAttrsFromContext(ctx context.Context) []slog.Attr {
	current, _ := ctx.Value(contextKeyLogAttrs).([]slog.Attr)
	attrs := slices.Clone(current)
	if requestID := GetRequestIDFromContext(ctx); len(requestID) > 0 {
		attrs = mergeLogAttrs(attrs, slog.String("request_id", requestID))
	}
	if span := trace.SpanContextFromContext(ctx); span.IsValid() {
		attrs = mergeLogAttrs(attrs,
			slog.String("trace_id", span.TraceID().String()),
			slog.String("span_id", span.SpanID().String()),
		)
	}
	return attrs
}

func mergeLogAttrs(current []slog.Attr, attrs ...slog.Attr) []slog.Attr {
	for _, attr := range attrs {
		index := slices.IndexFunc(current, func(existing slog.Attr) bool { return existing.Key == attr.Key })
		if index < 0 {
			current = append(current, attr)
		} else {
			current[index] = attr
		}
	}
	return current
}

// ContextFieldsHandler is wrapper around slog handler which automatically adds
// predefined fields to logs taking values from context.
type ContextFieldsHandler struct {
	slog.Handler
}

func SlogContextWrapper(h slog.Handler) slog.Handler {
	return &ContextFieldsHandler{h}
}

func (h *ContextFieldsHandler) Handle(ctx context.Context, r slog.Record) error {
	r = r.Clone()
	r.AddAttrs(LogAttrsFromContext(ctx)...)
	return h.Handler.Handle(ctx, r)
}

func (h *ContextFieldsHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &ContextFieldsHandler{
		Handler: h.Handler.WithAttrs(attrs),
	}
}

func (h *ContextFieldsHandler) WithGroup(name string) slog.Handler {
	return &ContextFieldsHandler{
		Handler: h.Handler.WithGroup(name),
	}
}
