package eventpolicy

import "testing"

func TestSubscribable_AllowList(t *testing.T) {
	allowed := []string{
		"dms.document.created.v1",
		"dms.version.uploaded.v1",
		"dms.folder.created.v1",
		"dms.workflow.completed.v1",
		"dms.signature.completed.v1",
		"dms.task.assigned.v1",
		"dms.tag.added.v1",
		"dms.comment.created.v1",
		"dms.record.declared.v1",
	}
	for _, s := range allowed {
		if !Subscribable(s) {
			t.Errorf("Subscribable(%q) = false, want true", s)
		}
	}
	denied := []string{
		"dms.auth.password_reset_requested.v1",
		"dms.auth.login_success.v1",
		"dms.session.created.v1",
		"dms.apikey.rotated.v1",
		"dms.user.invited.v1",
		"dms.audit.event.v1",
		"dms.policy.changed.v1",
		"dms.billing.invoice.v1",
		"dms.webhook.test.v1",
		"dms.documents.created.v1", // prefix must end at the domain dot
		"dms.document",             // bare domain, no action
		"document.created.v1",
		"",
		"DMS.DOCUMENT.CREATED.V1",
	}
	for _, s := range denied {
		if Subscribable(s) {
			t.Errorf("Subscribable(%q) = true, want false", s)
		}
	}
}

func TestDeliverable_AllowsOnlyTestEventBeyondSubscribable(t *testing.T) {
	if !Deliverable(TestEventType) {
		t.Fatalf("Deliverable(%q) = false, want true", TestEventType)
	}
	if !Deliverable("dms.document.created.v1") {
		t.Fatal("document events must be deliverable")
	}
	if Deliverable("dms.auth.password_reset_requested.v1") {
		t.Fatal("auth events must never be deliverable")
	}
}

func TestFamilies_MatchAllowList(t *testing.T) {
	if len(Families()) != len(prefixes) {
		t.Fatalf("Families() has %d entries, want %d", len(Families()), len(prefixes))
	}
	for _, f := range Families() {
		// "dms.document.>" → a concrete subject under it must be subscribable.
		if !Subscribable(f[:len(f)-1] + "x.v1") {
			t.Errorf("family %q is not covered by Subscribable", f)
		}
	}
}
