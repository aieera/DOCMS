package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
// become "_", and names that are empty or made only of dots ("." / "..",
// which would address the current or parent directory) become "_".
func sanitize(name string) string {
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "\x00", "_")
	name = strings.TrimSpace(name)
	if strings.Trim(name, ".") == "" {
		return "_"
	}
	return name
}

func statePath(root string) string { return filepath.Join(root, ".dms-sync-state.json") }

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
	if strings.HasPrefix(filepath.ToSlash(clean), ".dms-sync-state.json") {
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
