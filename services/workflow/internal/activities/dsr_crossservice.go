// Wave 12.4 — cross-service DSR activities.
//
// Activities that reach out to sibling services (search, qdrant,
// connector, etc.) to complete a GDPR subject erase. Each
// service owns its own scrub semantics — this package's job is
// orchestration: it knows the URL, formats the request, and
// handles timeouts / retries via Temporal's activity retry policy.
//
// The URL for each service comes from `Activities.ServiceURLs`, a
// map populated at wire-time in cmd/server/main.go. Missing URLs
// are treated as "service not deployed" → the activity logs +
// returns success (vs. failing the whole workflow). This matches
// the pilot posture where not every service is stood up in every
// environment.
package activities

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// PurgeSubjectFromSearch calls the search service's internal
// `/purge-subject` endpoint (Wave 12.4). Returns the deleted count
// OpenSearch reports. A missing search URL (dev deployment without
// the service) is a soft no-op.
func (a *Activities) PurgeSubjectFromSearch(ctx context.Context, tenantID, subjectID string) (int64, error) {
	url := a.ServiceURLs["search"]
	if url == "" {
		a.Log.Info().Str("tenant_id", tenantID).Str("subject_id", subjectID).
			Msg("dsr purge: search service URL not configured; skipping")
		return 0, nil
	}
	payload, _ := json.Marshal(map[string]string{
		"tenant_id":  tenantID,
		"subject_id": subjectID,
	})
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, url+"/internal/v1/search/purge-subject", bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("search purge POST: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("search purge returned %d", resp.StatusCode)
	}
	var body struct {
		Deleted int64 `json:"deleted"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return body.Deleted, nil
}

// PurgeSubjectFromVectors is the Qdrant equivalent. Qdrant is
// stubbed in the search service today (spec: "semantic via Qdrant
// stubbed for Phase 11"). Until a real Qdrant client is wired, the
// activity logs the intent and records zero points deleted. Wire
// the real call when the Qdrant adapter ships.
func (a *Activities) PurgeSubjectFromVectors(ctx context.Context, tenantID, subjectID string) (int64, error) {
	a.Log.Info().
		Str("tenant_id", tenantID).
		Str("subject_id", subjectID).
		Msg("dsr purge: Qdrant adapter not yet wired (Wave 12.4b); intent recorded only")
	return 0, nil
}

// PurgeSubjectFromConnectors calls the connector service's
// internal `/purge-subject` endpoint (Wave 12.6). The endpoint
// currently returns 0 because the schema is tenant-scoped — no
// `granted_by` column on connector_configs — but the call is
// logged for audit and the contract is stable for when per-user
// grant tracking lands (Wave 12.6b).
func (a *Activities) PurgeSubjectFromConnectors(ctx context.Context, tenantID, subjectID string) (int64, error) {
	url := a.ServiceURLs["connector"]
	if url == "" {
		a.Log.Info().Str("tenant_id", tenantID).Str("subject_id", subjectID).
			Msg("dsr purge: connector service URL not configured; skipping")
		return 0, nil
	}
	payload, _ := json.Marshal(map[string]string{
		"tenant_id":  tenantID,
		"subject_id": subjectID,
	})
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost,
		url+"/internal/v1/connectors/purge-subject", bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("connector purge POST: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("connector purge returned %d", resp.StatusCode)
	}
	var body struct {
		Purged int64 `json:"purged"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return body.Purged, nil
}
