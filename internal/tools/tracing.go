package tools

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

func TraceReturnT[T any](
	ctx context.Context,
	tracerName string,
	spanName string,
	fn func(context.Context) T,
) T {
	tracer := otel.Tracer(tracerName)
	if tracer == nil {
		return fn(ctx)
	}
	tracerCtx, span := tracer.Start(ctx, spanName)
	defer span.End()
	result := fn(tracerCtx)
	span.SetStatus(codes.Ok, "success")
	return result
}

func TraceReturnTWithErr[T any](
	ctx context.Context,
	tracerName string,
	spanName string,
	fn func(context.Context) (T, error),
) (T, error) {
	tracer := otel.Tracer(tracerName)
	if tracer == nil {
		return fn(ctx)
	}
	tracerCtx, span := tracer.Start(ctx, spanName)
	defer span.End()
	result, err := fn(tracerCtx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "success")
	}
	return result, err
}

func TraceReturnErr(
	ctx context.Context,
	tracerName string,
	spanName string,
	fn func(context.Context) error,
) error {
	tracer := otel.Tracer(tracerName)
	if tracer == nil {
		return fn(ctx)
	}
	tracerCtx, span := tracer.Start(ctx, spanName)
	defer span.End()
	err := fn(tracerCtx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "success")
	}
	return err
}

func TraceNoReturn(
	ctx context.Context,
	tracerName string,
	spanName string,
	fn func(context.Context),
) {
	tracer := otel.Tracer(tracerName)
	if tracer == nil {
		fn(ctx)
		return
	}
	tracerCtx, span := tracer.Start(ctx, spanName)
	defer span.End()
	fn(tracerCtx)
}
