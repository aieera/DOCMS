package events

import "testing"

// TestDefaultStreamsCoverEveryKnownSubject is the canary for silent
// drops. If a new event emits under a prefix no stream covers, the
// broker accepts the publish and routes to nothing — the exact bug
// Wave 5 Prompt 5.2 was landed to kill. Add the subject to the right
// StreamSpec rather than bypassing this test.
func TestDefaultStreamsCoverEveryKnownSubject(t *testing.T) {
	knownPrefixes := []string{
		"dms.document.",
		"dms.version.",
		"dms.workspace.",
		"dms.user.",
		"dms.session.",
		"dms.apikey.",
		"dms.policy.",
		"dms.permission.",
		"dms.billing.",
		"dms.subscription.",
		"dms.usage.",
		"dms.audit.",
		"dms.search.",
		"dms.workflow.",
		"dms.task.",
		"dms.ocr.",
		"dms.classify.",
		"dms.embed.",
		"dms.ner.",
		"dms.notify.",
		// Legacy — retained in LEGACY_EVENTS. Remove when emitters are migrated.
		"dms.sharelink.",
		"dms.folder.",
		"dms.intelligence.",
		"dms.rotation.",
	}
	for _, p := range knownPrefixes {
		if !streamExistsFor(p) {
			t.Errorf("no stream binds %s — silent-drop hazard", p)
		}
	}
}

// TestNoDuplicateStreamNames catches copy-paste bugs that shadow a
// primary stream with itself or with a legacy name.
func TestNoDuplicateStreamNames(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range DefaultStreams {
		if seen[s.Name] {
			t.Errorf("duplicate stream name: %s", s.Name)
		}
		seen[s.Name] = true
	}
}

// TestNoSubjectOverlap catches accidental overlaps between primary
// streams. NATS JetStream forbids this — AddStream errors — but
// catching it in a unit test gives a readable failure earlier.
func TestNoSubjectOverlap(t *testing.T) {
	owner := map[string]string{}
	for _, s := range DefaultStreams {
		for _, subj := range s.Subjects {
			if prev, ok := owner[subj]; ok {
				t.Errorf("subject %q bound by both %s and %s", subj, prev, s.Name)
			}
			owner[subj] = s.Name
		}
	}
}

func streamExistsFor(prefix string) bool {
	for _, s := range DefaultStreams {
		for _, subj := range s.Subjects {
			// StreamSpec subjects look like "dms.user.>"; strip the
			// trailing ".>" and check the prefix match.
			bare := subj
			if n := len(bare); n >= 2 && bare[n-2:] == ".>" {
				bare = bare[:n-1]
			}
			if prefix == bare {
				return true
			}
		}
	}
	return false
}
