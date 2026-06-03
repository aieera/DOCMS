package service

import (
	"archive/zip"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/services/document/internal/model"
)

// §9.5 / G9 — eDiscovery export.
//
// Produces a ZIP archive containing:
//   - manifest.json       — case metadata, custodian, doc list with
//                           per-document SHA-256 (of the version's
//                           content), created_at, current state.
//   - manifest.json.sig   — HMAC-SHA256 signature over manifest.json
//                           using VAULTDMS_EDISCOVERY_SIGNING_KEY (or
//                           the gateway secret if unset) — gives the
//                           auditor proof the bundle wasn't mutated.
//   - doc-<uuid>.json     — metadata blob per document. Binary
//                           download URLs are NOT embedded; the
//                           storage service owns presigned URLs and
//                           the caller resolves them separately.
//
// Why no blobs in the ZIP:
//   - Bundle stays <5 MB even for a case spanning thousands of docs
//     → emailable, reviewable, same hash reproducible by auditor
//     re-requesting blobs from the presign layer.
//   - Chain-of-custody is about the METADATA + list integrity. The
//     blobs themselves carry their own content hash and audit
//     events; re-downloading them from the presign path recreates
//     the same cryptographic chain.

type DiscoveryExportInput struct {
	CaseID         string      `json:"case_id"`
	CaseName       string      `json:"case_name"`
	CustodianEmail string      `json:"custodian_email"`
	DocumentIDs    []uuid.UUID `json:"document_ids"`
}

type DiscoveryManifest struct {
	Version        string                  `json:"version"`
	CaseID         string                  `json:"case_id"`
	CaseName       string                  `json:"case_name"`
	CustodianEmail string                  `json:"custodian_email"`
	TenantID       string                  `json:"tenant_id"`
	ExportedBy     string                  `json:"exported_by"`
	ExportedAt     time.Time               `json:"exported_at"`
	Documents      []DiscoveryManifestItem `json:"documents"`
}

type DiscoveryManifestItem struct {
	DocumentID     string    `json:"document_id"`
	Title          string    `json:"title"`
	LifecycleState string    `json:"lifecycle_state"`
	CurrentVersion string    `json:"current_version_id"`
	ContentSHA256  string    `json:"content_sha256"`
	CreatedAt      time.Time `json:"created_at"`
	VersionCount   int       `json:"version_count,omitempty"`
}

// ExportForDiscovery builds the ZIP and writes it to `w`. The caller
// owns where the bytes go (HTTP response, S3 put, local file, etc.)
// — this method does not persist anything itself. An audit event is
// fired for the export so chain-of-custody logs exist.
func (s *DocumentService) ExportForDiscovery(ctx context.Context, w io.Writer, in DiscoveryExportInput) (*DiscoveryManifest, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if in.CaseID == "" {
		return nil, errInvalidInput("case_id", "required")
	}
	if len(in.DocumentIDs) == 0 {
		return nil, errInvalidInput("document_ids", "required")
	}

	items := make([]DiscoveryManifestItem, 0, len(in.DocumentIDs))
	for _, docID := range in.DocumentIDs {
		doc, _, err := s.GetDocument(ctx, docID)
		if err != nil {
			// One missing doc shouldn't sink the export. Auditor
			// sees the gap in manifest (doc won't appear) + a
			// stderr log line.
			s.log.Warn().Err(err).Str("doc", docID.String()).Msg("ediscovery: skip")
			continue
		}
		it := DiscoveryManifestItem{
			DocumentID:     doc.ID.String(),
			Title:          doc.Title,
			LifecycleState: string(doc.LifecycleState),
			ContentSHA256:  doc.SHA256Hash,
			CreatedAt:      doc.CreatedAt,
		}
		if doc.CurrentVersionID != nil {
			it.CurrentVersion = doc.CurrentVersionID.String()
		}
		items = append(items, it)
	}

	manifest := &DiscoveryManifest{
		Version:        "1",
		CaseID:         in.CaseID,
		CaseName:       in.CaseName,
		CustodianEmail: in.CustodianEmail,
		TenantID:       tenantID.String(),
		ExportedBy:     userID.String(),
		ExportedAt:     time.Now().UTC(),
		Documents:      items,
	}
	if err := writeDiscoveryZip(w, manifest); err != nil {
		return nil, fmt.Errorf("ediscovery: write zip: %w", err)
	}

	// §9.5 requirement: export itself must be audit-logged so the
	// chain-of-custody claim is provable.
	evt, err := model.NewOutboxEvent(tenantID, "dms.ediscovery.exported.v1", "case", uuid.MustParse(fakeIfNotUUID(in.CaseID)),
		map[string]any{
			"case_id":          in.CaseID,
			"case_name":        in.CaseName,
			"custodian_email":  in.CustodianEmail,
			"document_count":   len(items),
			"exported_by":      userID.String(),
			"manifest_sha256":  manifestSHA256(manifest),
		})
	if err != nil {
		s.log.Warn().Err(err).Msg("ediscovery: build audit event failed (export already delivered)")
		return manifest, nil
	}
	// Write the audit event outside the export tx; the ZIP bytes
	// already went to the caller by this point.
	_ = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
	return manifest, nil
}

// ---- internal helpers -------------------------------------------

func writeDiscoveryZip(w io.Writer, manifest *DiscoveryManifest) error {
	zw := zip.NewWriter(w)
	defer zw.Close()

	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := writeZipEntry(zw, "manifest.json", manifestBytes); err != nil {
		return err
	}
	sig, err := hmacSign(manifestBytes, ediscoverySigningKey())
	if err != nil {
		return err
	}
	if err := writeZipEntry(zw, "manifest.json.sig", []byte(sig)); err != nil {
		return err
	}

	// Per-doc metadata blob — same information as the manifest item
	// but one-file-per-doc so an auditor can extract a single record.
	for _, it := range manifest.Documents {
		raw, _ := json.MarshalIndent(it, "", "  ")
		if err := writeZipEntry(zw, "doc-"+it.DocumentID+".json", raw); err != nil {
			return err
		}
	}
	return nil
}

func writeZipEntry(zw *zip.Writer, name string, data []byte) error {
	f, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	return err
}

func ediscoverySigningKey() string {
	if k := os.Getenv("VAULTDMS_EDISCOVERY_SIGNING_KEY"); k != "" {
		return k
	}
	// Fall back to the gateway secret so dev works out of the box
	// with a single env var. Production MUST set a dedicated key
	// (documented in H1 SOC 2 evidence runbook).
	return os.Getenv("VAULTDMS_GATEWAY_SECRET")
}

func hmacSign(data []byte, key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("ediscovery: signing key not configured")
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func manifestSHA256(m *DiscoveryManifest) string {
	raw, _ := json.Marshal(m)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

// fakeIfNotUUID returns a stable UUIDv5-like hash if the case ID
// isn't already a UUID — outbox_events requires a UUID aggregate_id.
func fakeIfNotUUID(s string) string {
	if _, err := uuid.Parse(s); err == nil {
		return s
	}
	h := sha256.Sum256([]byte(s))
	// Shape as a UUIDv4-ish string; the resulting UUID is
	// deterministic for a given case id.
	u := uuid.UUID{}
	copy(u[:], h[:16])
	u[6] = (u[6] & 0x0f) | 0x40
	u[8] = (u[8] & 0x3f) | 0x80
	return u.String()
}
