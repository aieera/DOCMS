// Package model holds the storage service's domain types.
package model

import (
	"time"

	"github.com/google/uuid"
)

// UploadStatus mirrors the CHECK constraint on upload_sessions.status.
type UploadStatus string

const (
	UploadInitiated  UploadStatus = "initiated"
	UploadUploading  UploadStatus = "uploading"
	UploadScanning   UploadStatus = "scanning"
	UploadCompleted  UploadStatus = "completed"
	UploadFailed     UploadStatus = "failed"
	UploadQuarantine UploadStatus = "quarantined"
)

// UploadType mirrors the CHECK on upload_sessions.upload_type.
type UploadType string

const (
	UploadSingle    UploadType = "single"
	UploadMultipart UploadType = "multipart"
	UploadTUS       UploadType = "tus"
)

// ScanResult mirrors the state ClamAV returns through us.
type ScanResult string

const (
	ScanClean    ScanResult = "clean"
	ScanInfected ScanResult = "infected"
	ScanPending  ScanResult = "pending"
	ScanError    ScanResult = "error"
)

// UploadSession is one row in upload_sessions.
type UploadSession struct {
	TenantID       uuid.UUID
	ID             uuid.UUID
	DocumentID     *uuid.UUID
	Filename       string
	TotalSize      int64
	MimeType       string
	UploadType     UploadType
	StorageRegion  string
	S3UploadID     string
	Status         UploadStatus
	PartsCompleted int
	PartsTotal     int
	CreatedBy      uuid.UUID
	CreatedAt      time.Time
	CompletedAt    *time.Time
	ExpiresAt      time.Time
	// ContentBlobID is set when the session completes (migration
	// 000095) and points at the blob it produced — the blob an
	// idempotent re-complete replays. Dedup completions point at the
	// pre-existing blob, whose storage key differs from the session's,
	// so this cannot be derived. Nil on legacy/pre-completion rows.
	ContentBlobID *uuid.UUID
	StorageBucket string // derived from region + tier; not persisted on every row
	StorageKey    string // content-addressable path in the bucket
}

// ScanRecord is one row in scan_results.
type ScanRecord struct {
	TenantID  uuid.UUID
	ID        uuid.UUID
	UploadID  uuid.UUID
	Result    ScanResult
	Signature string // clamav virus name on hit; empty on clean
	ScannedAt time.Time
}

// ContentBlob is one row in the content_blobs table. Represents one
// physical object in S3 with its encryption envelope.
type ContentBlob struct {
	ID              uuid.UUID
	TenantID        uuid.UUID
	SHA256Hash      string
	StorageRegion   string
	StorageBucket   string
	StorageKey      string
	StorageClass    string // hot | warm | cold | quarantine
	SizeBytes       int64
	MimeType        string
	EncryptionKeyID string // legacy: KMS kek id as string (alt path for compat)
	EncryptedDEK    []byte // envelope: KMS-wrapped DEK (nil = not enveloped)
	DEKNonce        []byte // AES-GCM nonce used to encrypt the S3 object body
	KEKID           string // fully-qualified KEK identifier used for this wrap
	ReferenceCount  int
	CreatedAt       time.Time
}

// LifecycleJob is one row in lifecycle_jobs.
type LifecycleJob struct {
	TenantID    uuid.UUID
	ID          uuid.UUID
	DocumentID  uuid.UUID
	FromTier    string // hot | warm | cold | quarantine
	ToTier      string
	Status      string // queued | running | done | failed
	Reason      string
	Error       string
	CreatedAt   time.Time
	CompletedAt *time.Time
}
