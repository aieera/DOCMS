package service

import (
	"testing"

	"github.com/google/uuid"
)

func TestStaleBaseVersion(t *testing.T) {
	v1 := uuid.New()
	v2 := uuid.New()
	ptr := func(u uuid.UUID) *uuid.UUID { return &u }

	cases := []struct {
		name    string
		base    *uuid.UUID
		current *uuid.UUID
		stale   bool
	}{
		{"no base skips check (last-write-wins)", nil, ptr(v2), false},
		{"base matches head is fresh", ptr(v1), ptr(v1), false},
		{"base stale vs moved head -> conflict", ptr(v1), ptr(v2), true},
		{"base set but doc has no head yet -> conflict", ptr(v1), nil, true},
		{"first version: nil base, nil head is fresh", nil, nil, false},
	}
	for _, c := range cases {
		if got := StaleBaseVersion(c.base, c.current); got != c.stale {
			t.Fatalf("%s: want stale=%v got %v", c.name, c.stale, got)
		}
	}
}
