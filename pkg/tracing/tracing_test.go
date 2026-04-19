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
