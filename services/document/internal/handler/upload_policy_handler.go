// Admin REST surface for the per-tenant upload format allowlist
// (migration 000060). Owner / admin only. The storage service reads
// the same `tenant_upload_policies` table on InitiateUpload, so this
// page is the single source of truth for what users can upload
// org-wide. Empty lists = "no allowlist enforced" (the executable
// blocklist in services/storage/internal/scanner is the only gate).
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

type UploadPolicyHandler struct {
	pool *pgxpool.Pool
}

func NewUploadPolicyHandler(pool *pgxpool.Pool) *UploadPolicyHandler {
	return &UploadPolicyHandler{pool: pool}
}

// Register mounts the routes. Caller wraps with SessionAuth at the
// mount site; the handler does its own owner|admin role check.
func (h *UploadPolicyHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/tenant/upload-policy", h.get)
	mux.HandleFunc("PUT /api/v1/admin/tenant/upload-policy", h.put)
}

type uploadPolicyResponse struct {
	AllowedMimeTypes  []string `json:"allowed_mime_types"`
	AllowedExtensions []string `json:"allowed_extensions"`
}

func (h *UploadPolicyHandler) get(w http.ResponseWriter, r *http.Request) {
	tenantID, err := auth.GetTenantID(r.Context())
	if err != nil || tenantID == uuid.Nil {
		writeErr(w, r, vdmserr.ErrUnauthorized)
		return
	}
	if role := auth.GetUserRole(r.Context()); role != "owner" && role != "admin" {
		writeErr(w, r, vdmserr.ErrForbidden)
		return
	}
	out := uploadPolicyResponse{AllowedMimeTypes: []string{}, AllowedExtensions: []string{}}
	dbErr := database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		var mimeRaw, extRaw []byte
		err := tx.QueryRow(r.Context(), `
			SELECT allowed_mime_types, allowed_extensions
			FROM tenant_upload_policies
			WHERE tenant_id = $1
		`, tenantID).Scan(&mimeRaw, &extRaw)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if len(mimeRaw) > 0 {
			_ = json.Unmarshal(mimeRaw, &out.AllowedMimeTypes)
		}
		if len(extRaw) > 0 {
			_ = json.Unmarshal(extRaw, &out.AllowedExtensions)
		}
		return nil
	})
	if dbErr != nil {
		writeErr(w, r, dbErr)
		return
	}
	writeJSONStatus(w, http.StatusOK, out)
}

func (h *UploadPolicyHandler) put(w http.ResponseWriter, r *http.Request) {
	tenantID, err := auth.GetTenantID(r.Context())
	if err != nil || tenantID == uuid.Nil {
		writeErr(w, r, vdmserr.ErrUnauthorized)
		return
	}
	userID, _ := auth.GetUserID(r.Context())
	if role := auth.GetUserRole(r.Context()); role != "owner" && role != "admin" {
		writeErr(w, r, vdmserr.ErrForbidden)
		return
	}
	var body uploadPolicyResponse
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	// Defensive normalisation: trim, drop empties, lowercase exts so
	// the storage-side EqualFold check never has to second-guess
	// stray whitespace from a paste.
	body.AllowedMimeTypes = cleanList(body.AllowedMimeTypes, false)
	body.AllowedExtensions = cleanList(body.AllowedExtensions, true)

	mimeJSON, _ := json.Marshal(body.AllowedMimeTypes)
	extJSON, _ := json.Marshal(body.AllowedExtensions)

	dbErr := database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `
			INSERT INTO tenant_upload_policies (tenant_id, allowed_mime_types, allowed_extensions, updated_by, updated_at)
			VALUES ($1, $2, $3, $4, now())
			ON CONFLICT (tenant_id) DO UPDATE
			SET allowed_mime_types = EXCLUDED.allowed_mime_types,
			    allowed_extensions = EXCLUDED.allowed_extensions,
			    updated_by         = EXCLUDED.updated_by,
			    updated_at         = now()
		`, tenantID, mimeJSON, extJSON, userID)
		return err
	})
	if dbErr != nil {
		writeErr(w, r, dbErr)
		return
	}
	writeJSONStatus(w, http.StatusOK, body)
}

func cleanList(in []string, dotPrefix bool) []string {
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, s := range in {
		s = trimSpace(s)
		if s == "" {
			continue
		}
		if dotPrefix {
			s = toLower(s)
			if s[0] != '.' {
				s = "." + s
			}
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// Tiny helpers — avoiding pulling strings into the import set for a
// two-call dependency.
func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}
