// compare_handler — cross-format document compare (ADR 0101 §18 F8).
//
// POST /api/v1/compare
//   Body: {doc_a_id, version_a_id?, doc_b_id, version_b_id?, granularity?}
//   Auth: SessionAuth (caller's tenant comes from ctx).
//
// Resolves canonical text per side from `ocr_results`, runs
// diff-match-patch (paragraph-level for Phase 1), returns the diff
// operations + a small summary. No persistence — comparisons are
// one-shot.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sergi/go-diff/diffmatchpatch"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
)

const (
	maxCompareChars = 200_000 // per side; beyond this diff is unusable
	defaultGran     = "paragraph"
)

type CompareHandler struct{ pool *pgxpool.Pool }

func NewCompareHandler(pool *pgxpool.Pool) *CompareHandler { return &CompareHandler{pool: pool} }

func (h *CompareHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/compare", h.compare)
}

type compareReq struct {
	DocAID      string `json:"doc_a_id"`
	VersionAID  string `json:"version_a_id,omitempty"`
	DocBID      string `json:"doc_b_id"`
	VersionBID  string `json:"version_b_id,omitempty"`
	Granularity string `json:"granularity,omitempty"`
}

type compareSideMeta struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	VersionID string `json:"version_id"`
	TextChars int    `json:"text_chars"`
}

type compareOp struct {
	Op   string `json:"op"`   // "equal" | "insert" | "delete"
	Text string `json:"text"`
}

type compareSummary struct {
	AddedChars    int `json:"added_chars"`
	RemovedChars  int `json:"removed_chars"`
	ChangedBlocks int `json:"changed_blocks"`
}

type compareResp struct {
	DocA        compareSideMeta `json:"doc_a"`
	DocB        compareSideMeta `json:"doc_b"`
	Granularity string          `json:"granularity"`
	Diff        struct {
		Operations []compareOp `json:"operations"`
	} `json:"diff"`
	Summary   compareSummary `json:"summary"`
	Truncated bool           `json:"truncated"`
}

func (h *CompareHandler) compare(w http.ResponseWriter, r *http.Request) {
	tid, err := auth.GetTenantID(r.Context())
	if err != nil || tid == uuid.Nil {
		writeJSON(w, 401, map[string]string{"error": "no tenant"})
		return
	}
	var in compareReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid json"})
		return
	}
	docA, err := uuid.Parse(in.DocAID)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "doc_a_id not a uuid"})
		return
	}
	docB, err := uuid.Parse(in.DocBID)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "doc_b_id not a uuid"})
		return
	}
	if docA == docB && in.VersionAID == in.VersionBID {
		writeJSON(w, 400, map[string]string{"error": "comparing a document to itself"})
		return
	}
	gran := in.Granularity
	if gran == "" {
		gran = defaultGran
	}
	if gran != "paragraph" {
		writeJSON(w, 400, map[string]string{"error": "Phase 1 supports granularity=paragraph only"})
		return
	}

	var resp compareResp
	resp.Granularity = gran

	err = database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		var err error
		var (
			textA, textB string
			truncA, truncB bool
		)
		resp.DocA, textA, truncA, err = loadCanonicalText(r.Context(), tx, tid, docA, in.VersionAID, "a")
		if err != nil {
			return err
		}
		resp.DocB, textB, truncB, err = loadCanonicalText(r.Context(), tx, tid, docB, in.VersionBID, "b")
		if err != nil {
			return err
		}
		resp.Truncated = truncA || truncB

		dmp := diffmatchpatch.New()
		// LinesToChars: hash each line so diff-match-patch operates on
		// the line stream (much faster + paragraph-level natural).
		// CharsToLines reverses the encoding before we serialise.
		chA, chB, lines := dmp.DiffLinesToChars(textA, textB)
		diffs := dmp.DiffMain(chA, chB, false)
		diffs = dmp.DiffCharsToLines(diffs, lines)
		diffs = dmp.DiffCleanupSemantic(diffs)

		ops := make([]compareOp, 0, len(diffs))
		var added, removed, changedBlocks int
		for _, d := range diffs {
			switch d.Type {
			case diffmatchpatch.DiffEqual:
				ops = append(ops, compareOp{Op: "equal", Text: d.Text})
			case diffmatchpatch.DiffInsert:
				ops = append(ops, compareOp{Op: "insert", Text: d.Text})
				added += len(d.Text)
				changedBlocks++
			case diffmatchpatch.DiffDelete:
				ops = append(ops, compareOp{Op: "delete", Text: d.Text})
				removed += len(d.Text)
				changedBlocks++
			}
		}
		resp.Diff.Operations = ops
		resp.Summary = compareSummary{
			AddedChars:    added,
			RemovedChars:  removed,
			ChangedBlocks: changedBlocks,
		}
		return nil
	})
	if err != nil {
		// Categorize: side-specific "no canonical text" comes through as
		// a typed sentinel below; everything else is 500 + slog.
		var nt *noCanonicalTextErr
		if errors.As(err, &nt) {
			writeJSON(w, 422, map[string]string{"side": nt.side, "reason": "no_canonical_text"})
			return
		}
		slog.Error("compare failed", "tenant", tid, "doc_a", docA, "doc_b", docB, "err", err.Error())
		writeJSON(w, 500, map[string]string{"error": "compare", "detail": err.Error()})
		return
	}
	writeJSON(w, 200, resp)
}

