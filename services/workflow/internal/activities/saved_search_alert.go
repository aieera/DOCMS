// ADR 0085 — saved-search alert activities.
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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/database"
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

// LoadSavedSearchAlertInput identifies the alert to load. TenantID is
// REQUIRED: the tables are FORCE RLS, so the read must run under the
// tenant's app.current_tenant (Wave A.1.a, issue #70 — the raw-pool
// version of this read returned no rows in prod and alerts never
// fired). Tenant rides in the Temporal schedule args, so the workflow
// always has it before the first DB touch.
type LoadSavedSearchAlertInput struct {
	SavedSearchID string `json:"saved_search_id"`
	TenantID      string `json:"tenant_id"`
}

// LoadSavedSearchAlert fetches the saved-search row + the subscriber
// list in one tenant-scoped transaction. The "owner is implicitly
// subscribed" semantic is applied here so the workflow body stays
// simple.
func (a *Activities) LoadSavedSearchAlert(ctx context.Context, in LoadSavedSearchAlertInput) (*SavedSearchAlertLoaded, error) {
	out := &SavedSearchAlertLoaded{}
	var notify bool
	err := a.runTenant(ctx, in.TenantID, func(tx pgx.Tx) error {
		var (
			filtersJSON   []byte
			lastMatchJSON []byte
		)
		row := tx.QueryRow(ctx, `
			SELECT tenant_id::text, user_id::text, name, query,
			       filters, notify,
			       COALESCE(last_match_doc_ids, '[]'::jsonb)
			  FROM saved_searches
			 WHERE id = $1
		`, in.SavedSearchID)
		if err := row.Scan(&out.TenantID, &out.OwnerUserID, &out.Name, &out.Query,
			&filtersJSON, &notify, &lastMatchJSON); err != nil {
			return fmt.Errorf("load saved search %s: %w", in.SavedSearchID, err)
		}
		out.NotifyEnabled = notify
		_ = json.Unmarshal(filtersJSON, &out.Filters)
		_ = json.Unmarshal(lastMatchJSON, &out.LastMatchDocIDs)

		rows, err := tx.Query(ctx, `
			SELECT user_id::text, channels
			  FROM saved_search_subscribers
			 WHERE saved_search_id = $1
		`, in.SavedSearchID)
		if err != nil {
			return fmt.Errorf("load subscribers: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var s SavedSearchAlertSubscriber
			if err := rows.Scan(&s.UserID, &s.Channels); err != nil {
				return err
			}
			out.Subscribers = append(out.Subscribers, s)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
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
	req.Header.Set("X-Gateway-Signature", os.Getenv("SEDOC_GATEWAY_SECRET"))
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
	Channels        []string `json:"channels"`
	MatchedDocIDs   []string `json:"matched_doc_ids"`
}

// EmitSavedSearchMatch inserts dms.notify.saved_search_match.v1 into
// the transactional OUTBOX (§4.7 / C5 — never a direct NATS publish
// from service code). The shared outbox publisher wraps the payload in
// the CloudEvents envelope and ships it; the notification service's
// `dms.notify.>` consumer reads the DeliveryPayload-shaped data block.
//
// One event per SUBSCRIBER, carrying the subscriber's chosen channels
// as the DeliveryPayload `channels` consent hint — the notification
// service delivers on those channels (in-app row exactly once,
// email/digest per choice, still gated by snooze/DND/matrix disables).
func (a *Activities) EmitSavedSearchMatch(ctx context.Context, in EmitMatchInput) error {
	if a.Pool == nil || a.Outbox == nil {
		return errors.New("outbox not configured on activities")
	}
	savedSearchUUID, err := uuid.Parse(in.SavedSearchID)
	if err != nil {
		return fmt.Errorf("saved_search_id: %w", err)
	}
	tenantUUID, err := uuid.Parse(in.TenantID)
	if err != nil {
		return fmt.Errorf("tenant_id: %w", err)
	}
	count := len(in.MatchedDocIDs)
	plural := "matches"
	if count == 1 {
		plural = "match"
	}
	title := fmt.Sprintf("New %s for %q", plural, in.SavedSearchName)
	body := fmt.Sprintf("%d new %s in your saved search", count, plural)

	// DeliveryPayload-shaped — the saved_search_id rides as resource_id
	// so the in-app row deep-links back to the saved search;
	// matched_doc_ids is a sidecar for future grouping UIs.
	data, err := json.Marshal(map[string]any{
		"tenant_id":       in.TenantID,
		"user_ids":        []string{in.SubscriberID},
		"type":            "saved_search_match",
		"title":           title,
		"body":            body,
		"resource_type":   "saved_search",
		"resource_id":     in.SavedSearchID,
		"channels":        in.Channels,
		"matched_doc_ids": in.MatchedDocIDs,
	})
	if err != nil {
		return err
	}
	evt := database.NewOutboxEvent(tenantUUID,
		"dms.notify.saved_search_match.v1", "saved_search", savedSearchUUID, data)
	return a.runTenant(ctx, in.TenantID, func(tx pgx.Tx) error {
		return a.Outbox.Insert(ctx, tx, evt)
	})
}

// UpdateCursorInput drives UpdateSavedSearchAlertCursor. TenantID is
// required — the write runs under the tenant's RLS context (issue #70).
type UpdateCursorInput struct {
	SavedSearchID   string   `json:"saved_search_id"`
	TenantID        string   `json:"tenant_id"`
	LastMatchDocIDs []string `json:"last_match_doc_ids"`
}

// UpdateSavedSearchAlertCursor writes last_match_doc_ids + last_run_at
// at the end of each tick, tenant-scoped.
func (a *Activities) UpdateSavedSearchAlertCursor(ctx context.Context, in UpdateCursorInput) error {
	b, err := json.Marshal(in.LastMatchDocIDs)
	if err != nil {
		return err
	}
	return a.runTenant(ctx, in.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE saved_searches
			    SET last_match_doc_ids = $2,
			        last_run_at        = now()
			  WHERE id = $1`, in.SavedSearchID, b)
		return err
	})
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
