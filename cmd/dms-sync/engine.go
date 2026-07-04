// Package main — dms-sync is a headless reference sync agent for the SeDoc
// virtual-drive / selective-sync client (§3/§5). It mirrors a workspace into a
// local folder using the cursor-based sync-delta endpoint, pushes local edits
// as new versions (with optimistic-concurrency base_version_id), surfaces
// concurrent-edit conflicts as side-by-side "conflicted copy" files, and — since
// the local filesystem itself is the offline queue — re-uploads pending edits on
// reconnect. It is stdlib-only (periodic scan, JSON state file); the native
// mounted virtual-drive + tray UI is a separate, deferred client.
package main

// ---- pure three-way reconcile (the DoD core; unit-tested) ----------------

// Remote is the server's view of a file at a path (from the delta feed).
type Remote struct {
	DocID     string
	VersionID string
	SHA256    string
	Deleted   bool
}

// Local is the working-copy view of a file (from a filesystem scan).
type Local struct {
	SHA256 string
	Exists bool
}

// Known is the last-synced baseline for a path (persisted state) — the common
// ancestor for three-way reconciliation.
type Known struct {
	DocID     string
	VersionID string
	SHA256    string
}

// ActionType is the reconciler's decision for one path.
type ActionType string

const (
	ActNoop        ActionType = "noop"
	ActDownload    ActionType = "download"     // pull remote -> local
	ActUpload      ActionType = "upload"       // push local edit -> new version
	ActCreate      ActionType = "create"       // brand-new local file -> new document
	ActDeleteLocal ActionType = "delete_local" // remote tombstone -> remove local copy
	ActConflict    ActionType = "conflict"     // both sides diverged -> keep both (conflicted copy)
)

// Action is a reconciler decision for a single path.
type Action struct {
	Path      string
	Type      ActionType
	DocID     string // target document (upload/download/delete)
	BaseVer   string // version the local edit is based on (upload optimistic concurrency)
	RemoteVer string // remote version to record after a download
	RemoteSHA string
	LocalSHA  string
}

// Reconcile computes the sync actions for every path present in any of the three
// views. It is pure: no I/O, fully unit-testable. Semantics (three-way,
// baseline = Known):
//   - remote changed, local unchanged      -> download
//   - local changed, remote unchanged      -> upload (base = Known.VersionID)
//   - both changed and differ              -> conflict
//   - remote deleted, local unchanged      -> delete_local
//   - remote deleted, local changed        -> conflict (don't silently drop local work)
//   - new remote (not known, not local)    -> download
//   - new local (not known, not remote)    -> create
//   - identical / unchanged                -> noop
func Reconcile(remote map[string]Remote, local map[string]Local, known map[string]Known) []Action {
	paths := map[string]struct{}{}
	for p := range remote {
		paths[p] = struct{}{}
	}
	for p := range local {
		paths[p] = struct{}{}
	}
	for p := range known {
		paths[p] = struct{}{}
	}

	var out []Action
	for p := range paths {
		r, hasR := remote[p]
		l, hasL := local[p]
		k, hasK := known[p]

		// remoteChanged: the server view differs from the last-synced baseline.
		remoteChanged := false
		if hasR {
			if r.Deleted {
				remoteChanged = hasK // a tombstone only matters if we had the file
			} else if !hasK || r.SHA256 != k.SHA256 {
				remoteChanged = true
			}
		}
		// localChanged: the working copy differs from the baseline.
		localChanged := false
		if l.Exists {
			if !hasK || l.SHA256 != k.SHA256 {
				localChanged = true
			}
		} else if hasK {
			localChanged = true // was synced, now removed locally
		}

		switch {
		// --- remote tombstone ------------------------------------------------
		case hasR && r.Deleted:
			if !hasL || !l.Exists {
				out = append(out, Action{Path: p, Type: ActNoop})
			} else if !localChanged {
				out = append(out, Action{Path: p, Type: ActDeleteLocal, DocID: r.DocID})
			} else {
				out = append(out, conflict(p, r, l))
			}

		// --- new local file (not on server, no baseline) ---------------------
		case (!hasR || r.DocID == "") && (!hasK) && hasL && l.Exists:
			out = append(out, Action{Path: p, Type: ActCreate, LocalSHA: l.SHA256})

		// --- both sides diverged ---------------------------------------------
		case remoteChanged && localChanged && hasR && hasL && l.Exists && r.SHA256 != l.SHA256:
			out = append(out, conflict(p, r, l))

		// --- pull: remote moved, local at baseline (or absent) ---------------
		case hasR && remoteChanged && !localChanged:
			out = append(out, Action{Path: p, Type: ActDownload, DocID: r.DocID, RemoteVer: r.VersionID, RemoteSHA: r.SHA256})

		// --- push: local edited, remote at baseline --------------------------
		case hasL && l.Exists && localChanged && !remoteChanged:
			base := ""
			doc := ""
			if hasK {
				base = k.VersionID
				doc = k.DocID
			}
			if hasR {
				doc = r.DocID
			}
			out = append(out, Action{Path: p, Type: ActUpload, DocID: doc, BaseVer: base, LocalSHA: l.SHA256})

		// --- local deleted, remote at baseline -> (leave as noop for v1) -----
		// A local delete could be pushed as a document delete; v1 does not
		// propagate local deletions to avoid accidental server-side removals.
		default:
			out = append(out, Action{Path: p, Type: ActNoop})
		}
	}
	return out
}

func conflict(p string, r Remote, l Local) Action {
	return Action{Path: p, Type: ActConflict, DocID: r.DocID, RemoteVer: r.VersionID, RemoteSHA: r.SHA256, LocalSHA: l.SHA256}
}
