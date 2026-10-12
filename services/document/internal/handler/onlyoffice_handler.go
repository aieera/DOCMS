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

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// OnlyOfficeSaver commits an edited file (fetched from the Document
// Server's URL) as a new document version. Implemented by
// DBWOPIResolver.SaveFromURL so OnlyOffice and WOPI saves share one
// write path. Nil-safe: without a saver, status 2/6 events are
// acknowledged-but-logged exactly like the pre-wiring behavior.
type OnlyOfficeSaver interface {
	SaveFromURL(ctx context.Context, claims *WOPIClaims, fileURL, allowedHost, changeSummary string) error
}

// CoauthAuthorizer resolves a version to its document and reports whether
// the caller may view (error otherwise) and edit it. Implemented by
// DocumentService.AuthorizeCoauth — the same gate coauth/start uses.
type CoauthAuthorizer interface {
	AuthorizeCoauth(ctx context.Context, versionID uuid.UUID) (docID uuid.UUID, canWrite bool, err error)
}

// DevOnlyOfficeJWT is the published dev value docker-compose injects for
// SEDOC_ONLYOFFICE_JWT; accepted only when Environment is "dev" (D-13).
const DevOnlyOfficeJWT = "dev-only-onlyoffice-jwt-rotate-in-prod"

// OnlyOfficeHandler mounts the E6 endpoints.
type OnlyOfficeHandler struct {
	log zerolog.Logger
	// Environment is pkg/config Environment (dev | staging | prod). Outside
	// dev the JWT secret must be set and must not be DevOnlyOfficeJWT.
	Environment string
	// Authz gates the config endpoint (D-6): an edit config — and the
	// write-capable callback token inside it — is only minted for a caller
	// who holds edit on the version's document. Wired by main; nil fails
	// the config endpoint closed (503).
	Authz CoauthAuthorizer
	// Saver commits status-2/6 callbacks as new versions. Wired by main.
	Saver OnlyOfficeSaver
	// Redis holds the WOPI lock keys; the callback refuses a save-back
	// while another editor session holds the version's lock. Optional —
	// nil skips the check (unit tests without Redis).
	Redis *redis.Client
}

// jwtSecret returns the Document Server shared secret per D-13: in dev an
// unset SEDOC_ONLYOFFICE_JWT falls back to DevOnlyOfficeJWT (what compose
// injects); elsewhere it must be set and must not be that published value.
// Read per request so tests can t.Setenv.
func (h *OnlyOfficeHandler) jwtSecret() (string, bool) {
	b, err := service.ResolveSecret(h.Environment, os.Getenv("SEDOC_ONLYOFFICE_JWT"), DevOnlyOfficeJWT)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// onlyOfficeCallbackTokenTTL bounds the write-capable save-back token the
// config endpoint embeds in the callback URL.
const onlyOfficeCallbackTokenTTL = time.Hour

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
	Document     oneOfficeDoc `json:"document"`
	DocumentType string       `json:"documentType"`
	EditorConfig oneOfficeEd  `json:"editorConfig"`
	Token        string       `json:"token,omitempty"` // JWT of the rest of the config
}

type oneOfficeDoc struct {
	FileType    string         `json:"fileType"`
	Key         string         `json:"key"`
	Title       string         `json:"title"`
	URL         string         `json:"url"`
	Permissions map[string]any `json:"permissions,omitempty"`
}

