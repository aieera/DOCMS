// ADR 0066 — comments + reactions REST surface.
//
//	POST   /api/v1/documents/{id}/comments                — create top-level
//	POST   /api/v1/comments/{cid}/replies                  — create reply
//	GET    /api/v1/documents/{id}/comments                 — list (?include_resolved=true to include)
//	PATCH  /api/v1/comments/{cid}                          — edit body
//	DELETE /api/v1/comments/{cid}                          — soft-delete
//	POST   /api/v1/comments/{cid}/resolve                  — mark resolved
//	POST   /api/v1/comments/{cid}/unresolve                — clear resolved
//	POST   /api/v1/comments/{cid}/reactions  {emoji}       — add (idempotent)
//	DELETE /api/v1/comments/{cid}/reactions  {emoji}       — remove (idempotent)
//	GET    /api/v1/comments/{cid}/reactions                — rolled up by emoji
//
// Real-time fan-out is via the existing collaboration WS service —
// it consumes the dms.comment.* NATS subjects this layer's outbox
// emits.
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/repository"
	"github.com/vaultdms/vaultdms/services/document/internal/service"
)

// CommentsHandler mounts every /comments/* route.
type CommentsHandler struct {
	svc *service.DocumentService
	log zerolog.Logger
}

// NewCommentsHandler constructs the handler.
func NewCommentsHandler(svc *service.DocumentService, log zerolog.Logger) *CommentsHandler {
	return &CommentsHandler{svc: svc, log: log}
}

// Register mounts the routes onto a mux.
func (h *CommentsHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/documents/{id}/comments",   h.create)
	mux.HandleFunc("GET /api/v1/documents/{id}/comments",    h.list)
	mux.HandleFunc("POST /api/v1/comments/{cid}/replies",    h.reply)
	mux.HandleFunc("PATCH /api/v1/comments/{cid}",           h.update)
	mux.HandleFunc("DELETE /api/v1/comments/{cid}",          h.delete)
	mux.HandleFunc("POST /api/v1/comments/{cid}/resolve",    h.resolve)
	mux.HandleFunc("POST /api/v1/comments/{cid}/unresolve",  h.unresolve)
	mux.HandleFunc("POST /api/v1/comments/{cid}/reactions",  h.addReaction)
	mux.HandleFunc("DELETE /api/v1/comments/{cid}/reactions", h.removeReaction)
	mux.HandleFunc("GET /api/v1/comments/{cid}/reactions",   h.listReactions)
}

// ---- bodies -------------------------------------------------------------

type commentCreateBody struct {
	Body      string  `json:"body"`
	VersionID *string `json:"version_id,omitempty"`
}

type commentUpdateBody struct {
	Body string `json:"body"`
}

type reactionBody struct {
	Emoji string `json:"emoji"`
}

// ---- handlers -----------------------------------------------------------

func (h *CommentsHandler) create(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	var body commentCreateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	in := &service.CreateCommentInput{DocumentID: docID, Body: body.Body}
	if body.VersionID != nil && *body.VersionID != "" {
		vid, err := uuid.Parse(*body.VersionID)
		if err != nil {
			writeErr(w, r, vdmserr.Validation("version_id", "invalid uuid"))
			return
		}
		in.VersionID = &vid
	}
	c, err := h.svc.CreateComment(ctx, in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, c)
}

func (h *CommentsHandler) reply(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	parentID, err := uuid.Parse(r.PathValue("cid"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("cid", "invalid uuid"))
		return
	}
	var body commentCreateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	// Reply needs the parent's document_id; the service's
	// GetCommentByID enforces view permission as part of the lookup.
	parent, err := h.svc.GetCommentByID(ctx, parentID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	c, err := h.svc.CreateComment(ctx, &service.CreateCommentInput{
		DocumentID: parent.DocumentID,
		ParentID:   &parentID,
		Body:       body.Body,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, c)
}

func (h *CommentsHandler) list(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	includeResolved := r.URL.Query().Get("include_resolved") == "true"
	out, err := h.svc.ListComments(ctx, docID, includeResolved)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if out == nil {
		out = []repository.Comment{}
	}
	writeJSONStatus(w, http.StatusOK, out)
}

func (h *CommentsHandler) update(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("cid"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("cid", "invalid uuid"))
		return
	}
	var body commentUpdateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	c, err := h.svc.UpdateComment(ctx, id, body.Body)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, c)
}

func (h *CommentsHandler) delete(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("cid"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("cid", "invalid uuid"))
		return
	}
	if err := h.svc.SoftDeleteComment(ctx, id); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *CommentsHandler) resolve(w http.ResponseWriter, r *http.Request) {
	h.setResolved(w, r, true)
}

func (h *CommentsHandler) unresolve(w http.ResponseWriter, r *http.Request) {
	h.setResolved(w, r, false)
}

func (h *CommentsHandler) setResolved(w http.ResponseWriter, r *http.Request, resolved bool) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("cid"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("cid", "invalid uuid"))
		return
	}
	if err := h.svc.SetCommentResolved(ctx, id, resolved); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *CommentsHandler) addReaction(w http.ResponseWriter, r *http.Request) {
	h.toggleReaction(w, r, true)
}

func (h *CommentsHandler) removeReaction(w http.ResponseWriter, r *http.Request) {
	h.toggleReaction(w, r, false)
}

func (h *CommentsHandler) toggleReaction(w http.ResponseWriter, r *http.Request, add bool) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	cid, err := uuid.Parse(r.PathValue("cid"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("cid", "invalid uuid"))
		return
	}
	var body reactionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if add {
		err = h.svc.AddReaction(ctx, cid, body.Emoji)
	} else {
		err = h.svc.RemoveReaction(ctx, cid, body.Emoji)
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listReactions returns the rolled-up shape the FE renders directly:
// one entry per emoji with the count + the user_id list.
type reactionAggregate struct {
	Emoji string   `json:"emoji"`
	Count int      `json:"count"`
	Users []string `json:"users"`
}

func (h *CommentsHandler) listReactions(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	cid, err := uuid.Parse(r.PathValue("cid"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("cid", "invalid uuid"))
		return
	}
	rows, err := h.svc.ListReactions(ctx, cid)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := AggregateReactions(rows)
	writeJSONStatus(w, http.StatusOK, out)
}

// AggregateReactions rolls a flat reaction list up by emoji.
// Exported so the service-layer tests can pin the aggregation
// invariant without going through the HTTP layer.
func AggregateReactions(rows []repository.CommentReaction) []reactionAggregate {
	by := map[string]*reactionAggregate{}
	order := []string{}
	for _, r := range rows {
		agg, ok := by[r.Emoji]
		if !ok {
			agg = &reactionAggregate{Emoji: r.Emoji}
			by[r.Emoji] = agg
			order = append(order, r.Emoji)
		}
		agg.Count++
		agg.Users = append(agg.Users, r.UserID.String())
	}
	out := make([]reactionAggregate, 0, len(order))
	for _, e := range order {
		out = append(out, *by[e])
	}
	return out
}

