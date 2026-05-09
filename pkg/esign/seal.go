// Token sealing helper.
//
// Per ADR 0071 the access_token + refresh_token columns are
// encrypted at rest so a Postgres dump alone doesn't yield usable
// bearer tokens. We use AES-256-GCM with a 32-byte secret the
// service derives from the per-tenant KEK (ADR 0022); the wire
// format is `nonce_b64 . ciphertext_b64` so callers can store one
// string per column.
//
// This is a deliberately small wrapper. Full Vault-derived per-
// tenant KEK rotation lives in pkg/crypto.KeyManager; this file
// covers the "we have one secret in memory, encrypt with AES-GCM"
// case which is what the OAuth-token flow actually needs.
package esign

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
)

// SealString encrypts plaintext with the supplied 32-byte key.
// Empty plaintext seals to empty string (so callers don't have to
// branch on optional fields like refresh_token).
func SealString(plaintext, key []byte) (string, error) {
	if plaintext == nil || len(plaintext) == 0 {
		return "", nil
	}
	if len(key) != 32 {
		return "", errors.New("esign seal: key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nil, nonce, plaintext, nil)
	return base64.RawURLEncoding.EncodeToString(nonce) + "." + base64.RawURLEncoding.EncodeToString(ct), nil
}

// UnsealString reverses SealString. Empty input returns "".
func UnsealString(sealed string, key []byte) (string, error) {
	if sealed == "" {
		return "", nil
	}
	if len(key) != 32 {
		return "", errors.New("esign unseal: key must be 32 bytes")
	}
	dot := strings.IndexByte(sealed, '.')
	if dot < 0 {
		return "", errors.New("esign unseal: bad format")
	}
	nonce, err := base64.RawURLEncoding.DecodeString(sealed[:dot])
	if err != nil {
		return "", err
	}
	ct, err := base64.RawURLEncoding.DecodeString(sealed[dot+1:])
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
