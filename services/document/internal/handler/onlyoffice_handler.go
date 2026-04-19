// §10.3 / E6 — OnlyOffice Document Server integration skeleton.
//
//	GET  /api/v1/documents/{id}/versions/{vid}/onlyoffice/config
//	     → signed editor config JSON that the frontend hands to the
//	       OnlyOffice JS SDK (new DocsAPI.DocEditor(element, config)).
//	POST /api/v1/documents/{id}/versions/{vid}/onlyoffice/callback
//	     → OnlyOffice posts save / status events here. Currently a
//	       minimal `{error: 0}` acknowledgement so the editor is
//	       happy; full download-new-version flow is a follow-up.
//
// Security:
//   - Config JSON is signed with HS256 using VAULTDMS_ONLYOFFICE_JWT.
//     OnlyOffice Document Server is configured with the same secret
//     (JWT_ENABLED=true) and refuses requests whose token signature
//     doesn't match — this is the only thing preventing an attacker
//     from swapping the document URL.
//   - Callback must also verify the JWT OnlyOffice sends back; not
//     enforced in this skeleton (TODO marked).
package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

// OnlyOfficeHandler mounts the E6 endpoints.
type OnlyOfficeHandler struct {
	log zerolog.Logger
}

// NewOnlyOfficeHandler constructs the handler. Secret and URL are
// read per-request from env so tests can override via t.Setenv.
func NewOnlyOfficeHandler(log zerolog.Logger) *OnlyOfficeHandler {
	return &OnlyOfficeHandler{log: log}
}

// Register mounts both routes on a ServeMux.
func (h *OnlyOfficeHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/documents/{id}/versions/{vid}/onlyoffice/config", h.config)
	mux.HandleFunc("POST /api/v1/documents/{id}/versions/{vid}/onlyoffice/callback", h.callback)
}

// ---- config endpoint ----------------------------------------------

type onlyOfficeConfig struct {
	Document   oneOfficeDoc   `json:"document"`
	DocumentType string       `json:"documentType"`
	EditorConfig oneOfficeEd  `json:"editorConfig"`
	Token      string         `json:"token,omitempty"` // JWT of the rest of the config
}

type oneOfficeDoc struct {
	FileType string          `json:"fileType"`
	Key      string          `json:"key"`
	Title    string          `json:"title"`
	URL      string          `json:"url"`
	Permissions map[string]any `json:"permissions,omitempty"`
}

type oneOfficeEd struct {
	Mode     string        `json:"mode"`
	Lang     string        `json:"lang"`
	CallbackURL string     `json:"callbackUrl"`
	User     oneOfficeUser `json:"user"`
}

type oneOfficeUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (h *OnlyOfficeHandler) config(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	versionID, err := uuid.Parse(r.PathValue("vid"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("vid", "invalid uuid"))
		return
	}

	secret := os.Getenv("VAULTDMS_ONLYOFFICE_JWT")
	if secret == "" {
		writeErr(w, r, vdmserr.Internal("onlyoffice not configured"))
		return
	}
	publicBase := os.Getenv("VAULTDMS_PUBLIC_URL")
	if publicBase == "" {
		publicBase = "http://localhost:8080"
	}

	cfg := onlyOfficeConfig{
		Document: oneOfficeDoc{
			FileType: "docx",
			// Document service sets the download URL and the OnlyOffice
			// container fetches it via in-cluster DNS. `key` MUST change
			// when the document content changes so OnlyOffice invalidates
			// its cache — composite of version_id is the canonical choice.
			Key:   versionID.String(),
			Title: "Document " + docID.String()[:8],
			URL:   publicBase + "/api/v1/documents/" + docID.String() + "/versions/" + versionID.String() + "/download",
			Permissions: map[string]any{
				"edit":     true,
				"download": true,
				"comment":  true,
			},
		},
		DocumentType: "word",
		EditorConfig: oneOfficeEd{
			Mode: "edit",
			Lang: "en",
			CallbackURL: publicBase + "/api/v1/documents/" + docID.String() +
				"/versions/" + versionID.String() + "/onlyoffice/callback",
			User: oneOfficeUser{
				ID:   userID.String(),
				Name: "User " + userID.String()[:8],
			},
		},
	}

	// Sign the config with HS256 — required when JWT_ENABLED is
	// true on OnlyOffice Document Server. The token payload is
	// literally the config object; Document Server validates.
	token, err := hs256(cfg, secret)
	if err != nil {
		writeErr(w, r, vdmserr.Internal("sign onlyoffice config"))
		return
	}
	cfg.Token = token

	_ = tenantID // silence: tenant scoping applied via future download URL middleware
	writeJSONStatus(w, http.StatusOK, cfg)
}

// ---- callback endpoint --------------------------------------------

func (h *OnlyOfficeHandler) callback(w http.ResponseWriter, r *http.Request) {
	// OnlyOffice sends status events:
	//   0 = no changes   1 = editing   2 = ready to save
	//   3 = save error   4 = closed unchanged   6 = force save ready
	// Skeleton: acknowledge only; a follow-up PR downloads the new
	// file when status == 2 and runs it through CreateVersion().
	var body struct {
		Status int    `json:"status"`
		URL    string `json:"url"`
		Key    string `json:"key"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	h.log.Info().
		Int("status", body.Status).
		Str("key", body.Key).
		Msg("onlyoffice callback (skeleton; download-and-commit is a follow-up)")

	// TODO E6 follow-up: verify JWT on r.Header.Get("Authorization")
	// with the same secret; reject if invalid. Skeleton accepts
	// unsigned callbacks so dev works before signing is wired.

	writeJSONStatus(w, http.StatusOK, map[string]any{"error": 0})
}

// ---- tiny HS256 signer (no external dep) --------------------------

// hs256 encodes `payload` as a JSON-serialized JWT with the given
// secret. Header is fixed `{"alg":"HS256","typ":"JWT"}`. Good enough
// for the single call-site here; don't export without broader
// review.
func hs256(payload any, secret string) (string, error) {
	header := []byte(`{"alg":"HS256","typ":"JWT"}`)
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	h := base64Url(header)
	p := base64Url(body)
	sig := hmac.New(sha256.New, []byte(secret))
	sig.Write([]byte(h + "." + p))
	s := base64Url(sig.Sum(nil))
	return h + "." + p + "." + s, nil
}

func base64Url(b []byte) string {
	s := base64.StdEncoding.EncodeToString(b)
	s = strings.TrimRight(s, "=")
	s = strings.ReplaceAll(s, "+", "-")
	s = strings.ReplaceAll(s, "/", "_")
	return s
}

// silence unused — time may be needed when we add iat/exp in the
// stricter signing pass.
var _ = time.Now
