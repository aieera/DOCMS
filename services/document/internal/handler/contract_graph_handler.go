// contract_graph_handler — contract intelligence graph (ADR 0099 §18 F4).
//
// Three endpoints:
//
//	GET    /api/v1/contracts/{document_id}/graph?depth=N
//	  — public-to-tenant. Walks the (src→dst) and (dst→src) edges up to
//	    `depth` hops (default 1, cap 3) and returns {nodes, edges}.
//	    Subgraph capped at 500 nodes; over-cap responses include
//	    `"truncated": true` so the UI can offer "expand more."
//
//	POST   /api/v1/contracts/{document_id}/edges
//	  — admin/owner. Body: {target_document_id, edge_type, metadata}.
//	    Idempotent via uq_contract_edges_triplet (UPSERT on conflict).
//
//	DELETE /api/v1/contracts/{document_id}/edges/{edge_id}
//	  — admin/owner. Removes the edge by ID (caller must own the doc
//	    that's the edge's `src_document`, enforced via RLS).
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vaultdms/vaultdms/pkg/auth"
	"github.com/vaultdms/vaultdms/pkg/database"
)

type ContractGraphHandler struct {
	pool *pgxpool.Pool
}

func NewContractGraphHandler(pool *pgxpool.Pool) *ContractGraphHandler {
	return &ContractGraphHandler{pool: pool}
}

func (h *ContractGraphHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/contracts/{document_id}/graph", h.getGraph)
	mux.HandleFunc("POST /api/v1/contracts/{document_id}/edges", h.createEdge)
	mux.HandleFunc("DELETE /api/v1/contracts/{document_id}/edges/{edge_id}", h.deleteEdge)
}

// ---- types ---------------------------------------------------------

type graphNode struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	MimeType   string `json:"mime_type"`
	CreatedAt  string `json:"created_at"`
	IsRoot     bool   `json:"is_root,omitempty"`
}

type graphEdge struct {
	ID         string                 `json:"id"`
	Src        string                 `json:"src"`
	Dst        string                 `json:"dst"`
	Type       string                 `json:"type"`
	Confidence float64                `json:"confidence"`
	Metadata   map[string]any         `json:"metadata"`
	CreatedAt  string                 `json:"created_at"`
}

type graphResp struct {
	RootID    string      `json:"root_id"`
	Nodes     []graphNode `json:"nodes"`
	Edges     []graphEdge `json:"edges"`
	Truncated bool        `json:"truncated"`
}

// ---- getGraph -----------------------------------------------------

func (h *ContractGraphHandler) getGraph(w http.ResponseWriter, r *http.Request) {
	tid, err := auth.GetTenantID(r.Context())
	if err != nil || tid == uuid.Nil {
		writeJSON(w, 401, map[string]string{"error": "no tenant"})
		return
	}
	rootID, err := uuid.Parse(r.PathValue("document_id"))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "document_id not a uuid"})
		return
	}
	depth := 1
	if d, err := strconv.Atoi(r.URL.Query().Get("depth")); err == nil {
		if d < 1 {
			d = 1
		}
		if d > 3 {
			d = 3
		}
		depth = d
	}
	const nodeCap = 500

	resp := graphResp{
		RootID: rootID.String(),
		Nodes:  []graphNode{},
		Edges:  []graphEdge{},
	}

	err = database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		// 1. Collect distinct document IDs reachable within `depth` hops
		//    in either direction. Walking both sides in one CTE is
		//    cheaper than two queries; LIMIT keeps the result bounded.
		ids := []uuid.UUID{rootID}
		// Postgres only allows ONE UNION between the seed and the
		// recursive term, so we collapse the two directions into a
		// single recursive SELECT and use CASE to pick the "other end"
		// of the edge regardless of which side connects to the walked
		// node. RLS on contract_graph_edges (app.current_tenant) is
		// applied via the WithTenantTx context above.
		rows, err := tx.Query(r.Context(), `
			WITH RECURSIVE walk(doc_id, depth) AS (
			    SELECT $1::uuid, 0
			    UNION
			    SELECT
			        CASE WHEN e.src_document = w.doc_id
			             THEN e.dst_document
			             ELSE e.src_document END,
			        w.depth + 1
			      FROM contract_graph_edges e
			      JOIN walk w
			        ON e.src_document = w.doc_id OR e.dst_document = w.doc_id
			     WHERE w.depth < $2
			)
			SELECT DISTINCT doc_id FROM walk LIMIT $3`,
			rootID, depth, nodeCap+1)
		if err != nil {
			return fmt.Errorf("walk: %w", err)
		}
		defer rows.Close()
		seen := map[uuid.UUID]struct{}{rootID: {}}
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(ids) > nodeCap {
			resp.Truncated = true
			ids = ids[:nodeCap]
		}

		// 2. Hydrate node metadata. ANY($1) avoids a per-row roundtrip.
		nodeRows, err := tx.Query(r.Context(), `
			SELECT id, title, COALESCE(mime_type, ''), created_at
			FROM documents
			WHERE id = ANY($1) AND deleted_at IS NULL`,
			ids)
		if err != nil {
			return fmt.Errorf("nodes: %w", err)
		}
		for nodeRows.Next() {
			var n graphNode
			var ts time.Time
			var idStr uuid.UUID
			if err := nodeRows.Scan(&idStr, &n.Title, &n.MimeType, &ts); err != nil {
				nodeRows.Close()
				return err
			}
			n.ID = idStr.String()
			n.CreatedAt = ts.UTC().Format(time.RFC3339)
			n.IsRoot = idStr == rootID
			resp.Nodes = append(resp.Nodes, n)
		}
		nodeRows.Close()

		// 3. All edges between any two of those nodes.
		edgeRows, err := tx.Query(r.Context(), `
			SELECT id, src_document, dst_document, edge_type, confidence,
			       metadata, created_at
			FROM contract_graph_edges
			WHERE src_document = ANY($1) AND dst_document = ANY($1)`,
			ids)
		if err != nil {
			return fmt.Errorf("edges: %w", err)
		}
		defer edgeRows.Close()
		for edgeRows.Next() {
			var (
				id, src, dst uuid.UUID
				edgeType     string
				conf         float32 // Postgres `real` is single-precision; scan into float32
				metaRaw      []byte
				ts           time.Time
			)
			if err := edgeRows.Scan(&id, &src, &dst, &edgeType, &conf, &metaRaw, &ts); err != nil {
				return fmt.Errorf("edge scan: %w", err)
			}
			meta := map[string]any{}
			if len(metaRaw) > 0 {
				_ = json.Unmarshal(metaRaw, &meta)
			}
			resp.Edges = append(resp.Edges, graphEdge{
				ID:         id.String(),
				Src:        src.String(),
				Dst:        dst.String(),
				Type:       edgeType,
				Confidence: float64(conf),
				Metadata:   meta,
				CreatedAt:  ts.UTC().Format(time.RFC3339),
			})
		}
		return edgeRows.Err()
	})
	if err != nil {
		// Surface the actual SQL/scan error to the doc service log so
		// the next 500 isn't a black box; the response body still
		// carries the detail for the API caller.
		slog.Error("contract graph query failed",
			"tenant", tid, "root", rootID, "depth", depth, "err", err.Error())
		writeJSON(w, 500, map[string]string{"error": "graph query", "detail": err.Error()})
		return
	}
	writeJSON(w, 200, resp)
}

