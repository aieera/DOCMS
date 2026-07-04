package main

import "testing"

// find returns the action for a path (or a noop if absent).
func find(as []Action, path string) Action {
	for _, a := range as {
		if a.Path == path {
			return a
		}
	}
	return Action{Path: path, Type: ActNoop}
}

func TestReconcile_PullRemoteChange(t *testing.T) {
	// Remote moved to v2/hashB; local still at the synced baseline -> download.
	remote := map[string]Remote{"a.txt": {DocID: "d1", VersionID: "v2", SHA256: "B"}}
	local := map[string]Local{"a.txt": {SHA256: "A", Exists: true}}
	known := map[string]Known{"a.txt": {DocID: "d1", VersionID: "v1", SHA256: "A"}}
	if got := find(Reconcile(remote, local, known), "a.txt"); got.Type != ActDownload || got.RemoteVer != "v2" {
		t.Fatalf("want download v2, got %+v", got)
	}
}

func TestReconcile_PushLocalEdit(t *testing.T) {
	// Local edited to hashC; remote still at baseline -> upload with base=v1.
	remote := map[string]Remote{"a.txt": {DocID: "d1", VersionID: "v1", SHA256: "A"}}
	local := map[string]Local{"a.txt": {SHA256: "C", Exists: true}}
	known := map[string]Known{"a.txt": {DocID: "d1", VersionID: "v1", SHA256: "A"}}
	got := find(Reconcile(remote, local, known), "a.txt")
	if got.Type != ActUpload || got.BaseVer != "v1" || got.DocID != "d1" {
		t.Fatalf("want upload base=v1, got %+v", got)
	}
}

func TestReconcile_ConcurrentEditIsConflict(t *testing.T) {
	// Both sides changed from the baseline and differ -> conflict (DoD).
	remote := map[string]Remote{"a.txt": {DocID: "d1", VersionID: "v2", SHA256: "B"}}
	local := map[string]Local{"a.txt": {SHA256: "C", Exists: true}}
	known := map[string]Known{"a.txt": {DocID: "d1", VersionID: "v1", SHA256: "A"}}
	if got := find(Reconcile(remote, local, known), "a.txt"); got.Type != ActConflict {
		t.Fatalf("concurrent edit must be a conflict, got %+v", got)
	}
}

func TestReconcile_RemoteDeleteRemovesCleanLocal(t *testing.T) {
	remote := map[string]Remote{"a.txt": {DocID: "d1", Deleted: true}}
	local := map[string]Local{"a.txt": {SHA256: "A", Exists: true}}
	known := map[string]Known{"a.txt": {DocID: "d1", VersionID: "v1", SHA256: "A"}}
	if got := find(Reconcile(remote, local, known), "a.txt"); got.Type != ActDeleteLocal {
		t.Fatalf("clean remote delete must remove local, got %+v", got)
	}
}

func TestReconcile_RemoteDeleteButLocalEditedIsConflict(t *testing.T) {
	remote := map[string]Remote{"a.txt": {DocID: "d1", Deleted: true}}
	local := map[string]Local{"a.txt": {SHA256: "C", Exists: true}} // locally edited
	known := map[string]Known{"a.txt": {DocID: "d1", VersionID: "v1", SHA256: "A"}}
	if got := find(Reconcile(remote, local, known), "a.txt"); got.Type != ActConflict {
		t.Fatalf("remote-delete vs local-edit must be a conflict, got %+v", got)
	}
}

func TestReconcile_NewLocalFileCreates(t *testing.T) {
	remote := map[string]Remote{}
	local := map[string]Local{"new.txt": {SHA256: "Z", Exists: true}}
	known := map[string]Known{}
	if got := find(Reconcile(remote, local, known), "new.txt"); got.Type != ActCreate {
		t.Fatalf("new local file must create, got %+v", got)
	}
}

func TestReconcile_NewRemoteFileDownloads(t *testing.T) {
	remote := map[string]Remote{"r.txt": {DocID: "d9", VersionID: "v1", SHA256: "Q"}}
	local := map[string]Local{}
	known := map[string]Known{}
	if got := find(Reconcile(remote, local, known), "r.txt"); got.Type != ActDownload {
		t.Fatalf("new remote file must download, got %+v", got)
	}
}

func TestReconcile_UnchangedIsNoop(t *testing.T) {
	remote := map[string]Remote{"a.txt": {DocID: "d1", VersionID: "v1", SHA256: "A"}}
	local := map[string]Local{"a.txt": {SHA256: "A", Exists: true}}
	known := map[string]Known{"a.txt": {DocID: "d1", VersionID: "v1", SHA256: "A"}}
	if got := find(Reconcile(remote, local, known), "a.txt"); got.Type != ActNoop {
		t.Fatalf("unchanged must be noop, got %+v", got)
	}
}

// Offline-edit-then-reconnect: while offline the local file diverged from the
// baseline; on reconnect the remote is still at the baseline, so it uploads.
func TestReconcile_OfflineEditSyncsOnReconnect(t *testing.T) {
	remote := map[string]Remote{"a.txt": {DocID: "d1", VersionID: "v1", SHA256: "A"}}
	local := map[string]Local{"a.txt": {SHA256: "OFFLINE_EDIT", Exists: true}}
	known := map[string]Known{"a.txt": {DocID: "d1", VersionID: "v1", SHA256: "A"}}
	if got := find(Reconcile(remote, local, known), "a.txt"); got.Type != ActUpload {
		t.Fatalf("offline edit must upload on reconnect, got %+v", got)
	}
}

// A server-side rename/move surfaces (via pull) as an old-path tombstone plus a
// new-path upsert; reconcile must remove the clean old file and download the new
// one — no orphan left behind.
func TestReconcile_RenameRemovesOldDownloadsNew(t *testing.T) {
	remote := map[string]Remote{
		"old.txt": {DocID: "d1", Deleted: true},
		"new.txt": {DocID: "d1", VersionID: "v2", SHA256: "A"},
	}
	local := map[string]Local{"old.txt": {SHA256: "A", Exists: true}}
	known := map[string]Known{"old.txt": {DocID: "d1", VersionID: "v1", SHA256: "A"}}
	acts := Reconcile(remote, local, known)
	if got := find(acts, "old.txt"); got.Type != ActDeleteLocal {
		t.Fatalf("rename should remove old path, got %+v", got)
	}
	if got := find(acts, "new.txt"); got.Type != ActDownload {
		t.Fatalf("rename should download new path, got %+v", got)
	}
}
