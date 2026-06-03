// Active-learning admin REST endpoints (ADR 0060).
//
//   GET    /api/v1/admin/models
//   GET    /api/v1/admin/models/{id}
//   POST   /api/v1/admin/models/{id}/promote
//   POST   /api/v1/admin/models/{id}/retire
//   POST   /api/v1/admin/models/retrain
//   GET    /api/v1/admin/training-examples/stats
//   DELETE /api/v1/admin/training-examples/{id}
//   GET    /api/v1/admin/active-learning/config
//   PUT    /api/v1/admin/active-learning/config
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

type ActiveLearningHandler struct {
	svc *service.DocumentService
	log zerolog.Logger
}

func NewActiveLearningHandler(svc *service.DocumentService, log zerolog.Logger) *ActiveLearningHandler {
	return &ActiveLearningHandler{svc: svc, log: log}
}

func (h *ActiveLearningHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/models", h.listVersions)
	mux.HandleFunc("GET /api/v1/admin/models/{id}", h.getVersion)
	mux.HandleFunc("POST /api/v1/admin/models/{id}/promote", h.promote)
	mux.HandleFunc("POST /api/v1/admin/models/{id}/retire", h.retire)
	mux.HandleFunc("POST /api/v1/admin/models/retrain", h.retrain)
	mux.HandleFunc("GET /api/v1/admin/training-examples/stats", h.exampleStats)
	mux.HandleFunc("DELETE /api/v1/admin/training-examples/{id}", h.deleteExample)
	mux.HandleFunc("GET /api/v1/admin/active-learning/config", h.getConfig)
	mux.HandleFunc("PUT /api/v1/admin/active-learning/config", h.upsertConfig)
}

// ---- DTOs ---------------------------------------------------------------

type modelVersionDTO struct {
	ID                    string          `json:"id"`
	ModelType             string          `json:"model_type"`
	VersionTag            string          `json:"version_tag"`
	BaseModel             string          `json:"base_model"`
	S3ArtifactPath        string          `json:"s3_artifact_path"`
	TrainingExamplesCount int32           `json:"training_examples_count"`
	TrainingMetrics       json.RawMessage `json:"training_metrics"`
	EvalMetrics           json.RawMessage `json:"eval_metrics"`
	Status                string          `json:"status"`
	ErrorMessage          string          `json:"error_message,omitempty"`
	PromotedAt            *string         `json:"promoted_at,omitempty"`
	PromotedBy            *string         `json:"promoted_by,omitempty"`
	RetiredAt             *string         `json:"retired_at,omitempty"`
	CreatedAt             string          `json:"created_at"`
	UpdatedAt             string          `json:"updated_at"`
}

type retrainBody struct {
	ModelType string `json:"model_type,omitempty"`
}

type configBody struct {
	Enabled                *bool    `json:"enabled,omitempty"`
	MinExamplesForRetrain  *int32   `json:"min_examples_for_retrain,omitempty"`
	RetrainIncrement       *int32   `json:"retrain_increment,omitempty"`
	AutoPromoteIfBetter    *bool    `json:"auto_promote_if_better,omitempty"`
	MinAccuracyImprovement *float32 `json:"min_accuracy_improvement,omitempty"`
	TrainValidationSplit   *float32 `json:"train_validation_split,omitempty"`
	TrainTestSplit         *float32 `json:"train_test_split,omitempty"`
	GpuQueue               *string  `json:"gpu_queue,omitempty"`
}

type configDTO struct {
	Enabled                bool    `json:"enabled"`
	MinExamplesForRetrain  int32   `json:"min_examples_for_retrain"`
	RetrainIncrement       int32   `json:"retrain_increment"`
	AutoPromoteIfBetter    bool    `json:"auto_promote_if_better"`
	MinAccuracyImprovement float32 `json:"min_accuracy_improvement"`
	TrainValidationSplit   float32 `json:"train_validation_split"`
	TrainTestSplit         float32 `json:"train_test_split"`
	GpuQueue               string  `json:"gpu_queue"`
}

// ---- handlers -----------------------------------------------------------

func (h *ActiveLearningHandler) listVersions(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	q := r.URL.Query()
	opts := repository.ListVersionsOpts{
		ModelType: q.Get("model_type"),
		Status:    q.Get("status"),
		Limit:     int32(parseInt(q.Get("limit"), 50)),
		Offset:    int32(parseInt(q.Get("offset"), 0)),
	}
	ctx := r.Context()
	rows, total, err := h.svc.ListModelVersions(ctx, opts)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := make([]modelVersionDTO, 0, len(rows))
	for _, v := range rows {
		out = append(out, versionToDTO(&v))
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"versions": out, "total": total,
		"limit": opts.Limit, "offset": opts.Offset,
	})
}

