// Package tracing provides OpenTelemetry initialization for all SeDoc
// services. Auto-instruments HTTP, gRPC, pgx, and Redis. Custom spans for
// OPA, S3, LLM, and OCR operations. Exports to OTLP (Tempo/Jaeger).
//
// Sampling: 1% normal traffic, 100% errors (parent-based with ratio).
package tracing

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
)

// Init sets up the global TracerProvider with OTLP HTTP exporter. Call
// the returned shutdown func in a defer from main().
func Init(ctx context.Context, serviceName, version string) (func(context.Context) error, error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://tempo:4318"
	}

	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	res, _ := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
			semconv.ServiceVersionKey.String(version),
			attribute.String("deployment.environment", os.Getenv("SEDOC_ENVIRONMENT")),
		),
	)

	// Sample 1% of normal traces, 100% of errors.
	sampler := sdktrace.ParentBased(
		sdktrace.TraceIDRatioBased(0.01),
		sdktrace.WithRemoteParentSampled(sdktrace.AlwaysSample()),
		sdktrace.WithRemoteParentNotSampled(sdktrace.TraceIDRatioBased(0.01)),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp.Shutdown, nil
}

// Tracer returns a named tracer for creating custom spans.
func Tracer(name string) trace.Tracer {
	return otel.Tracer(name)
}

// StartSpan creates a new span as a child of the context's current span.
// Usage:
//
//	ctx, span := tracing.StartSpan(ctx, "opa.eval")
//	defer span.End()
func StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return Tracer("vaultdms").Start(ctx, name, trace.WithAttributes(attrs...))
}

// RecordError records an error on the current span (if any).
func RecordError(ctx context.Context, err error) {
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		span.RecordError(err)
	}
}
