package persisted

import (
	"errors"
	"testing"
)

func TestLoad_EmbeddedManifest(t *testing.T) {
	s, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.Size() == 0 {
		t.Fatalf("expected at least one persisted entry")
	}
}

func TestLookup_UnknownHashReturnsErrUnknown(t *testing.T) {
	s, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	_, err = s.Lookup("0000000000000000000000000000000000000000000000000000000000000000")
	if !errors.Is(err, ErrUnknownHash) {
		t.Fatalf("expected ErrUnknownHash, got %v", err)
	}
}

func TestHash_DeterministicAndDifferent(t *testing.T) {
	a := Hash("query Foo { __typename }")
	b := Hash("query Foo { __typename }")
	c := Hash("query Bar { __typename }")
	if a != b {
		t.Fatalf("hash should be deterministic: %s vs %s", a, b)
	}
	if a == c {
		t.Fatalf("different docs should hash differently")
	}
	if len(a) != 64 {
		t.Fatalf("expected 64-char hex, got %d", len(a))
	}
}
