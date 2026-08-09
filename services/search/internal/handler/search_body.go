// POST /api/v1/search body decoding.
//
// Why this is not a plain json.Decode: the endpoint used to ignore any
// key it didn't recognise. A client that sent {"mode":"hybrid"} — the
// spelling the GET variant of this SAME endpoint uses — got a silent
// lexical answer with an HTTP 200, and no way to tell that its
// parameter had been dropped. Same for a mistyped filter or page size.
//
// Two changes fix that class:
//
//  1. Documented synonyms are HONOURED (searchBodyAliases), so the GET
//     and POST spellings of one endpoint agree.
//  2. Any other unrecognised TOP-LEVEL key is REJECTED with 400 naming
//     the field — matching how the auth and policy services decode
//     bodies (dec.DisallowUnknownFields → validation error).
//
// Strictness stops at the top level on purpose. `filters` is decoded
// leniently because two existing producers ship keys this struct does
// not declare: the saved-search alert activity replays the
// saved_searches.filters JSONB verbatim, and that column was written by
// json.Marshal(model.SearchFilters) — a struct with NO json tags, so the
// keys are Go field names ("WorkspaceID", "Tags", …). Rejecting those
// would 400 every saved-search alert. They are silently ignored today
// and still are; see the report notes.
package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// maxSearchBodyBytes mirrors the 64 KiB cap the auth and policy services
// put on decoded request bodies.
const maxSearchBodyBytes = 64 * 1024

// searchBodyAliases maps accepted synonyms onto the canonical POST field
// name. `q` and `mode` are the spellings GET /api/v1/search uses for the
// query string and the ranker mode (see url_search.go), so accepting them
// here makes one endpoint behave the same in both shapes. `limit` is the
// page-size spelling used by the MCP server's search_documents tool.
var searchBodyAliases = map[string]string{
	"q":     "query",
	"mode":  "search_mode",
	"limit": "page_size",
}

// searchBodyFields is the set of canonical top-level keys, derived from
// the struct tags so it cannot drift from searchRequestBody.
var searchBodyFields = map[string]struct{}{
	"query": {}, "filters": {}, "facets": {}, "sort_by": {}, "sort_order": {},
	"page_size": {}, "page_token": {}, "search_mode": {}, "highlight": {},
	"explain": {}, "share_token": {},
}

// decodeSearchBody reads the POST /search body, resolves aliases, and
// rejects unknown top-level keys. The returned error is already
// caller-facing text (the handler writes it as the 400 body).
func decodeSearchBody(r *http.Request) (searchRequestBody, error) {
	var out searchRequestBody
	if r.ContentLength > maxSearchBodyBytes {
		return out, fmt.Errorf("request body too large (max %d bytes)", maxSearchBodyBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxSearchBodyBytes+1))
	if err != nil {
		return out, fmt.Errorf("invalid json")
	}
	if len(raw) > maxSearchBodyBytes {
		return out, fmt.Errorf("request body too large (max %d bytes)", maxSearchBodyBytes)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return out, fmt.Errorf("invalid json")
	}

	canonical := make(map[string]json.RawMessage, len(top))
	var unknown []string
	for k, v := range top {
		name := k
		if alias, ok := searchBodyAliases[k]; ok {
			name = alias
		}
		if _, ok := searchBodyFields[name]; !ok {
			unknown = append(unknown, k)
			continue
		}
		// An explicit canonical key wins over an alias for the same
		// field, so {"query":"a","q":"b"} is unambiguous.
		if _, taken := canonical[name]; taken && name != k {
			continue
		}
		canonical[name] = v
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return out, fmt.Errorf("unknown field(s): %s — did you mean one of: %s?",
			strings.Join(unknown, ", "), knownSearchFields())
	}

	merged, err := json.Marshal(canonical)
	if err != nil {
		return out, fmt.Errorf("invalid json")
	}
	if err := json.Unmarshal(merged, &out); err != nil {
		return out, fmt.Errorf("invalid json")
	}
	return out, nil
}

// knownSearchFields renders the accepted top-level keys for the 400 body,
// so a caller can fix a typo without reading the source.
func knownSearchFields() string {
	names := make([]string, 0, len(searchBodyFields)+len(searchBodyAliases))
	for k := range searchBodyFields {
		names = append(names, k)
	}
	for k := range searchBodyAliases {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
