package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// State is the agent's persisted baseline: the delta cursor, the last-synced
// (doc, version, sha) per relative path, and the folder-id -> path map used to
// place documents in the local mirror. Persisted as JSON in the mirror root so a
// restart resumes where it left off (and offline edits, which live on disk, are
// re-detected). The filesystem itself is the offline queue.
type State struct {
	Cursor string           `json:"cursor"`
	Files  map[string]Known `json:"files"` // relpath -> baseline
	// Folder tree (name + parent) so a document's relative dir can be rebuilt
	// by walking parents; documents in the delta carry only their folder id.
	FolderName   map[string]string `json:"folder_name"`
	FolderParent map[string]string `json:"folder_parent"`
	// DocPath maps a document id back to its relpath so tombstones (which carry
	// only the doc id) can find the local file to remove.
	DocPath map[string]string `json:"doc_path"`
}

func newState() *State {
	return &State{
		Files: map[string]Known{}, FolderName: map[string]string{},
		FolderParent: map[string]string{}, DocPath: map[string]string{},
	}
}

// relDir walks folder parents to build the relative directory for a folder id.
func (s *State) relDir(folderID string) string {
	var parts []string
	seen := map[string]bool{}
	for folderID != "" && !seen[folderID] {
		seen[folderID] = true
		name := s.FolderName[folderID]
		if name == "" {
			break
		}
		parts = append([]string{sanitize(name)}, parts...)
		folderID = s.FolderParent[folderID]
	}
	return strings.Join(parts, "/")
}

// sanitize keeps a name safe as a single path segment: separators and NUL
// become "_", trailing dots and spaces are stripped (Windows drops them, so
// "..." or ". ." would still address a parent directory), and a name left
// empty or made only of dots ("." / "..") becomes "_".
func sanitize(name string) string {
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "\x00", "_")
	name = strings.TrimRight(strings.TrimSpace(name), ". ")
	if strings.Trim(name, ".") == "" {
		return "_"
	}
	return name
}

// stateFileName is the agent's own state file in the mirror root.
const stateFileName = ".dms-sync-state.json"

// reservedName reports whether a first path segment addresses the state
// file (or its .tmp sibling): case-insensitive, ignoring the trailing dots
// and spaces Windows strips.
func reservedName(first string) bool {
	first = strings.TrimRight(first, ". ")
	return len(first) >= len(stateFileName) && strings.EqualFold(first[:len(stateFileName)], stateFileName)
}

func statePath(root string) string { return filepath.Join(root, stateFileName) }

func loadState(root string) (*State, error) {
	b, err := os.ReadFile(statePath(root))
	if os.IsNotExist(err) {
		return newState(), nil
	}
	if err != nil {
		return nil, err
	}
	s := newState()
	if err := json.Unmarshal(b, s); err != nil {
		return nil, err
	}
	if s.Files == nil {
		s.Files = map[string]Known{}
	}
	if s.FolderName == nil {
		s.FolderName = map[string]string{}
	}
	if s.FolderParent == nil {
		s.FolderParent = map[string]string{}
	}
	if s.DocPath == nil {
		s.DocPath = map[string]string{}
	}
	return s, nil
}

func (s *State) save(root string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := statePath(root) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, statePath(root)) // atomic
}

// scanLocal walks the mirror root and returns relpath -> Local (sha256), skipping
// the state file and conflicted-copy markers' own re-detection is harmless.
func scanLocal(root string) (map[string]Local, error) {
	out := map[string]Local{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Never hash through a symlink (its target may be outside the
		// mirror); a linked directory is not descended into by Walk either.
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, ".dms-sync-state.json") {
			return nil
		}
		sum, err := fileSHA256(p)
		if err != nil {
			return err
		}
		out[rel] = Local{SHA256: sum, Exists: true}
		return nil
	})
	return out, err
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// conflictedName inserts a "(conflicted copy)" marker before the extension.
func conflictedName(rel, deviceName string) string {
	dir := filepath.Dir(rel)
	base := filepath.Base(rel)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	name := stem + " (conflicted copy - " + deviceName + ")" + ext
	if dir == "." {
		return name
	}
	return filepath.ToSlash(filepath.Join(dir, name))
}

// errUnsafePath is returned when a relative path would resolve outside the
// mirror root (or onto the agent's own state file). Names in the delta are
// server-controlled, so every filesystem operation goes through safePath.
var errUnsafePath = errors.New("path escapes the mirror root")

// safePath resolves a mirror-relative path to an absolute one, refusing
// absolute paths, any ".." segment that climbs out of root, and the agent's
// state file (and its temp sibling).
func (a *agent) safePath(rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || filepath.IsAbs(filepath.FromSlash(rel)) || filepath.VolumeName(rel) != "" {
		return "", fmt.Errorf("%w: %q", errUnsafePath, rel)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", errUnsafePath, rel)
	}
	if reservedName(strings.SplitN(filepath.ToSlash(clean), "/", 2)[0]) {
		return "", fmt.Errorf("%w: %q is reserved", errUnsafePath, rel)
	}
	root := filepath.Clean(a.root)
	abs := filepath.Join(root, clean)
	back, err := filepath.Rel(root, abs)
	if err != nil || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", errUnsafePath, rel)
	}
	return abs, nil
}

// errSymlink is returned when a mirror path, or any directory on the way to
// it, is a symbolic link. The agent never follows links inside the mirror: a
// pre-existing link (docs -> ~/.ssh, or a linked file) would otherwise let a
// server-chosen path write, read or delete outside the root (H-14).
var errSymlink = errors.New("refusing to follow a symlink in the mirror")

// guardPath checks abs (already lexically confined by safePath) against the
// filesystem before any read, write or remove:
//   - every existing directory between the root and abs must be a real
//     directory, not a symlink; with create, missing ones are made one level
//     at a time (so MkdirAll never follows a link);
//   - the resolved parent directory must lie inside the resolved root;
//   - abs itself, if it exists, must not be a symlink.
//
// Without create, a missing directory on the way returns an error wrapping
// fs.ErrNotExist (the target cannot exist either).
func (a *agent) guardPath(abs string, create bool) error {
	root := filepath.Clean(a.root)
	relDir, err := filepath.Rel(root, filepath.Dir(abs))
	if err != nil {
		return fmt.Errorf("%w: %q", errUnsafePath, abs)
	}
	cur := root
	if relDir != "." {
		for _, part := range strings.Split(relDir, string(filepath.Separator)) {
			cur = filepath.Join(cur, part)
			fi, lstatErr := os.Lstat(cur)
			switch {
			case errors.Is(lstatErr, fs.ErrNotExist) && create:
				// #nosec G301 -- directories inside the user's own sync root mirror the remote folder tree; 0755 matches a normal user-created folder.
				if mkErr := os.Mkdir(cur, 0o755); mkErr != nil && !errors.Is(mkErr, fs.ErrExist) {
					return mkErr
				}
				if fi, lstatErr = os.Lstat(cur); lstatErr != nil {
					return lstatErr
				}
			case lstatErr != nil:
				return lstatErr
			}
			if fi.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%w: %q", errSymlink, cur)
			}
			if !fi.IsDir() {
				return fmt.Errorf("%q is not a directory", cur)
			}
		}
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	realDir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return err
	}
	if back, relErr := filepath.Rel(realRoot, realDir); relErr != nil || back == ".." ||
		strings.HasPrefix(back, ".."+string(filepath.Separator)) || filepath.IsAbs(back) {
		return fmt.Errorf("%w: %q resolves to %q", errUnsafePath, abs, realDir)
	}
	fi, err := os.Lstat(abs)
	if err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %q", errSymlink, abs)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
