package handler

// Wave 15.4 HTTP surface for saved signature profiles.
//
//   GET    /api/v1/signatures/profiles
//   POST   /api/v1/signatures/profiles                    multipart or JSON
//   PATCH  /api/v1/signatures/profiles/{id}               {name, set_default}
//   DELETE /api/v1/signatures/profiles/{id}
//   GET    /api/v1/signatures/profiles/{id}/image         decrypted bytes

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/google/uuid"

	"github.com/vaultdms/vaultdms/pkg/auth"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/signature/internal/model"
	"github.com/vaultdms/vaultdms/services/signature/internal/service"
)

// ProfileHandler is the REST adapter for profile routes.
type ProfileHandler struct {
	svc *service.ProfileService
}

// NewProfileHandler constructs one.
func NewProfileHandler(svc *service.ProfileService) *ProfileHandler {
	return &ProfileHandler{svc: svc}
}

// Register mounts the profile routes on a chi-compatible mux.
// Uses net/http routing patterns to match the service's existing
// style.
func (h *ProfileHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET    /api/v1/signatures/profiles", h.list)
	mux.HandleFunc("POST   /api/v1/signatures/profiles", h.create)
	mux.HandleFunc("PATCH  /api/v1/signatures/profiles/{id}", h.patch)
	mux.HandleFunc("DELETE /api/v1/signatures/profiles/{id}", h.delete)
	mux.HandleFunc("GET    /api/v1/signatures/profiles/{id}/image", h.image)
}

// extractIdentity reads the authenticated session set by the
// upstream session-auth middleware (pkg/middleware/sessionauth).
// NEVER trust X-Auth-* headers here — they are gateway-injected
// for other services that don't own the session, and an attacker
// that reaches this listener directly could set them to spoof any
// tenant/user. The session-auth middleware populates auth.User(ctx)
// from the cookie-bound, server-validated session; everything on
// this handler routes through it.
func extractIdentity(r *http.Request) (tenantID, userID uuid.UUID, err error) {
	u, err := auth.User(r.Context())
	if err != nil {
		return uuid.Nil, uuid.Nil, vdmserr.ErrUnauthorized
	}
	if u.TenantID == uuid.Nil || u.ID == uuid.Nil {
		return uuid.Nil, uuid.Nil, vdmserr.ErrUnauthorized
	}
	return u.TenantID, u.ID, nil
}

type createProfileBody struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	FontStyle   string `json:"font_style,omitempty"`
	ImageBase64 string `json:"image_base64"` // client-drawn PNG or typed render
	SetDefault  bool   `json:"set_default"`
}

func (h *ProfileHandler) create(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, err := extractIdentity(r)
	if err != nil {
		writeErrProfile(w, err)
		return
	}
	defer r.Body.Close()
	var body createProfileBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 2*service.ProfileMaxBytes)).Decode(&body); err != nil {
		writeErrProfile(w, vdmserr.Validation("body", "invalid json"))
		return
	}
	img, err := base64.StdEncoding.DecodeString(body.ImageBase64)
	if err != nil {
		writeErrProfile(w, vdmserr.Validation("image_base64", "not valid base64"))
		return
	}
	p, err := h.svc.CreateProfile(r.Context(), service.CreateProfileInput{
		TenantID:   tenantID,
		UserID:     userID,
		Name:       body.Name,
		Kind:       model.SignatureProfileKind(body.Kind),
		FontStyle:  body.FontStyle,
		Image:      img,
		SetDefault: body.SetDefault,
	})
	if err != nil {
		writeErrProfile(w, err)
		return
	}
	writeJSONProfile(w, http.StatusCreated, p.ToPublic())
}

func (h *ProfileHandler) list(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, err := extractIdentity(r)
	if err != nil {
		writeErrProfile(w, err)
		return
	}
	list, err := h.svc.List(r.Context(), tenantID, userID)
	if err != nil {
		writeErrProfile(w, err)
		return
	}
	out := make([]model.SignatureProfilePublicView, 0, len(list))
	for _, p := range list {
		out = append(out, p.ToPublic())
	}
	writeJSONProfile(w, http.StatusOK, map[string]any{"profiles": out})
}

type patchProfileBody struct {
	Name       *string `json:"name,omitempty"`
	SetDefault *bool   `json:"set_default,omitempty"`
}

func (h *ProfileHandler) patch(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, err := extractIdentity(r)
	if err != nil {
		writeErrProfile(w, err)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErrProfile(w, vdmserr.Validation("id", "not a uuid"))
		return
	}
	var body patchProfileBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErrProfile(w, vdmserr.Validation("body", "invalid json"))
		return
	}
	if body.Name != nil {
		if err := h.svc.Rename(r.Context(), tenantID, userID, id, *body.Name); err != nil {
			writeErrProfile(w, err)
			return
		}
	}
	if body.SetDefault != nil && *body.SetDefault {
		if err := h.svc.SetDefault(r.Context(), tenantID, userID, id); err != nil {
			writeErrProfile(w, err)
			return
		}
	}
	writeJSONProfile(w, http.StatusOK, map[string]any{"id": id.String(), "updated": true})
}

func (h *ProfileHandler) delete(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, err := extractIdentity(r)
	if err != nil {
		writeErrProfile(w, err)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErrProfile(w, vdmserr.Validation("id", "not a uuid"))
		return
	}
	if err := h.svc.Delete(r.Context(), tenantID, userID, id); err != nil {
		writeErrProfile(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// image returns decrypted bytes. Short-lived presigned URL is the
// brief's preferred mechanism but requires a signed-get path that
// decrypts on read — deferred to a follow-up. Today this endpoint
// just streams the plaintext directly over the session-authenticated
// connection; the response has no caching headers.
func (h *ProfileHandler) image(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, err := extractIdentity(r)
	if err != nil {
		writeErrProfile(w, err)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErrProfile(w, vdmserr.Validation("id", "not a uuid"))
		return
	}
	img, err := h.svc.ResolveImage(r.Context(), tenantID, userID, id)
	if err != nil {
		writeErrProfile(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(img)
}

// ---- wire helpers ---------------------------------------------------------

func writeJSONProfile(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErrProfile(w http.ResponseWriter, err error) {
	var verr *vdmserr.Error
	if !errors.As(err, &verr) {
		writeJSONProfile(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"code": "INTERNAL", "message": "internal error"}})
		return
	}
	code := http.StatusInternalServerError
	switch verr.Kind {
	case vdmserr.KindValidation:
		code = http.StatusBadRequest
	case vdmserr.KindUnauthorized:
		code = http.StatusUnauthorized
	case vdmserr.KindForbidden:
		code = http.StatusForbidden
	case vdmserr.KindNotFound:
		code = http.StatusNotFound
	case vdmserr.KindConflict, vdmserr.KindAlreadyExists:
		code = http.StatusConflict
	}
	writeJSONProfile(w, code, map[string]any{"error": map[string]any{"code": verr.Code, "message": verr.Message}})
}
