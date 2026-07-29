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

// PublishedSubjects is the canonical list of subjects the SeDoc
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
	"dms.auth.password_reset_requested.v1",
	"dms.auth.password_changed.v1",
	// annotation (§17.3 / D10)
	"dms.annotation.created.v1",
	"dms.annotation.updated.v1",
	"dms.annotation.deleted.v1",
	// connector
	"dms.connector.synced.v1",
	// document
	"dms.document.created.v1",
	"dms.document.deleted.v1",
	"dms.document.edited.v1", // collaborative-edit snapshot (collaboration svc, §17.4)
	"dms.document.moved.v1",
	"dms.document.purged.v1",   // trash permanent delete (S3 + DB)
	"dms.document.restored.v1", // trash restore
	"dms.document.state_changed.v1",
	"dms.document.updated.v1",
	// irm — protected-container license lifecycle (§5/§8)
	"dms.irm.license_issued.v1",
	"dms.irm.opened.v1",
	"dms.irm.revoked.v1",
	// folder
	"dms.folder.created.v1",
	"dms.folder.deleted.v1",  // cascade soft-delete (cohort)
	"dms.folder.moved.v1",
	"dms.folder.purged.v1",   // trash permanent delete of a cohort
	"dms.folder.restored.v1", // cohort restore from trash

	// workspace templates (ADR 0118)
	"dms.template.provisioned.v1",

	// analytics reports (ADR 0119)
	"dms.report.generated.v1",
	"dms.notify.report_ready.v1",
	// hold
	"dms.hold.applied.v1",
	"dms.hold.released.v1",
	"dms.hold.updated.v1",
	// notify
	"dms.notify.document.uploaded.v1",
	"dms.notify.document_shared.v1",
	"dms.notify.dsr_verify.v1",
	"dms.notify.signature_requested.v1",
	"dms.notify.workflow_assigned.v1",
	// residency
	"dms.residency.migrated.v1",
	// sharelink
	"dms.sharelink.created.v1",
	// signature
	"dms.signature.applied.v1",
	"dms.signature.completed.v1",
	// user
	"dms.user.invited.v1",
	"dms.user.mfa_reset.v1",
	"dms.user.mfa_changed.v1",
	"dms.user.suspended.v1",
	// version
	"dms.version.restored.v1",
	"dms.version.uploaded.v1",
	// workspace
	"dms.workspace.created.v1",
	"dms.workspace.deleted.v1",
	"dms.workspace.updated.v1",

	// python emitters (services/intelligence + services/preview).
	// These reach the broker via the SHARED sedoc.outbox (drained by the
	// Go OutboxPublisher) or direct JetStream publish, so they need the
	// same stream coverage as Go subjects. Kept complete mechanically:
	// coverage_python_test.go harvests every dms.*.vN literal from the
	// Python services and fails the build if one is missing here or
	// unbound in DefaultStreams.
	"dms.anomaly.completed.v1",
	"dms.autotag.completed.v1",
	"dms.billing.llm.usage.v1",
	"dms.classify.completed.v1",
	"dms.classify.corrected.v1",
	"dms.compliance.completed.v1",
	"dms.compliance.rescan_requested.v1",
	"dms.document.redacted.v1",
	"dms.embed.completed.v1",
	"dms.ingestion.processed.v1",
	"dms.ingestion.received.v1",
	"dms.language.detected.v1",
	"dms.model.evaluated.v1",
	"dms.model.promoted.v1",
	"dms.model.retrain_requested.v1",
	"dms.model.trained.v1",
	"dms.ner.completed.v1",
	"dms.notification.send.v1",
	"dms.ocr.failed.v1",
	"dms.ocr.quality.completed.v1",
	"dms.redaction.applied.v1",
	"dms.redaction.apply_requested.v1",
	"dms.routing.completed.v1",
	"dms.training_example.collected.v1",
	"dms.translation.completed.v1",
	"dms.version.fields_extracted.v1",
	"dms.version.ocr_completed.v1",
	"dms.version.ocr_retry_requested.v1",
	"dms.version.preview_ready.v1",
}