func (h *ActiveLearningHandler) getVersion(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	ctx := r.Context()
	v, err := h.svc.GetModelVersion(ctx, id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, versionToDTO(v))
}

func (h *ActiveLearningHandler) promote(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	ctx := r.Context()
	v, err := h.svc.PromoteModel(ctx, id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, versionToDTO(v))
}

func (h *ActiveLearningHandler) retire(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	ctx := r.Context()
	if err := h.svc.RetireModel(ctx, id); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusNoContent, nil)
}

func (h *ActiveLearningHandler) retrain(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	var body retrainBody
	if r.ContentLength > 0 {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	ctx := r.Context()
	if err := h.svc.TriggerRetrain(ctx, body.ModelType); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, map[string]any{"status": "queued"})
}

func (h *ActiveLearningHandler) exampleStats(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	ctx := r.Context()
	st, err := h.svc.TrainingExampleStats(ctx)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	type lc struct {
		Label string `json:"label"`
		Count int64  `json:"count"`
	}
	pl := make([]lc, 0, len(st.PerLabel))
	for _, l := range st.PerLabel {
		pl = append(pl, lc{l.Label, l.Count})
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"total":         st.TotalCount,
		"unused":        st.UnusedCount,
		"per_split":     st.PerSplit,
		"per_label":     pl,
	})
}

func (h *ActiveLearningHandler) deleteExample(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	ctx := r.Context()
	if err := h.svc.DeleteTrainingExample(ctx, id); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusNoContent, nil)
}

func (h *ActiveLearningHandler) getConfig(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	ctx := r.Context()
	c, err := h.svc.GetActiveLearningConfig(ctx)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if c == nil {
		writeJSONStatus(w, http.StatusOK, defaultActiveLearningConfigDTO())
		return
	}
	writeJSONStatus(w, http.StatusOK, configToALDTO(c))
}

func (h *ActiveLearningHandler) upsertConfig(w http.ResponseWriter, r *http.Request) {
	_, _, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	var body configBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	ctx := r.Context()
	c, err := h.svc.UpsertActiveLearningConfig(ctx, repository.ActiveLearningConfigPatch{
		Enabled:                body.Enabled,
		MinExamplesForRetrain:  body.MinExamplesForRetrain,
		RetrainIncrement:       body.RetrainIncrement,
		AutoPromoteIfBetter:    body.AutoPromoteIfBetter,
		MinAccuracyImprovement: body.MinAccuracyImprovement,
		TrainValidationSplit:   body.TrainValidationSplit,
		TrainTestSplit:         body.TrainTestSplit,
		GpuQueue:               body.GpuQueue,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, configToALDTO(c))
}

// ---- DTO converters -----------------------------------------------------

func versionToDTO(v *repository.ModelVersion) modelVersionDTO {
	dto := modelVersionDTO{
		ID:                    v.ID.String(),
		ModelType:             v.ModelType,
		VersionTag:            v.VersionTag,
		BaseModel:             v.BaseModel,
		S3ArtifactPath:        v.S3ArtifactPath,
		TrainingExamplesCount: v.TrainingExamplesCount,
		TrainingMetrics:       v.TrainingMetrics,
		EvalMetrics:           v.EvalMetrics,
		Status:                v.Status,
		ErrorMessage:          v.ErrorMessage,
		CreatedAt:             v.CreatedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
		UpdatedAt:             v.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
	}
	if v.PromotedAt != nil {
		s := v.PromotedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00")
		dto.PromotedAt = &s
	}
	if v.PromotedBy != nil {
		s := v.PromotedBy.String()
		dto.PromotedBy = &s
	}
	if v.RetiredAt != nil {
		s := v.RetiredAt.UTC().Format("2006-01-02T15:04:05.999Z07:00")
		dto.RetiredAt = &s
	}
	return dto
}

func configToALDTO(c *repository.ActiveLearningConfig) configDTO {
	return configDTO{
		Enabled:                c.Enabled,
		MinExamplesForRetrain:  c.MinExamplesForRetrain,
		RetrainIncrement:       c.RetrainIncrement,
		AutoPromoteIfBetter:    c.AutoPromoteIfBetter,
		MinAccuracyImprovement: c.MinAccuracyImprovement,
		TrainValidationSplit:   c.TrainValidationSplit,
		TrainTestSplit:         c.TrainTestSplit,
		GpuQueue:               c.GpuQueue,
	}
}

func defaultActiveLearningConfigDTO() configDTO {
	return configDTO{
		Enabled:                false,
		MinExamplesForRetrain:  50,
		RetrainIncrement:       25,
		AutoPromoteIfBetter:    false,
		MinAccuracyImprovement: 0.02,
		TrainValidationSplit:   0.10,
		TrainTestSplit:         0.10,
		GpuQueue:               "intelligence-gpu",
	}
}
