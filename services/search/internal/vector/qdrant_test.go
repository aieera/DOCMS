package vector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSemanticSearch_HappyPath pins the two-hop wire contract —
// embed-query returns a vector, then Qdrant search returns points
// with document_id payload. Both upstreams are replaced with
// httptest servers so this runs without intelligence/Qdrant.
func TestSemanticSearch_HappyPath(t *testing.T) {
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body embedReq
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Query != "find me a contract" {
			t.Errorf("embed got unexpected query %q", body.Query)
		}
		_ = json.NewEncoder(w).Encode(embedResp{
			Embedding: []float32{0.1, 0.2, 0.3},
			Dimension: 3,
			Model:     "bge-m3",
		})
	}))
	defer embedSrv.Close()

	qdrantSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Pin: request body carries the tenant filter.
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		filter, _ := body["filter"].(map[string]any)
		must, _ := filter["must"].([]any)
		if len(must) == 0 {
			t.Fatal("qdrant search must carry tenant_id filter")
		}
		// Respond with two chunks of the same doc + one chunk of another.
		_, _ = w.Write([]byte(`{"result":[
			{"score": 0.92, "payload": {"document_id": "doc-a"}},
			{"score": 0.88, "payload": {"document_id": "doc-a"}},
			{"score": 0.70, "payload": {"document_id": "doc-b"}}
		]}`))
	}))
	defer qdrantSrv.Close()

	c, err := New(Config{
		IntelligenceEmbedURL: embedSrv.URL,
		QdrantBaseURL:        qdrantSrv.URL,
		Collection:           "chunks",
	})
	if err != nil {
		t.Fatal(err)
	}

	hits, err := c.SemanticSearch(context.Background(), "find me a contract",
		"tenant-1", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	// Two chunks of doc-a collapse to one hit; doc-b keeps its own hit.
	if len(hits) != 2 || hits[0].DocumentID != "doc-a" || hits[1].DocumentID != "doc-b" {
		t.Errorf("collapsing or ordering wrong: %+v", hits)
	}
	if hits[0].Score != 0.92 {
		t.Errorf("top score should be best of chunks: got %v", hits[0].Score)
	}
}

func TestSemanticSearch_EmptyQueryNoTrip(t *testing.T) {
	// No servers — if the client tries to hit them, DNS/TCP error.
	c, _ := New(Config{IntelligenceEmbedURL: "http://127.0.0.1:1",
		QdrantBaseURL: "http://127.0.0.1:1", Collection: "x"})
	hits, err := c.SemanticSearch(context.Background(), "", "t", nil, 10)
	if err != nil {
		t.Errorf("empty query must be a silent no-op, got error: %v", err)
	}
	if hits != nil {
		t.Errorf("empty query must return nil hits")
	}
}

func TestSemanticSearch_EmbedUpstream500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", 500)
	}))
	defer srv.Close()
	c, _ := New(Config{IntelligenceEmbedURL: srv.URL,
		QdrantBaseURL: srv.URL, Collection: "x"})
	_, err := c.SemanticSearch(context.Background(), "q", "t", nil, 10)
	if err == nil || !strings.Contains(err.Error(), "embed-query") {
		t.Fatalf("want embed-query error, got %v", err)
	}
}

func TestNew_RejectsEmptyConfig(t *testing.T) {
	_, err := New(Config{})
	if err == nil {
		t.Fatal("zero config should error")
	}
}

func TestSemanticSearch_ReadableByFilterPropagated(t *testing.T) {
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(embedResp{Embedding: []float32{1}, Dimension: 1, Model: "x"})
	}))
	defer embedSrv.Close()

	var capturedBody map[string]any
	qdrantSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		_, _ = w.Write([]byte(`{"result":[]}`))
	}))
	defer qdrantSrv.Close()

	c, _ := New(Config{IntelligenceEmbedURL: embedSrv.URL,
		QdrantBaseURL: qdrantSrv.URL, Collection: "c"})
	_, err := c.SemanticSearch(context.Background(), "q", "t",
		[]string{"grp-a", "grp-b"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	filter, _ := capturedBody["filter"].(map[string]any)
	must, _ := filter["must"].([]any)
	if len(must) != 2 {
		t.Fatalf("expected 2 must clauses (tenant + readable_by), got %d", len(must))
	}
}
