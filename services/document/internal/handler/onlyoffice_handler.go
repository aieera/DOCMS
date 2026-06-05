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
//   - Config JSON is signed with HS256 using SEDOC_ONLYOFFICE_JWT.
//     OnlyOffice Document Server is configured with the same secret
//     (JWT_ENABLED=true) and refuses requests whose token signature
//     doesn't match — this is the only thing preventing an attacker
//     from swapping the document URL.
//   - Callback verifies the JWT OnlyOffice signs the event with (same
//     secret) and rejects missing/invalid tokens with 403 when
//     SEDOC_ONLYOFFICE_JWT is set — so a save event can't be forged.
package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
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

	secret := os.Getenv("SEDOC_ONLYOFFICE_JWT")
	if secret == "" {
		writeErr(w, r, vdmserr.Internal("onlyoffice not configured"))
		return
	}
	publicBase := os.Getenv("SEDOC_PUBLIC_URL")
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
	// download-and-commit on status==2/6 is a follow-up.
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var body struct {
		Status int    `json:"status"`
		URL    string `json:"url"`
		Key    string `json:"key"`
		Token  string `json:"token"`
	}
	_ = json.Unmarshal(raw, &body)

	// Verify the JWT OnlyOffice signs the callback with (JWT_ENABLED=true).
	// Without this an attacker who can reach the callback URL could forge a
	// "save" event pointing at a malicious file. The token rides in the
	// Authorization header (token.inbox.header) or the body `token` field;
	// the signed payload is the authoritative status/url/key.
	if secret := os.Getenv("SEDOC_ONLYOFFICE_JWT"); secret != "" {
		token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if token == "" {
			token = body.Token
		}
		payload, ok := verifyHS256(token, secret)
		if !ok {
			h.log.Warn().Str("key", body.Key).Msg("onlyoffice callback rejected: missing/invalid JWT")
			writeJSONStatus(w, http.StatusForbidden, map[string]any{"error": 1})
			return
		}
		// Header-form tokens wrap the event under "payload"; body-form tokens
		// are the event directly. Trust whichever decodes.
		var signed struct {
			Status  int    `json:"status"`
			URL     string `json:"url"`
			Key     string `json:"key"`
			Payload *struct {
				Status int    `json:"status"`
				URL    string `json:"url"`
				Key    string `json:"key"`
			} `json:"payload"`
		}
		if json.Unmarshal(payload, &signed) == nil {
			if signed.Payload != nil {
				body.Status, body.URL, body.Key = signed.Payload.Status, signed.Payload.URL, signed.Payload.Key
			} else {
				body.Status, body.URL, body.Key = signed.Status, signed.URL, signed.Key
			}
		}
	}

	h.log.Info().
		Int("status", body.Status).
		Str("key", body.Key).
		Msg("onlyoffice callback (verified; download-and-commit is a follow-up)")

	writeJSONStatus(w, http.StatusOK, map[string]any{"error": 0})
}

// verifyHS256 checks an OnlyOffice-issued JWT (header.payload.signature,
// HS256) against secret in constant time and returns the decoded payload.
// ok=false on a malformed token or signature mismatch.
func verifyHS256(token, secret string) ([]byte, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	expected := base64Url(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return nil, false
	}
	payload, err := base64UrlDecode(parts[1])
	if err != nil {
		return nil, false
	}
	return payload, true
}

func base64UrlDecode(s string) ([]byte, error) {
	s = strings.ReplaceAll(s, "-", "+")
	s = strings.ReplaceAll(s, "_", "/")
	if pad := len(s) % 4; pad != 0 {
		s += strings.Repeat("=", 4-pad)
	}
	return base64.StdEncoding.DecodeString(s)
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