type noCanonicalTextErr struct{ side string }

func (e *noCanonicalTextErr) Error() string { return "no canonical text for side " + e.side }

// loadCanonicalText returns the document meta + concatenated OCR text
// for the named document, optionally pinned to a specific version_id.
// `side` is "a" or "b" — propagated into noCanonicalTextErr so the
// frontend can tell the user which side lacks canonical text.
// Truncates at maxCompareChars and signals that via the bool.
func loadCanonicalText(
	ctx context.Context,
	tx pgx.Tx,
	tid uuid.UUID,
	docID uuid.UUID,
	versionIDOverride string,
	side string,
) (compareSideMeta, string, bool, error) {
	if versionIDOverride == "" && docID == uuid.Nil {
		// can't happen for either side at this point; defensive
		return compareSideMeta{}, "", false, errors.New("invalid input")
	}

	// 1. Resolve version_id.
	var verID uuid.UUID
	if versionIDOverride != "" {
		var err error
		verID, err = uuid.Parse(versionIDOverride)
		if err != nil {
			return compareSideMeta{}, "", false, err
		}
	} else {
		// default to current_version_id on the document row
		if err := tx.QueryRow(ctx,
			`SELECT current_version_id FROM documents WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`,
			tid, docID,
		).Scan(&verID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return compareSideMeta{}, "", false, &noCanonicalTextErr{side: side}
			}
			return compareSideMeta{}, "", false, err
		}
		if verID == uuid.Nil {
			return compareSideMeta{}, "", false, &noCanonicalTextErr{side: side}
		}
	}

	// 2. Doc title.
	var title string
	if err := tx.QueryRow(ctx,
		`SELECT title FROM documents WHERE tenant_id = $1 AND id = $2`,
		tid, docID,
	).Scan(&title); err != nil {
		return compareSideMeta{}, "", false, err
	}

	// 3. Concatenate OCR text by page.
	rows, err := tx.Query(ctx, `
		SELECT text_content FROM ocr_results
		WHERE tenant_id = $1 AND version_id = $2
		ORDER BY page_number ASC`, tid, verID)
	if err != nil {
		return compareSideMeta{}, "", false, err
	}
	defer rows.Close()

	var sb strings.Builder
	hasAny := false
	truncated := false
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return compareSideMeta{}, "", false, err
		}
		hasAny = true
		// Trim trailing whitespace per line (minimal Phase 1 normalisation).
		t = strings.TrimRight(t, " \t\r\n")
		if sb.Len()+len(t)+2 > maxCompareChars {
			remaining := maxCompareChars - sb.Len()
			if remaining > 0 {
				sb.WriteString(t[:remaining])
			}
			truncated = true
			break
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(t)
	}
	if err := rows.Err(); err != nil {
		return compareSideMeta{}, "", false, err
	}
	if !hasAny {
		return compareSideMeta{}, "", false, &noCanonicalTextErr{side: side}
	}

	text := sb.String()
	return compareSideMeta{
		ID:        docID.String(),
		Title:     title,
		VersionID: verID.String(),
		TextChars: len(text),
	}, text, truncated, nil
}

