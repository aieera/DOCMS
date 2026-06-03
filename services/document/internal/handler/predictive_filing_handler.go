// predictive_filing_handler — predictive filing suggestions (ADR 0102 §18 F9).
//
// Two endpoints:
//
//	POST /api/v1/uploads/predict
//	  Body: {filename, mime_type, workspace_id?, sha256?}
//	  Returns: {prediction_id, classification, suggested_folder, suggested_tags}
//
//	POST /api/v1/uploads/predict/feedback
//	  Body: feedback shape from the ADR
//	  204 No Content
//
// Phase 1 uses a frequency heuristic for folder suggestion. The
// classification + tag predictions are left as placeholders: real
// values come from the intelligence service's classify + auto-tag
// tasks AFTER OCR. Phase 1 returns filename-keyword-based guesses
// pre-OCR; a refined prediction lands when the doc finishes OCR
// (frontend doesn't yet subscribe to that refinement — Phase 2).
package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
)

type PredictiveFilingHandler struct{ pool *pgxpool.Pool }

func NewPredictiveFilingHandler(pool *pgxpool.Pool) *PredictiveFilingHandler {
	return &PredictiveFilingHandler{pool: pool}
}

func (h *PredictiveFilingHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/uploads/predict", h.predict)
	mux.HandleFunc("POST /api/v1/uploads/predict/feedback", h.feedback)
}

// ---- types --------------------------------------------------------

