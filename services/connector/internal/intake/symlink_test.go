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
