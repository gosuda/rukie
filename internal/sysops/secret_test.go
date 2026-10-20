package sysops

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestSecretKeyGenerateAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	id, err := GenerateSecretKey(path)
	if err != nil {
		t.Fatalf("GenerateSecretKey: %v", err)
	}
	if len(id) != 16 {
		t.Fatalf("key id = %q, want 16 lowercase hex chars", id)
	}
	for _, c := range id {
		if c < '0' || c > 'f' || (c > '9' && c < 'a') {
			t.Fatalf("key id = %q, want lowercase hex", id)
		}
	}
	key, err := LoadSecretKey(path)
	if err != nil {
		t.Fatalf("LoadSecretKey: %v", err)
	}
	if KeyID(key) != id {
		t.Fatal("loaded key id differs from the generated id")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %o, want 600", info.Mode().Perm())
	}
}

func TestSecretKeyRefusesOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	first, err := GenerateSecretKey(path)
	if err != nil {
		t.Fatalf("GenerateSecretKey: %v", err)
	}
	if _, gerr := GenerateSecretKey(path); gerr == nil {
		t.Fatal("second GenerateSecretKey succeeded, want overwrite refusal")
	}
	second, err := RotateSecretKey(path)
	if err != nil {
		t.Fatalf("RotateSecretKey: %v", err)
	}
	if first == second {
		t.Fatal("rotation returned the same key id; the key did not change")
	}
	key, err := LoadSecretKey(path)
	if err != nil {
		t.Fatalf("LoadSecretKey: %v", err)
	}
	if KeyID(key) != second {
		t.Fatal("rotated file does not hold the rotated key")
	}
}

func TestSecretKeyRejectsWrongLength(t *testing.T) {
	path := filepath.Join(t.TempDir(), "short.key")
	if err := os.WriteFile(path, []byte("too short"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadSecretKey(path); err == nil {
		t.Fatal("LoadSecretKey accepted a 9-byte file, want a length error")
	}
	if _, err := LoadSecretKey(filepath.Join(t.TempDir(), "missing.key")); err == nil {
		t.Fatal("LoadSecretKey accepted a missing file, want an error")
	}
}

func TestKeyIDIsStable(t *testing.T) {
	key := make([]byte, SecretKeySize)
	for i := range key {
		key[i] = byte(i)
	}
	first := KeyID(key)
	second := KeyID(key)
	if first != second {
		t.Fatal("KeyID is not deterministic")
	}
	sum := sha256.Sum256(key)
	if want := hex.EncodeToString(sum[:8]); first != want {
		t.Fatalf("KeyID = %q, want hex of the first 8 SHA-256 bytes %q", first, want)
	}
	other := make([]byte, SecretKeySize)
	copy(other, key)
	other[0] ^= 0xff
	if KeyID(key) == KeyID(other) {
		t.Fatal("KeyID ignores key bytes")
	}
}

func TestSecretKeyRefusesLoosePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	if _, err := GenerateSecretKey(path); err != nil {
		t.Fatalf("GenerateSecretKey: %v", err)
	}
	for _, mode := range []os.FileMode{0o644, 0o640, 0o601} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatalf("chmod %o: %v", mode, err)
		}
		if _, err := LoadSecretKey(path); err == nil {
			t.Fatalf("LoadSecretKey accepted mode %04o, want refusal", mode)
		}
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod 0600: %v", err)
	}
	if _, err := LoadSecretKey(path); err != nil {
		t.Fatalf("LoadSecretKey refused mode 0600: %v", err)
	}
}