type predictReq struct {
	Filename    string `json:"filename"`
	MimeType    string `json:"mime_type"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
}

type classification struct {
	PredictedClass string  `json:"predicted_class"`
	Confidence     float64 `json:"confidence"`
}

type suggestedFolder struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Score  float64 `json:"score"`
	Reason string  `json:"reason"`
}

type suggestedTag struct {
	Tag        string  `json:"tag"`
	Confidence float64 `json:"confidence"`
}

type predictResp struct {
	PredictionID    string           `json:"prediction_id"`
	Classification  classification   `json:"classification"`
	SuggestedFolder *suggestedFolder `json:"suggested_folder,omitempty"`
	SuggestedTags   []suggestedTag   `json:"suggested_tags"`
}

// ---- predict ------------------------------------------------------

func (h *PredictiveFilingHandler) predict(w http.ResponseWriter, r *http.Request) {
	tid, err := auth.GetTenantID(r.Context())
	if err != nil || tid == uuid.Nil {
		writeJSON(w, 401, map[string]string{"error": "no tenant"})
		return
	}
	var in predictReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid json"})
		return
	}
	if in.Filename == "" {
		writeJSON(w, 400, map[string]string{"error": "filename required"})
		return
	}

	predictionID := uuid.New()
	cls, tags := filenameHeuristics(in.Filename, in.MimeType)

	var folder *suggestedFolder
	if err := database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		f, e := suggestFolder(r.Context(), tx, tid, cls.PredictedClass, extractTagNames(tags))
		folder = f
		return e
	}); err != nil {
		slog.Error("predictive filing: folder suggest failed",
			"tenant", tid, "filename", in.Filename, "err", err.Error())
		// Suggestion failure is non-fatal — fall back to no-folder.
		folder = nil
	}

	writeJSON(w, 200, predictResp{
		PredictionID:    predictionID.String(),
		Classification:  cls,
		SuggestedFolder: folder,
		SuggestedTags:   tags,
	})
}

// filenameHeuristics is the pre-OCR best-guess. Refined by the
// intelligence-service classify task after OCR completes; ADR 0102
// Phase 2 wires a callback so the frontend can swap the suggestion
// when the better answer lands.
//
// Strategy: simple keyword match against a small set of known classes.
// Returns (classification, top tags). Tags are confidence-scored.
func filenameHeuristics(filename, mime string) (classification, []suggestedTag) {
	low := strings.ToLower(filename)
	// Order matters: more specific terms first so "invoice for contract
	// review" gets classified as invoice, not contract.
	for _, rule := range filenameClassRules {
		if rule.re.MatchString(low) {
			return classification{
				PredictedClass: rule.class,
				Confidence:     rule.confidence,
			}, deriveTags(low, rule.class, mime)
		}
	}
	return classification{
		PredictedClass: "general",
		Confidence:     0.3,
	}, deriveTags(low, "general", mime)
}

type fnClassRule struct {
	re         *regexp.Regexp
	class      string
	confidence float64
}

var filenameClassRules = []fnClassRule{
	{regexp.MustCompile(`\b(invoice|inv-?\d|bill)\b`), "invoice", 0.7},
	{regexp.MustCompile(`\b(receipt|payment)\b`), "receipt", 0.7},
	{regexp.MustCompile(`\b(msa|nda|sla|dpa|contract|agreement|terms)\b`), "contract", 0.75},
	{regexp.MustCompile(`\b(amendment|addendum|exhibit|schedule)\b`), "contract_amendment", 0.7},
	{regexp.MustCompile(`\b(policy|policies|handbook|guidelines)\b`), "policy", 0.65},
	{regexp.MustCompile(`\b(report|q[1-4]|quarterly|annual)\b`), "report", 0.6},
	{regexp.MustCompile(`\b(presentation|slides|deck)\b`), "presentation", 0.65},
	{regexp.MustCompile(`\b(memo|minutes|notes)\b`), "internal_memo", 0.55},
}

func deriveTags(filename, class, mime string) []suggestedTag {
	tags := make([]suggestedTag, 0, 6)
	tagSet := map[string]float64{}
	add := func(t string, c float64) {
		t = strings.ToLower(t)
		if t == "" {
			return
		}
		if existing, ok := tagSet[t]; !ok || c > existing {
			tagSet[t] = c
		}
	}

	// Class itself becomes a tag with the class confidence.
	if class != "" && class != "general" {
		add(class, 0.6)
	}
	// Year tokens.
	if m := regexp.MustCompile(`\b(20\d{2})\b`).FindString(filename); m != "" {
		add(m, 0.85)
	}
	// Common known parties / vendors / departments (heuristic; pre-OCR).
	// Limited list — Phase 2 reads the tenant's historical tag vocab
	// and matches against the filename rather than this hard-coded set.
	for _, kw := range []string{"acme", "vendor", "client", "internal", "legal", "hr", "finance", "ops"} {
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(kw) + `\b`).MatchString(filename) {
			add(kw, 0.55)
		}
	}
	// MIME-derived hints.
	if strings.Contains(mime, "spreadsheet") || strings.HasSuffix(filename, ".xlsx") || strings.HasSuffix(filename, ".csv") {
		add("spreadsheet", 0.7)
	}
	if strings.Contains(mime, "presentation") {
		add("presentation", 0.7)
	}

	for t, c := range tagSet {
		tags = append(tags, suggestedTag{Tag: t, Confidence: c})
	}
	// Stable sort by confidence desc — Go's range over map is random,
	// so the unsorted list jitters between requests. The frontend
	// trusts the ordering for "accept top-N" UX.
	sortTagsByConfidence(tags)
	if len(tags) > 6 {
		tags = tags[:6]
	}
	return tags
}

func sortTagsByConfidence(tags []suggestedTag) {
	// Simple insertion sort — n <= 8 in practice.
	for i := 1; i < len(tags); i++ {
		for j := i; j > 0 && tags[j-1].Confidence < tags[j].Confidence; j-- {
			tags[j-1], tags[j] = tags[j], tags[j-1]
		}
	}
}

func extractTagNames(tags []suggestedTag) []string {
	out := make([]string, len(tags))
	for i, t := range tags {
		out[i] = t.Tag
	}
	return out
}

// suggestFolder is the Phase 1 frequency heuristic.
func suggestFolder(
	ctx context.Context,
	tx pgx.Tx,
	tid uuid.UUID,
	class string,
	tags []string,
) (*suggestedFolder, error) {
	// Minimum signal threshold: at least 5 historical docs in the
	// winning folder. Below this we'd be making things up.
	const minHits = 5

	type row struct {
		folderID uuid.UUID
		name     string
		hits     int
		total    int
	}

	rows, err := tx.Query(ctx, `
		WITH candidate_docs AS (
		    SELECT d.folder_id
		      FROM documents d
		     WHERE d.tenant_id = $1
		       AND d.deleted_at IS NULL
		       AND (d.document_class = $2 OR d.tags && $3::text[])
		),
		folder_hits AS (
		    SELECT folder_id, COUNT(*) AS hits FROM candidate_docs
		    GROUP BY folder_id
		),
		total AS (
		    SELECT COALESCE(SUM(hits), 0) AS t FROM folder_hits
		)
		SELECT fh.folder_id, f.name, fh.hits, total.t
		  FROM folder_hits fh
		  JOIN folders f ON f.tenant_id = $1 AND f.id = fh.folder_id
		  CROSS JOIN total
		  WHERE f.deleted_at IS NULL
		  ORDER BY fh.hits DESC
		  LIMIT 1`,
		tid, class, tags)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil
	}
	var r row
	if err := rows.Scan(&r.folderID, &r.name, &r.hits, &r.total); err != nil {
		return nil, err
	}
	if r.hits < minHits || r.total == 0 {
		return nil, nil
	}
	score := float64(r.hits) / float64(r.total)
	reason := buildReason(class, tags, r.hits, score)
	return &suggestedFolder{
		ID:     r.folderID.String(),
		Name:   r.name,
		Score:  round2(score),
		Reason: reason,
	}, nil
}

func buildReason(class string, tags []string, hits int, score float64) string {
	if class != "" && class != "general" {
		return fmtPct(score) + "% of documents classified as '" + class + "' are filed here"
	}
	if len(tags) > 0 {
		return "Documents tagged with '" + tags[0] + "' frequently file here (" + itoa(hits) + " matches)"
	}
	return itoa(hits) + " similar documents live here"
}

// ---- feedback -----------------------------------------------------

type feedbackReq struct {
	PredictionID         string   `json:"prediction_id"`
	ClassAccepted        bool     `json:"class_accepted"`
	FolderAccepted       bool     `json:"folder_accepted"`
	TagsAccepted         []string `json:"tags_accepted"`
	TagsRejected         []string `json:"tags_rejected"`
	FinalClass           string   `json:"final_class"`
	FinalFolderID        string   `json:"final_folder_id,omitempty"`
	FinalTags            []string `json:"final_tags"`
	// Pass back the original predicted values too so we can store
	// the audit row without a roundtrip back to /predict — they came
	// from the same response just moments ago.
	PredictedClass       string   `json:"predicted_class"`
	PredictedClassScore  float64  `json:"predicted_class_score"`
	PredictedFolderID    string   `json:"predicted_folder_id,omitempty"`
	PredictedFolderScore float64  `json:"predicted_folder_score"`
	PredictedTags        []string `json:"predicted_tags"`
	// Context
	Filename             string `json:"filename"`
	MimeType             string `json:"mime_type"`
	WorkspaceID          string `json:"workspace_id,omitempty"`
}

func (h *PredictiveFilingHandler) feedback(w http.ResponseWriter, r *http.Request) {
	tid, err := auth.GetTenantID(r.Context())
	if err != nil || tid == uuid.Nil {
		writeJSON(w, 401, map[string]string{"error": "no tenant"})
		return
	}
	uid, uerr := tenantOwnerOrFail(r, w)
	if uerr != nil {
		return
	}
	var in feedbackReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid json"})
		return
	}
	pid, err := uuid.Parse(in.PredictionID)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "prediction_id not a uuid"})
		return
	}
	var predFolder, finalFolder *uuid.UUID
	if in.PredictedFolderID != "" {
		if id, err := uuid.Parse(in.PredictedFolderID); err == nil {
			predFolder = &id
		}
	}
	if in.FinalFolderID != "" {
		if id, err := uuid.Parse(in.FinalFolderID); err == nil {
			finalFolder = &id
		}
	}
	var wsID *uuid.UUID
	if in.WorkspaceID != "" {
		if id, err := uuid.Parse(in.WorkspaceID); err == nil {
			wsID = &id
		}
	}

	err = database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		_, e := tx.Exec(r.Context(), `
			INSERT INTO filing_decisions
				(tenant_id, user_id, prediction_id,
				 predicted_class, predicted_class_score,
				 predicted_folder_id, predicted_folder_score, predicted_tags,
				 final_class, final_folder_id, final_tags,
				 class_accepted, folder_accepted,
				 tags_accept_count, tags_reject_count,
				 filename, mime_type, workspace_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
			ON CONFLICT (tenant_id, prediction_id) DO UPDATE
			SET final_class       = EXCLUDED.final_class,
			    final_folder_id   = EXCLUDED.final_folder_id,
			    final_tags        = EXCLUDED.final_tags,
			    class_accepted    = EXCLUDED.class_accepted,
			    folder_accepted   = EXCLUDED.folder_accepted,
			    tags_accept_count = EXCLUDED.tags_accept_count,
			    tags_reject_count = EXCLUDED.tags_reject_count`,
			tid, uid, pid,
			nullableText(in.PredictedClass), float32(in.PredictedClassScore),
			predFolder, float32(in.PredictedFolderScore), in.PredictedTags,
			nullableText(in.FinalClass), finalFolder, in.FinalTags,
			in.ClassAccepted, in.FolderAccepted,
			len(in.TagsAccepted), len(in.TagsRejected),
			in.Filename, in.MimeType, wsID,
		)
		return e
	})
	if err != nil {
		slog.Error("filing feedback insert failed", "tenant", tid, "pred", pid, "err", err.Error())
		writeJSON(w, 500, map[string]string{"error": "feedback", "detail": err.Error()})
		return
	}
	w.WriteHeader(204)
}

// ---- helpers ------------------------------------------------------

func nullableText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

func fmtPct(score float64) string {
	return itoa(int(score*100 + 0.5))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits [20]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	out := string(digits[i:])
	if neg {
		out = "-" + out
	}
	return out
}

