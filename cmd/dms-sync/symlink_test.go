package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// symlinkFixture: a fake server, a mirror root, and a directory outside the
// mirror holding a file the agent must never touch.
func symlinkFixture(t *testing.T) (*agent, *fakeServer, string) {
	t.Helper()
	f := &fakeServer{content: "pwned", deltas: [][]map[string]any{{}}}
	a, done := newFakeAgent(t, f)
	t.Cleanup(done)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "victim.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	return a, f, outside
}

func assertVictimIntact(t *testing.T, outside string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(outside, "victim.txt"))
	if err != nil || string(b) != "keep" {
		t.Fatalf("file outside the mirror was modified or removed: %q, %v", b, err)
	}
	entries, _ := os.ReadDir(outside)
	for _, e := range entries {
		if e.Name() != "victim.txt" {
			t.Fatalf("agent created %q outside the mirror", e.Name())
		}
	}
}

// H-14: a directory symlink already inside the mirror (docs -> ~/.ssh) must
// not let a server-chosen path write, read or delete through it.
func TestApply_RefusesSymlinkedDirectory(t *testing.T) {
	a, f, outside := symlinkFixture(t)
	if err := os.Symlink(outside, filepath.Join(a.root, "docs")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	ctx := context.Background()
	for _, act := range []Action{
		{Path: "docs/new.txt", Type: ActDownload, DocID: "d1"},
		{Path: "docs/sub/new.txt", Type: ActDownload, DocID: "d1"},
		{Path: "docs/victim.txt", Type: ActDownload, DocID: "d1"},
		{Path: "docs/victim.txt", Type: ActDeleteLocal, DocID: "d1"},
		{Path: "docs/victim.txt", Type: ActUpload, DocID: "d1"},
		{Path: "docs/victim.txt", Type: ActConflict, DocID: "d1"},
	} {
		if err := a.apply(ctx, act); err == nil {
			t.Errorf("apply(%s %q) through a symlinked directory succeeded; want refusal", act.Type, act.Path)
		}
	}
	assertVictimIntact(t, outside)
	if f.initiates != 0 {
		t.Fatalf("a file behind a symlinked directory was uploaded (%d initiates)", f.initiates)
	}
}

// H-14: a symlinked file inside the mirror must not be written through,
// removed, or read and uploaded.
func TestApply_RefusesSymlinkedFile(t *testing.T) {
	a, f, outside := symlinkFixture(t)
	if err := os.Symlink(filepath.Join(outside, "victim.txt"), filepath.Join(a.root, "a.txt")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	ctx := context.Background()
	for _, act := range []Action{
		{Path: "a.txt", Type: ActDownload, DocID: "d1"},
		{Path: "a.txt", Type: ActDeleteLocal, DocID: "d1"},
		{Path: "a.txt", Type: ActUpload, DocID: "d1"},
		{Path: "a.txt", Type: ActConflict, DocID: "d1"},
	} {
		if err := a.apply(ctx, act); err == nil {
			t.Errorf("apply(%s %q) on a symlinked file succeeded; want refusal", act.Type, act.Path)
		}
	}
	assertVictimIntact(t, outside)
	if fi, err := os.Lstat(filepath.Join(a.root, "a.txt")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlink itself should be left alone: %v", err)
	}
	if f.initiates != 0 {
		t.Fatalf("a symlink target was uploaded (%d initiates)", f.initiates)
	}
}
