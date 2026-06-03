// ADR 0065 — WOPI host implementation. Mounts under /wopi/*.
//
// The protocol Office Online + Collabora + OnlyOffice all speak.
// Lets one backend serve all three editors without duplicating
// glue. Today VaultDMS picks one editor per tenant (see
// tenant_settings.coauth_provider); the WOPI surface stays the
// same regardless.
//
// Routes:
//
//   GET  /wopi/files/{file_id}                  — CheckFileInfo
//   GET  /wopi/files/{file_id}/contents         — GetFile (binary)
//   POST /wopi/files/{file_id}                  — Lock | Unlock | RefreshLock | PutRelativeFile
//                                                  (multiplexed via X-WOPI-Override)
//   POST /wopi/files/{file_id}/contents         — PutFile (binary)
//   GET  /wopi/hosting/discovery                — discovery.xml
//
// Auth: every WOPI call must carry ?access_token=… The token is a
// 4-segment HMAC-signed string built by IssueWOPIToken below; the
// editor passes it back unmodified. Token expiry is 1h by default.
package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

// WOPIHandler handles every /wopi/* route. Holds the Redis client
// for lock storage and the editor URL for discovery.
type WOPIHandler struct {
	rdb *redis.Client
	log zerolog.Logger
	// FileResolver returns the doc + version metadata + a download
	// reader. Concrete impl wired by main; nil-safe — without a
	// resolver every WOPI route returns 503.
	FileResolver WOPIFileResolver
	// Auditor receives session start/end events. Optional.
	Auditor WOPIAuditor
}

// WOPIFileInfo is what CheckFileInfo returns. Trimmed to the fields
// every editor agrees on; full WOPI spec has ~80 properties.
type WOPIFileInfo struct {
	BaseFileName             string `json:"BaseFileName"`
	OwnerID                  string `json:"OwnerId"`
	Size                     int64  `json:"Size"`
	UserID                   string `json:"UserId"`
	UserFriendlyName         string `json:"UserFriendlyName"`
	Version                  string `json:"Version"`
	UserCanWrite             bool   `json:"UserCanWrite"`
	UserCanRename            bool   `json:"UserCanRename"`
	ReadOnly                 bool   `json:"ReadOnly"`
	SupportsLocks            bool   `json:"SupportsLocks"`
	SupportsUpdate           bool   `json:"SupportsUpdate"`
	SupportsRename           bool   `json:"SupportsRename"`
	SupportsGetLock          bool   `json:"SupportsGetLock"`
	SupportsExtendedLockLength bool `json:"SupportsExtendedLockLength"`
	HostEditUrl              string `json:"HostEditUrl,omitempty"`
	HostViewUrl              string `json:"HostViewUrl,omitempty"`
	BreadcrumbDocName        string `json:"BreadcrumbDocName,omitempty"`
}

// WOPIDoc is the resolver's view of one (file_id, requesting_user) pair.
type WOPIDoc struct {
	BaseFileName     string
	OwnerID          string
	Size             int64
	Version          string
	UserCanWrite     bool
	UserCanRename    bool
	UserFriendlyName string
	DocumentID       string
}

// WOPIFileResolver is what main wires. The default impl reads from
// the documents + document_versions tables and applies policy via
// the existing services/policy gRPC client.
type WOPIFileResolver interface {
	Resolve(r *http.Request, claims *WOPIClaims) (*WOPIDoc, error)
	Open(r *http.Request, claims *WOPIClaims) (io.ReadCloser, int64, error)
	Save(r *http.Request, claims *WOPIClaims, body io.Reader) error
	SaveAs(r *http.Request, claims *WOPIClaims, suggestedName string, relativeTarget string, body io.Reader) (newFileID, newName, hostURL string, err error)
}

// WOPIAuditor is the audit-event sink. Optional.
type WOPIAuditor interface {
	SessionStarted(claims *WOPIClaims)
	SessionEnded(claims *WOPIClaims, durationSeconds int64)
}

// NewWOPIHandler constructs the handler.
func NewWOPIHandler(rdb *redis.Client, log zerolog.Logger) *WOPIHandler {
	return &WOPIHandler{rdb: rdb, log: log}
}

