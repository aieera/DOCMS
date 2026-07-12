package tracing

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// A test-scoped TracerProvider (in-memory exporter) lets us assert span
// attributes and names without a real OTLP endpoint.

func setupTestProvider(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(rec),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)
	return rec
}

func TestStartSpan_CreatesChildAndSetsAttributes(t *testing.T) {
	rec := setupTestProvider(t)

	_, span := StartSpan(context.Background(), "unit.test.op",
		attribute.String("tenant_id", "t-abc"),
		attribute.Int("pages", 3),
	)
	span.End()

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	s := spans[0]
	if s.Name() != "unit.test.op" {
		t.Errorf("span name: %q", s.Name())
	}
	attrs := map[string]string{}
	for _, a := range s.Attributes() {
		attrs[string(a.Key)] = a.Value.Emit()
	}
	if attrs["tenant_id"] != "t-abc" {
		t.Errorf("tenant_id attribute: %v", attrs)
	}
	if attrs["pages"] != "3" {
		t.Errorf("pages attribute: %v", attrs)
	}
}

func TestStartSpan_Nested(t *testing.T) {
	rec := setupTestProvider(t)

	parentCtx, parent := StartSpan(context.Background(), "parent")
	_, child := StartSpan(parentCtx, "child")
	child.End()
	parent.End()

	spans := rec.Ended()
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(spans))
	}
	// The child's parent span id should match the parent's span id.
	var parentSID, childParentSID trace.SpanID
	for _, s := range spans {
		switch s.Name() {
		case "parent":
			parentSID = s.SpanContext().SpanID()
		case "child":
			childParentSID = s.Parent().SpanID()
		}
	}
	if parentSID != childParentSID {
		t.Errorf("child parent id %s ≠ parent span id %s", childParentSID, parentSID)
	}
}

func TestRecordError_AttachesErrorToActiveSpan(t *testing.T) {
	rec := setupTestProvider(t)
	ctx, span := StartSpan(context.Background(), "op-with-error")

	RecordError(ctx, errors.New("boom"))
	span.End()

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	events := spans[0].Events()
	if len(events) == 0 {
		t.Fatal("expected an error event on the span")
	}
	// The otel SDK records errors as an event named "exception".
	found := false
	for _, e := range events {
		if e.Name == "exception" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no 'exception' event in span events: %+v", events)
	}
}

func TestRecordError_NoActiveSpanIsSafe(t *testing.T) {
	// Must not panic when called outside a span.
	RecordError(context.Background(), errors.New("no span"))
}

func TestTracer_ReturnsNonNil(t *testing.T) {
	setupTestProvider(t)
	tr := Tracer("x")
	if tr == nil {
		t.Fatal("Tracer returned nil")
	}
}

// ---- Async propagation helpers (outbox → NATS → consumer) ----

func TestInjectExtract_RoundTripPreservesTrace(t *testing.T) {
	setupTestProvider(t)
	SetGlobalPropagator()

	ctx, span := StartSpan(context.Background(), "producer")
	defer span.End()
	want := span.SpanContext().TraceID()

	// Inject → serialize to a plain map (what the outbox row stores).
	m := InjectToMap(ctx)
	if len(m) == 0 {
		t.Fatal("InjectToMap returned empty for an active sampled span")
	}
	if _, ok := m["traceparent"]; !ok {
		t.Fatalf("expected traceparent key, got %v", m)
	}

	// Extract on the "consumer" side → same trace id, no active parent ctx.
	got := trace.SpanContextFromContext(ExtractFromMap(context.Background(), m))
	if !got.IsValid() {
		t.Fatal("ExtractFromMap produced an invalid span context")
	}
	if got.TraceID() != want {
		t.Errorf("trace id not preserved: want %s got %s", want, got.TraceID())
	}
	if !got.IsRemote() {
		t.Error("extracted span context should be marked remote")
	}
}

func TestInjectToMap_NoActiveSpanReturnsNil(t *testing.T) {
	setupTestProvider(t)
	SetGlobalPropagator()
	if m := InjectToMap(context.Background()); m != nil {
		t.Errorf("expected nil for no active span, got %v", m)
	}
}

func TestExtractFromMap_EmptyReturnsParentUnchanged(t *testing.T) {
	parent := context.Background()
	if got := ExtractFromMap(parent, nil); got != parent {
		t.Error("nil carrier should return the parent context unchanged")
	}
	if got := ExtractFromMap(parent, map[string]string{}); got != parent {
		t.Error("empty carrier should return the parent context unchanged")
	}
}

func TestConsumerContext_LinksToProducerTrace(t *testing.T) {
	rec := setupTestProvider(t)
	SetGlobalPropagator()

	// Producer stamps context onto a carrier (as the outbox does).
	pctx, pspan := StartSpan(context.Background(), "producer")
	want := pspan.SpanContext().TraceID()
	carrier := InjectToMap(pctx)
	pspan.End()

	// Consumer opens its span from the carrier.
	_, cspan := ConsumerContext(context.Background(), carrier, "consume")
	cspan.End()

	var consumer sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == "consume" {
			consumer = s
		}
	}
	if consumer == nil {
		t.Fatal("consumer span not recorded")
	}
	if consumer.SpanContext().TraceID() != want {
		t.Errorf("consumer trace id %s ≠ producer %s (trace broke across the carrier)",
			consumer.SpanContext().TraceID(), want)
	}
	if consumer.SpanKind() != trace.SpanKindConsumer {
		t.Errorf("expected consumer span kind, got %v", consumer.SpanKind())
	}
}

func TestSampleRatio_EnvParsing(t *testing.T) {
	cases := []struct {
		env  string
		set  bool
		want float64
	}{
		{"", false, 0.01},   // unset → default
		{"", true, 0.01},    // empty → default
		{"0.5", true, 0.5},  // valid
		{"1", true, 1.0},    // valid boundary
		{"0", true, 0.0},    // valid boundary
		{"nonsense", true, 0.01}, // unparseable → default
		{"-0.1", true, 0.01},     // out of range → default
		{"1.5", true, 0.01},      // out of range → default
	}
	for _, c := range cases {
		if c.set {
			t.Setenv("SEDOC_TRACE_SAMPLE_RATIO", c.env)
		} else {
			// Ensure it's not inherited from the environment.
			t.Setenv("SEDOC_TRACE_SAMPLE_RATIO", "")
		}
		if got := sampleRatio(); got != c.want {
			t.Errorf("sampleRatio(%q,set=%v) = %v, want %v", c.env, c.set, got, c.want)
		}
	}
}

func TestEnabled_Gate(t *testing.T) {
	// Both unset → off.
	t.Setenv("SEDOC_TRACING_ENABLED", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	if Enabled() {
		t.Error("Enabled() should be false when neither env var is set")
	}
	// Explicit flag on.
	t.Setenv("SEDOC_TRACING_ENABLED", "1")
	if !Enabled() {
		t.Error("Enabled() should be true for SEDOC_TRACING_ENABLED=1")
	}
	t.Setenv("SEDOC_TRACING_ENABLED", "true")
	if !Enabled() {
		t.Error("Enabled() should be true for SEDOC_TRACING_ENABLED=true")
	}
	// Endpoint set (flag off) → on.
	t.Setenv("SEDOC_TRACING_ENABLED", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://jaeger:4318")
	if !Enabled() {
		t.Error("Enabled() should be true when OTEL_EXPORTER_OTLP_ENDPOINT is set")
	}
}
