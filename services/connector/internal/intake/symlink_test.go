package intake

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/google/uuid"
)

// N-6: intake must only ever read regular files that really live under the
// folder. Every case below is rejected before the claim / DB step (pool is
// nil, so reaching it would panic) and leaves the entry untouched.
func TestIntake_SymlinkSkipped(t *testing.T) {
	s, root, tenant := newConfinedSvc(t)
	s.settleDelay = 0
	inbox := filepath.Join(root, tenant, "scans")
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("outside the intake root"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideDir := filepath.Dir(secret)

	fileLink := filepath.Join(inbox, "invoice.pdf")
	if err := os.Symlink(secret, fileLink); err != nil {
		t.Fatal(err)
	}
	dirLink := filepath.Join(inbox, "linked")
	if err := os.Symlink(outsideDir, dirLink); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(inbox, "pipe.pdf")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	f := Folder{ID: uuid.NewString(), TenantID: tenant, HostPath: inbox, Recurse: true,
		ProcessedSubdir: "processed", QuarantineSubdir: "quarantine"}

	for _, p := range []string{fileLink, filepath.Join(dirLink, "secret.txt"), fifo} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("handleFile(%s) went past the regular-file check (panic: %v)", p, r)
				}
			}()
			s.handleFile(context.Background(), f, p)
		}()
	}
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("sweep followed a link (panic: %v)", r)
			}
		}()
		s.sweep(context.Background(), f)
	}()

	if fi, err := os.Lstat(fileLink); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("symlink was claimed/moved: %v", err)
	}
	if _, err := os.Stat(secret); err != nil {
		t.Errorf("link target touched: %v", err)
	}
	if _, err := os.Lstat(fifo); err != nil {
		t.Errorf("fifo was claimed/moved: %v", err)
	}
}

// H-4: confinement must hold for the whole life of a watcher, not just at
// start. Someone with write access to <root>/<tenant> swaps the watched
// directory for a symlink to a directory outside the root (e.g.
// /run/secrets); neither the periodic sweep nor an fsnotify-reported path
// may read, rename or ingest anything there. (pool is nil: reaching the DB
// would panic.)
func TestIntake_SymlinkSwapAfterStartSkipped(t *testing.T) {
	s, root, tenant := newConfinedSvc(t)
	s.settleDelay = 0
	inbox := filepath.Join(root, tenant, "scans")
	f := Folder{ID: uuid.NewString(), TenantID: tenant, HostPath: inbox,
		ProcessedSubdir: "processed", QuarantineSubdir: "quarantine"}
	if err := s.watchable(f); err != nil {
		t.Fatalf("folder should be watchable at start: %v", err)
	}

	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.pdf")
	if err := os.WriteFile(secret, []byte("outside the intake root"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(inbox); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, inbox); err != nil {
		t.Fatal(err)
	}

	run := func(name string, fn func()) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("%s went past confinement after the swap (panic: %v)", name, r)
			}
		}()
		fn()
	}
	run("sweep", func() { s.sweep(context.Background(), f) })
	run("handleFile", func() { s.handleFile(context.Background(), f, filepath.Join(inbox, "secret.pdf")) })

	if _, err := os.Stat(secret); err != nil {
		t.Fatalf("file outside the root was moved/claimed: %v", err)
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 1 {
		t.Fatalf("outside dir modified: %v", entries)
	}
}
