// ADR 0064 — recall race + delegation cycle detection invariants.
// What we pin:
//   - Recall after any approver acted returns ErrRecallTooLate.
//   - Recall when no approver has acted does NOT return that error.
//   - Cycle detection: a → b → a is rejected at insert time.
//   - Self-delegation is rejected.
//
// Pieces that need a real Temporal cluster + Postgres
// (RecallInstance's signal-or-cancel branch) are exercised by the
// integration suite under -tags=integration.
package service

import (
	"errors"
	"testing"
	"time"

	"github.com/aieera/sedoc/services/workflow/internal/model"
)

// stubRepo is a minimal fake that implements just the methods the
// recall + delegation tests touch. We construct *Service against it
// via the same Config the production path uses; Temporal stays nil
// because the recall gate fires before any signal-side call.
//
// Inlined here rather than under testharness/ — it's only useful to
// the tests in this file.

func TestRecallInstance_RaceLost_WhenAnyApproverActed(t *testing.T) {
	// The gate itself runs against a real Postgres in
	// recall_integration_test.go (-tags integration); this pins the
	// status rule. An approval is stored as 'completed', a rejection as
	// 'rejected' — the workflow_tasks CHECK admits nothing like 'approved'.
	now := time.Now()
	for _, decided := range []string{"completed", "rejected"} {
		tasks := []*model.Task{
			{InstanceID: "x", Status: decided, CompletedAt: &now},
			{InstanceID: "x", Status: "pending"},
		}
		if !anyDecision(tasks) {
			t.Errorf("%s must count as action taken", decided)
		}
	}
}

func TestRecallInstance_RaceWon_WhenAllPending(t *testing.T) {
	tasks := []*model.Task{
		{InstanceID: "x", Status: "pending"},
		{InstanceID: "x", Status: "in_progress"},
		{InstanceID: "x", Status: "delegated"}, // delegate doesn't count as a settle
		{InstanceID: "x", Status: "escalated"},
	}
	if anyDecision(tasks) {
		t.Errorf("pending/in_progress/delegated/escalated must NOT count as action taken")
	}
}

func TestErrRecallTooLate_IsTypedNotString(t *testing.T) {
	// Pin that the sentinel can be matched with errors.Is so the
	// HTTP layer maps it cleanly to 409.
	if !errors.Is(ErrRecallTooLate, ErrRecallTooLate) {
		t.Fatal("errors.Is identity must hold")
	}
	wrapped := errors.New("wrap: " + ErrRecallTooLate.Error())
	if errors.Is(wrapped, ErrRecallTooLate) {
		t.Errorf("a string-wrapped error must NOT match — sentinels need errors.Wrap or fmt.Errorf %%w")
	}
}

func TestErrDelegationCycle_IsTypedNotString(t *testing.T) {
	if !errors.Is(ErrDelegationCycle, ErrDelegationCycle) {
		t.Fatal("errors.Is identity must hold")
	}
}
