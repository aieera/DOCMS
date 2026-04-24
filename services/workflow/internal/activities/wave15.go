// Wave 15 — Temporal activities for password-expiry sweeper (15.3)
// and acknowledgement-reminder sweeper (15.1).
//
// Both activities hit admin HTTP endpoints on their owning service.
// URLs come from Activities.ServiceURLs keyed "auth" and
// "acknowledgement" respectively. A missing URL is a soft no-op +
// log — same posture as the DSR cross-service activities.
package activities

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// SweepPasswordExpiriesResult is the auth-service response shape for
// /admin/password-policy/sweep-expired. Matches Wave 15.3's handler.
type SweepPasswordExpiriesResult struct {
	Flagged int `json:"flagged"`
}

// SweepPasswordExpiries invokes the auth service's sweeper endpoint
// for a single tenant. The endpoint runs under the tenant's GUC (set
// from the X-Auth-Tenant-ID header the gateway normally injects), so
// we mint that header ourselves and rely on the gateway-signature
// middleware being disabled on the internal/admin path — or on this
// worker running inside the same zero-trust perimeter as the gateway
// (Wave 3 deployment assumption).
//
// On any non-2xx the activity returns an error so Temporal's retry
// policy kicks in.
func (a *Activities) SweepPasswordExpiries(ctx context.Context, tenantID string) (int, error) {
	url := a.ServiceURLs["auth"]
	if url == "" {
		a.Log.Info().Str("tenant_id", tenantID).
			Msg("password sweep: auth service URL not configured; skipping")
		return 0, nil
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(
		cctx, http.MethodPost,
		url+"/api/v1/admin/password-policy/sweep-expired",
		bytes.NewReader([]byte(`{}`)),
	)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Tenant-ID", tenantID)
	// Worker is internal; the admin path's gateway-signature check
	// is bypassed by the mesh / run-inside-trust-boundary posture.
	// See Wave 3 deployment notes.
	req.Header.Set("X-Internal-Worker", "workflow")
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("password sweep POST: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("password sweep returned %d", resp.StatusCode)
	}
	var body SweepPasswordExpiriesResult
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return body.Flagged, nil
}

// SweepAcknowledgementRemindersResult is the ack-service response
// shape. Mirrors the endpoint's return (to be added in Wave 15.1
// follow-up).
type SweepAcknowledgementRemindersResult struct {
	Reminded  int `json:"reminded"`
	Escalated int `json:"escalated"`
}

// SweepSignatureProfileOrphansResult mirrors the signature service's
// /internal/v1/signature/orphan-sweep response. Count is per-call,
// not cumulative.
type SweepSignatureProfileOrphansResult struct {
	Swept int `json:"swept"`
}

// SweepSignatureProfileOrphans calls the signature service's T-D-4
// orphan-sweep endpoint for a single tenant. Rows whose Delete()
// crashed between tx1 and the S3 step are found, their S3 object is
// deleted, image_ref is nulled, and an audit outbox event is emitted
// (see services/signature/internal/service/orphan_sweeper.go).
func (a *Activities) SweepSignatureProfileOrphans(ctx context.Context, tenantID string) (SweepSignatureProfileOrphansResult, error) {
	var out SweepSignatureProfileOrphansResult
	url := a.ServiceURLs["signature"]
	if url == "" {
		a.Log.Info().Str("tenant_id", tenantID).
			Msg("signature orphan sweep: signature service URL not configured; skipping")
		return out, nil
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(
		cctx, http.MethodPost,
		url+"/internal/v1/signature/orphan-sweep",
		bytes.NewReader([]byte(`{}`)),
	)
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Tenant-ID", tenantID)
	req.Header.Set("X-Internal-Worker", "workflow")
	resp, err := httpClient.Do(req)
	if err != nil {
		return out, fmt.Errorf("signature orphan sweep POST: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return out, fmt.Errorf("signature orphan sweep returned %d", resp.StatusCode)
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out, nil
}

// SweepAcknowledgementReminders calls the acknowledgement service's
// internal sweep endpoint per tenant. The sweep:
//   1. Finds assignments past (due_at - 3d) without ack → remind.
//   2. Finds assignments past (due_at + 7d) without ack → escalate.
// It emits dms.acknowledgement.reminded.v1 / escalated.v1 via the
// tenant outbox.
func (a *Activities) SweepAcknowledgementReminders(ctx context.Context, tenantID string) (SweepAcknowledgementRemindersResult, error) {
	var out SweepAcknowledgementRemindersResult
	url := a.ServiceURLs["acknowledgement"]
	if url == "" {
		a.Log.Info().Str("tenant_id", tenantID).
			Msg("ack reminder sweep: acknowledgement service URL not configured; skipping")
		return out, nil
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(
		cctx, http.MethodPost,
		url+"/internal/v1/acknowledgement/sweep-reminders",
		bytes.NewReader([]byte(`{}`)),
	)
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Tenant-ID", tenantID)
	req.Header.Set("X-Internal-Worker", "workflow")
	resp, err := httpClient.Do(req)
	if err != nil {
		return out, fmt.Errorf("ack reminder sweep POST: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return out, fmt.Errorf("ack reminder sweep returned %d", resp.StatusCode)
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out, nil
}
