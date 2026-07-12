// Package tracing provides OpenTelemetry initialization for all SeDoc
// services. Auto-instruments HTTP, gRPC, pgx, and Redis. Custom spans for
// OPA, S3, LLM, and OCR operations. Exports to OTLP (Tempo/Jaeger).
//
// Sampling is env-configurable (see Init); default is parent-based at the
// SEDOC_TRACE_SAMPLE_RATIO ratio (0.01) so a sampled upstream span keeps
// the whole cross-service trace, but new roots are cheap.
//
// Cross-service context crosses three boundaries in SeDoc — HTTP (otelhttp
// + the W3C propagator set here), gRPC (otelgrpc), and NATS. NATS carries
// no otel instrumentation, so context rides the transactional OUTBOX:
// Insert stamps the current trace context onto the row, the publisher
// injects it as `traceparent` on the NATS message, and the consumer
// re-extracts it (InjectToMap / ExtractFromMap / ConsumerContext below).
package tracing

import (
	"context"
	"os"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
)

// Enabled reports whether tracing should be initialized in this process.
// Off by default so a service with no collector pays nothing and never
// errors at boot; opt in with SEDOC_TRACING_ENABLED=1 or by setting
// OTEL_EXPORTER_OTLP_ENDPOINT. main() should skip Init when this is false.
func Enabled() bool {
	if v := os.Getenv("SEDOC_TRACING_ENABLED"); v == "1" || v == "true" {
		return true
	}
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != ""
}

// sampleRatio reads SEDOC_TRACE_SAMPLE_RATIO (0..1), defaulting to 0.01.
// Out-of-range / unparseable values fall back to the default.
func sampleRatio() float64 {
	s := os.Getenv("SEDOC_TRACE_SAMPLE_RATIO")
	if s == "" {
		return 0.01
	}
	r, err := strconv.ParseFloat(s, 64)
	if err != nil || r < 0 || r > 1 {
		return 0.01
	}
	return r
}

// Init sets up the global TracerProvider with an OTLP HTTP exporter and
// the W3C propagator. Call the returned shutdown func in a defer from
// main(). Callers should gate on Enabled() first. Sampling is
// parent-based at SEDOC_TRACE_SAMPLE_RATIO (default 0.01): a sampled
// remote parent keeps the whole trace, so an end-to-end request that gets
// sampled at the edge stays connected across services regardless of the
// per-hop ratio.
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

	ratio := sampleRatio()
	sampler := sdktrace.ParentBased(
		sdktrace.TraceIDRatioBased(ratio),
		sdktrace.WithRemoteParentSampled(sdktrace.AlwaysSample()),
		sdktrace.WithRemoteParentNotSampled(sdktrace.TraceIDRatioBased(ratio)),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	)

	otel.SetTracerProvider(tp)
	SetGlobalPropagator()

	return tp.Shutdown, nil
}

// SetGlobalPropagator installs the W3C trace-context + baggage
// propagator globally. Init calls it; the inject/extract helpers below
// fall back to installing it on first use so they work even in a process
// that only relays context (e.g. the outbox publisher) without a full
// Init.
func SetGlobalPropagator() {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
}

// InjectToMap serializes the trace context on ctx into a plain string map
// (W3C `traceparent`/`tracestate`). Returns nil when there is no active
// span context. Used to stamp the outbox row + NATS headers so context
// survives the async boundary.
func InjectToMap(ctx context.Context) map[string]string {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return nil
	}
	carrier := propagation.MapCarrier{}
	propagatorOrDefault().Inject(ctx, carrier)
	if len(carrier) == 0 {
		return nil
	}
	return map[string]string(carrier)
}

// ExtractFromMap returns a context carrying the trace context encoded in
// `m` (as produced by InjectToMap), so a consumer's span becomes a child
// of the producer's. A nil/empty map returns parent unchanged.
func ExtractFromMap(parent context.Context, m map[string]string) context.Context {
	if len(m) == 0 {
		return parent
	}
	return propagatorOrDefault().Extract(parent, propagation.MapCarrier(m))
}

func propagatorOrDefault() propagation.TextMapPropagator {
	p := otel.GetTextMapPropagator()
	// A zero (no-op) propagator won't round-trip traceparent; install the
	// real one lazily so relay-only processes still propagate.
	if _, ok := p.(propagation.TextMapPropagator); !ok || len(p.Fields()) == 0 {
		SetGlobalPropagator()
		p = otel.GetTextMapPropagator()
	}
	return p
}

// ConsumerContext starts a CONSUMER span linked to the trace context
// carried in `carrier` (the map an async producer injected). Use it at
// the top of a NATS/queue handler:
//
//	ctx, span := tracing.ConsumerContext(parent, hdr, "search.index.document")
//	defer span.End()
func ConsumerContext(parent context.Context, carrier map[string]string, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	ctx := ExtractFromMap(parent, carrier)
	return Tracer("vaultdms").Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(attrs...))
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
