// Wave 15.4 — Saved signature profiles.
//
// Lets a user draw/upload/type a signature once and reuse it on
// every PAdES envelope. The crypto of Wave 9 (PAdES-B-LT) does NOT
// change — we only add a per-user image that the signing page
// embeds into the visual appearance. The cryptographic signature
// itself is still produced by the signer sidecar over the PDF bytes.
//
// Privacy + security:
//   - Image bytes are encrypted at rest with a per-profile DEK that
//     is itself wrapped under the tenant KEK. Two profiles for the
//     same user use independent DEKs so deleting one does not
//     expose the other.
//   - Delete is crypto-shred: the S3 object is removed, then the
//     row is removed. After HardDelete the DEK is unrecoverable,
//     so any stray ciphertext is cryptographically opaque.
//   - Server NEVER trusts client-provided pixels at signing time —
//     the signing page references a profile by id, and the server
//     fetches + decrypts the bytes on the backend path.
package service

import (
	"bytes"
	"context"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/crypto"
	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/pkg/storage"
	"github.com/vaultdms/vaultdms/services/signature/internal/model"
	"github.com/vaultdms/vaultdms/services/signature/internal/repository"
)

// ProfileMaxBytes caps the encrypted image size. 1 MiB is plenty
// for a canvas-drawn PNG or a photographed scan — anything larger
// is almost certainly a misuse (uploading a document, not a
// signature) and should be rejected early.
const ProfileMaxBytes = 1 << 20

// ProfileBucket is the S3 bucket name. A per-service prefix keeps
// signature-profile objects from colliding with document blobs.
const ProfileBucket = "vaultdms-signature-profiles"

// ProfileService owns the CRUD + crypto path.
type ProfileService struct {
	pool    *pgxpool.Pool
	repo    repository.ProfileRepo
	kms     crypto.KeyManager
	store   ProfileObjectStore
	outbox  *database.OutboxRepository
	log     zerolog.Logger
	kekID   func(tenantID uuid.UUID) string
	clock   func() time.Time
}

// ProfileObjectStore is the narrow slice of storage we need. Lets
// tests inject an in-memory store without running MinIO.
type ProfileObjectStore interface {
	PutObject(ctx context.Context, bucket, key string, reader io.Reader, size int64, contentType string) error
	GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error)
	DeleteObject(ctx context.Context, bucket, key string) error
}

// ProfileServiceConfig bundles deps.
type ProfileServiceConfig struct {
	Pool   *pgxpool.Pool
	Repo   repository.ProfileRepo
	KMS    crypto.KeyManager
	Store  ProfileObjectStore
	Outbox *database.OutboxRepository
	Logger zerolog.Logger
}

// NewProfileService constructs a service.
func NewProfileService(cfg ProfileServiceConfig) *ProfileService {
	return &ProfileService{
		pool:   cfg.Pool,
		repo:   cfg.Repo,
		kms:    cfg.KMS,
		store:  cfg.Store,
		outbox: cfg.Outbox,
		log:    cfg.Logger,
		kekID: func(t uuid.UUID) string {
			return "vaultdms/tenant/" + t.String() + "/signature-profiles"
		},
		clock: time.Now,
	}
}

// ---- Create ---------------------------------------------------------------

// CreateProfileInput is the validated shape.
type CreateProfileInput struct {
	TenantID  uuid.UUID
	UserID    uuid.UUID
	Name      string
	Kind      model.SignatureProfileKind
	FontStyle string // only for Kind=typed
	Image     []byte // plaintext; service encrypts before upload
	SetDefault bool
}