// Register mounts every WOPI route on the given mux.
func (h *WOPIHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET  /wopi/hosting/discovery",     h.discovery)
	mux.HandleFunc("GET  /wopi/files/{file_id}",       h.checkFileInfo)
	mux.HandleFunc("GET  /wopi/files/{file_id}/contents", h.getFile)
	mux.HandleFunc("POST /wopi/files/{file_id}",       h.fileOperation)  // multiplexed via X-WOPI-Override
	mux.HandleFunc("POST /wopi/files/{file_id}/contents", h.putFile)
}

// ---- token --------------------------------------------------------------

// WOPIClaims is the parsed access_token. Carried on every WOPI
// request; never leaves the host.
type WOPIClaims struct {
	TenantID  uuid.UUID
	UserID    uuid.UUID
	FileID    uuid.UUID // version_id
	ExpiresAt time.Time
	CanWrite  bool
}

// IssueWOPIToken builds an HMAC-signed token bound to (tenant, user,
// file_id, expiry, can_write). The frontend embeds it in the iframe
// URL: `?access_token=...&access_token_ttl=...`.
func IssueWOPIToken(secret string, c WOPIClaims) string {
	body := fmt.Sprintf("%s|%s|%s|%d|%t",
		c.TenantID, c.UserID, c.FileID, c.ExpiresAt.Unix(), c.CanWrite)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(body)) + "." + sig
}

// parseWOPIToken validates the HMAC + expiry. Returns nil claims on
// any failure; the handler maps that to 401.
func parseWOPIToken(secret, token string) (*WOPIClaims, error) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return nil, errors.New("malformed token")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, err
	}
	got, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), got) {
		return nil, errors.New("bad signature")
	}
	fields := strings.Split(string(body), "|")
	if len(fields) != 5 {
		return nil, errors.New("bad body")
	}
	tenantID, err := uuid.Parse(fields[0])
	if err != nil {
		return nil, err
	}
	userID, err := uuid.Parse(fields[1])
	if err != nil {
		return nil, err
	}
	fileID, err := uuid.Parse(fields[2])
	if err != nil {
		return nil, err
	}
	expUnix, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return nil, err
	}
	exp := time.Unix(expUnix, 0)
	if time.Now().After(exp) {
		return nil, errors.New("expired")
	}
	canWrite := fields[4] == "true"
	return &WOPIClaims{TenantID: tenantID, UserID: userID, FileID: fileID, ExpiresAt: exp, CanWrite: canWrite}, nil
}

// authenticate parses and validates the access_token query param.
// Also enforces that the URL path's file_id matches the token's
// claim (defense-in-depth; the editor shouldn't be able to read
// file B with a token for file A).
func (h *WOPIHandler) authenticate(w http.ResponseWriter, r *http.Request) *WOPIClaims {
	secret := os.Getenv("SEDOC_WOPI_SECRET")
	if secret == "" {
		http.Error(w, "wopi disabled", http.StatusServiceUnavailable)
		return nil
	}
	tok := r.URL.Query().Get("access_token")
	if tok == "" {
		http.Error(w, "access_token required", http.StatusUnauthorized)
		return nil
	}
	c, err := parseWOPIToken(secret, tok)
	if err != nil {
		http.Error(w, "invalid token: "+err.Error(), http.StatusUnauthorized)
		return nil
	}
	pathID := r.PathValue("file_id")
	if pathID != "" && pathID != c.FileID.String() {
		http.Error(w, "token/path file_id mismatch", http.StatusUnauthorized)
		return nil
	}
	return c
}

// ---- CheckFileInfo ------------------------------------------------------

