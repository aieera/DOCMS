package handler

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// POST /api/v1/saved-searches accepted a `filters` object and dropped it:
// the saved search (and every alert run from it) matched the bare query
// over the whole tenant. Every filter field must reach the persisted row.
func TestCreateSavedSearch_PersistsFilters(t *testing.T) {
	var body createSavedSearchBody
	require.NoError(t, json.Unmarshal([]byte(`{
		"name": "contracts", "query": "renewal",
		"filters": {
			"workspace_id": "ws-1", "folder_id": "f-1",
			"document_class": ["contract"], "lifecycle_state": ["active"],
			"tags": ["legal"], "mime_type": ["application/pdf"],
			"created_after": "2026-01-01", "created_before": "2026-06-30T12:00:00Z",
			"size_min_bytes": 10, "size_max_bytes": 1000,
			"created_by": "u-1", "created_by_name": ["Alice"], "region_pin": ["eu-west-1"],
			"custom_metadata": {"client": "acme"}, "has_content": true
		}}`), &body))

	ss, err := newSavedSearch("t-1", "u-1", &body)
	require.NoError(t, err)
	f := ss.Filters
	require.Equal(t, "ws-1", f.WorkspaceID)
	require.Equal(t, "f-1", f.FolderID)
	require.Equal(t, []string{"contract"}, f.DocumentClass)
	require.Equal(t, []string{"active"}, f.LifecycleState)
	require.Equal(t, []string{"legal"}, f.Tags)
	require.Equal(t, []string{"application/pdf"}, f.MimeType)
	require.NotNil(t, f.CreatedAfter)
	require.True(t, f.CreatedAfter.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	require.NotNil(t, f.CreatedBefore)
	require.True(t, f.CreatedBefore.Equal(time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)))
	require.EqualValues(t, 10, *f.SizeMinBytes)
	require.EqualValues(t, 1000, *f.SizeMaxBytes)
	require.Equal(t, "u-1", f.CreatedBy)
	require.Equal(t, []string{"Alice"}, f.CreatedByName)
	require.Equal(t, []string{"eu-west-1"}, f.RegionPin)
	require.Equal(t, map[string]string{"client": "acme"}, f.CustomMetadata)
	require.True(t, *f.HasContent)

	body.Filters.CreatedAfter = "not-a-date"
	_, err = newSavedSearch("t-1", "u-1", &body)
	require.Error(t, err, "a malformed date must be rejected, not silently dropped")
}
