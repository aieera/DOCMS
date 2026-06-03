// ADR 0082 — GET /api/v1/search URL syntax.
//
// Spec form (§7.2):
//   /search?q=X&facet=tag,author&filter=tag:contract&filter=author:alice
//
// `filter=` is a repeating param, key:value, with bracket notation
// reserved for ranges:
//   filter=created_at:[2026-01-01,2026-04-30]
//   filter=size:[100000,]
//
// This file owns the URL → SearchRequest mapping. The result feeds
// the same service.Search() entry point as the POST /search body —
// only parsing differs.
package handler

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/search/internal/model"
)

// filterKeyToFilters routes a single `filter=key:value` pair into
// the correct field on SearchFilters. Unknown keys are folded into
// CustomMetadata so a tenant can use any indexed JSONB field as a
// filter without a code change. Range-bracket values on `created_at`
// and `size` are parsed inline.
func filterKeyToFilters(key, value string, f *model.SearchFilters) {
	switch key {
	case "tag":
		f.Tags = append(f.Tags, value)
	case "author", "created_by_name":
		// `created_by_name` is the indexed display-name field; the
		// §7.2 "author" facet maps to it. Routes through the new
		// CreatedByName slice on SearchFilters so the OpenSearch
		// `terms` filter uses the top-level field directly.
		f.CreatedByName = append(f.CreatedByName, value)
	case "classification", "document_class":
		f.DocumentClass = append(f.DocumentClass, value)
	case "lifecycle_state":
		f.LifecycleState = append(f.LifecycleState, value)
	case "doc_type", "content_type", "mime_type":
		f.MimeType = append(f.MimeType, value)
	case "workspace_id":
		f.WorkspaceID = value
	case "folder_id":
		f.FolderID = value
	case "created_by":
		f.CreatedBy = value
	case "created_at":
		// Bracket-notation date range: [from, to]. Either side may
		// be empty for open-ended.
		from, to, ok := parseRangeBrackets(value)
		if !ok {
			return
		}
		if from != "" {
			if t, err := time.Parse(time.RFC3339, ensureDateTime(from)); err == nil {
				f.CreatedAfter = &t
			}
		}
		if to != "" {
			if t, err := time.Parse(time.RFC3339, ensureDateTime(to)); err == nil {
				f.CreatedBefore = &t
			}
		}
	case "size":
		from, to, ok := parseRangeBrackets(value)
		if !ok {
			return
		}
		if from != "" {
			if n, err := strconv.ParseInt(from, 10, 64); err == nil {
				f.SizeMinBytes = &n
			}
		}
		if to != "" {
			if n, err := strconv.ParseInt(to, 10, 64); err == nil {
				f.SizeMaxBytes = &n
			}
		}
	case "region_pin":
		// region_pin is an indexed keyword field — routed through
		// the dedicated RegionPin slice rather than CustomMetadata
		// (which prefixes `custom_metadata.` and would miss the
		// top-level field).
		f.RegionPin = append(f.RegionPin, value)
	default:
		// Unknown key — treat as a custom-metadata field. Lets a
		// tenant filter on `custom:contract_value` style fields
		// without per-field code changes here.
		if strings.HasPrefix(key, "custom_") || strings.HasPrefix(key, "custom:") {
			key = strings.TrimPrefix(strings.TrimPrefix(key, "custom_"), "custom:")
		}
		if f.CustomMetadata == nil {
			f.CustomMetadata = map[string]string{}
		}
		f.CustomMetadata[key] = value
	}
}

// parseRangeBrackets unpacks `[from,to]` (either side optional) into
// (from, to, ok). Returns ok=false on malformed input so the caller
// can drop the filter rather than 4xx the whole request — a stale
// client URL shouldn't break search.
func parseRangeBrackets(v string) (string, string, bool) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "[") || !strings.HasSuffix(v, "]") {
		return "", "", false
	}
	inner := v[1 : len(v)-1]
	parts := strings.SplitN(inner, ",", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
}

// ensureDateTime promotes a bare YYYY-MM-DD to a full RFC3339
// timestamp at midnight UTC so downstream parsing works with one
// shape only.
func ensureDateTime(s string) string {
	if len(s) == 10 && s[4] == '-' && s[7] == '-' {
		return s + "T00:00:00Z"
	}
	return s
}

// parseSearchRequestFromURL builds a model.SearchRequest from the
// querystring. Identity is read from the SessionAuth-populated ctx
// (FIX-1 follow-up — Kong strips X-Auth-Tenant-ID and X-User-ID at
// the edge, so the previous header reads silently yielded empty
// strings, which the OpenSearch routing then concatenated into the
// nonsensical "dms-documents-" index name).
func parseSearchRequestFromURL(r *http.Request) *model.SearchRequest {
	q := r.URL.Query()

	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	groups := splitHeader(r.Header.Get("X-Group-IDs"))

	// share_token via header is the production shape (gateway-injected
	// from the public URL); query-param accepted as a fallback for
	// dev + integration testing.
	shareToken := r.Header.Get("X-Share-Token")
	if shareToken == "" {
		shareToken = q.Get("share_token")
	}
	req := &model.SearchRequest{
		TenantID:   tenantID,
		UserID:     userID,
		GroupIDs:   groups,
		ShareToken: shareToken,
		Query:      q.Get("q"),
		Mode:       model.NormalizeMode(q.Get("mode")),
		SortBy:     q.Get("sort_by"),
		SortOrder:  q.Get("sort_order"),
		PageToken:  q.Get("page_token"),
	}
	if v, err := strconv.Atoi(q.Get("page_size")); err == nil {
		req.PageSize = v
	}
	if q.Get("highlight") == "true" || q.Get("highlight") == "1" {
		req.Highlight = true
	}

	// `facet=tag,author&facet=region_pin` — both shapes accepted.
	for _, raw := range q["facet"] {
		for _, name := range strings.Split(raw, ",") {
			name = strings.TrimSpace(name)
			if name != "" {
				req.Facets = append(req.Facets, name)
			}
		}
	}

	for _, raw := range q["filter"] {
		key, value, ok := splitKeyValue(raw)
		if !ok {
			continue
		}
		filterKeyToFilters(key, value, &req.Filters)
	}

	return req
}

// splitKeyValue splits "key:value" on the FIRST colon so values
// can themselves contain colons (e.g. ISO timestamps inside range
// brackets — `created_at:[2026-01-01T00:00:00Z,...]`). Returns
// ok=false on missing colon.
func splitKeyValue(s string) (string, string, bool) {
	idx := strings.Index(s, ":")
	if idx < 1 || idx == len(s)-1 {
		return "", "", false
	}
	return s[:idx], s[idx+1:], true
}
