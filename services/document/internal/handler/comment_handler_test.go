// ADR 0066 — pure function tests for AggregateReactions.
package handler

import (
	"testing"

	"github.com/google/uuid"

	"github.com/vaultdms/vaultdms/services/document/internal/repository"
)

func TestAggregateReactions_GroupsByEmoji(t *testing.T) {
	tenant := uuid.New()
	cid := uuid.New()
	a, b, c := uuid.New(), uuid.New(), uuid.New()

	rows := []repository.CommentReaction{
		{TenantID: tenant, CommentID: cid, UserID: a, Emoji: "👍"},
		{TenantID: tenant, CommentID: cid, UserID: b, Emoji: "👍"},
		{TenantID: tenant, CommentID: cid, UserID: c, Emoji: "🎉"},
	}
	got := AggregateReactions(rows)
	if len(got) != 2 {
		t.Fatalf("got %d emoji buckets; want 2", len(got))
	}

	byEmoji := map[string]reactionAggregate{}
	for _, r := range got {
		byEmoji[r.Emoji] = r
	}
	thumbs, ok := byEmoji["👍"]
	if !ok || thumbs.Count != 2 {
		t.Errorf("👍 bucket wrong: %+v", thumbs)
	}
	if len(thumbs.Users) != 2 {
		t.Errorf("👍 users len %d want 2", len(thumbs.Users))
	}
	celeb, ok := byEmoji["🎉"]
	if !ok || celeb.Count != 1 {
		t.Errorf("🎉 bucket wrong: %+v", celeb)
	}
}

func TestAggregateReactions_PreservesInsertionOrder(t *testing.T) {
	tenant := uuid.New()
	cid := uuid.New()
	rows := []repository.CommentReaction{
		{TenantID: tenant, CommentID: cid, UserID: uuid.New(), Emoji: "🎉"},
		{TenantID: tenant, CommentID: cid, UserID: uuid.New(), Emoji: "👍"},
		{TenantID: tenant, CommentID: cid, UserID: uuid.New(), Emoji: "🎉"},
	}
	got := AggregateReactions(rows)
	// First-seen-emoji ordering: 🎉 then 👍.
	if got[0].Emoji != "🎉" || got[1].Emoji != "👍" {
		t.Errorf("ordering broken: %+v", got)
	}
}

func TestAggregateReactions_Empty(t *testing.T) {
	if got := AggregateReactions(nil); len(got) != 0 {
		t.Errorf("nil → %v want empty", got)
	}
}
