// Package vector holds the search service's dense-vector query path
// (§7.1 / D6 part 2). Talks HTTP to Qdrant — we don't pull in the
// full github.com/qdrant/go-client gRPC dependency because one
// search query is a flat call with no streaming.
//
// The embedding generator is the intelligence service's
// /internal/v1/embed-query endpoint; this package only carries the
// HTTP clients to call both. Keeping the clients in one package
// makes it obvious they're a pair.
package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Hit is one dense-vector result. Matches the shape the search
// service's hybrid.go consumes — we don't expose Qdrant's full
// response schema because the only thing we use is id + score.
type Hit struct {
	DocumentID string
	Score      float64
}

// Client wraps the two HTTP round-trips a hybrid query needs:
//  1. POST intelligence://internal/v1/embed-query   → []float32
//  2. POST qdrant://collections/<coll>/points/search → []Hit
//
// Zero value is unusable; use New().
// DefaultMinScore is the minimum cosine similarity a chunk must reach
// to count as a semantic match. ANN search always returns the K nearest
// neighbours — with no floor, a nonsense query "matched" every document
// in the tenant at ~0.1 similarity (QA SD-02). Measured on the live
// corpus (all-MiniLM-L6-v2): gibberish-vs-short-content pairs reach
// 0.32-0.33, genuine weak matches start ~0.34-0.40, clear matches sit
// 0.4+. 0.35 excludes the noise band; weak matches it also excludes are
// covered by the lexical leg in hybrid mode. Tune per deployment via
// SEDOC_SEMANTIC_MIN_SCORE.
const DefaultMinScore = 0.35

type Client struct {
	httpc        *http.Client
	embedURL     string // e.g. "http://intelligence:8080/internal/v1/embed-query"
	qdrantBase   string // e.g. "http://qdrant:6333"
	collection   string // e.g. "vaultdms_chunks"
	minScore     float64
}

// Config is the DI shape; each field is required.
type Config struct {
	HTTPClient *http.Client
	// IntelligenceEmbedURL is the full URL (host:port/path) of the
	// embed-query endpoint. Required so the search service doesn't
	// have to know the intelligence service's port mapping.
	IntelligenceEmbedURL string
	QdrantBaseURL        string
	// Collection is the Qdrant collection name. The current
	// intelligence pipeline uses a single shared collection with a
	// `tenant_id` payload filter (blueprint §6.8); tenant-specific
	// collections are a future optimization.
	Collection string
	// MinScore overrides DefaultMinScore when > 0 (env:
	// SEDOC_SEMANTIC_MIN_SCORE in the search service's main).
	MinScore float64
}

// New constructs a Client. A 5-second default timeout is applied when
// HTTPClient is nil — search is latency-sensitive and should never
// block a request-handler goroutine for the OS default (~no timeout).
func New(cfg Config) (*Client, error) {
	if cfg.IntelligenceEmbedURL == "" {
		return nil, errors.New("vector: IntelligenceEmbedURL required")
	}
	if cfg.QdrantBaseURL == "" {
		return nil, errors.New("vector: QdrantBaseURL required")
	}
	if cfg.Collection == "" {
		return nil, errors.New("vector: Collection required")
	}
	httpc := cfg.HTTPClient
	if httpc == nil {
		httpc = &http.Client{Timeout: 5 * time.Second}
	}
	minScore := cfg.MinScore
	if minScore <= 0 {
		minScore = DefaultMinScore
	}
	return &Client{
		httpc:      httpc,
		embedURL:   cfg.IntelligenceEmbedURL,
		qdrantBase: cfg.QdrantBaseURL,
		collection: cfg.Collection,
		minScore:   minScore,
	}, nil
}

// SemanticSearch embeds the query, ANN-queries Qdrant with a
// tenant_id + optional readable_by payload filter, returns up to
// `limit` hits sorted by score desc. Errors surface to the caller;
// the search service's hybrid.go falls back to lexical when this
// returns an error, so reliability > aggressive retries here.
func (c *Client) SemanticSearch(ctx context.Context, query, tenantID string, readableBy []string, limit int) ([]Hit, error) {
	if query == "" || tenantID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}

	vec, err := c.embedQuery(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	return c.qdrantSearch(ctx, vec, tenantID, readableBy, limit)
}

// ---- internal ------------------------------------------------------

type embedReq struct {
	Query string `json:"query"`
}
type embedResp struct {
	Embedding []float32 `json:"embedding"`
	Dimension int       `json:"dimension"`
	Model     string    `json:"model"`
}

func (c *Client) embedQuery(ctx context.Context, q string) ([]float32, error) {
	body, _ := json.Marshal(embedReq{Query: q})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.embedURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embed-query status %d: %s", resp.StatusCode, string(msg))
	}
	var r embedResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	if len(r.Embedding) == 0 {
		return nil, errors.New("embed-query returned empty vector")
	}
	return r.Embedding, nil
}

// qdrantSearch builds Qdrant's POST /collections/<coll>/points/search
// body. Payload filter enforces tenant isolation per §7.3 — without
// this, a user from tenant B could retrieve tenant A's chunks.
func (c *Client) qdrantSearch(ctx context.Context, vec []float32, tenantID string, readableBy []string, limit int) ([]Hit, error) {
	must := []map[string]any{
		{"key": "tenant_id", "match": map[string]any{"value": tenantID}},
	}
	// readable_by filter: any one of the user's groups must match.
	// Blueprint §7.3 expects this as payload.readable_by[] scalar
	// field on each chunk; `match.any` is Qdrant's "one-of" primitive.
	if len(readableBy) > 0 {
		anyVals := make([]any, len(readableBy))
		for i, g := range readableBy {
			anyVals[i] = g
		}
		must = append(must, map[string]any{
			"key": "readable_by",
			"match": map[string]any{"any": anyVals},
		})
	}

	body, _ := json.Marshal(map[string]any{
		"vector": vec,
		"limit":  limit,
		// Relevance floor (QA SD-02): without it ANN returns the K
		// nearest neighbours for ANY query, so nonsense terms matched
		// the whole tenant. Also enforced client-side below.
		"score_threshold": c.minScore,
		"filter":          map[string]any{"must": must},
		// with_payload=false — we only need the point id (document id)
		// and score; the search service hydrates the rest from the
		// OpenSearch _source. Saves ~10 KB of payload round-trip
		// per hit on large documents.
		"with_payload": []string{"document_id"},
	})

	url := fmt.Sprintf("%s/collections/%s/points/search", c.qdrantBase, c.collection)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("qdrant search status %d: %s", resp.StatusCode, string(msg))
	}

	var r struct {
		Result []struct {
			Score   float64        `json:"score"`
			Payload map[string]any `json:"payload"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}

	hits := make([]Hit, 0, len(r.Result))
	seen := make(map[string]bool, len(r.Result))
	for _, p := range r.Result {
		// Belt-and-braces with the score_threshold sent to Qdrant: a
		// backend (or test double) that ignores it must not reintroduce
		// match-everything semantics.
		if p.Score < c.minScore {
			continue
		}
		docID, _ := p.Payload["document_id"].(string)
		if docID == "" || seen[docID] {
			// Collapse multiple chunks of the same document into a
			// single hit with the best score — fusion happens at
			// document granularity.
			continue
		}
		seen[docID] = true
		hits = append(hits, Hit{DocumentID: docID, Score: p.Score})
	}
	return hits, nil
}
