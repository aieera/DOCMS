package intake

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

func newConfinedSvc(t *testing.T) (*Service, string, string) {
	t.Helper()
	root := t.TempDir()
	tenant := uuid.NewString()
	if err := os.MkdirAll(filepath.Join(root, tenant, "scans"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := &Service{root: root, log: zerolog.Nop(), stopFns: map[string]context.CancelFunc{}}
	return s, root, tenant
}

func TestIntakeCreate_PathOutsideRootRejected(t *testing.T) {
	s, root, tenant := newConfinedSvc(t)
	other := uuid.NewString()
	if err := os.MkdirAll(filepath.Join(root, other), 0o755); err != nil {
		t.Fatal(err)
	}
	// A symlink inside the tenant's root that escapes it.
	escape := filepath.Join(root, tenant, "escape")
	if err := os.Symlink("/etc", escape); err != nil {
		t.Fatal(err)
	}
	bad := []string{
		"/",
		"/etc",
		root,                                     // the shared root itself
		filepath.Join(root, other),               // another tenant's root
		filepath.Join(root, other, "inbox"),      // under another tenant
		filepath.Join(root, tenant, "..", other), // traversal
		root + "/" + tenant + "x",                // sibling sharing the prefix
		escape,                                   // symlink out of the root
		filepath.Join(escape, "passwd"),
		"relative/path",
	}
	for _, p := range bad {
		// pool is nil: a path that passed validation would panic on the DB
		// write, so a clean error proves rejection happens first.
		_, err := s.CreateFolder(context.Background(), tenant, uuid.NewString(), CreateInput{Label: "x", HostPath: p})
		if err == nil {
			t.Errorf("CreateFolder accepted host_path %q outside %s/%s", p, root, tenant)
		}
	}

	good := []string{
		filepath.Join(root, tenant),
		filepath.Join(root, tenant, "scans"),
		filepath.Join(root, tenant, "not-yet-created", "deep"),
	}
	for _, p := range good {
		if _, err := s.confinedHostPath(tenant, p); err != nil {
			t.Errorf("confinedHostPath(%q) rejected a path inside the tenant root: %v", p, err)
		}
	}
}

func TestIntakeCreate_SubdirTraversalRejected(t *testing.T) {
	s, root, tenant := newConfinedSvc(t)
	for _, sub := range []string{"..", "../x", "a/b", "/abs", "."} {
		_, err := s.CreateFolder(context.Background(), tenant, uuid.NewString(), CreateInput{
			Label: "x", HostPath: filepath.Join(root, tenant, "scans"), ProcessedSubdir: sub,
		})
		if err == nil {
			t.Errorf("CreateFolder accepted processed_subdir %q", sub)
		}
		_, err = s.CreateFolder(context.Background(), tenant, uuid.NewString(), CreateInput{
			Label: "x", HostPath: filepath.Join(root, tenant, "scans"), QuarantineSubdir: sub,
		})
		if err == nil {
			t.Errorf("CreateFolder accepted quarantine_subdir %q", sub)
		}
		bad := sub
		if _, err := s.PatchFolder(context.Background(), tenant, uuid.NewString(), PatchInput{ProcessedSubdir: &bad}); err == nil {
			t.Errorf("PatchFolder accepted processed_subdir %q", sub)
		}
	}
}

// Rows created before confinement (e.g. host_path "/") must not be watched
// or swept.
func TestIntakeWatch_RefusesUnconfinedExistingRow(t *testing.T) {
	s, root, tenant := newConfinedSvc(t)
	for _, f := range []Folder{
		{ID: uuid.NewString(), TenantID: tenant, HostPath: "/", ProcessedSubdir: "processed", QuarantineSubdir: "quarantine"},
		{ID: uuid.NewString(), TenantID: tenant, HostPath: filepath.Join(root, tenant), ProcessedSubdir: "../../x", QuarantineSubdir: "quarantine"},
	} {
		if err := s.watchable(&f); !errors.Is(err, ErrHostPathNotConfined) {
			t.Errorf("watchable(%+v) = %v, want ErrHostPathNotConfined", f, err)
		}
	}
	ok := Folder{ID: uuid.NewString(), TenantID: tenant, HostPath: filepath.Join(root, tenant, "scans"), ProcessedSubdir: "processed", QuarantineSubdir: "quarantine"}
	if err := s.watchable(&ok); err != nil {
		t.Errorf("watchable(confined) = %v", err)
	}
}
