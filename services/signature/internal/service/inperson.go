// ADR 0073 — in-person signing ceremony.
//
// One device, sequential signer + witness. The /sign/in-person/{id}
// page calls SignInPerson once per signer in order_index sequence.
// Each call:
//  1. Loads the request and verifies that the supplied signer is
//     the next one expected. If not, returns ErrOutOfOrder with
//     the expected signer id so the UI can fast-forward.
//  2. Records the signature via RecordSignature, which on the
//     last-signer commit also stamps final_hash_sha256 from the
//     caller-supplied document hash.
//
// The ceremony is auth-gated by the calling session, not by per-
// signer magic-link tokens — the device user is the workflow
// operator (e.g. a counter clerk), the signer identity comes from
// the named signer row.
package service

import (
	"context"
	"errors"
	"fmt"
)

// ErrOutOfOrder is returned by SignInPerson when the caller tries to
// sign for a signer that isn't next in the sequence. The wrapped
// error message carries the expected signer id; the handler maps
// this to HTTP 409 with { expected_signer_id: "..." }.
var ErrOutOfOrder = errors.New("signer out of order")

// OutOfOrderError is the typed error returned with the expected next
// signer id so the in-person UI can recover by jumping to the right
// step instead of leaving the operator stuck.
type OutOfOrderError struct {
	ExpectedSignerID string
}

func (e *OutOfOrderError) Error() string {
	return fmt.Sprintf("signer out of order; expected %s", e.ExpectedSignerID)
}
func (e *OutOfOrderError) Unwrap() error { return ErrOutOfOrder }

// InPersonSignInput is the per-step payload from the tablet UI.
type InPersonSignInput struct {
	TenantID   string
	RequestID  string
	SignerID   string
	IPAddress  string
	SVGPath    string
	DeviceKind string
	DocHashHex string

	// OperatorUserID + OperatorIsAdmin identify the device operator running the
	// ceremony. The operator must be the request's creator or a tenant admin —
	// the in-person path has no per-signer token, so this is the only thing
	// stopping any authenticated tenant member from completing a ceremony for
	// any signer on any request.
	OperatorUserID  string
	OperatorIsAdmin bool
}

// SignInPerson records one step of the in-person ceremony. The calling session
// must be authenticated AND authorized as the ceremony operator (request creator
// or tenant admin); the named signer row is what gets marked as signed.
func (s *Service) SignInPerson(ctx context.Context, in InPersonSignInput) error {
	req, err := s.repo.GetByID(ctx, in.TenantID, in.RequestID)
	if err != nil || req == nil {
		return fmt.Errorf("request not found")
	}
	if !in.OperatorIsAdmin && req.CreatedBy != in.OperatorUserID {
		return fmt.Errorf("forbidden: only the request creator or an admin may run the in-person ceremony")
	}
	if req.SigningMode != "in_person" {
		return fmt.Errorf("request signing_mode is %q, not in_person", req.SigningMode)
	}
	if req.Status == "completed" {
		return fmt.Errorf("request already completed")
	}
	// SECURITY NOTE (Epic 5 #12, TRACKED): in.DocHashHex is the hash the CLIENT
	// says the signer saw; it is stored and later stamped as final_hash_sha256
	// (Verify uses it to detect post-sign blob swaps). It is NOT recomputed over
	// the real version bytes here, so a hostile operator device could stamp a hash
	// that doesn't match the sealed document. The fix is to fetch the version
	// bytes (as the seal path's fetchVersionPDF does) and recompute the hash
	// server-side. Deferred — needs the document-fetch dependency wired into this
	// path. See docs/security/epic5-signature-followups.md.
	expected := s.NextExpectedSigner(req)
	if expected == "" {
		return fmt.Errorf("no pending signers")
	}
	if expected != in.SignerID {
		return &OutOfOrderError{ExpectedSignerID: expected}
	}
	return s.RecordSignature(ctx, RecordSignatureInput{
		TenantID:   in.TenantID,
		RequestID:  in.RequestID,
		SignerID:   in.SignerID,
		IPAddress:  in.IPAddress,
		SVGPath:    in.SVGPath,
		DeviceKind: in.DeviceKind,
		DocHashHex: in.DocHashHex,
		// Operator authority checked above; no per-signer magic-link token exists
		// for the in-person flow, so exempt it from the token gate.
		operatorSigned: true,
	})
}
