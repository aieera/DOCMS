package intake

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// IntakeRootEnv names the directory every watched folder must live under.
// Each tenant is confined to <root>/<tenant_id>; nothing outside it is ever
// watched, read, renamed or ingested (H-4).
const IntakeRootEnv = "SEDOC_INTAKE_ROOT"

// DefaultIntakeRoot is the mount docker-compose gives the connector
// (ADR 0088: intake_data:/data/intake).
const DefaultIntakeRoot = "/data/intake"

// ErrHostPathNotConfined rejects a host_path (or subdir) that is not inside
// the tenant's intake root.
var ErrHostPathNotConfined = errors.New("host_path must be inside the tenant's intake root")

// intakeRootFromEnv returns the configured intake root, cleaned.
func intakeRootFromEnv() string {
	r := strings.TrimSpace(os.Getenv(IntakeRootEnv))
	if r == "" {
		r = DefaultIntakeRoot
	}
	return filepath.Clean(r)
}

// tenantRoot returns <root>/<tenant_id> for a canonical tenant UUID.
func (s *Service) tenantRoot(tenantID string) (string, error) {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return "", fmt.Errorf("tenant_id: %w", err)
	}
	return filepath.Join(s.root, tid.String()), nil
}

// confinedHostPath validates that hostPath is absolute and, both lexically
// and after resolving symlinks in its existing prefix, inside the tenant's
// root. Returns the cleaned path to persist.
func (s *Service) confinedHostPath(tenantID, hostPath string) (string, error) {
	if !filepath.IsAbs(hostPath) {
		return "", errors.New("host_path must be absolute")
	}
	tr, err := s.tenantRoot(tenantID)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(hostPath)
	if !within(tr, clean) {
		return "", fmt.Errorf("%w (%s)", ErrHostPathNotConfined, tr)
	}
	resolvedRoot, err := resolveExisting(tr)
	if err != nil {
		return "", err
	}
	resolved, err := resolveExisting(clean)
	if err != nil {
		return "", err
	}
	if !within(resolvedRoot, resolved) {
		return "", fmt.Errorf("%w (%s, via symlink)", ErrHostPathNotConfined, tr)
	}
	return clean, nil
}

// validSubdir accepts a single path segment for the processed/quarantine
// subdirs, so filepath.Join(host_path, subdir) can never leave host_path.
func validSubdir(name string) error {
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) || filepath.Clean(name) != name {
		return fmt.Errorf("%w: subdir %q must be a single directory name", ErrHostPathNotConfined, name)
	}
	return nil
}

// watchable re-checks a stored folder before it is watched or swept, so a
// row created before confinement (e.g. host_path "/") is never touched.
func (s *Service) watchable(f *Folder) error {
	if _, err := s.confinedHostPath(f.TenantID, f.HostPath); err != nil {
		if errors.Is(err, ErrHostPathNotConfined) {
			return err
		}
		return fmt.Errorf("%w: %v", ErrHostPathNotConfined, err)
	}
	for _, sub := range []string{f.ProcessedSubdir, f.QuarantineSubdir} {
		if err := validSubdir(sub); err != nil {
			return err
		}
	}
	return nil
}

// within reports whether p is base or inside it (both cleaned, absolute).
func within(base, p string) bool {
	rel, err := filepath.Rel(base, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// resolveExisting resolves symlinks in the longest existing prefix of p and
// re-appends the not-yet-existing remainder.
func resolveExisting(p string) (string, error) {
	cur := p
	var rest []string
	for {
		if _, err := os.Lstat(cur); err == nil {
			r, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", err
			}
			for i := len(rest) - 1; i >= 0; i-- {
				r = filepath.Join(r, rest[i])
			}
			return r, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p, nil
		}
		rest = append(rest, filepath.Base(cur))
		cur = parent
	}
}

// ErrHostPathOverlap rejects a host_path equal to, inside, or containing
// another watched folder of the tenant (N-6).
var ErrHostPathOverlap = errors.New("host_path overlaps an existing intake folder")
