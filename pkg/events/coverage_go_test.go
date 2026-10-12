package events

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// goSubjectRe matches a dms.<domain>.<…>.v<N> subject written as a Go
// string literal (interpreted or raw). Format strings ("dms.notify.%s.v1")
// and wildcard filters ("dms.audit.>") deliberately do not match.
var goSubjectRe = regexp.MustCompile("[\"`](dms\\.[a-z0-9_]+(?:\\.[a-z0-9_]+)*\\.v[0-9]+)[\"`]")

var goSkipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "testdata": true,
}

// harvestGoSubjects walks every non-test .go file under services/ and
// returns the sorted, de-duplicated subject literals. Outbox event types
// ARE NATS subjects (the OutboxPublisher publishes event_type verbatim),
// so every literal here can reach the broker.
func harvestGoSubjects(t *testing.T) map[string][]string {
	t.Helper()
	root := findRepoRootFromWD(t)
	base := filepath.Join(root, "services")
	found := map[string][]string{} // subject -> files (for the failure message)
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if goSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, m := range goSubjectRe.FindAllStringSubmatch(string(data), -1) {
			found[m[1]] = append(found[m[1]], rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", base, err)
	}
	return found
}

// TestGoEmittedSubjectsCovered (CB-8) replaces trust in the hand-kept
// PublishedSubjects list with a mechanical harvest of the Go services,
// mirroring the Python gate in coverage_python_test.go. Every subject
// literal in services/**/*.go must be (a) listed in PublishedSubjects, so
// the boot-time AssertLiveCoverage guard defends it, and (b) bound by a
// DefaultStreams filter — an unbound subject is a silently dropped event
// at best and, through the shared outbox drain, a wedged publisher at
// worst.
func TestGoEmittedSubjectsCovered(t *testing.T) {
	found := harvestGoSubjects(t)
	// Sanity floor: a near-empty harvest means the walker or regex broke;
	// fail loudly instead of passing vacuously.
	if len(found) < 100 {
		t.Fatalf("go subject harvest found only %d subjects — harvest is likely broken", len(found))
	}
	subjects := make([]string, 0, len(found))
	for s := range found {
		subjects = append(subjects, s)
	}
	sort.Strings(subjects)

	listed := map[string]bool{}
	for _, s := range PublishedSubjects {
		listed[s] = true
	}
	var unlisted []string
	for _, s := range subjects {
		if !listed[s] {
			unlisted = append(unlisted, s+"  ("+found[s][0]+")")
		}
	}
	if len(unlisted) > 0 {
		t.Errorf("%d Go-emitted subjects missing from PublishedSubjects "+
			"(add them in pkg/events/coverage.go so AssertLiveCoverage defends them at boot):\n  %s",
			len(unlisted), strings.Join(unlisted, "\n  "))
	}
	if missing := CheckCoverage(subjects, DefaultStreams); len(missing) > 0 {
		for i, s := range missing {
			missing[i] = s + "  (" + found[s][0] + ")"
		}
		t.Errorf("%d Go-emitted subjects not covered by any stream in DefaultStreams "+
			"(bind each in pkg/events/publisher.go):\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}
