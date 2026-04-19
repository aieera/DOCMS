package model

import "fmt"

// transitionKey is the composite key into the state-machine table.
type transitionKey struct {
	from   LifecycleState
	action LifecycleAction
}

// lifecycleTransitions is the canonical state machine. Any (from, action)
// not present in this map is an invalid transition.
//
// Note on release_hold: the sentinel target is StateDraft. The service layer
// must consult legal_hold_documents.previous_lifecycle_state to discover the
// actual prior state and apply that, overriding this sentinel.
var lifecycleTransitions = map[transitionKey]LifecycleState{
	{StateDraft, ActionSubmitForReview}:    StateInReview,
	{StateInReview, ActionApprove}:         StateActive,
	{StateInReview, ActionReject}:          StateDraft,
	{StateActive, ActionSupersede}:         StateSuperseded,
	{StateActive, ActionArchive}:           StateRetained,
	{StateSuperseded, ActionArchive}:       StateRetained,
	{StateRetained, ActionArchive}:         StateArchived,
	{StateRetained, ActionRestore}:         StateActive,
	{StateArchived, ActionDispose}:         StateDisposed,

	// Legal hold is enterable from every non-disposed state.
	{StateDraft, ActionApplyHold}:      StateLegalHold,
	{StateInReview, ActionApplyHold}:   StateLegalHold,
	{StateActive, ActionApplyHold}:     StateLegalHold,
	{StateSuperseded, ActionApplyHold}: StateLegalHold,
	{StateRetained, ActionApplyHold}:   StateLegalHold,
	{StateArchived, ActionApplyHold}:   StateLegalHold,

	// release_hold: sentinel — service resolves previous_lifecycle_state.
	{StateLegalHold, ActionReleaseHold}: StateDraft,
}

// ValidateTransition returns the target state for a (current, action) pair
// or an error describing why the transition is illegal.
func ValidateTransition(current LifecycleState, action LifecycleAction) (LifecycleState, error) {
	to, ok := lifecycleTransitions[transitionKey{current, action}]
	if !ok {
		return "", fmt.Errorf("invalid state transition: cannot perform %q on document in %q state", action, current)
	}
	return to, nil
}

// IsLegalHoldBlocked reports whether an operation is forbidden by legal hold.
// Returns false for any state other than StateLegalHold; returns true for
// unknown operations (conservative default).
//
// Under hold:
//   - blocked: delete, hard_delete, create_version, move, update_retention
//   - allowed: view, download, comment, annotate, update_title, update_metadata
func IsLegalHoldBlocked(state LifecycleState, operation string) bool {
	if state != StateLegalHold {
		return false
	}
	allowed := map[string]struct{}{
		"view":            {},
		"download":        {},
		"comment":         {},
		"annotate":        {},
		"update_title":    {},
		"update_metadata": {},
		"list_versions":   {},
	}
	_, ok := allowed[operation]
	return !ok
}

// AllowedActions returns the actions legal to perform from a given state.
// Useful for UI affordances.
func AllowedActions(state LifecycleState) []LifecycleAction {
	var actions []LifecycleAction
	for k := range lifecycleTransitions {
		if k.from == state {
			actions = append(actions, k.action)
		}
	}
	return actions
}
