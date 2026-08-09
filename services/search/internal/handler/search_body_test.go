// POST /api/v1/search body decoding: aliases honoured, unknown
// top-level fields rejected, date bounds parsed rather than dropped.
//
// The reported defect: {"mode":"hybrid"} — the spelling the GET variant
// of this same endpoint uses — was silently ignored and the caller got a
// lexical answer with a 200 and no signal that its parameter had been
// dropped.
package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func decode(t *testing.T, body string) (searchRequestBody, error) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/search", bytes.NewBufferString(body))
	return decodeSearchBody(r)
}

func TestDecodeSearchBody_HonorsCanonicalFields(t *testing.T) {
	got, err := decode(t, `{"query":"invoice","search_mode":"hybrid","page_size":25,"highlight":true}`)
	require.NoError(t, err)
	require.Equal(t, "invoice", got.Query)
	require.Equal(t, "hybrid", got.SearchMode)
	require.Equal(t, 25, got.PageSize)
	require.True(t, got.Highlight)
}

// TestDecodeSearchBody_HonorsModeAlias is the reported bug: `mode` is how
// GET /api/v1/search spells the ranker, so the POST shape of the same
// endpoint must not silently drop it.
func TestDecodeSearchBody_HonorsModeAlias(t *testing.T) {
	got, err := decode(t, `{"query":"invoice","mode":"hybrid"}`)
	require.NoError(t, err)
	require.Equal(t, "hybrid", got.SearchMode,
		`{"mode":"hybrid"} must not degrade to a silent lexical search`)
}

func TestDecodeSearchBody_HonorsQueryAndLimitAliases(t *testing.T) {
	got, err := decode(t, `{"q":"invoice","limit":5}`)
	require.NoError(t, err)
	require.Equal(t, "invoice", got.Query, "`q` is the GET spelling of `query`")
	require.Equal(t, 5, got.PageSize, "`limit` is the page-size spelling used by the MCP tool")
}

func TestDecodeSearchBody_CanonicalWinsOverAlias(t *testing.T) {
	got, err := decode(t, `{"query":"canonical","q":"alias"}`)
	require.NoError(t, err)
	require.Equal(t, "canonical", got.Query)
}

func TestDecodeSearchBody_RejectsUnknownField(t *testing.T) {
	_, err := decode(t, `{"query":"invoice","serch_mode":"hybrid"}`)
	require.Error(t, err, "a typo must not be swallowed with a 200")
	require.Contains(t, err.Error(), "serch_mode", "the 400 must name the offending field")
	require.Contains(t, err.Error(), "search_mode", "the 400 should list the accepted names")
}

func TestDecodeSearchBody_ReportsEveryUnknownField(t *testing.T) {
	_, err := decode(t, `{"foo":1,"bar":2}`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "bar")
	require.Contains(t, err.Error(), "foo")
}

// Nested `filters` keys stay lenient on purpose — the saved-search alert
// activity replays saved_searches.filters verbatim, and that JSONB was
// written from a struct with no json tags (Go field names). Rejecting
// those would 400 every alert run.
func TestDecodeSearchBody_NestedFilterKeysStayLenient(t *testing.T) {
	got, err := decode(t, `{"query":"x","filters":{"WorkspaceID":"ws-1","tags":["a"]}}`)
	require.NoError(t, err)
	require.Equal(t, []string{"a"}, got.Filters.Tags)
}

func TestDecodeSearchBody_InvalidJSON(t *testing.T) {
	_, err := decode(t, `{"query":`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid json")
}

func TestDecodeSearchBody_RejectsOversizeBody(t *testing.T) {
	big := `{"query":"` + strings.Repeat("x", maxSearchBodyBytes) + `"}`
	_, err := decode(t, big)
	require.Error(t, err)
	require.Contains(t, err.Error(), "too large")
}

func TestParseFilterTime(t *testing.T) {
	cases := []struct {
		in   string
		ok   bool
		want string
	}{
		{"2026-12-31T00:00:00Z", true, "2026-12-31T00:00:00Z"},
		// A bare date is what a date picker emits; it used to be dropped
		// silently, which looked exactly like "filter matched everything".
		{"2026-12-31", true, "2026-12-31T00:00:00Z"},
		{" 2026-12-31 ", true, "2026-12-31T00:00:00Z"},
		{"31/12/2026", false, ""},
		{"tomorrow", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseFilterTime(tc.in)
			if !tc.ok {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got.UTC().Format("2006-01-02T15:04:05Z"))
		})
	}
}

// The GET variant accepted the bracket syntax only; flat created_after /
// created_before params were parsed by nothing and dropped in silence.
func TestParseSearchRequestFromURL_FlatDateBounds(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet,
		"/api/v1/search?q=invoice&created_after=2026-12-31&created_before=2020-01-01", nil)
	req := parseSearchRequestFromURL(r)
	require.NotNil(t, req.Filters.CreatedAfter)
	require.NotNil(t, req.Filters.CreatedBefore)
	require.Equal(t, 2026, req.Filters.CreatedAfter.Year())
	require.Equal(t, 2020, req.Filters.CreatedBefore.Year())
}

func TestParseSearchRequestFromURL_ModeIsHonored(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/search?q=x&mode=hybrid", nil)
	require.Equal(t, "hybrid", parseSearchRequestFromURL(r).Mode)
}
