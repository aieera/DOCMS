package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	var (
		base      = flag.String("base", envOr("DMS_SYNC_BASE", "http://localhost:8080"), "SeDoc API base URL")
		key       = flag.String("key", os.Getenv("DMS_SYNC_KEY"), "API key (vdms_...) with documents:read,documents:write")
		dir       = flag.String("dir", "", "local mirror directory")
		workspace = flag.String("workspace", os.Getenv("DMS_SYNC_WORKSPACE"), "workspace id to mirror (required)")
		deviceID  = flag.String("device-id", os.Getenv("DMS_SYNC_DEVICE_ID"), "registered device id (enforces revocation + server cursor)")
		device    = flag.String("device", hostname(), "device display name (for conflicted-copy labels)")
		interval  = flag.Duration("interval", 15*time.Second, "poll interval")
		once      = flag.Bool("once", false, "run one sync cycle and exit")
	)
	flag.Parse()
	if *key == "" || *dir == "" || *workspace == "" {
		log.Fatal("dms-sync: -key, -dir and -workspace are required (see -help)")
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		log.Fatalf("dms-sync: mkdir mirror: %v", err)
	}

	agent := &agent{
		client:   newClient(*base, *key),
		root:     *dir,
		ws:       *workspace,
		deviceID: *deviceID,
		device:   *device,
	}
	state, err := loadState(*dir)
	if err != nil {
		log.Fatalf("dms-sync: load state: %v", err)
	}
	agent.state = state

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	for {
		if err := agent.cycle(ctx); err != nil {
			if errors.Is(err, errOffline) {
				log.Printf("dms-sync: offline; will retry (%v)", err)
			} else if ctx.Err() != nil {
				break
			} else {
				log.Printf("dms-sync: cycle error: %v", err)
			}
		}
		if *once || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(*interval):
		}
	}
}

type agent struct {
	client   *Client
	root     string
	ws       string
	deviceID string
	device   string
	state    *State
}

// cycle runs one full reconcile: pull the delta, scan local, reconcile, execute.
func (a *agent) cycle(ctx context.Context) error {
	remote, err := a.pull(ctx)
	if err != nil {
		return err // offline -> retry next tick; cursor NOT advanced
	}
	local, err := scanLocal(a.root)
	if err != nil {
		return err
	}
	actions := Reconcile(remote, local, a.state.Files)
	for _, act := range actions {
		if err := a.apply(ctx, act); err != nil {
			if errors.Is(err, errOffline) {
				return err // stop this cycle; state for already-applied actions is saved below
			}
			log.Printf("dms-sync: %s %s: %v", act.Type, act.Path, err)
		}
	}
	return a.state.save(a.root)
}

// pull drains the delta feed into a relpath -> Remote map, updating the folder
// tree + cursor as it goes.
func (a *agent) pull(ctx context.Context) (map[string]Remote, error) {
	remote := map[string]Remote{}
	cursor := a.state.Cursor
	for {
		d, err := a.client.Delta(ctx, a.ws, a.deviceID, cursor)
		if err != nil {
			return nil, err
		}
		for _, ch := range d.Changes {
			switch ch.Kind {
			case "folder":
				if ch.Op == "delete" {
					delete(a.state.FolderName, ch.ID)
					delete(a.state.FolderParent, ch.ID)
					continue
				}
				a.state.FolderName[ch.ID] = ch.Name
				a.state.FolderParent[ch.ID] = ch.ParentID
			case "document":
				if ch.Op == "delete" {
					if rel := a.state.DocPath[ch.ID]; rel != "" {
						remote[rel] = Remote{DocID: ch.ID, Deleted: true}
					}
					continue
				}
				rel := a.docRel(ch)
				// Rename/move: retire the previous path as a tombstone so the old
				// local file is removed (or conflicts if locally edited) instead
				// of being orphaned alongside the new copy.
				if old := a.state.DocPath[ch.ID]; old != "" && old != rel {
					remote[old] = Remote{DocID: ch.ID, Deleted: true}
				}
				remote[rel] = Remote{DocID: ch.ID, VersionID: ch.VersionID, SHA256: ch.SHA256}
				a.state.DocPath[ch.ID] = rel
			}
		}
		cursor = d.NextCursor
		if !d.HasMore {
			break
		}
	}
	a.state.Cursor = cursor
	return remote, nil
}

func (a *agent) docRel(ch deltaChange) string {
	dir := a.state.relDir(ch.ParentID)
	name := sanitize(ch.Name)
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

func (a *agent) apply(ctx context.Context, act Action) error {
	abs := filepath.Join(a.root, filepath.FromSlash(act.Path))
	switch act.Type {
	case ActDownload:
		data, err := a.client.DownloadContent(ctx, act.DocID)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(abs, data, 0o644); err != nil {
			return err
		}
		a.state.Files[act.Path] = Known{DocID: act.DocID, VersionID: act.RemoteVer, SHA256: act.RemoteSHA}
		log.Printf("dms-sync: pulled %s", act.Path)

	case ActDeleteLocal:
		_ = os.Remove(abs)
		delete(a.state.Files, act.Path)
		log.Printf("dms-sync: removed (remote-deleted) %s", act.Path)

	case ActUpload:
		data, err := os.ReadFile(abs)
		if err != nil {
			return err
		}
		sha, _ := fileSHA256(abs)
		blob, err := a.client.UploadBlob(ctx, data, filepath.Base(act.Path), "application/octet-stream", sha, a.ws, "")
		if err != nil {
			return err
		}
		ver, err := a.client.CreateVersion(ctx, act.DocID, blob, act.BaseVer, "Synced by "+a.device)
		if errors.Is(err, errConflict) {
			return a.resolveConflict(ctx, act, data)
		}
		if err != nil {
			return err
		}
		a.state.Files[act.Path] = Known{DocID: act.DocID, VersionID: ver, SHA256: sha}
		log.Printf("dms-sync: pushed %s (v=%s)", act.Path, ver)

	case ActConflict:
		data, _ := os.ReadFile(abs)
		return a.resolveConflict(ctx, act, data)

	case ActCreate:
		// New local files need a target document + folder resolution; v1 syncs
		// existing documents only. Surface it rather than silently ignore.
		log.Printf("dms-sync: NOTE new local file %s not uploaded (v1 syncs existing documents; create-new is a follow-up)", act.Path)
	}
	return nil
}

// resolveConflict keeps BOTH copies: the server version at the canonical path
// and the local divergent bytes as a "(conflicted copy)" file for the user to
// reconcile. The baseline advances to the server version so the loop settles.
func (a *agent) resolveConflict(ctx context.Context, act Action, localData []byte) error {
	// 1. Write the local bytes to a conflicted-copy sibling.
	confRel := conflictedName(act.Path, a.device)
	confAbs := filepath.Join(a.root, filepath.FromSlash(confRel))
	if err := os.MkdirAll(filepath.Dir(confAbs), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(confAbs, localData, 0o644); err != nil {
		return err
	}
	// 2. Overwrite the canonical path with the server's current version.
	data, err := a.client.DownloadContent(ctx, act.DocID)
	if err != nil {
		return err
	}
	abs := filepath.Join(a.root, filepath.FromSlash(act.Path))
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		return err
	}
	a.state.Files[act.Path] = Known{DocID: act.DocID, VersionID: act.RemoteVer, SHA256: act.RemoteSHA}
	log.Printf("dms-sync: CONFLICT on %s — kept server copy; your version saved as %s", act.Path, confRel)
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "device"
	}
	return h
}