// CreateProfile encrypts the image, uploads to S3, and persists the
// row. If SetDefault is true, any existing default for the user is
// flipped off in the same tx.
func (s *ProfileService) CreateProfile(ctx context.Context, in CreateProfileInput) (*model.SignatureProfile, error) {
	if err := validateCreateProfile(in); err != nil {
		return nil, err
	}

	kekID := s.kekID(in.TenantID)
	plaintextDEK, wrappedDEK, err := s.kms.GenerateDataKey(ctx, kekID)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(plaintextDEK) // wipe plaintext key from memory on return

	ciphertext, nonce, err := crypto.EncryptData(in.Image, plaintextDEK)
	if err != nil {
		return nil, err
	}

	id := uuid.New()
	key := profileS3Key(in.TenantID, in.UserID, id)

	if err := s.store.PutObject(ctx, ProfileBucket, key, bytes.NewReader(ciphertext), int64(len(ciphertext)), "application/octet-stream"); err != nil {
		return nil, err
	}

	p := &model.SignatureProfile{
		TenantID:       in.TenantID,
		ID:             id,
		UserID:         in.UserID,
		Name:           in.Name,
		Kind:           in.Kind,
		FontStyle:      in.FontStyle,
		ImageRef:       key,
		WrappedDEK:     wrappedDEK,
		KEKID:          kekID,
		Nonce:          nonce,
		ImageSizeBytes: len(ciphertext),
		IsDefault:      in.SetDefault,
		CreatedAt:      s.clock().UTC(),
		UpdatedAt:      s.clock().UTC(),
	}
	err = database.WithTenantTx(ctx, s.pool, in.TenantID, func(tx pgx.Tx) error {
		if in.SetDefault {
			// Flip any existing default off before insert; the
			// partial unique index would otherwise reject.
			if _, err := tx.Exec(ctx, `
				UPDATE signature_profiles SET is_default = false, updated_at = now()
				 WHERE tenant_id = $1 AND user_id = $2 AND is_default = true`,
				in.TenantID, in.UserID); err != nil {
				return err
			}
		}
		return s.repo.Insert(ctx, tx, p)
	})
	if err != nil {
		// Best-effort cleanup — if the DB write failed, drop the
		// orphaned S3 object so we don't leak ciphertext at rest.
		_ = s.store.DeleteObject(context.Background(), ProfileBucket, key)
		return nil, err
	}
	return p, nil
}

// ---- List / Get ---------------------------------------------------------

// List returns the caller's non-revoked profiles.
func (s *ProfileService) List(ctx context.Context, tenantID, userID uuid.UUID) ([]model.SignatureProfile, error) {
	var out []model.SignatureProfile
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		list, err := s.repo.ListByUser(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		out = list
		return nil
	})
	return out, err
}

// ---- Rename / SetDefault --------------------------------------------------

// Rename updates the display name. Caller must be the profile owner.
func (s *ProfileService) Rename(ctx context.Context, tenantID, userID, id uuid.UUID, name string) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		p, err := s.repo.Get(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		if p.UserID != userID {
			return vdmserr.ErrForbidden
		}
		return s.repo.Rename(ctx, tx, tenantID, id, name)
	})
}

// SetDefault swaps the user's default profile.
func (s *ProfileService) SetDefault(ctx context.Context, tenantID, userID, id uuid.UUID) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		p, err := s.repo.Get(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		if p.UserID != userID {
			return vdmserr.ErrForbidden
		}
		return s.repo.SetDefault(ctx, tx, tenantID, userID, id)
	})
}

// ---- Delete (crypto-shred) ------------------------------------------------

// Delete removes the S3 object first, then deletes the row. After
// this returns, the per-profile DEK is unrecoverable — the wrapped
// form is gone and the plaintext was never persisted.
func (s *ProfileService) Delete(ctx context.Context, tenantID, userID, id uuid.UUID) error {
	var key string
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		p, err := s.repo.Get(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		if p.UserID != userID {
			return vdmserr.ErrForbidden
		}
		key = p.ImageRef
		// Mark revoked inside the tx so a subsequent List sees the
		// user-visible effect even if the S3 delete is slow.
		return s.repo.Revoke(ctx, tx, tenantID, id, s.clock().UTC())
	})
	if err != nil {
		return err
	}
	if key != "" {
		if err := s.store.DeleteObject(ctx, ProfileBucket, key); err != nil {
			// S3 delete failed — leave the row revoked but not
			// hard-deleted; an operator-triggered sweeper retries.
			// Tracked as Wave 15.4 follow-up.
			return err
		}
	}
	// Hard-delete the row in a fresh tx so the S3-delete failure
	// mode above doesn't roll back the soft-delete.
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return s.repo.HardDelete(ctx, tx, tenantID, id)
	})
}

