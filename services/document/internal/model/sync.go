package model

import (
	"time"

	"github.com/google/uuid"
)

// Sync delta + per-device state (§3/§5). The delta is derived from the
// documents/folders tables (keyset on updated_at,id, tombstones included); this
// model carries the wire shapes.

// Sync change kinds + ops.
const (
	SyncKindDocument = "document"
	SyncKindFolder   = "folder"
	SyncOpUpsert     = "upsert"
	SyncOpDelete     = "delete"
)

// SyncChange is one entry in the delta feed. For a document, ParentID is its
// folder and VersionID/SHA256/Size/Mime describe the current version. For a
// folder, ParentID is its parent folder and Path is the ltree path; the client
// reconstructs each file's path from folder + document changes.
type SyncChange struct {
	Kind      string
	Op        string
	ID        uuid.UUID
	ParentID  *uuid.UUID
	Name      string
	Path      string
	VersionID *uuid.UUID
	SHA256    string
	SizeBytes int64
	Mime      string
	UpdatedAt time.Time
}

// SyncDevice is a registered sync client + its position/config.
type SyncDevice struct {
	TenantID         uuid.UUID
	ID               uuid.UUID
	UserID           uuid.UUID
	Name             string
	Platform         string
	WorkspaceID      *uuid.UUID
	Cursor           string
	CreatedAt        time.Time
	LastSeenAt       *time.Time
	RevokedAt        *time.Time
	SelectiveFolders []uuid.UUID
}
