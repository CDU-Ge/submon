// Package secret encrypts provider tokens at rest with AES-256-GCM.
//
// Key management is fully self-contained: a random 32-byte master key is
// generated on first use and stored base64-encoded at
// ~/.config/submon/secret.key (0600, XDG_CONFIG_HOME respected). No OS
// keyring or external credential store is involved — the goal is simply to
// keep plaintext secrets out of config.toml (which may be synced, backed up
// or shared), while the key file stays local.
//
// Encrypted values are stored as "enc:v1:<base64(nonce)>:<base64(ciphertext)>"
// wherever a plaintext token used to live. Decrypt() passes plaintext through
// untouched, so mixed plaintext/ciphertext configs keep working during
// migration.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Prefix marks encrypted values.
const Prefix = "enc:v1:"

// keyFilePath resolves the master key location.
func keyFilePath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "submon", "secret.key")
}

// masterKey returns the 32-byte master key, creating and persisting one on
// first use.
func masterKey() ([]byte, error) {
	path := keyFilePath()
	if raw, err := os.ReadFile(path); err == nil {
		return decodeKey(string(raw))
	}
	k := generateKey()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(encodeKey(k)), 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

// KeyPath exposes the key file location (for `submon token` diagnostics).
func KeyPath() string { return keyFilePath() }

func generateKey() []byte {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return k
}

func encodeKey(k []byte) string { return base64.StdEncoding.EncodeToString(k) }
func decodeKey(s string) ([]byte, error) {
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("decode key file: %w", err)
	}
	if len(k) != 32 {
		return nil, fmt.Errorf("key file has %d bytes, want 32", len(k))
	}
	return k, nil
}

// Encrypt encrypts a secret, returning "enc:v1:<nonce>:<ciphertext>".
func Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	key, err := masterKey()
	if err != nil {
		return "", fmt.Errorf("master key: %w", err)
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
	ct := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	return Prefix + base64.StdEncoding.EncodeToString(nonce) + ":" + base64.StdEncoding.EncodeToString(ct), nil
}

// IsEncrypted reports whether a stored value is in encrypted form.
func IsEncrypted(v string) bool { return strings.HasPrefix(v, Prefix) }

// Decrypt decrypts an "enc:v1:..." value; plaintext passes through unchanged.
func Decrypt(v string) (string, error) {
	if !IsEncrypted(v) {
		return v, nil
	}
	key, err := masterKey()
	if err != nil {
		return "", fmt.Errorf("master key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	parts := strings.SplitN(strings.TrimPrefix(v, Prefix), ":", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("malformed encrypted value")
	}
	nonce, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}
	ct, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("ciphertext: %w", err)
	}
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt failed (key file changed?)")
	}
	return string(pt), nil
}
