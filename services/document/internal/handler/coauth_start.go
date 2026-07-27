// ADR 0065 — POST /api/v1/documents/{id}/versions/{vid}/coauth/start
//
// Mints a WOPI access_token for the calling user, scoped to (tenant,
// user, version_id, expiry, can_write). The frontend embeds this in
// the iframe URL so the editor (OnlyOffice or Collabora) can call
// /wopi/* on our behalf.
//
// Provider selection:
//   - SEDOC_COAUTH_PROVIDER (env): "onlyoffice" | "collabora" | "disabled"
//   - SEDOC_COAUTH_URL (env): base URL of the editor
//
// Per-tenant overrides land later via a tenant_settings row; today
// it's deploy-wide.
package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// CoauthStartHandler mounts /coauth/start.
type CoauthStartHandler struct {
	svc *service.DocumentService
	log zerolog.Logger
}

// NewCoauthStartHandler constructs the handler. svc gates token issuance on
// the caller's per-document view/edit permission.
func NewCoauthStartHandler(svc *service.DocumentService, log zerolog.Logger) *CoauthStartHandler {
	return &CoauthStartHandler{svc: svc, log: log}
}

// Register attaches the route.
func (h *CoauthStartHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/documents/{id}/versions/{vid}/coauth/start", h.start)
}

type coauthStartReq struct {
	Mode string `json:"mode"` // edit | view
}

type coauthStartResp struct {
	IframeURL       string `json:"iframe_url"`
	Provider        string `json:"provider"` // onlyoffice | collabora | disabled
	EditorHealthURL string `json:"editor_health_url"`
	AccessToken     string `json:"access_token"`
	AccessTokenTTL  int64  `json:"access_token_ttl"` // ms
	Mode            string `json:"mode"`
}

func (h *CoauthStartHandler) start(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	versionID, err := uuid.Parse(r.PathValue("vid"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("vid", "invalid uuid"))
		return
	}

	// Authorize BEFORE minting a token: the caller must be able to view the
	// version's document (fail-closed), and a write token is only issued if
	// they also hold edit. Previously start() minted a write-capable token
	// for any tenant version_id with no permission check, enabling WOPI
	// lock-DoS and forged co-auth audit events.
	docID, canEdit, err := h.svc.AuthorizeCoauth(r.Context(), versionID)
	if err != nil {
		writeErr(w, r, err)
		return
	}

	provider := strings.ToLower(strings.TrimSpace(os.Getenv("SEDOC_COAUTH_PROVIDER")))
	if provider == "" {
		provider = "disabled"
	}
	if provider == "disabled" {
		writeJSONStatus(w, http.StatusOK, coauthStartResp{Provider: "disabled"})
		return
	}

	editorBase := strings.TrimRight(os.Getenv("SEDOC_COAUTH_URL"), "/")
	if editorBase == "" {
		writeErr(w, r, vdmserr.Internal("SEDOC_COAUTH_URL not set"))
		return
	}

	secret := os.Getenv("SEDOC_WOPI_SECRET")
	if secret == "" {
		writeErr(w, r, vdmserr.Internal("SEDOC_WOPI_SECRET not set"))
		return
	}

	publicBase := os.Getenv("SEDOC_PUBLIC_URL")
	if publicBase == "" {
		publicBase = "http://localhost:8080"
	}

	var body coauthStartReq
	_ = json.NewDecoder(r.Body).Decode(&body)
	mode := body.Mode
	if mode != "view" {
		mode = "edit"
	}
	// Downgrade to a read-only session when the caller can view but not edit:
	// they co-view, but never receive a write token (which would let them
	// lock the version and block the legitimate editor).
	if mode == "edit" && !canEdit {
		mode = "view"
	}

	ttl := 1 * time.Hour
	claims := WOPIClaims{
		TenantID:   tenantID,
		UserID:     userID,
		FileID:     versionID,
		DocumentID: docID, // lock scope — see WOPIClaims.lockID
		ExpiresAt:  time.Now().Add(ttl),
		CanWrite:   mode == "edit",
	}
	token := IssueWOPIToken(secret, claims)

	// WOPISrc the editor calls back into. Both OnlyOffice (when
	// running in WOPI mode) and Collabora follow the same shape.
	wopiSrc := publicBase + "/wopi/files/" + versionID.String()

	var iframeURL, healthURL string
	switch provider {
	case "collabora":
		// Collabora reads its discovery.xml at boot; the iframe URL
		// shape is /browser/<hash>/cool.html?WOPISrc=...&access_token=...
		// Discovery action URL we serve is fine to drop into here —
		// Collabora replaces <wopisrc> with the encoded host URL.
		iframeURL = fmt.Sprintf("%s/browser/dist/cool.html?WOPISrc=%s&access_token=%s&lang=en",
			editorBase, url.QueryEscape(wopiSrc), url.QueryEscape(token))
		healthURL = editorBase + "/hosting/discovery"
	case "onlyoffice":
		// OnlyOffice in WOPI mode uses /hosting/discovery action
		// URLs. We pass WOPISrc + token; OnlyOffice fetches
		// CheckFileInfo + GetFile from us.
		iframeURL = fmt.Sprintf("%s/hosting/wopi/word/edit?WOPISrc=%s&access_token=%s",
			editorBase, url.QueryEscape(wopiSrc), url.QueryEscape(token))
		healthURL = editorBase + "/healthcheck"
	default:
		writeErr(w, r, vdmserr.Validation("provider", "must be onlyoffice|collabora|disabled"))
		return
	}

	writeJSONStatus(w, http.StatusOK, coauthStartResp{
		IframeURL:       iframeURL,
		Provider:        provider,
		EditorHealthURL: healthURL,
		AccessToken:     token,
		AccessTokenTTL:  ttl.Milliseconds(),
		Mode:            mode,
	})
}
