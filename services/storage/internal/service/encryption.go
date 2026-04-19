package service

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/vaultdms/vaultdms/pkg/crypto"
)

// envelopeResult carries the outputs of a single file encryption operation.
// The DEK itself is never returned — callers only need the wrapped form
// (to persist) and the nonce (to store alongside the ciphertext).
type envelopeResult struct {
	EncryptedDEK []byte
	Nonce        []byte
	KEKID        string
	Ciphertext   []byte // full ciphertext (GCM appends auth tag)
}

// encryptAll reads the entire plaintext from r into memory, generates a
// fresh DEK via the KeyManager, AES-256-GCM encrypts with a random nonce,
// and returns the envelope outputs ready for storage + re-upload.
//
// The in-memory buffer is an honest trade-off: AES-GCM can't be streamed
// with authenticity (the tag is only verifiable after reading all bytes),
// and our single-PUT ceiling is 5 GiB, which callers can budget for. When
// multipart lands in Phase B1.1 we'll switch to GCM-SIV with per-part
// nonces or move to a streaming AEAD (e.g. miscreant) — both land in a
// dedicated encryption revision.
func (s *Service) encryptAll(ctx context.Context, r io.Reader, kekID string) (*envelopeResult, error) {
	if s.cfg.KMS == nil {
		return nil, fmt.Errorf("envelope encryption: KeyManager not configured")
	}
	plainDEK, encDEK, err := s.cfg.KMS.GenerateDataKey(ctx, kekID)
	if err != nil {
		return nil, fmt.Errorf("gen dek: %w", err)
	}

	buf, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read plaintext: %w", err)
	}
	ct, nonce, err := crypto.EncryptData(buf, plainDEK)
	if err != nil {
		return nil, fmt.Errorf("gcm encrypt: %w", err)
	}

	// Zero the plaintext DEK ASAP — best-effort; Go's GC can still retain
	// copies, but we reduce the window for accidental exposure (log dump,
	// core file). The DEK is 32 bytes.
	for i := range plainDEK {
		plainDEK[i] = 0
	}

	return &envelopeResult{
		EncryptedDEK: encDEK,
		Nonce:        nonce,
		KEKID:        kekID,
		Ciphertext:   ct,
	}, nil
}

// decryptAll reverses encryptAll. Reads S3 ciphertext, unwraps DEK via
// KeyManager, returns plaintext ready to stream to the download handler.
// Also in-memory for the same auth-tag reason.
func (s *Service) decryptAll(ctx context.Context, r io.Reader, kekID string, encryptedDEK, nonce []byte) ([]byte, error) {
	if s.cfg.KMS == nil {
		return nil, fmt.Errorf("envelope encryption: KeyManager not configured")
	}
	plainDEK, err := s.cfg.KMS.DecryptDataKey(ctx, kekID, encryptedDEK)
	if err != nil {
		return nil, fmt.Errorf("unwrap dek: %w", err)
	}
	defer func() {
		for i := range plainDEK {
			plainDEK[i] = 0
		}
	}()
	ct, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read ciphertext: %w", err)
	}
	pt, err := crypto.DecryptData(ct, nonce, plainDEK)
	if err != nil {
		return nil, fmt.Errorf("gcm decrypt: %w", err)
	}
	return pt, nil
}

// ensureBuffer materializes r into a bytes.Buffer for callers that need to
// read the stream more than once (scan + hash + encrypt). Same 5 GiB
// caveat as encryptAll.
func ensureBuffer(r io.Reader) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		return nil, err
	}
	return &buf, nil
}