func (h *WOPIHandler) checkFileInfo(w http.ResponseWriter, r *http.Request) {
	c := h.authenticate(w, r)
	if c == nil {
		return
	}
	if h.FileResolver == nil {
		http.Error(w, "wopi resolver not wired", http.StatusServiceUnavailable)
		return
	}
	doc, err := h.FileResolver.Resolve(r, c)
	if err != nil {
		http.Error(w, "resolve: "+err.Error(), http.StatusNotFound)
		return
	}
	out := WOPIFileInfo{
		BaseFileName:               doc.BaseFileName,
		OwnerID:                    doc.OwnerID,
		Size:                       doc.Size,
		UserID:                     c.UserID.String(),
		UserFriendlyName:           doc.UserFriendlyName,
		Version:                    doc.Version,
		UserCanWrite:               c.CanWrite && doc.UserCanWrite,
		UserCanRename:              c.CanWrite && doc.UserCanRename,
		ReadOnly:                   !c.CanWrite || !doc.UserCanWrite,
		SupportsLocks:              true,
		SupportsUpdate:             true,
		SupportsRename:             true,
		SupportsGetLock:            true,
		SupportsExtendedLockLength: true,
		BreadcrumbDocName:          doc.BaseFileName,
	}
	h.maybeStartSession(r.Context(), c)
	writeJSONStatus(w, http.StatusOK, out)
}

// ---- GetFile ------------------------------------------------------------

