// Package opensearch wraps OpenSearch for search indexing and querying.
// All tenant-scoped operations use routing=tenant_id. We use raw HTTP
// rather than opensearch-go's client surface (v3 API changed between
// patches) — this makes the module dependency optional.
package opensearch

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/services/search/internal/model"
)

// Config for creating the client.
type Config struct {
	URL      string
	Username string
	Password string
	Insecure bool
	Logger   zerolog.Logger
}

// ---- raw HTTP transport ---------------------------------------------------

type rawClient struct {
	base string
	user string
	pass string
	hc   *http.Client
}

func newRawClient(url, user, pass string, insecure bool) *rawClient {
	return &rawClient{
		base: strings.TrimRight(url, "/"),
		user: user,
		pass: pass,
		hc: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}, //nolint:gosec
			},
		},
	}
}

func (rc *rawClient) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rc.base+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if rc.user != "" {
		req.SetBasicAuth(rc.user, rc.pass)
	}
	return rc.hc.Do(req)
}

func (rc *rawClient) doJSON(ctx context.Context, method, path string, body any) (map[string]any, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	resp, err := rc.do(ctx, method, path, r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("opensearch %s %s: %d %s", method, path, resp.StatusCode, string(raw[:min(len(raw), 500)]))
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out, nil
}

// ---- RealClient -----------------------------------------------------------

// RealClient is the production OpenSearch client. Domain methods live here.
type RealClient struct {
	raw *rawClient
	log zerolog.Logger
}

// NewReal creates a RealClient and bootstraps the index template.
func NewReal(ctx context.Context, cfg Config) (*RealClient, error) {
	rc := &RealClient{
		raw: newRawClient(cfg.URL, cfg.Username, cfg.Password, cfg.Insecure),
		log: cfg.Logger,
	}
	if err := rc.EnsureTemplate(ctx); err != nil {
		return nil, fmt.Errorf("ensure template: %w", err)
	}
	return rc, nil
}

// EnsureTemplate creates or updates the composable index template.
func (c *RealClient) EnsureTemplate(ctx context.Context) error {
	resp, err := c.raw.do(ctx, http.MethodPut,
		"/_index_template/"+TemplateName,
		strings.NewReader(TemplateJSON))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("put template: %d", resp.StatusCode)
	}
	c.log.Info().Str("template", TemplateName).Msg("index template applied")
	return nil
}

// EnsureIndex creates the per-tenant index if it doesn't exist.
func (c *RealClient) EnsureIndex(ctx context.Context, tenantID string) error {
	idx := indexName(tenantID)
	resp, err := c.raw.do(ctx, http.MethodHead, "/"+idx, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		return nil
	}
	_, err = c.raw.doJSON(ctx, http.MethodPut, "/"+idx, nil)
	return err
}

// Index upserts a document into OpenSearch with routing=tenant_id.
func (c *RealClient) Index(ctx context.Context, doc *model.IndexDocument) error {
	idx := indexName(doc.TenantID)
	if err := c.EnsureIndex(ctx, doc.TenantID); err != nil {
		c.log.Warn().Err(err).Str("index", idx).Msg("ensure index")
	}
	path := fmt.Sprintf("/%s/_doc/%s?routing=%s", idx, doc.DocumentID, doc.TenantID)
	_, err := c.raw.doJSON(ctx, http.MethodPut, path, doc)
	return err
}

// PartialUpdate applies a partial update to an existing document.
func (c *RealClient) PartialUpdate(ctx context.Context, tenantID, documentID string, fields map[string]any) error {
	idx := indexName(tenantID)
	path := fmt.Sprintf("/%s/_update/%s?routing=%s", idx, documentID, tenantID)
	_, err := c.raw.doJSON(ctx, http.MethodPost, path, map[string]any{"doc": fields})
	return err
}

// Delete removes a document from the index.
func (c *RealClient) Delete(ctx context.Context, tenantID, documentID string) error {
	idx := indexName(tenantID)
	path := fmt.Sprintf("/%s/_doc/%s?routing=%s", idx, documentID, tenantID)
	resp, err := c.raw.do(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("delete %s: %d", path, resp.StatusCode)
	}
	return nil
}

