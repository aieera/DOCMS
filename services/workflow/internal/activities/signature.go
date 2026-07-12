package activities

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// SealCeremonyResult mirrors the signature service's
// /internal/seal-ceremony response.
type SealCeremonyResult struct {
	NewVersionID  string `json:"new_version_id"`
	Level         string `json:"level"`
	Fingerprint   string `json:"fingerprint"`
	AlreadySealed bool   `json:"already_sealed"`
}

// SealSignatureCeremony is the workflow-owned PAdES seal (ADR 0025 Wave 9):
// after all signers approve, the SignatureWorkflow calls this to materialise
// the ceremony as REAL cryptographic PAdES-B-LT revisions — one incremental
// revision per signer plus a final organizational seal, produced by the DSS
// sidecar — and ingest the LTV-sealed version. RegionPin (C.4) is enforced
// inside the signature service (ResolveRegionOrFail, fail-closed) before any
// bytes are signed.
//
// Idempotent via the signature service's per-request seal claim: a Temporal
// retry after a prior success returns already_sealed=true. Returns a typed
// error when the signature URL isn't configured so the workflow can fall back
// to the event-driven consumer seal.
func (a *Activities) SealSignatureCeremony(ctx context.Context, tenantID, documentID, versionID, requestID, initiatedBy string) (*SealCeremonyResult, error) {
	base := a.ServiceURLs["signature"]
	if base == "" {
		return nil, fmt.Errorf("signature service URL not configured")
	}
	payload, _ := json.Marshal(map[string]string{
		"document_id":  documentID,
		"version_id":   versionID,
		"request_id":   requestID,
		"initiated_by": initiatedBy,
	})
	// A B-LT DSS sign of an N-signer envelope + the re-upload can take a while.
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost,
		base+"/api/v1/signatures/internal/seal-ceremony", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// Service-to-service identity: the seal endpoint's SessionOrAPIKey accepts
	// the internal key + the tenant/user/role it names in the headers. Role
	// admin so the storage OPA owner/admin rule authorises the sealed-version
	// write (mirrors the fallback consumer's system-seal identity).
	req.Header.Set("X-Internal-Service-Key", a.InternalKey)
	req.Header.Set("X-Auth-Tenant-ID", tenantID)
	req.Header.Set("X-User-Role", "admin")
	if initiatedBy != "" {
		req.Header.Set("X-User-ID", initiatedBy)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("seal-ceremony POST: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return nil, fmt.Errorf("seal-ceremony returned %d: %s", resp.StatusCode, string(b))
	}
	var out SealCeremonyResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode seal-ceremony response: %w", err)
	}
	return &out, nil
}
