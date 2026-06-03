// ADR 0070 — QES handler routes.
//
//   POST /api/v1/signatures/qes/start         body: { request_id, signer_id, provider, signer_email, ... }
//   GET  /api/v1/signatures/qes/return        QTSP redirects user back here; we 302 to /sign/done
//   GET  /api/v1/signatures/qes/session/:id   poll endpoint for the post-redirect UI
//   GET  /api/v1/signatures/qes/certificates  ?request_id=…   cert-display block
package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	tsplib "github.com/aieera/sedoc/pkg/signing/tsp"
	"github.com/aieera/sedoc/services/signature/internal/service"
)

// RegisterQES mounts the QES routes alongside the existing
// /signatures surface registered in Register.
func (h *Handler) RegisterQES(mux *http.ServeMux, frontendBase string) {
	q := &qesRoutes{svc: h.svc, log: h.log, frontendBase: frontendBase}
	mux.HandleFunc("POST /api/v1/signatures/qes/start", q.start)
	mux.HandleFunc("GET /api/v1/signatures/qes/return", q.handleReturn)
	mux.HandleFunc("GET /api/v1/signatures/qes/session/{id}", q.session)
	mux.HandleFunc("GET /api/v1/signatures/qes/certificates", q.certificates)
}

type qesRoutes struct {
	svc          *service.Service
	log          interface{ /* zerolog.Logger value */ }
	frontendBase string // e.g. "/sign/done" or full https://app.vaultdms…/sign
}

type startBody struct {
	RequestID    string `json:"request_id"`
	SignerID     string `json:"signer_id"`
	Provider     string `json:"provider"`
	SignerEmail  string `json:"signer_email"`
	SignerName   string `json:"signer_name"`
	CountryCode  string `json:"country_code"`
	Reason       string `json:"reason"`
	Location     string `json:"location"`
	// DocumentBytesB64 is the to-be-signed PDF, base64'd. The
	// service hashes once and discards. Frontend only sends this
	// for QES; the regular signing flow uses the document service's
	// already-stored bytes.
	DocumentBytesB64 string `json:"document_bytes_b64"`
}

func (q *qesRoutes) start(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "tenant + user required")
		return
	}
	var body startBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.RequestID == "" || body.SignerID == "" || body.Provider == "" || body.SignerEmail == "" {
		writeError(w, http.StatusBadRequest, "request_id, signer_id, provider, signer_email required")
		return
	}
	docBytes, err := base64.StdEncoding.DecodeString(body.DocumentBytesB64)
	if err != nil || len(docBytes) == 0 {
		writeError(w, http.StatusBadRequest, "document_bytes_b64 required")
		return
	}
	out, err := q.svc.StartQES(r.Context(), tenantID, userID, service.StartQESInput{
		RequestID: body.RequestID, SignerID: body.SignerID,
		Provider:    tsplib.Provider(body.Provider),
		DocumentBytes: docBytes,
		SignerEmail: body.SignerEmail, SignerName: body.SignerName,
		CountryCode: body.CountryCode,
		Reason:      body.Reason, Location: body.Location,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "qes start: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleReturn is hit by the QTSP redirect. We 302 to the frontend
// "sign done" route with a status so the user lands on a page that
// reads /qes/session/:id and renders.
func (q *qesRoutes) handleReturn(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	if tenantID == "" {
		// The QTSP redirect won't carry our gateway headers. The
		// gateway must set X-Auth-Tenant-ID on this path from the
		// session cookie before forwarding (deploy/gateway/routes.yaml
		// already does this for other authenticated routes).
		writeError(w, http.StatusUnauthorized, "tenant context missing")
		return
	}
	sessionID := r.URL.Query().Get("session")
	state := r.URL.Query().Get("state")
	authCode := r.URL.Query().Get("code")
	if sessionID == "" || state == "" || authCode == "" {
		writeError(w, http.StatusBadRequest, "session, state, code required")
		return
	}
	res, err := q.svc.HandleReturn(r.Context(), tenantID, service.HandleReturnInput{
		SessionID: sessionID, State: state, AuthCode: authCode,
	})
	if err != nil {
		// Auth/state mismatch is the only path here that returns a
		// non-nil error; the "expired"/"failed" cases come back via
		// res.Status without an error.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	target := fmt.Sprintf("%s?session=%s&status=%s", q.frontendBase, sessionID, res.Status)
	if res.FailureReason != "" {
		target += "&reason=" + res.FailureReason
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// sessionResp is what the post-redirect UI polls for. We expose only
// the fields the UI needs — never the state secret or the full cert.
type sessionResp struct {
	ID            string `json:"id"`
	RequestID     string `json:"request_id"`
	Status        string `json:"status"`
	Provider      string `json:"provider"`
	FailureReason string `json:"failure_reason,omitempty"`
}

func (q *qesRoutes) session(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	id := r.PathValue("id")
	row, err := q.svcSession(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if row == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, row)
}

// svcSession is the thin pass-through; kept here to avoid pulling
// the full TSPSession struct into the public service surface.
func (q *qesRoutes) svcSession(ctx context.Context, tenantID, id string) (*sessionResp, error) {
	row, err := q.svc.GetSession(ctx, tenantID, id)
	if err != nil || row == nil {
		return nil, err
	}
	return &sessionResp{
		ID: row.ID, RequestID: row.RequestID, Status: row.Status,
		Provider: row.Provider, FailureReason: row.FailureReason,
	}, nil
}

type certResp struct {
	ID         string `json:"id"`
	SignerID   string `json:"signer_id"`
	Provider   string `json:"provider"`
	SubjectDN  string `json:"subject_dn"`
	IssuerDN   string `json:"issuer_dn"`
	SerialHex  string `json:"serial_hex"`
	NotBefore  string `json:"not_before"`
	NotAfter   string `json:"not_after"`
	CreatedAt  string `json:"created_at"`
}

func (q *qesRoutes) certificates(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	requestID := r.URL.Query().Get("request_id")
	if requestID == "" {
		writeError(w, http.StatusBadRequest, "request_id required")
		return
	}
	rows, err := q.svc.GetCertificates(r.Context(), tenantID, requestID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	out := make([]certResp, 0, len(rows))
	for _, c := range rows {
		out = append(out, certResp{
			ID: c.ID, SignerID: c.SignerID, Provider: c.Provider,
			SubjectDN: c.SubjectDN, IssuerDN: c.IssuerDN,
			SerialHex: c.SerialHex,
			NotBefore: c.NotBefore.Format("2006-01-02T15:04:05Z07:00"),
			NotAfter:  c.NotAfter.Format("2006-01-02T15:04:05Z07:00"),
			CreatedAt: c.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"certificates": out})
}

// silence unused import
var _ = errors.New
