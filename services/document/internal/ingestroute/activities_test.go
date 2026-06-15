package ingestroute

import (
	"context"
	"testing"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// TestDecide pins the routing gate: a usable key at/above the threshold commits;
// a missing key or a below-threshold confidence goes to review with the right
// reason. Decide reads only the service's MatchThreshold(), so it needs no DB.
func TestDecide(t *testing.T) {
	svc := service.New(nil, nil, nil, zerolog.Nop())
	svc.SetMatchThreshold(0.85)
	acts := NewActivities(svc, zerolog.Nop())
	ctx := context.Background()

	cases := []struct {
		name       string
		key        string
		confidence float64
		wantAction string
		wantReason string
	}{
		{"high confidence commits", "INV-1", 0.95, "commit", ""},
		{"exactly at threshold commits", "INV-1", 0.85, "commit", ""},
		{"below threshold reviews", "INV-1", 0.84, "review", string(model.ReasonBelowThreshold)},
		{"empty key reviews", "", 0.99, "review", string(model.ReasonNoExternalKey)},
		{"whitespace key reviews", "   ", 0.99, "review", string(model.ReasonNoExternalKey)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := acts.Decide(ctx, tc.key, tc.confidence, "")
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if got.Action != tc.wantAction {
				t.Fatalf("action = %q, want %q", got.Action, tc.wantAction)
			}
			if got.Reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", got.Reason, tc.wantReason)
			}
		})
	}
}

// TestMatchThresholdDefault confirms the 0.85 default when unset.
func TestMatchThresholdDefault(t *testing.T) {
	svc := service.New(nil, nil, nil, zerolog.Nop())
	if got := svc.MatchThreshold(); got != 0.85 {
		t.Fatalf("default threshold = %v, want 0.85", got)
	}
}
