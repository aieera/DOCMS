package service

import (
	"testing"
	"time"

	"github.com/aieera/sedoc/services/notification/internal/model"
)

func TestInDNDWindow(t *testing.T) {
	// Anchor "now" to a deterministic UTC instant; we shift the
	// user's tz offset by setting Timezone, so the local time the
	// matcher compares against is reproducible.
	mar1Noon := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	mar1_2230 := time.Date(2026, 3, 1, 22, 30, 0, 0, time.UTC)
	mar2_0500 := time.Date(2026, 3, 2, 5, 0, 0, 0, time.UTC)
	mar1_0730 := time.Date(2026, 3, 1, 7, 30, 0, 0, time.UTC)

	cases := []struct {
		name string
		dnd  *model.DND
		now  time.Time
		want bool
	}{
		{"non-wrap window — outside", &model.DND{DNDStart: "13:00", DNDEnd: "17:00", Timezone: "UTC"}, mar1Noon, false},
		{"non-wrap window — inside", &model.DND{DNDStart: "11:00", DNDEnd: "13:00", Timezone: "UTC"}, mar1Noon, true},
		{"wrap window — late evening", &model.DND{DNDStart: "22:00", DNDEnd: "07:00", Timezone: "UTC"}, mar1_2230, true},
		{"wrap window — early morning", &model.DND{DNDStart: "22:00", DNDEnd: "07:00", Timezone: "UTC"}, mar2_0500, true},
		{"wrap window — daytime gap", &model.DND{DNDStart: "22:00", DNDEnd: "07:00", Timezone: "UTC"}, mar1_0730, false},
		{"degenerate (start==end)", &model.DND{DNDStart: "08:00", DNDEnd: "08:00", Timezone: "UTC"}, mar1Noon, false},
		{"bad time string fails-open", &model.DND{DNDStart: "nope", DNDEnd: "07:00", Timezone: "UTC"}, mar1_0500(), false},
		{"unknown tz falls back to UTC", &model.DND{DNDStart: "11:00", DNDEnd: "13:00", Timezone: "Not/Real"}, mar1Noon, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := inDNDWindow(c.dnd, c.now); got != c.want {
				t.Fatalf("inDNDWindow=%v, want %v", got, c.want)
			}
		})
	}
}

func mar1_0500() time.Time { return time.Date(2026, 3, 1, 5, 0, 0, 0, time.UTC) }

func TestIndexEnabledCells(t *testing.T) {
	cells := []model.PrefCell{
		{Channel: "email", EventType: "comment.mention", IsEnabled: true, DigestEnabled: true},
		{Channel: "in_app", EventType: "comment.mention", IsEnabled: true, DigestEnabled: false},
		{Channel: "sms", EventType: "comment.mention", IsEnabled: false},
		{Channel: "in_app", EventType: "*", IsEnabled: true},
	}

	exact := indexEnabledCells(cells, "comment.mention")
	if !exact[model.ChannelEmail] {
		t.Fatalf("expected email in exact match with digest=true; got %+v", exact)
	}
	if exact[model.ChannelSMS] {
		t.Fatal("disabled cell must not appear")
	}
	if _, ok := exact[model.Channel("in_app")]; !ok {
		t.Fatalf("expected in_app row to be selected")
	}

	// No exact rows for this event_type → wildcard fallback.
	wild := indexEnabledCells([]model.PrefCell{
		{Channel: "in_app", EventType: "*", IsEnabled: true},
	}, "task.due")
	if _, ok := wild["in_app"]; !ok {
		t.Fatal("expected wildcard in_app fallback when no exact rows match")
	}
}

func TestParseChannelConsent(t *testing.T) {
	if c := ParseChannelConsent(nil); c != nil {
		t.Fatalf("nil input must yield nil consent, got %v", c)
	}
	if c := ParseChannelConsent([]string{"bogus"}); c != nil {
		t.Fatalf("unknown-only input must yield nil consent, got %v", c)
	}
	c := ParseChannelConsent([]string{"in_app", "email", "push"})
	if len(c) != 3 || c[model.ChannelInApp] || c[model.ChannelEmail] || c[model.ChannelPush] {
		t.Fatalf("plain channels must map digest=false: %v", c)
	}
	// digest → email folded; a later "email" must not clobber digest=true.
	c = ParseChannelConsent([]string{"digest", "email"})
	if !c[model.ChannelEmail] {
		t.Fatalf("digest+email must keep forceDigest=true: %v", c)
	}
	c = ParseChannelConsent([]string{"email", "digest"})
	if !c[model.ChannelEmail] {
		t.Fatalf("email+digest must end forceDigest=true: %v", c)
	}
}

func TestApplyConsent(t *testing.T) {
	// Consent enables a channel with no matrix cell.
	enabled := map[model.Channel]bool{model.ChannelInApp: false}
	applyConsent(enabled, nil, ChannelConsent{model.ChannelEmail: false})
	if digest, ok := enabled[model.ChannelEmail]; !ok || digest {
		t.Fatalf("consented email must be enabled immediate: %v", enabled)
	}

	// Consent must NOT override an explicit disable for the event type.
	enabled = map[model.Channel]bool{model.ChannelInApp: false}
	disabled := map[model.Channel]struct{}{model.ChannelEmail: {}}
	applyConsent(enabled, disabled, ChannelConsent{model.ChannelEmail: false})
	if _, ok := enabled[model.ChannelEmail]; ok {
		t.Fatalf("explicitly disabled email must stay off: %v", enabled)
	}

	// forceDigest wins over an immediate-mode matrix cell.
	enabled = map[model.Channel]bool{model.ChannelEmail: false}
	applyConsent(enabled, nil, ChannelConsent{model.ChannelEmail: true})
	if !enabled[model.ChannelEmail] {
		t.Fatalf("consent digest must fold an immediate cell: %v", enabled)
	}

	// A matrix digest cell is not downgraded by plain-email consent.
	enabled = map[model.Channel]bool{model.ChannelEmail: true}
	applyConsent(enabled, nil, ChannelConsent{model.ChannelEmail: false})
	if !enabled[model.ChannelEmail] {
		t.Fatalf("matrix digest cell must survive plain consent: %v", enabled)
	}
}

func TestExplicitlyDisabled(t *testing.T) {
	cells := []model.PrefCell{
		{EventType: "saved_search_match", Channel: "email", IsEnabled: false},
		{EventType: "saved_search_match", Channel: "in_app", IsEnabled: true},
		{EventType: "*", Channel: "push", IsEnabled: false}, // wildcard ≠ exact
	}
	d := explicitlyDisabled(cells, "saved_search_match")
	if _, ok := d[model.ChannelEmail]; !ok {
		t.Fatal("exact disabled email row must be collected")
	}
	if _, ok := d[model.ChannelPush]; ok {
		t.Fatal("wildcard disable must not block consent for the exact type")
	}
	if len(d) != 1 {
		t.Fatalf("want 1 disabled entry, got %v", d)
	}
}
