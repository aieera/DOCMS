package email

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"

	"github.com/aieera/sedoc/pkg/crypto"
)

// keyFromEnv reads SEDOC_LOCAL_KEK (a base64-encoded 32-byte key) or, if
// absent, derives one by SHA-256ing whatever's in SEDOC_GATEWAY_SECRET
// — the dev-shape fallback so a fresh checkout doesn't need an extra env
// var to bring email ingestion up. Production deploys MUST set
// SEDOC_LOCAL_KEK explicitly; the absence of an explicit KEK in prod is
// caught by the same config-validation that the rest of the platform uses.
func keyFromEnv() ([]byte, error) {
	if raw := os.Getenv("SEDOC_LOCAL_KEK"); raw != "" {
		b, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("SEDOC_LOCAL_KEK decode: %w", err)
		}
		if len(b) != crypto.DEKSize {
			return nil, fmt.Errorf("SEDOC_LOCAL_KEK must decode to %d bytes, got %d", crypto.DEKSize, len(b))
		}
		return b, nil
	}
	fallback := os.Getenv("SEDOC_GATEWAY_SECRET")
	if fallback == "" {
		return nil, errors.New("no encryption key: set SEDOC_LOCAL_KEK")
	}
	h := sha256.Sum256([]byte(fallback))
	return h[:], nil
}

// encryptPassword wraps plaintext with AES-256-GCM. The on-disk layout is
// `<12-byte nonce><ciphertext+tag>` so we can store the whole thing in a
// single BYTEA column and decrypt without a second metadata column.
func encryptPassword(plaintext string) ([]byte, error) {
	if plaintext == "" {
		return nil, nil
	}
	key, err := keyFromEnv()
	if err != nil {
		return nil, err
	}
	ct, nonce, err := crypto.EncryptData([]byte(plaintext), key)
	if err != nil {
		return nil, err
	}
	return append(nonce, ct...), nil
}

// decryptPassword reverses encryptPassword. An empty/nil blob returns "".
func decryptPassword(blob []byte) (string, error) {
	if len(blob) == 0 {
		return "", nil
	}
	if len(blob) < crypto.NonceSize+1 {
		return "", errors.New("ciphertext too short")
	}
	key, err := keyFromEnv()
	if err != nil {
		return "", err
	}
	nonce, ct := blob[:crypto.NonceSize], blob[crypto.NonceSize:]
	pt, err := crypto.DecryptData(ct, nonce, key)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
