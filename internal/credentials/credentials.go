// Package credentials stores and retrieves the Deezer ARL encrypted at rest.
//
// The ARL is encrypted with XChaCha20-Poly1305 using a 32-byte key kept in a
// separate file alongside the database. The ciphertext (nonce‖tag‖plaintext) is
// stored hex-encoded in the meta table under the key "credentials.arl".
// Keeping the key file and the database in separate files means the ciphertext
// cannot be decrypted by someone who obtains only the database.
package credentials

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	metaKeyARL = "credentials.arl"
	keyFileName = ".deebeets.key"
)

// metaStore is the subset of store.Store used here, allowing the package to
// avoid a circular import.
type metaStore interface {
	GetMeta(ctx context.Context, key string) (string, error)
	SetMeta(ctx context.Context, key, value string) error
}

func keyFilePath(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), keyFileName)
}

// loadKey reads the existing key file. Returns os.ErrNotExist if absent.
func loadKey(dbPath string) ([]byte, error) {
	data, err := os.ReadFile(keyFilePath(dbPath))
	if err != nil {
		return nil, err
	}
	if len(data) != chacha20poly1305.KeySize {
		return nil, fmt.Errorf("credentials: key file has wrong length (expected %d bytes)", chacha20poly1305.KeySize)
	}
	return data, nil
}

// loadOrCreateKey returns the key, generating and persisting one if absent.
func loadOrCreateKey(dbPath string) ([]byte, error) {
	key, err := loadKey(dbPath)
	if err == nil {
		return key, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("credentials: read key file: %w", err)
	}
	key = make([]byte, chacha20poly1305.KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("credentials: generate key: %w", err)
	}
	if err := os.WriteFile(keyFilePath(dbPath), key, 0600); err != nil {
		return nil, fmt.Errorf("credentials: write key file: %w", err)
	}
	return key, nil
}

// LoadARL returns configARL if non-empty, otherwise reads the encrypted
// credential from st. This centralises the resolution order: config/env first,
// then the encrypted credential in the database.
func LoadARL(ctx context.Context, configARL, dbPath string, st metaStore) (string, error) {
	if configARL != "" {
		return configARL, nil
	}
	return GetARL(ctx, st, dbPath)
}

// SetARL encrypts arl and persists it in the meta table.
func SetARL(ctx context.Context, st metaStore, dbPath, arl string) error {
	key, err := loadOrCreateKey(dbPath)
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
func GetARL(ctx context.Context, st metaStore, dbPath string) (string, error) {
	encoded, err := st.GetMeta(ctx, metaKeyARL)
	if err != nil {
		return "", err
	}
	if encoded == "" {
		return "", nil
	}
	// Key must already exist; we don't create one during reads because that
	// would silently swap in a new key that cannot decrypt the stored blob.
	key, err := loadKey(dbPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("credentials: key file missing — re-run `deebeets login` to re-save the ARL")
		}
		return "", fmt.Errorf("credentials: read key file: %w", err)
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
		return "", fmt.Errorf("credentials: decrypt failed (wrong key?): %w", err)
	}
	return string(plaintext), nil
}
