package service

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/services/document/internal/repository"
)

// The paginated query path needs Postgres (covered by integration tests);
// these pin the opaque-cursor codec, which is pure and the most likely
// thing to silently break a client's paging.

func TestAnnotationCursor_RoundTrip(t *testing.T) {
	in := repository.AnnotationCursor{
		Page:      7,
		CreatedAt: time.Unix(0, 1717171717123456789).UTC(),
		ID:        uuid.Must(uuid.NewV7()),
	}
	out, err := decodeAnnotationCursor(encodeAnnotationCursor(in))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out == nil {
		t.Fatal("round-tripped cursor is nil")
	}
	if out.Page != in.Page || !out.CreatedAt.Equal(in.CreatedAt) || out.ID != in.ID {
		t.Fatalf("round-trip mismatch: got %+v want %+v", *out, in)
	}
}

func TestAnnotationCursor_EmptyDecodesToNil(t *testing.T) {
	c, err := decodeAnnotationCursor("")
	if err != nil || c != nil {
		t.Fatalf("empty cursor must decode to (nil,nil); got (%v, %v)", c, err)
	}
}

func TestAnnotationCursor_RejectsGarbage(t *testing.T) {
	if _, err := decodeAnnotationCursor("!!!not base64!!!"); err == nil {
		t.Fatal("expected an error for a non-base64 cursor")
	}
	if _, err := decodeAnnotationCursor(encodeAnnotationCursor(repository.AnnotationCursor{})[:3]); err == nil {
		t.Fatal("expected an error for a truncated cursor")
	}
}
