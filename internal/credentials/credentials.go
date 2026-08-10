// Package credentials stores and retrieves the Deezer ARL encrypted at rest.
//
// The ARL is encrypted with XChaCha20-Poly1305. The encryption key is derived
// from the DEEZNT_SECRET environment variable using HKDF-SHA256. The
// ciphertext (nonce‖ciphertext‖tag) is stored hex-encoded in the meta table
// under the key "credentials.arl".
package credentials

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const (
	metaKeyARL = "credentials.arl"

	// EnvSecret is the environment variable that provides the passphrase used
	// to derive the ARL encryption key.
	EnvSecret = "DEEZNT_SECRET"
)

// metaStore is the subset of store.Store used here, avoiding a circular import.
type metaStore interface {
	GetMeta(ctx context.Context, key string) (string, error)
	SetMeta(ctx context.Context, key, value string) error
}

// ErrNoSecret is returned when DEEZNT_SECRET is not set.
var ErrNoSecret = errors.New("DEEZNT_SECRET is not set — set it to encrypt/decrypt the stored ARL")

// deriveKey produces a 32-byte encryption key from secret via HKDF-SHA256.
func deriveKey(secret string) ([]byte, error) {
	r := hkdf.New(sha256.New, []byte(secret), nil, []byte("deeznt-arl-encryption-key-v1"))
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("credentials: derive key: %w", err)
	}
	return key, nil
}

// keyFromEnv reads DEEZNT_SECRET and derives the encryption key from it.
func keyFromEnv() ([]byte, error) {
	secret := os.Getenv(EnvSecret)
	if secret == "" {
		return nil, ErrNoSecret
	}
	return deriveKey(secret)
}

// LoadARL returns configARL if non-empty, otherwise reads the encrypted
// credential from st. This centralises the resolution order: config/env first,
// then the encrypted credential in the database.
func LoadARL(ctx context.Context, configARL string, st metaStore) (string, error) {
	if configARL != "" {
		return configARL, nil
	}
	return GetARL(ctx, st)
}

// SetARL encrypts arl with the key derived from DEEZNT_SECRET and persists
// it in the meta table.
func SetARL(ctx context.Context, st metaStore, arl string) error {
	key, err := keyFromEnv()
	if err != nil {
		return err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return fmt.Errorf("credentials: init cipher: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("credentials: generate nonce: %w", err)
	}
	// Seal appends ciphertext+tag to nonce, producing nonce‖ciphertext‖tag.
	blob := aead.Seal(nonce, nonce, []byte(arl), nil)
	return st.SetMeta(ctx, metaKeyARL, hex.EncodeToString(blob))
}

// GetARL returns the decrypted ARL, or "" if none has been saved yet.
// Returns ErrNoSecret if DEEZNT_SECRET is unset but an encrypted ARL exists.
func GetARL(ctx context.Context, st metaStore) (string, error) {
	encoded, err := st.GetMeta(ctx, metaKeyARL)
	if err != nil {
		return "", err
	}
	if encoded == "" {
		return "", nil
	}
	key, err := keyFromEnv()
	if err != nil {
		return "", err
	}
	blob, err := hex.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("credentials: decode blob: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return "", fmt.Errorf("credentials: init cipher: %w", err)
	}
	nonceSize := aead.NonceSize()
	if len(blob) < nonceSize {
		return "", fmt.Errorf("credentials: blob too short")
	}
	plaintext, err := aead.Open(nil, blob[:nonceSize], blob[nonceSize:], nil)
	if err != nil {
		return "", fmt.Errorf("credentials: decrypt failed (wrong DEEZNT_SECRET?): %w", err)
	}
	return string(plaintext), nil
}