func (h *WOPIHandler) getFile(w http.ResponseWriter, r *http.Request) {
	c := h.authenticate(w, r)
	if c == nil {
		return
	}
	if h.FileResolver == nil {
		http.Error(w, "wopi resolver not wired", http.StatusServiceUnavailable)
		return
	}
	rc, size, err := h.FileResolver.Open(r, c)
	if err != nil {
		http.Error(w, "open: "+err.Error(), http.StatusNotFound)
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	_, _ = io.Copy(w, rc)
}

// ---- POST /wopi/files/{file_id} (multiplexed) --------------------------

func (h *WOPIHandler) fileOperation(w http.ResponseWriter, r *http.Request) {
	c := h.authenticate(w, r)
	if c == nil {
		return
	}
	op := strings.ToUpper(r.Header.Get("X-WOPI-Override"))
	switch op {
	case "LOCK":
		h.lock(w, r, c)
	case "UNLOCK":
		h.unlock(w, r, c)
	case "REFRESH_LOCK":
		h.refreshLock(w, r, c)
	case "GET_LOCK":
		h.getLock(w, r, c)
	case "PUT_RELATIVE":
		h.putRelative(w, r, c)
	default:
		http.Error(w, "unsupported X-WOPI-Override: "+op, http.StatusNotImplemented)
	}
}

// ---- Lock / Unlock / RefreshLock / GetLock ------------------------------

const lockTTL = 30 * time.Minute

func wopiLockKey(fileID uuid.UUID) string { return "wopi:lock:" + fileID.String() }

func (h *WOPIHandler) lock(w http.ResponseWriter, r *http.Request, c *WOPIClaims) {
	if !c.CanWrite {
		http.Error(w, "read-only token", http.StatusUnauthorized)
		return
	}
	wantLock := r.Header.Get("X-WOPI-Lock")
	if wantLock == "" {
		http.Error(w, "X-WOPI-Lock required", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	// SETNX-with-TTL via SET NX EX. Returns false on conflict.
	ok, err := h.rdb.SetNX(ctx, wopiLockKey(c.FileID), wantLock, lockTTL).Result()
	if err != nil {
		http.Error(w, "redis: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		// Already locked. WOPI: return existing lock in X-WOPI-Lock + 409.
		// Special case: if the existing lock matches the requested
		// lock, treat as a refresh (same editor, same session).
		got, err := h.rdb.Get(ctx, wopiLockKey(c.FileID)).Result()
		if err == nil && got == wantLock {
			h.rdb.Expire(ctx, wopiLockKey(c.FileID), lockTTL)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("X-WOPI-Lock", got)
		http.Error(w, "locked", http.StatusConflict)
		return
	}
	h.maybeStartSession(r.Context(), c)
	w.WriteHeader(http.StatusOK)
}

func (h *WOPIHandler) unlock(w http.ResponseWriter, r *http.Request, c *WOPIClaims) {
	wantLock := r.Header.Get("X-WOPI-Lock")
	if wantLock == "" {
		http.Error(w, "X-WOPI-Lock required", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	got, err := h.rdb.Get(ctx, wopiLockKey(c.FileID)).Result()
	if err == redis.Nil {
		http.Error(w, "not locked", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "redis: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if got != wantLock {
		w.Header().Set("X-WOPI-Lock", got)
		http.Error(w, "lock mismatch", http.StatusConflict)
		return
	}
	if err := h.rdb.Del(ctx, wopiLockKey(c.FileID)).Err(); err != nil {
		http.Error(w, "redis: "+err.Error(), http.StatusInternalServerError)
		return
	}
	h.endSession(r.Context(), c)
	w.WriteHeader(http.StatusOK)
}

func (h *WOPIHandler) refreshLock(w http.ResponseWriter, r *http.Request, c *WOPIClaims) {
	wantLock := r.Header.Get("X-WOPI-Lock")
	ctx := r.Context()
	got, err := h.rdb.Get(ctx, wopiLockKey(c.FileID)).Result()
	if err == redis.Nil || got != wantLock {
		if got != "" {
			w.Header().Set("X-WOPI-Lock", got)
		}
		http.Error(w, "lock mismatch", http.StatusConflict)
		return
	}
	if err := h.rdb.Expire(ctx, wopiLockKey(c.FileID), lockTTL).Err(); err != nil {
		http.Error(w, "redis: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *WOPIHandler) getLock(w http.ResponseWriter, r *http.Request, c *WOPIClaims) {
	got, _ := h.rdb.Get(r.Context(), wopiLockKey(c.FileID)).Result()
	w.Header().Set("X-WOPI-Lock", got)
	w.WriteHeader(http.StatusOK)
}

// ---- PutFile (with lock match check) ------------------------------------

func (h *WOPIHandler) putFile(w http.ResponseWriter, r *http.Request) {
	c := h.authenticate(w, r)
	if c == nil {
		return
	}
	if !c.CanWrite {
		http.Error(w, "read-only token", http.StatusUnauthorized)
		return
	}
	wantLock := r.Header.Get("X-WOPI-Lock")
	if wantLock != "" {
		got, err := h.rdb.Get(r.Context(), wopiLockKey(c.FileID)).Result()
		if err == redis.Nil {
			// 409 with empty X-WOPI-Lock means "no lock; require one".
			http.Error(w, "no lock", http.StatusConflict)
			return
		}
		if err == nil && got != wantLock {
			w.Header().Set("X-WOPI-Lock", got)
			http.Error(w, "lock mismatch", http.StatusConflict)
			return
		}
	}
	if h.FileResolver == nil {
		http.Error(w, "wopi resolver not wired", http.StatusServiceUnavailable)
		return
	}
	if err := h.FileResolver.Save(r, c, r.Body); err != nil {
		http.Error(w, "save: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ---- PutRelativeFile (Save As) ------------------------------------------

func (h *WOPIHandler) putRelative(w http.ResponseWriter, r *http.Request, c *WOPIClaims) {
	if !c.CanWrite {
		http.Error(w, "read-only token", http.StatusUnauthorized)
		return
	}
	if h.FileResolver == nil {
		http.Error(w, "wopi resolver not wired", http.StatusServiceUnavailable)
		return
	}
	suggested := decodeWopiHeader(r.Header.Get("X-WOPI-SuggestedTarget"))
	relative := decodeWopiHeader(r.Header.Get("X-WOPI-RelativeTarget"))
	newID, newName, hostURL, err := h.FileResolver.SaveAs(r, c, suggested, relative, r.Body)
	if err != nil {
		http.Error(w, "save as: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"Name":    newName,
		"Url":     hostURL,
		"FileID":  newID,
	})
}

// decodeWopiHeader handles the ISO-8859-1 + percent-encoded shape WOPI
// uses for filenames. UTF-8 escaped via %xx; we just url-decode and
// trim quotes.
func decodeWopiHeader(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"`)
	if s == "" {
		return ""
	}
	if dec, err := decodePercent(s); err == nil {
		return dec
	}
	return s
}

func decodePercent(s string) (string, error) {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			b, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
			if err != nil {
				return "", err
			}
			out = append(out, byte(b))
			i += 2
			continue
		}
		out = append(out, s[i])
	}
	return string(out), nil
}

// ---- Discovery (XML for Collabora) --------------------------------------

type discoveryDoc struct {
	XMLName  xml.Name        `xml:"wopi-discovery"`
	NetZones []discoveryZone `xml:"net-zone"`
}

type discoveryZone struct {
	Name string         `xml:"name,attr"`
	Apps []discoveryApp `xml:"app"`
}

type discoveryApp struct {
	Name    string            `xml:"name,attr"`
	Actions []discoveryAction `xml:"action"`
}

type discoveryAction struct {
	Name    string `xml:"name,attr"`
	Ext     string `xml:"ext,attr"`
	URLSrc  string `xml:"urlsrc,attr"`
	Default string `xml:"default,attr,omitempty"`
}

// discovery serves the XML map editors use to learn which WOPI
// host action URLs we expose for which file extensions.
func (h *WOPIHandler) discovery(w http.ResponseWriter, r *http.Request) {
	publicBase := os.Getenv("SEDOC_PUBLIC_URL")
	if publicBase == "" {
		publicBase = "http://localhost:8080"
	}
	editURL := publicBase + "/wopi/host/edit?WOPISrc=<wopisrc>"
	viewURL := publicBase + "/wopi/host/view?WOPISrc=<wopisrc>"

	doc := discoveryDoc{
		NetZones: []discoveryZone{{
			Name: "external-http",
			Apps: []discoveryApp{
				{Name: "Word",       Actions: docxActions(editURL, viewURL)},
				{Name: "Excel",      Actions: xlsxActions(editURL, viewURL)},
				{Name: "PowerPoint", Actions: pptxActions(editURL, viewURL)},
			},
		}},
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	_ = enc.Encode(doc)
}

func docxActions(edit, view string) []discoveryAction {
	return []discoveryAction{
		{Name: "edit",       Ext: "docx", URLSrc: edit, Default: "true"},
		{Name: "view",       Ext: "docx", URLSrc: view},
		{Name: "edit",       Ext: "doc",  URLSrc: edit},
		{Name: "view",       Ext: "doc",  URLSrc: view},
	}
}
func xlsxActions(edit, view string) []discoveryAction {
	return []discoveryAction{
		{Name: "edit", Ext: "xlsx", URLSrc: edit, Default: "true"},
		{Name: "view", Ext: "xlsx", URLSrc: view},
		{Name: "edit", Ext: "xls",  URLSrc: edit},
		{Name: "view", Ext: "xls",  URLSrc: view},
	}
}
func pptxActions(edit, view string) []discoveryAction {
	return []discoveryAction{
		{Name: "edit", Ext: "pptx", URLSrc: edit, Default: "true"},
		{Name: "view", Ext: "pptx", URLSrc: view},
		{Name: "edit", Ext: "ppt",  URLSrc: edit},
		{Name: "view", Ext: "ppt",  URLSrc: view},
	}
}

// ---- session audit ------------------------------------------------------

func wopiSessionKey(c *WOPIClaims) string {
	return "wopi:session:" + c.UserID.String() + ":" + c.FileID.String()
}

// maybeStartSession emits a session.started.v1 audit event the FIRST
// time we see a (user, file) pair within the 1h sliding window. The
// window key carries the start timestamp; endSession reads it back
// to compute duration.
func (h *WOPIHandler) maybeStartSession(parent context.Context, c *WOPIClaims) {
	if h.rdb == nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	startedAt := strconv.FormatInt(time.Now().Unix(), 10)
	ok, _ := h.rdb.SetNX(ctx, wopiSessionKey(c), startedAt, time.Hour).Result()
	if ok && h.Auditor != nil {
		h.Auditor.SessionStarted(c)
	}
}

func (h *WOPIHandler) endSession(parent context.Context, c *WOPIClaims) {
	if h.rdb == nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	startStr, err := h.rdb.Get(ctx, wopiSessionKey(c)).Result()
	if err != nil {
		return
	}
	_ = h.rdb.Del(ctx, wopiSessionKey(c)).Err()
	if h.Auditor == nil {
		return
	}
	if startUnix, err := strconv.ParseInt(startStr, 10, 64); err == nil {
		h.Auditor.SessionEnded(c, time.Now().Unix()-startUnix)
	}
}
