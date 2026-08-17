package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncryptDecryptRoundtrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	pt := "sk-my-plain-token-123"
	enc, err := Encrypt(pt)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !strings.HasPrefix(enc, Prefix) {
		t.Fatalf("missing prefix: %s", enc)
	}
	if strings.Contains(enc, pt) {
		t.Fatal("plaintext leaked into ciphertext")
	}
	got, err := Decrypt(enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != pt {
		t.Fatalf("roundtrip: got %q want %q", got, pt)
	}

	// Key file created with tight permissions.
	key := filepath.Join(dir, "submon", "secret.key")
	info, err := os.Stat(key)
	if err != nil {
		t.Fatal("key file not created")
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestDecryptPassesPlaintextThrough(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	got, err := Decrypt("not-encrypted")
	if err != nil || got != "not-encrypted" {
		t.Fatalf("plaintext passthrough: %q %v", got, err)
	}
}

func TestDecryptFailsWithWrongKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	enc, err := Encrypt("secret")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a regenerated key file.
	keyPath := filepath.Join(dir, "submon", "secret.key")
	os.Remove(keyPath)
	if _, err := Decrypt(enc); err == nil {
		t.Fatal("expected decrypt failure after key rotation")
	}
}

func TestEmptyEncrypt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	out, err := Encrypt("")
	if err != nil || out != "" {
		t.Fatalf("empty encrypt: %q %v", out, err)
	}
}
