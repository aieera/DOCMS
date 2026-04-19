// Package crypto provides envelope encryption primitives (AES-256-GCM) and a
// KeyManager interface for wrapping/unwrapping data-encryption keys via a KMS.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
)

// DEKSize is the byte length of a data-encryption key (AES-256).
const DEKSize = 32

// NonceSize is the byte length of the GCM nonce.
const NonceSize = 12

// GenerateDEK returns a cryptographically random 32-byte DEK.
func GenerateDEK() ([]byte, error) {
	dek := make([]byte, DEKSize)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, fmt.Errorf("gen dek: %w", err)
	}
	return dek, nil
}

// EncryptData encrypts plaintext under dek using AES-256-GCM. Returns the
// ciphertext (with the auth tag appended) and the 12-byte nonce.
func EncryptData(plaintext, dek []byte) (ciphertext, nonce []byte, err error) {
	if len(dek) != DEKSize {
		return nil, nil, fmt.Errorf("dek must be %d bytes, got %d", DEKSize, len(dek))
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, nil, fmt.Errorf("new aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("new gcm: %w", err)
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("gen nonce: %w", err)
	}
	ciphertext = gcm.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nonce, nil
}

// DecryptData reverses EncryptData.
func DecryptData(ciphertext, nonce, dek []byte) ([]byte, error) {
	if len(dek) != DEKSize {
		return nil, fmt.Errorf("dek must be %d bytes, got %d", DEKSize, len(dek))
	}
	if len(nonce) != NonceSize {
		return nil, fmt.Errorf("nonce must be %d bytes, got %d", NonceSize, len(nonce))
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("new aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	pt, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("gcm open: %w", err)
	}
	return pt, nil
}
