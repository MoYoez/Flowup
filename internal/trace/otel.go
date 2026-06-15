//go:build otel

package trace

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// InitOTel installs a global TracerProvider with the given span processor (an
// OTLP exporter to Jaeger/Tempo in production, or an in-memory recorder in
// tests). Returns a shutdown func. When InitOTel is never called, spans are
// no-ops (zero behavior/overhead) — instrumentation is always safe to leave in.
func InitOTel(sp sdktrace.SpanProcessor) func(context.Context) error {
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sp),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "flowup"))),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown
}

// InitFromEnv wires OTel from FLOWUP_OTEL: "stdout" → a stdout exporter; anything
// else → no-op. (Point a real OTLP exporter at Jaeger when Docker is available.)
func InitFromEnv() (func(context.Context) error, error) {
	switch os.Getenv("FLOWUP_OTEL") {
	case "stdout":
		exp, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err != nil {
			return nil, err
		}
		return InitOTel(sdktrace.NewBatchSpanProcessor(exp)), nil
	default:
		return func(context.Context) error { return nil }, nil
	}
}

// Tracer is the package tracer.
func Tracer() oteltrace.Tracer { return otel.Tracer("flowup") }

// EndFunc finishes a span, recording the three-state outcome.
type EndFunc func(status, reason string)

func endWith(span oteltrace.Span) EndFunc {
	return func(status, reason string) {
		span.SetAttributes(attribute.String("flowup.status", status))
		if reason != "" {
			span.SetAttributes(attribute.String("flowup.reason", reason))
		}
		span.End()
	}
}

// StartRun starts a run span (correlation IDs as attributes).
func StartRun(ctx context.Context, pipelineID string) (context.Context, EndFunc) {
	ctx, span := Tracer().Start(ctx, "flowup.invoke",
		oteltrace.WithAttributes(attribute.String("flowup.pipeline_id", pipelineID)))
	return ctx, endWith(span)
}

// StartNode starts a node span.
func StartNode(ctx context.Context, nodeID, kind string) (context.Context, EndFunc) {
	ctx, span := Tracer().Start(ctx, "flowup.node",
		oteltrace.WithAttributes(
			attribute.String("flowup.node_id", nodeID),
			attribute.String("flowup.kind", kind),
		))
	return ctx, endWith(span)
}
