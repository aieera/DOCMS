package service

import (
	"testing"
	"time"

	"github.com/vaultdms/vaultdms/services/notification/internal/model"
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
		{"non-wrap window — outside",  &model.DND{DNDStart: "13:00", DNDEnd: "17:00", Timezone: "UTC"}, mar1Noon, false},
		{"non-wrap window — inside",   &model.DND{DNDStart: "11:00", DNDEnd: "13:00", Timezone: "UTC"}, mar1Noon, true},
		{"wrap window — late evening", &model.DND{DNDStart: "22:00", DNDEnd: "07:00", Timezone: "UTC"}, mar1_2230, true},
		{"wrap window — early morning", &model.DND{DNDStart: "22:00", DNDEnd: "07:00", Timezone: "UTC"}, mar2_0500, true},
		{"wrap window — daytime gap",   &model.DND{DNDStart: "22:00", DNDEnd: "07:00", Timezone: "UTC"}, mar1_0730, false},
		{"degenerate (start==end)",     &model.DND{DNDStart: "08:00", DNDEnd: "08:00", Timezone: "UTC"}, mar1Noon, false},
		{"bad time string fails-open",  &model.DND{DNDStart: "nope", DNDEnd: "07:00", Timezone: "UTC"}, mar1_0500(), false},
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
