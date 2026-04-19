package events

import "strings"

// SubjectCovers reports whether NATS subject filter `filter` covers every
// concrete message that would match subscribe subject `subj`. Pure Go,
// no NATS client dependency — callable from unit tests without a broker.
//
// Implements the subset of NATS subject matching required for preflight:
// literal tokens, `*` (single-token wildcard), and `>` (tail wildcard,
// legal only as the final token of a filter). A filter covers a subject
// iff every concrete message matching `subj` also matches `filter`.
//
// This is lifted verbatim from the `dms-admin nats check` implementation
// so runtime (`./dms-admin nats check ...`) and test-time
// (`go test ./pkg/events/...`) share one canonical algorithm.
func SubjectCovers(filter, subj string) bool {
	ft := strings.Split(filter, ".")
	st := strings.Split(subj, ".")
	for i := 0; i < len(ft); i++ {
		if ft[i] == ">" {
			// NATS `>` matches ONE OR MORE tokens; it does not match
			// zero tail tokens. So `dms.foo.>` covers `dms.foo.bar`
			// and `dms.foo.bar.baz` but NOT `dms.foo`.
			return i < len(st)
		}
		if i >= len(st) {
			return false
		}
		switch {
		case ft[i] == "*":
			if st[i] == ">" {
				return false
			}
		case ft[i] == st[i]:
		default:
			return false
		}
	}
	return len(ft) == len(st)
}

// CheckCoverage iterates every subject in `subjects` and returns the
// subset that is NOT covered by any stream in `streams`. An empty
// return slice means every subject has a home. Order of returned
// subjects matches the input order so callers can correlate errors.
func CheckCoverage(subjects []string, streams []StreamSpec) []string {
	var missing []string
	for _, s := range subjects {
		covered := false
		for _, stream := range streams {
			for _, filter := range stream.Subjects {
				if SubjectCovers(filter, s) {
					covered = true
					break
				}
			}
			if covered {
				break
			}
		}
		if !covered {
			missing = append(missing, s)
		}
	}
	return missing
}

// PublishedSubjects is the canonical list of subjects the VaultDMS
// codebase publishes or subscribes to. Every new subject introduced
// anywhere in the codebase MUST be added here AND covered by a stream
// in DefaultStreams — the build gate in coverage_test.go enforces both
// halves. Keep alphabetized within each aggregate to make merges easy.
//
// Harvested 2026-04-19 from:
//   - grep -r 'NewOutboxEvent' services/
//   - grep -r 'js.PublishMsg\|nc.Publish' services/ pkg/
//   - services/*/internal/service/*.go subscribe sites
//   - services/audit + services/connector broad consumers
var PublishedSubjects = []string{
	// auth
	"dms.auth.login_failed.v1",
	"dms.auth.login_success.v1",
	"dms.auth.api_key_issued.v1",
	"dms.auth.api_key_revoked.v1",
	// document
	"dms.document.created.v1",
	"dms.document.deleted.v1",
	"dms.document.moved.v1",
	"dms.document.state_changed.v1",
	"dms.document.updated.v1",
	// folder
	"dms.folder.created.v1",
	"dms.folder.moved.v1",
	// hold
	"dms.hold.applied.v1",
	"dms.hold.released.v1",
	"dms.hold.updated.v1",
	// notify
	"dms.notify.dsr_verify.v1",
	"dms.notify.signature_requested.v1",
	"dms.notify.workflow_assigned.v1",
	// residency
	"dms.residency.migrated.v1",
	// sharelink
	"dms.sharelink.created.v1",
	// signature
	"dms.signature.completed.v1",
	// user
	"dms.user.invited.v1",
	"dms.user.mfa_reset.v1",
	"dms.user.suspended.v1",
	// version
	"dms.version.restored.v1",
	"dms.version.uploaded.v1",
	// workspace
	"dms.workspace.created.v1",
	"dms.workspace.deleted.v1",
	"dms.workspace.updated.v1",
}
