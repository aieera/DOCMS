package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// H-14: names in the delta are server-controlled. A folder or document named
// ".." must never make the agent write or remove a file outside the mirror.
func TestApply_RefusesPathOutsideRoot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("pwned"))
	}))
	defer srv.Close()

	parent := t.TempDir()
	root := filepath.Join(parent, "mirror")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(parent, "victim.txt")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &agent{client: newClient(srv.URL, "k"), root: root, device: "dev", state: newState()}

	// A folder named ".." and a document named ".." must not climb out.
	a.state.FolderName["f1"] = ".."
	a.state.FolderParent["f1"] = ""
	rel := a.docRel(deltaChange{ParentID: "f1", Name: "victim.txt"})
	if abs, err := a.safePath(rel); err != nil || !within(root, abs) {
		t.Fatalf("docRel(..) = %q resolved outside root (abs=%q err=%v)", rel, abs, err)
	}
	if got := sanitize(".."); got == ".." || got == "." {
		t.Fatalf("sanitize(..) = %q", got)
	}
	if got := sanitize("."); got == ".." || got == "." {
		t.Fatalf("sanitize(.) = %q", got)
	}

	ctx := context.Background()
	for _, act := range []Action{
		{Path: "../victim.txt", Type: ActDownload, DocID: "d1"},
		{Path: "../victim.txt", Type: ActDeleteLocal, DocID: "d1"},
		{Path: "../victim.txt", Type: ActConflict, DocID: "d1"},
		{Path: "a/../../victim.txt", Type: ActDownload, DocID: "d1"},
		{Path: victim, Type: ActDownload, DocID: "d1"},
		{Path: ".dms-sync-state.json", Type: ActDownload, DocID: "d1"},
	} {
		if err := a.apply(ctx, act); err == nil {
			t.Errorf("apply(%s %q) succeeded; want refusal", act.Type, act.Path)
		}
	}
	b, err := os.ReadFile(victim)
	if err != nil || string(b) != "keep" {
		t.Fatalf("victim outside the mirror was modified: %q, %v", b, err)
	}
	if _, err := os.Stat(statePath(root)); err == nil {
		t.Fatal("a remote document overwrote the agent's state file")
	}
}

func within(root, abs string) bool {
	rel, err := filepath.Rel(root, abs)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && (len(rel) < 3 || rel[:3] != "../")
}
