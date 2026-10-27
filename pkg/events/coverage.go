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
	"dms.folder.deleted.v1", // cascade soft-delete (cohort)
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
	// task (2026-07-28 task-service design, Task 4/5/6 emitters)
	"dms.notify.task.assigned.v1",
	"dms.notify.task.completed.v1",
	"dms.notify.task.due_soon.v1",
	"dms.notify.task.mention.v1",
	"dms.notify.task.overdue.v1",
	"dms.task.assigned.v1",
	"dms.task.comment.created.v1",
	"dms.task.created.v1",
	"dms.task.deleted.v1",
	"dms.task.document_linked.v1",
	"dms.task.document_unlinked.v1",
	"dms.task.status_changed.v1",
	"dms.task.unassigned.v1",
	"dms.task.updated.v1",
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

	// Go emitters harvested mechanically (CB-8, 2026-10-12): the hand list
	// above missed these. coverage_go_test.go (TestGoEmittedSubjectsCovered)
	// walks services/**/*.go and fails the build if a subject literal is
	// missing here or unbound in DefaultStreams, so the list stays complete.
	// anomaly
	"dms.anomaly.reviewed.v1",
	// audit
	"dms.audit.esign.event.v1",
	"dms.audit.mcp_tool_invoked.v1",
	"dms.audit.qes.signed.v1",
	"dms.audit.qes.started.v1",
	// auth
	"dms.auth.logout.v1",
	"dms.auth.mfa_disabled.v1",
	"dms.auth.mfa_enabled.v1",
	"dms.auth.user_registered.v1",
	// autotag
	"dms.autotag.reviewed.v1",
	// coauth
	"dms.coauth.session.ended.v1",
	"dms.coauth.session.started.v1",
	// comment
	"dms.comment.created.v1",
	"dms.comment.deleted.v1",
	"dms.comment.reaction.v1",
	"dms.comment.resolved.v1",
	"dms.comment.updated.v1",
	// compliance
	"dms.compliance.reviewed.v1",
	// document
	"dms.document.copied.v1",
	"dms.document.downloaded.v1",
	"dms.document.reindexed.v1",
	"dms.document.retention_exempt_set.v1",
	"dms.document.trash_cleared.v1",
	"dms.document.version_labeled.v1",
	// dsr
	"dms.dsr.blocked.v1",
	"dms.dsr.completed.v1",
	"dms.dsr.failed.v1",
	"dms.dsr.requested.v1",
	// ediscovery
	"dms.ediscovery.exported.v1",
	// email
	"dms.email.ingested.v1",
	// entity
	"dms.entity.corrected.v1",
	// export
	"dms.export.completed.v1",
	// folder
	"dms.folder.grant_added.v1",
	"dms.folder.grant_removed.v1",
	"dms.folder.visibility_changed.v1",
	// idp
	"dms.idp.configured.v1",
	// ingestion
	"dms.ingestion.routed.v1",
	// m365
	"dms.m365.outlook.email.saved.v1",
	// ner_config
	"dms.ner_config.api_key_cleared.v1",
	"dms.ner_config.api_key_set.v1",
	// notify
	"dms.notify.comment.mention.v1",
	"dms.notify.saved_search_match.v1",
	// ocr
	"dms.ocr.quality.reviewed.v1",
	// permission
	"dms.permission.changed.v1",
	"dms.permission.granted.v1",
	"dms.permission.revoked.v1",
	// policy
	"dms.policy.denied.v1",
	// record
	"dms.record.declared.v1",
	"dms.record.disposed.v1",
	"dms.record.frozen.v1",
	"dms.record.metadata_set.v1",
	"dms.record.transfer_exported.v1",
	"dms.record.unfrozen.v1",
	"dms.record.vital_set.v1",
	// retention
	"dms.retention.archived.v1",
	"dms.retention.dispose_candidate.v1",
	"dms.retention.held.v1",
	// review
	"dms.review.approved.v1",
	"dms.review.created.v1",
	"dms.review.rejected.v1",
	"dms.review.resolved.v1",
	// routing
	"dms.routing.accepted.v1",
	"dms.routing.dismissed.v1",
	// siem
	"dms.siem.test.v1",
	// signature
	"dms.signature.declined.v1",
	// storage
	"dms.storage.upload_completed.v1",
	"dms.storage.upload_deduplicated.v1",
	"dms.storage.upload_initiated.v1",
	"dms.storage.upload_quarantined.v1",
	// tenant
	"dms.tenant.key_rotated.v1",
	// user
	"dms.user.created.v1",
	"dms.user.deprovisioned.v1",
	"dms.user.invite_accepted.v1",
	"dms.user.reactivated.v1",
	"dms.user.role_changed.v1",
	// version
	"dms.version.classified.v1",
	"dms.version.entities_detected.v1",
	// webhook
	"dms.webhook.test.v1",
	// workflow
	"dms.workflow.completed.v1",
	"dms.workflow.lifecycle_held.v1", // decision not applied: document under legal hold (DEF-02)
	"dms.workflow.recalled.v1",
	"dms.workflow.step_transition.v1",
	// workspace
	"dms.workspace.owner_transferred.v1",

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
