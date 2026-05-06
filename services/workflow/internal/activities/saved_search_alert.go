// ADR 0068 — saved-search alert activities.
//
// Four activities back the SavedSearchAlertWorkflow:
//   - LoadSavedSearchAlert        : fetch saved-search + subscribers
//   - RunSavedSearchAlert         : execute the search, return doc_ids
//   - EmitSavedSearchMatch        : publish the notify event
//   - UpdateSavedSearchAlertCursor: write last_match_doc_ids + last_run_at
//
// Activities own all the I/O; the workflow stays deterministic.
// Methods attach to *Activities so the cmd/worker registration
// shape (one struct value passed to RegisterActivity) doesn't need
// per-activity glue.
package activities

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
)

// SavedSearchAlertSubscriber — subset of model.SavedSearchSubscriber
// the workflow needs. Local copy avoids the workflow service
// importing the search service's model package.
type SavedSearchAlertSubscriber struct {
	UserID   string   `json:"user_id"`
	Channels []string `json:"channels"`
}

// SavedSearchAlertLoaded is the LoadSavedSearchAlert output.
type SavedSearchAlertLoaded struct {
	TenantID        string                       `json:"tenant_id"`
	Name            string                       `json:"name"`
	Query           string                       `json:"query"`
	Filters         map[string]any               `json:"filters"`
	NotifyEnabled   bool                         `json:"notify_enabled"`
	OwnerUserID     string                       `json:"owner_user_id"`
	OwnerGroupIDs   []string                     `json:"owner_group_ids"`
	LastMatchDocIDs []string                     `json:"last_match_doc_ids"`
	Subscribers     []SavedSearchAlertSubscriber `json:"subscribers"`
}

