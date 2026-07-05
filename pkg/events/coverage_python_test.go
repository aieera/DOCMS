package events

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The Python services (intelligence, preview) write rows into the SHARED
// sedoc.outbox; the Go OutboxPublisher publishes each row's event_type as
// a NATS subject and halts the whole drain on the first failure
// (pkg/database/outbox_publisher.go drainBatch — deliberate, to preserve
// per-aggregate ordering). A subject no stream binds therefore does not
// just lose one event: it wedges every event queued behind it, forever,
// because the same row fails again on every tick.
//
// The Go-side gate (TestSubjectCoverage_AllPublishedSubjectsHaveStreams)
// never saw those subjects because PublishedSubjects was harvested from
// Go emitters only — which is exactly how dms.notification.send.v1,
// dms.translation.completed.v1, and dms.training_example.collected.v1
// shipped unbound (STATE_OF_THE_PROJECT 2026-07-03, section C). These
// tests close that blind spot mechanically: every dms.*.vN literal in the
// Python services must be (a) covered by a DefaultStreams filter and
// (b) present in PublishedSubjects so the runtime boot guard
// (AssertLiveCoverage, wired in ConnectNATS) defends it in prod too.

var pySubjectRe = regexp.MustCompile(`["'](dms\.[a-z0-9_]+(?:\.[a-z0-9_]+)*\.v[0-9]+)["']`)

// pythonEmitterDirs are the services whose code can write the shared
// outbox (or publish to JetStream) from Python. Extend when a new
// Python service grows an event emitter.
var pythonEmitterDirs = []string{
	filepath.Join("services", "intelligence"),
	filepath.Join("services", "preview"),
}

var pySkipDirs = map[string]bool{
	".git": true, ".venv": true, "venv": true, "__pycache__": true,
	".pytest_cache": true, "node_modules": true,
}

func harvestPythonSubjects(t *testing.T) []string {
	t.Helper()
	root := findRepoRootFromWD(t)
	seen := map[string]bool{}
	for _, dir := range pythonEmitterDirs {
		base := filepath.Join(root, dir)
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if pySkipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(d.Name()) != ".py" {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range pySubjectRe.FindAllStringSubmatch(string(data), -1) {
				seen[m[1]] = true
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", base, err)
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// findRepoRootFromWD walks up from the test's working directory to the
// go.work file that anchors the monorepo (same convention as
// pkg/archtest).
func findRepoRootFromWD(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not find go.work walking up from test working directory")
	return ""
}

// TestSubjectCoverage_PythonSubjectsHaveStreams is the wedge-preventer:
// any Python-referenced subject with no stream binding fails the build,
// because an outbox row carrying it would halt the shared drain in prod.
func TestSubjectCoverage_PythonSubjectsHaveStreams(t *testing.T) {
	subjects := harvestPythonSubjects(t)
	// Sanity floor: the walker finding almost nothing means the harvest
	// itself broke (moved dirs, regex rot) — fail loudly instead of
	// vacuously passing.
	if len(subjects) < 20 {
		t.Fatalf("python subject harvest found only %d subjects (%v) — harvest is likely broken", len(subjects), subjects)
	}
	if missing := CheckCoverage(subjects, DefaultStreams); len(missing) > 0 {
		t.Fatalf(
			"python services reference subjects not covered by any stream in "+
				"DefaultStreams — an outbox row with one of these event_types "+
				"WEDGES the shared outbox drain in prod (drainBatch halts on "+
				"first failure). Bind each in pkg/events/publisher.go:\n  %s",
			strings.Join(missing, "\n  "),
		)
	}
}

// TestSubjectCoverage_PythonSubjectsAreInPublishedSubjects keeps the
// canonical list honest so the RUNTIME guard sees Python subjects: at
// boot, ConnectNATS runs AssertLiveCoverage(js, PublishedSubjects, …)
// against the streams that actually exist. A Python subject absent from
// PublishedSubjects gets no runtime defense — a shrunk or hand-edited
// prod stream would go unnoticed until the drain wedged.
func TestSubjectCoverage_PythonSubjectsAreInPublishedSubjects(t *testing.T) {
	listed := map[string]bool{}
	for _, s := range PublishedSubjects {
		listed[s] = true
	}
	var missing []string
	for _, s := range harvestPythonSubjects(t) {
		if !listed[s] {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 {
		t.Fatalf(
			"python-referenced subjects missing from PublishedSubjects "+
				"(add them so AssertLiveCoverage defends them at boot):\n  %s",
			strings.Join(missing, "\n  "),
		)
	}
}