// ---- createEdge ---------------------------------------------------

type createEdgeReq struct {
	TargetDocumentID string                 `json:"target_document_id"`
	EdgeType         string                 `json:"edge_type"`
	Metadata         map[string]any         `json:"metadata"`
}

func (h *ContractGraphHandler) createEdge(w http.ResponseWriter, r *http.Request) {
	tid, terr := auth.GetTenantID(r.Context())
	if terr != nil {
		writeJSON(w, 401, map[string]string{"error": "no tenant"})
		return
	}
	uid, err := tenantOwnerOrFail(r, w)
	if err != nil {
		return
	}
	srcID, err := uuid.Parse(r.PathValue("document_id"))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "document_id not a uuid"})
		return
	}
	var in createEdgeReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid json"})
		return
	}
	dstID, err := uuid.Parse(in.TargetDocumentID)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "target_document_id not a uuid"})
		return
	}
	if !validEdgeType(in.EdgeType) {
		writeJSON(w, 400, map[string]string{"error": "edge_type must be amends|supersedes|references"})
		return
	}
	if srcID == dstID {
		writeJSON(w, 400, map[string]string{"error": "self-loop not allowed"})
		return
	}
	metaJSON, _ := json.Marshal(in.Metadata)
	if len(metaJSON) == 0 {
		metaJSON = []byte("{}")
	}

	var edgeID uuid.UUID
	err = database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		// UPSERT on the triplet — re-asserting the same edge from the
		// UI shouldn't create duplicates, and shouldn't fail either.
		return tx.QueryRow(r.Context(), `
			INSERT INTO contract_graph_edges
				(tenant_id, src_document, dst_document, edge_type, confidence, metadata, created_by)
			VALUES ($1, $2, $3, $4, 1.0, $5::jsonb, $6)
			ON CONFLICT (tenant_id, src_document, dst_document, edge_type)
			DO UPDATE SET metadata = EXCLUDED.metadata, created_at = NOW()
			RETURNING id`,
			tid, srcID, dstID, in.EdgeType, metaJSON, uid,
		).Scan(&edgeID)
	})
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "insert", "detail": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"id": edgeID.String()})
}

// ---- deleteEdge ---------------------------------------------------

func (h *ContractGraphHandler) deleteEdge(w http.ResponseWriter, r *http.Request) {
	tid, terr := auth.GetTenantID(r.Context())
	if terr != nil {
		writeJSON(w, 401, map[string]string{"error": "no tenant"})
		return
	}
	edgeID, err := uuid.Parse(r.PathValue("edge_id"))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "edge_id not a uuid"})
		return
	}
	srcID, err := uuid.Parse(r.PathValue("document_id"))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "document_id not a uuid"})
		return
	}
	err = database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		ct, e := tx.Exec(r.Context(),
			`DELETE FROM contract_graph_edges
			 WHERE tenant_id = $1 AND id = $2 AND src_document = $3`,
			tid, edgeID, srcID)
		if e != nil {
			return e
		}
		if ct.RowsAffected() == 0 {
			return errors.New("not_found")
		}
		return nil
	})
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(204)
}

// ---- helpers ------------------------------------------------------

func validEdgeType(s string) bool {
	switch s {
	case "amends", "supersedes", "references":
		return true
	}
	return false
}