// Search runs the full query and returns parsed hits + facets.
func (c *RealClient) Search(ctx context.Context, tenantID string, query map[string]any) (*RawSearchResult, error) {
	idx := indexName(tenantID)
	path := fmt.Sprintf("/%s/_search?routing=%s", idx, tenantID)
	result, err := c.raw.doJSON(ctx, http.MethodPost, path, query)
	if err != nil {
		return nil, err
	}
	return parseSearchResult(result)
}

// UpdateByQuery runs an update-by-query for bulk field changes (e.g.
// permission propagation).
func (c *RealClient) UpdateByQuery(ctx context.Context, tenantID string, query map[string]any) error {
	idx := indexName(tenantID)
	path := fmt.Sprintf("/%s/_update_by_query?routing=%s&conflicts=proceed", idx, tenantID)
	_, err := c.raw.doJSON(ctx, http.MethodPost, path, query)
	return err
}

// DeleteByQuery bulk-deletes documents matching query. Wave 12.4
// uses this for GDPR subject erase — the DSR workflow calls the
// search service's purge-subject endpoint, which fans out to this
// method with `{"query": {"term": {"created_by": subjectID}}}`.
//
// conflicts=proceed mirrors UpdateByQuery so long-running erases
// don't abort on concurrent writes (e.g. a late indexing batch).
// Returns the `deleted` count from OpenSearch's response so the
// workflow can audit row counts in the privacy ledger.
func (c *RealClient) DeleteByQuery(ctx context.Context, tenantID string, query map[string]any) (int64, error) {
	idx := indexName(tenantID)
	path := fmt.Sprintf("/%s/_delete_by_query?routing=%s&conflicts=proceed", idx, tenantID)
	result, err := c.raw.doJSON(ctx, http.MethodPost, path, query)
	if err != nil {
		return 0, err
	}
	if v, ok := result["deleted"].(float64); ok {
		return int64(v), nil
	}
	return 0, nil
}

// ---- helpers --------------------------------------------------------------

func indexName(tenantID string) string {
	return "dms-documents-" + tenantID
}

// RawSearchResult is the parsed OpenSearch _search response.
type RawSearchResult struct {
	TotalHits int64
	Hits      []RawHit
	Aggs      map[string][]RawBucket
}

// RawHit is a single search hit.
type RawHit struct {
	ID        string
	Score     float64
	Source    map[string]any
	Highlight map[string][]string
}

// RawBucket is a single aggregation bucket.
type RawBucket struct {
	Key      string
	DocCount int64
}

func parseSearchResult(raw map[string]any) (*RawSearchResult, error) {
	out := &RawSearchResult{Aggs: make(map[string][]RawBucket)}

	hits, _ := raw["hits"].(map[string]any)
	if total, ok := hits["total"].(map[string]any); ok {
		if v, ok := total["value"].(float64); ok {
			out.TotalHits = int64(v)
		}
	}

	hitList, _ := hits["hits"].([]any)
	for _, h := range hitList {
		hm, _ := h.(map[string]any)
		rh := RawHit{
			ID:     strVal(hm, "_id"),
			Source: mapVal(hm, "_source"),
		}
		if s, ok := hm["_score"].(float64); ok {
			rh.Score = s
		}
		if hl, ok := hm["highlight"].(map[string]any); ok {
			rh.Highlight = make(map[string][]string)
			for k, v := range hl {
				if arr, ok := v.([]any); ok {
					for _, a := range arr {
						if s, ok := a.(string); ok {
							rh.Highlight[k] = append(rh.Highlight[k], s)
						}
					}
				}
			}
		}
		out.Hits = append(out.Hits, rh)
	}

	aggs, _ := raw["aggregations"].(map[string]any)
	for k, v := range aggs {
		aggMap, _ := v.(map[string]any)
		buckets, _ := aggMap["buckets"].([]any)
		for _, b := range buckets {
			bm, _ := b.(map[string]any)
			rb := RawBucket{Key: fmt.Sprint(bm["key"])}
			if dc, ok := bm["doc_count"].(float64); ok {
				rb.DocCount = int64(dc)
			}
			out.Aggs[k] = append(out.Aggs[k], rb)
		}
	}
	return out, nil
}

func strVal(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func mapVal(m map[string]any, key string) map[string]any {
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return nil
}