// ---- Image fetch ----------------------------------------------------------

// ResolveImage fetches + decrypts the profile image. Owner-only.
// An admin-audit path is NOT implemented at this layer — if one is
// needed later, add a separate `ResolveImageAsAdmin(ctx, tenantID,
// adminID, profileID) ([]byte, error)` method and route admin
// callers through it explicitly. Silent role bypass in a shared
// method would be a tenant-isolation footgun.
func (s *ProfileService) ResolveImage(ctx context.Context, tenantID, userID, id uuid.UUID) ([]byte, error) {
	var p *model.SignatureProfile
	if err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		loaded, err := s.repo.Get(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		p = loaded
		return nil
	}); err != nil {
		return nil, err
	}
	if p.UserID != userID {
		return nil, vdmserr.ErrForbidden
	}
	obj, err := s.store.GetObject(ctx, ProfileBucket, p.ImageRef)
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	ciphertext, err := io.ReadAll(obj)
	if err != nil {
		return nil, err
	}
	dek, err := s.kms.DecryptDataKey(ctx, p.KEKID, p.WrappedDEK)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(dek)
	return crypto.DecryptData(ciphertext, p.Nonce, dek)
}

// ---- helpers --------------------------------------------------------------

func profileS3Key(tenantID, userID, id uuid.UUID) string {
	return "t/" + tenantID.String() + "/u/" + userID.String() + "/p/" + id.String() + ".bin"
}

func validateCreateProfile(in CreateProfileInput) error {
	if in.Name == "" {
		return vdmserr.Validation("name", "required")
	}
	switch in.Kind {
	case model.ProfileKindDraw, model.ProfileKindUpload, model.ProfileKindTyped:
	default:
		return vdmserr.Validation("kind", "must be draw|upload|typed")
	}
	if in.Kind == model.ProfileKindTyped && in.FontStyle == "" {
		return vdmserr.Validation("font_style", "required for kind=typed")
	}
	if len(in.Image) == 0 {
		return vdmserr.Validation("image", "required")
	}
	if len(in.Image) > ProfileMaxBytes {
		return vdmserr.Validation("image", "exceeds 1 MiB cap")
	}
	return nil
}

// zeroBytes wipes a byte slice. Best-effort — Go's runtime is free
// to move the buffer before this runs, so this is defence-in-depth
// rather than a hard guarantee.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// Assert storage.S3Client satisfies ProfileObjectStore at compile
// time — if the S3Client signature drifts, this breaks the build
// rather than the runtime.
var _ ProfileObjectStore = (*profileS3Adapter)(nil)

// profileS3Adapter is used in wiring to adapt *storage.S3Client
// (which returns minio types) into the ProfileObjectStore interface.
// Defined here so tests don't need to import pkg/storage.
type profileS3Adapter struct{ inner *storage.S3Client }

// NewProfileS3Adapter wraps an S3Client.
func NewProfileS3Adapter(c *storage.S3Client) ProfileObjectStore {
	return &profileS3Adapter{inner: c}
}

func (a *profileS3Adapter) PutObject(ctx context.Context, bucket, key string, reader io.Reader, size int64, contentType string) error {
	return a.inner.PutObject(ctx, bucket, key, reader, size, contentType)
}
func (a *profileS3Adapter) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	return a.inner.GetObject(ctx, bucket, key)
}
func (a *profileS3Adapter) DeleteObject(ctx context.Context, bucket, key string) error {
	return a.inner.DeleteObject(ctx, bucket, key)
}
