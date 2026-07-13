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
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

// OnlyOfficeSaver commits an edited file (fetched from the Document
// Server's URL) as a new document version. Implemented by
// DBWOPIResolver.SaveFromURL so OnlyOffice and WOPI saves share one
// write path. Nil-safe: without a saver, status 2/6 events are
// acknowledged-but-logged exactly like the pre-wiring behavior.
type OnlyOfficeSaver interface {
	SaveFromURL(ctx context.Context, claims *WOPIClaims, fileURL, allowedHost, changeSummary string) error
}

// OnlyOfficeHandler mounts the E6 endpoints.
type OnlyOfficeHandler struct {
	log zerolog.Logger
	// Saver commits status-2/6 callbacks as new versions. Wired by main.
	Saver OnlyOfficeSaver
	// Redis holds the WOPI lock keys; the callback refuses a save-back
	// while another editor session holds the version's lock. Optional —
	// nil skips the check (unit tests without Redis).
	Redis *redis.Client
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

	// The callback is posted by the Document Server with no user
	// session, so the save-back path needs its own caller identity.
	// Mint the same HMAC token WOPI uses — (tenant, user, version,
	// can_write, expiry) — and ride it on the CallbackURL; the DS
	// echoes the URL verbatim on every status event. Signed with the
	// OnlyOffice shared secret so no extra env is required. 24h expiry:
	// a DS may hold the final save until the last co-editor closes.
	callbackToken := IssueWOPIToken(secret, WOPIClaims{
		TenantID:  tenantID,
		UserID:    userID,
		FileID:    versionID,
		ExpiresAt: time.Now().Add(24 * time.Hour),
		CanWrite:  true,
	})

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
				"/versions/" + versionID.String() + "/onlyoffice/callback" +
				"?access_token=" + url.QueryEscape(callbackToken),
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
	//   7 = force-save error
	// 2 and 6 carry a `url` to the edited bytes; we download and commit
	// them as a NEW document version. Anything else is acknowledged
	// without a write (silently dropping 2/6 was the data-loss bug).
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

	switch body.Status {
	case 2, 6: // ready-to-save / force-save ready → commit a new version
		if err := h.commitSave(r, body.Status, body.URL); err != nil {
			h.log.Error().Err(err).
				Int("status", body.Status).Str("key", body.Key).
				Msg("onlyoffice save-back failed")
			// Non-zero error tells the Document Server the save was NOT
			// accepted, so it retries / keeps the cached copy instead of
			// discarding the user's edits.
			writeJSONStatus(w, http.StatusOK, map[string]any{"error": 1})
			return
		}
		h.log.Info().Int("status", body.Status).Str("key", body.Key).
			Msg("onlyoffice save-back committed as new version")
	case 3, 7: // DS-side save error — surface loudly, nothing to commit
		h.log.Error().Int("status", body.Status).Str("key", body.Key).
			Msg("onlyoffice reported a save error")
	default: // 0/1/4 — editing lifecycle noise; nothing to persist
		h.log.Info().Int("status", body.Status).Str("key", body.Key).
			Msg("onlyoffice callback acknowledged")
	}

	writeJSONStatus(w, http.StatusOK, map[string]any{"error": 0})
}

// commitSave authenticates the callback's access_token, refuses to write
// over another editor's active WOPI lock, and downloads + commits the
// edited bytes through the shared save path (new version, never
// in-place).
func (h *OnlyOfficeHandler) commitSave(r *http.Request, status int, fileURL string) error {
	if h.Saver == nil {
		return vdmserr.Internal("onlyoffice saver not wired; edited document NOT saved")
	}
	if fileURL == "" {
		return vdmserr.Validation("url", "save event carried no download url")
	}
	secret := os.Getenv("SEDOC_ONLYOFFICE_JWT")
	if secret == "" {
		return vdmserr.Internal("SEDOC_ONLYOFFICE_JWT not set")
	}

	// The access_token minted by the config endpoint carries the caller
	// identity (tenant, user, version). Without it a forged-but-JWT-less
	// deployment (or a replayed URL) could write into an arbitrary
	// version id — parse + validate exactly like WOPI authenticate.
	claims, err := parseWOPIToken(secret, r.URL.Query().Get("access_token"))
	if err != nil {
		return vdmserr.Forbidden("callback access_token invalid: " + err.Error())
	}
	vid := r.PathValue("vid")
	if vid != "" && vid != claims.FileID.String() {
		return vdmserr.Forbidden("access_token/version mismatch")
	}
	if !claims.CanWrite {
		return vdmserr.Forbidden("read-only session")
	}

	// Respect the WOPI check-out lock: if another editor session holds
	// the version's lock (e.g. it is open in Collabora), a non-holder
	// save-back must be rejected, not silently interleaved.
	if h.Redis != nil {
		if lock, lerr := h.Redis.Get(r.Context(), wopiLockKey(claims.FileID)).Result(); lerr == nil && lock != "" {
			return vdmserr.Conflict("document version is locked by another editor session")
		}
	}

	// Detach from the callback's request context: the DS only waits
	// briefly for the ack, but the download+scan+encrypt+version write
	// must run to completion regardless.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Minute)
	defer cancel()

	summary := "Edited in OnlyOffice"
	if status == 6 {
		summary = "Edited in OnlyOffice (force save)"
	}
	return h.Saver.SaveFromURL(ctx, claims, fileURL, onlyOfficeAllowedHost(), summary)
}

// onlyOfficeAllowedHost pins save-back downloads to the configured
// Document Server (SSRF guard). Empty when no DS URL is configured —
// SaveFromURL then only enforces http(s).
func onlyOfficeAllowedHost() string {
	for _, env := range []string{"SEDOC_ONLYOFFICE_URL", "SEDOC_COAUTH_URL"} {
		if v := os.Getenv(env); v != "" {
			if u, err := url.Parse(v); err == nil && u.Host != "" {
				return u.Host
			}
		}
	}
	return ""
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
