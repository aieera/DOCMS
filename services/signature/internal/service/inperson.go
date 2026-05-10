// ADR 0073 — in-person signing ceremony.
//
// One device, sequential signer + witness. The /sign/in-person/{id}
// page calls SignInPerson once per signer in order_index sequence.
// Each call:
//   1. Loads the request and verifies that the supplied signer is
//      the next one expected. If not, returns ErrOutOfOrder with
//      the expected signer id so the UI can fast-forward.
//   2. Records the signature via RecordSignature, which on the
//      last-signer commit also stamps final_hash_sha256 from the
//      caller-supplied document hash.
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
}

// SignInPerson records one step of the in-person ceremony. The
// calling session must be authenticated and have the right to view
// the document; the named signer row is what gets marked as signed.
func (s *Service) SignInPerson(ctx context.Context, in InPersonSignInput) error {
	req, err := s.repo.GetByID(ctx, in.TenantID, in.RequestID)
	if err != nil || req == nil {
		return fmt.Errorf("request not found")
	}
	if req.SigningMode != "in_person" {
		return fmt.Errorf("request signing_mode is %q, not in_person", req.SigningMode)
	}
	if req.Status == "completed" {
		return fmt.Errorf("request already completed")
	}
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
	})
}