type oneOfficeEd struct {
	Mode        string        `json:"mode"`
	Lang        string        `json:"lang"`
	CallbackURL string        `json:"callbackUrl"`
	User        oneOfficeUser `json:"user"`
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

	secret, ok := h.jwtSecret()
	if !ok {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "onlyoffice not configured"})
		return
	}
	publicBase := os.Getenv("SEDOC_PUBLIC_URL")
	if publicBase == "" {
		publicBase = "http://localhost:8080"
	}

	// D-6: this config carries a write-capable save-back credential, so it
	// is minted only for a caller who can EDIT the version's document —
	// the same AuthorizeCoauth gate coauth/start uses. The version must
	// belong to the document in the path (version ids are tenant-unique).
	if h.Authz == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "onlyoffice authorizer not configured"})
		return
	}
	versionDocID, canEdit, err := h.Authz.AuthorizeCoauth(r.Context(), versionID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if versionDocID != docID {
		writeErr(w, r, vdmserr.ErrNotFound)
		return
	}
	if !canEdit {
		writeErr(w, r, vdmserr.Forbidden("edit permission required"))
		return
	}
	// D-11: the document key (the version id) is finalized once a status-2
	// save committed it; a new session on it would produce a save-back the
	// callback must reject. Point the caller at the current version.
	if h.Redis != nil {
		n, rerr := h.Redis.Exists(r.Context(), onlyOfficeCommittedKey(versionID.String())).Result()
		if rerr != nil {
			writeErr(w, r, vdmserr.Internal("replay cache unavailable"))
			return
		}
		if n > 0 {
			writeErr(w, r, vdmserr.Conflict("this version was already saved from an editing session; open the current version"))
			return
		}
	}

	// The callback is posted by the Document Server with no user
	// session, so the save-back path needs its own caller identity.
	// Mint the same HMAC token WOPI uses — (tenant, user, version,
	// document, can_write, expiry) — and ride it on the CallbackURL; the
	// DS echoes the URL verbatim on every status event. Signed with the
	// OnlyOffice shared secret so no extra env is required. DocumentID is
	// the WOPI lock scope (D-5); the token lives 1 h.
	callbackToken := IssueWOPIToken(secret, WOPIClaims{
		TenantID:   tenantID,
		UserID:     userID,
		FileID:     versionID,
		DocumentID: docID,
		ExpiresAt:  time.Now().Add(onlyOfficeCallbackTokenTTL),
		CanWrite:   true,
		Role:       auth.GetUserRole(r.Context()), // D-12: save-back policy identity
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
	// D-13: without a usable secret nothing here can be authenticated —
	// refuse rather than act on an unsigned event.
	secret, configured := h.jwtSecret()
	if !configured {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{"error": 1})
		return
	}
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
	// are the event directly. Trust whichever decodes. iat/exp are the
	// JWT's own claims (top level in both forms).
	var signed struct {
		Status  int    `json:"status"`
		URL     string `json:"url"`
		Key     string `json:"key"`
		Iat     *int64 `json:"iat"`
		Exp     *int64 `json:"exp"`
		Payload *struct {
			Status int    `json:"status"`
			URL    string `json:"url"`
			Key    string `json:"key"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(payload, &signed); err != nil {
		writeJSONStatus(w, http.StatusForbidden, map[string]any{"error": 1})
		return
	}
	// D-11: a signature alone is a bearer credential forever. The DS
	// signs every callback with iat + exp; both are required and the
	// token must be fresh (small skew allowance).
	if reason := onlyOfficeTokenStale(signed.Iat, signed.Exp, time.Now()); reason != "" {
		h.log.Warn().Str("key", body.Key).Str("reason", reason).Msg("onlyoffice callback rejected: stale JWT")
		writeJSONStatus(w, http.StatusForbidden, map[string]any{"error": 1})
		return
	}
	if signed.Payload != nil {
		body.Status, body.URL, body.Key = signed.Payload.Status, signed.Payload.URL, signed.Payload.Key
	} else {
		body.Status, body.URL, body.Key = signed.Status, signed.URL, signed.Key
	}

	switch body.Status {
	case 2, 6: // ready-to-save / force-save ready → commit a new version
		dup, err := h.beginSave(r.Context(), body.Status, body.Key, token)
		if err != nil {
			h.log.Warn().Err(err).Int("status", body.Status).Str("key", body.Key).
				Msg("onlyoffice save-back refused")
			writeJSONStatus(w, http.StatusOK, map[string]any{"error": 1})
			return
		}
		if dup {
			// Exact replay of a callback that already committed (e.g. the
			// DS re-sent after a lost response): acknowledge, write nothing.
			h.log.Info().Int("status", body.Status).Str("key", body.Key).
				Msg("onlyoffice callback replay acknowledged without a write")
			writeJSONStatus(w, http.StatusOK, map[string]any{"error": 0})
			return
		}
		if err := h.commitSave(r, body.Status, body.URL); err != nil {
			h.abortSave(r.Context(), body.Status, body.Key, token)
			h.log.Error().Err(err).
				Int("status", body.Status).Str("key", body.Key).
				Msg("onlyoffice save-back failed")
			// Non-zero error tells the Document Server the save was NOT
			// accepted, so it retries / keeps the cached copy instead of
			// discarding the user's edits.
			writeJSONStatus(w, http.StatusOK, map[string]any{"error": 1})
			return
		}
		h.finishSave(r.Context(), body.Status, body.Key, token)
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
	secret, ok := h.jwtSecret()
	if !ok {
		return vdmserr.Internal("SEDOC_ONLYOFFICE_JWT not configured")
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
	// the lock (e.g. the document is open in Collabora), a non-holder
	// save-back must be rejected, not silently interleaved. WOPI keys the
	// lock on the DOCUMENT (WOPIClaims.lockID) because every save moves the
	// head to a new version; keying it on the version here (D-5) meant the
	// two editors never saw each other's locks. A Redis error fails closed
	// (the DS keeps the edit and retries).
	if h.Redis != nil {
		lock, lerr := h.Redis.Get(r.Context(), wopiLockKey(claims.lockID())).Result()
		switch {
		case lerr == nil && lock != "":
			return vdmserr.Conflict("document is locked by another editor session")
		case lerr != nil && lerr != redis.Nil:
			return vdmserr.Internal("wopi lock check failed")
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

// ---- callback freshness + replay (D-11) ----------------------------

const (
	// onlyOfficeClockSkew tolerates DS/host clock drift on iat/exp.
	onlyOfficeClockSkew = 60 * time.Second
	// onlyOfficeCommittedKeyTTL: how long a document key stays finalized
	// after a status-2 save. Keys are version ids (immutable, never reused),
	// so this only bounds Redis growth; it must exceed any callback token's
	// lifetime.
	onlyOfficeCommittedKeyTTL = 30 * 24 * time.Hour
	// onlyOfficeTokenSeenTTL bounds the replay-cache entry for a callback
	// token (DS tokens live minutes; exp is enforced separately).
	onlyOfficeTokenSeenTTL = 24 * time.Hour
)

// onlyOfficeTokenStale returns a non-empty reason when the callback JWT's
// iat/exp are missing or out of range.
func onlyOfficeTokenStale(iat, exp *int64, now time.Time) string {
	if iat == nil || exp == nil {
		return "iat and exp are required"
	}
	if now.After(time.Unix(*exp, 0).Add(onlyOfficeClockSkew)) {
		return "expired"
	}
	if time.Unix(*iat, 0).After(now.Add(onlyOfficeClockSkew)) {
		return "issued in the future"
	}
	if *exp < *iat {
		return "exp before iat"
	}
	return ""
}

func onlyOfficeCommittedKey(key string) string { return "onlyoffice:committed:" + key }

func onlyOfficeTokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "onlyoffice:cbtoken:" + base64Url(sum[:])
}

// beginSave reserves a save-back. dup=true means this exact callback token
// already committed (acknowledge without writing). An error means refuse:
// the key was finalized by an earlier status-2 save, the same token is
// mid-flight, or Redis failed (fail closed; the DS retries). Without Redis
// (unit wiring) there is no replay state and every call proceeds.
func (h *OnlyOfficeHandler) beginSave(ctx context.Context, status int, key, token string) (dup bool, err error) {
	if h.Redis == nil {
		return false, nil
	}
	if token != "" {
		tk := onlyOfficeTokenKey(token)
		ok, err := h.Redis.SetNX(ctx, tk, "pending", onlyOfficeTokenSeenTTL).Result()
		if err != nil {
			return false, vdmserr.Internal("replay cache unavailable")
		}
		if !ok {
			if v, _ := h.Redis.Get(ctx, tk).Result(); v == "done" {
				return true, nil
			}
			return false, vdmserr.Conflict("callback already in flight")
		}
	}
	release := func() {
		if token != "" {
			_ = h.Redis.Del(ctx, onlyOfficeTokenKey(token)).Err()
		}
	}
	if key == "" {
		release()
		return false, vdmserr.Validation("key", "save event carried no document key")
	}
	if status == 2 {
		// Reserve the key: only one final save per document key.
		ok, err := h.Redis.SetNX(ctx, onlyOfficeCommittedKey(key), "pending", onlyOfficeCommittedKeyTTL).Result()
		if err != nil {
			release()
			return false, vdmserr.Internal("replay cache unavailable")
		}
		if !ok {
			release()
			return false, vdmserr.Conflict("document key already committed")
		}
		return false, nil
	}
	n, err := h.Redis.Exists(ctx, onlyOfficeCommittedKey(key)).Result()
	if err != nil {
		release()
		return false, vdmserr.Internal("replay cache unavailable")
	}
	if n > 0 {
		release()
		return false, vdmserr.Conflict("document key already committed")
	}
	return false, nil
}

// abortSave releases the reservations of a save that did not commit, so a
// DS retry can go through.
func (h *OnlyOfficeHandler) abortSave(ctx context.Context, status int, key, token string) {
	if h.Redis == nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	if token != "" {
		_ = h.Redis.Del(ctx, onlyOfficeTokenKey(token)).Err()
	}
	if status == 2 && key != "" {
		_ = h.Redis.Del(ctx, onlyOfficeCommittedKey(key)).Err()
	}
}

// finishSave marks the token done (an exact replay is then acknowledged
// without a write) and, for a final save, the document key committed.
func (h *OnlyOfficeHandler) finishSave(ctx context.Context, status int, key, token string) {
	if h.Redis == nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	if token != "" {
		_ = h.Redis.Set(ctx, onlyOfficeTokenKey(token), "done", onlyOfficeTokenSeenTTL).Err()
	}
	if status == 2 && key != "" {
		_ = h.Redis.Set(ctx, onlyOfficeCommittedKey(key), "done", onlyOfficeCommittedKeyTTL).Err()
	}
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