// LoadSavedSearchAlert fetches the saved-search row + the subscriber
// list in one round-trip. The "owner is implicitly subscribed"
// semantic is applied here so the workflow body stays simple.
func (a *Activities) LoadSavedSearchAlert(ctx context.Context, savedSearchID string) (*SavedSearchAlertLoaded, error) {
	out := &SavedSearchAlertLoaded{}
	var (
		filtersJSON   []byte
		lastMatchJSON []byte
		notify        bool
	)
	row := a.Pool.QueryRow(ctx, `
		SELECT tenant_id::text, user_id::text, name, query,
		       filters, notify,
		       COALESCE(last_match_doc_ids, '[]'::jsonb)
		  FROM saved_searches
		 WHERE id = $1
	`, savedSearchID)
	if err := row.Scan(&out.TenantID, &out.OwnerUserID, &out.Name, &out.Query,
		&filtersJSON, &notify, &lastMatchJSON); err != nil {
		return nil, fmt.Errorf("load saved search %s: %w", savedSearchID, err)
	}
	out.NotifyEnabled = notify
	_ = json.Unmarshal(filtersJSON, &out.Filters)
	_ = json.Unmarshal(lastMatchJSON, &out.LastMatchDocIDs)

	rows, err := a.Pool.Query(ctx, `
		SELECT user_id::text, channels
		  FROM saved_search_subscribers
		 WHERE saved_search_id = $1
	`, savedSearchID)
	if err != nil {
		return nil, fmt.Errorf("load subscribers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s SavedSearchAlertSubscriber
		if err := rows.Scan(&s.UserID, &s.Channels); err != nil {
			return nil, err
		}
		out.Subscribers = append(out.Subscribers, s)
	}
	// Owner is implicitly subscribed when alert is enabled. Add to
	// the list IFF not already explicit. Default channels: in_app.
	ownerInList := false
	for _, s := range out.Subscribers {
		if s.UserID == out.OwnerUserID {
			ownerInList = true
			break
		}
	}
	if !ownerInList && notify {
		out.Subscribers = append(out.Subscribers, SavedSearchAlertSubscriber{
			UserID:   out.OwnerUserID,
			Channels: []string{"in_app"},
		})
	}
	return out, nil
}

// RunSavedSearchInput drives RunSavedSearchAlert.
type RunSavedSearchInput struct {
	SavedSearchID string         `json:"saved_search_id"`
	TenantID      string         `json:"tenant_id"`
	OwnerUserID   string         `json:"owner_user_id"`
	OwnerGroupIDs []string       `json:"owner_group_ids"`
	Query         string         `json:"query"`
	Filters       map[string]any `json:"filters"`
}

// RunSavedSearchAlert calls POST /api/v1/search on the search
// service, scoped to the OWNER's identity (so an alert sees the
// docs the owner could see — a subscriber doesn't gain access via
// the alert). Returns the matched doc_ids.
func (a *Activities) RunSavedSearchAlert(ctx context.Context, in RunSavedSearchInput) ([]string, error) {
	searchURL := a.ServiceURLs["search"]
	if searchURL == "" {
		return nil, errors.New("search URL not configured")
	}
	body := map[string]any{
		"query":     in.Query,
		"filters":   in.Filters,
		"page_size": 100,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		searchURL+"/api/v1/search", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// Workflow service runs in the same trust zone as search; the
	// gateway-secret round-trip is just satisfying the search
	// middleware.
	req.Header.Set("X-Gateway-Signature", os.Getenv("VAULTDMS_GATEWAY_SECRET"))
	req.Header.Set("X-Auth-Tenant-ID", in.TenantID)
	req.Header.Set("X-User-ID", in.OwnerUserID)
	if len(in.OwnerGroupIDs) > 0 {
		req.Header.Set("X-Group-IDs", joinAlertCSV(in.OwnerGroupIDs))
	} else {
		req.Header.Set("X-Group-IDs", "everyone")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search call: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("search returned %d: %s", resp.StatusCode, string(body))
	}
	var parsed struct {
		Results []struct {
			DocumentID string `json:"document_id"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(parsed.Results))
	for _, r := range parsed.Results {
		if r.DocumentID != "" {
			out = append(out, r.DocumentID)
		}
	}
	return out, nil
}

// EmitMatchInput drives EmitSavedSearchMatch.
type EmitMatchInput struct {
	TenantID        string   `json:"tenant_id"`
	SavedSearchID   string   `json:"saved_search_id"`
	SavedSearchName string   `json:"saved_search_name"`
	SubscriberID    string   `json:"subscriber_id"`
	Channel         string   `json:"channel"`
	MatchedDocIDs   []string `json:"matched_doc_ids"`
}

// EmitSavedSearchMatch publishes dms.notify.saved_search_match.v1
// to JetStream. The CloudEvents `data` block is shaped to match the
// notification service's DeliveryPayload struct so its existing
// `dms.notify.>` consumer routes the in-app/email/digest delivery
// without any notification-service-side change.
//
// Channel preference is per-subscriber (one event per pairing),
// surfaced in the in-app notification's resource_type so a future
// digest worker can group across multiple matches.
func (a *Activities) EmitSavedSearchMatch(ctx context.Context, in EmitMatchInput) error {
	if a.JS == nil {
		return errors.New("jetstream not configured on activities")
	}
	count := len(in.MatchedDocIDs)
	plural := "matches"
	if count == 1 {
		plural = "match"
	}
	title := fmt.Sprintf("New %s for %q", plural, in.SavedSearchName)
	body := fmt.Sprintf("%d new %s in your saved search", count, plural)

	envelope := map[string]any{
		"specversion": "1.0",
		"id":          uuid.New().String(),
		"source":      "dms.workflow",
		"type":        "dms.notify.saved_search_match.v1",
		"subject":     "saved_search/" + in.SavedSearchID,
		"time":        time.Now().UTC().Format(time.RFC3339),
		// DeliveryPayload-shaped — notification service consumes this
		// directly. saved_search_id rides along as resource_id so the
		// in-app row can deep-link back to the saved-search detail.
		"data": map[string]any{
			"tenant_id":     in.TenantID,
			"user_ids":      []string{in.SubscriberID},
			"type":          "saved_search_match",
			"title":         title,
			"body":          body,
			"resource_type": "saved_search",
			"resource_id":   in.SavedSearchID,
			// Sidecar fields below the DeliveryPayload schema —
			// preserved verbatim by the notification service's
			// fallback unmarshal but not required by it. A future
			// digest worker reads matched_doc_ids to group + dedupe.
			"channel":         in.Channel,
			"matched_doc_ids": in.MatchedDocIDs,
		},
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	_, err = a.JS.Publish("dms.notify.saved_search_match.v1", payload)
	return err
}

// UpdateCursorInput drives UpdateSavedSearchAlertCursor.
type UpdateCursorInput struct {
	SavedSearchID   string   `json:"saved_search_id"`
	LastMatchDocIDs []string `json:"last_match_doc_ids"`
}

// UpdateSavedSearchAlertCursor writes last_match_doc_ids + last_run_at
// at the end of each tick.
func (a *Activities) UpdateSavedSearchAlertCursor(ctx context.Context, in UpdateCursorInput) error {
	b, err := json.Marshal(in.LastMatchDocIDs)
	if err != nil {
		return err
	}
	_, err = a.Pool.Exec(ctx,
		`UPDATE saved_searches
		    SET last_match_doc_ids = $2,
		        last_run_at        = now()
		  WHERE id = $1`, in.SavedSearchID, b)
	return err
}

// joinAlertCSV is a local helper — comma-joined string for the
// X-Group-IDs header. Inline to keep this file self-contained.
func joinAlertCSV(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += "," + p
	}
	return out
}
