package events

import (
	"context"
	"log/slog"

	"github.com/ThreeDotsLabs/watermill/message"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/go42-dev/go42/internal/tools"
)

const propagationRequestID = "request_id"

// PropagationFromContext captures the request ID and W3C trace context for asynchronous work.
// W3C trace context is propagated even when local span recording is disabled.
func PropagationFromContext(ctx context.Context) map[string]string {
	fields := make(map[string]string)
	propagation.TraceContext{}.Inject(ctx, propagation.MapCarrier(fields))
	if requestID := tools.GetRequestIDFromContext(ctx); len(requestID) > 0 {
		fields[propagationRequestID] = requestID
	}
	return fields
}

// ContextWithPropagation restores correlation onto the consumer's context,
// retaining its local values, cancellation and deadline.
func ContextWithPropagation(ctx context.Context, fields map[string]string) context.Context {
	ctx = propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier(fields))
	return tools.SetRequestIDToContext(ctx, fields[propagationRequestID])
}

// restoreMessageContext restores correlation and logging context for a message handler.
func restoreMessageContext(topic string) message.HandlerMiddleware {
	return func(next message.HandlerFunc) message.HandlerFunc {
		return func(msg *message.Message) ([]*message.Message, error) {
			ctx := tools.WithLogAttrs(
				ContextWithPropagation(msg.Context(), msg.Metadata),
				slog.String("message_uuid", msg.UUID),
				slog.String("topic", topic),
			)
			msg.SetContext(ctx)
			return next(msg)
		}
	}
}

// Each delivery has one processing span, including its local retry attempts.
// Single-message consumers use the producer as both parent and link.
func traceMessage(topic string) message.HandlerMiddleware {
	return func(next message.HandlerFunc) message.HandlerFunc {
		return func(msg *message.Message) ([]*message.Message, error) {
			ctx := msg.Context()
			opts := []trace.SpanStartOption{
				trace.WithSpanKind(trace.SpanKindConsumer),
				trace.WithAttributes(
					attribute.String("messaging.destination.name", topic),
					attribute.String("messaging.message.id", msg.UUID),
				),
			}
			if parent := trace.SpanContextFromContext(ctx); parent.IsValid() {
				opts = append(opts, trace.WithLinks(trace.Link{SpanContext: parent}))
			}
			ctx, span := otel.Tracer("events").Start(ctx, "process "+topic, opts...)
			defer span.End()
			msg.SetContext(ctx)

			messages, err := next(msg)
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
			}
			return messages, err
		}
	}
}
