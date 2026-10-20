package sysops

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// SecretKeySize is the raw key length in bytes.
const SecretKeySize = 32

// DefaultSecretKeyPath keeps the key outside data_dir per O36.
var DefaultSecretKeyPath = filepath.Join("/etc", "rukie", "secret.key")

// KeyID returns the lowercase hex of the first 8 bytes of SHA-256 over
// the key. Manifests and logs carry the id, never the key.
func KeyID(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:8])
}

// GenerateSecretKey writes a fresh 32-byte key to path at mode 0600 and
// returns its key id. An existing file is refused; rotate it explicitly.
func GenerateSecretKey(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("sysops: secret key path is required")
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("sysops: create key dir: %w", err)
		}
	}
	f, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return "", fmt.Errorf("sysops: %s exists; refusing to overwrite (use --force to rotate)", path)
		}
		return "", fmt.Errorf("sysops: create key file: %w", err)
	}
	key := make([]byte, SecretKeySize)
	if _, rerr := rand.Read(key); rerr != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("sysops: randomize key: %w", rerr)
	}
	if _, werr := f.Write(key); werr != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("sysops: write key file: %w", werr)
	}
	if serr := f.Sync(); serr != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("sysops: sync key file: %w", serr)
	}
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("sysops: close key file: %w", cerr)
	}
	return KeyID(key), nil
}

// RotateSecretKey replaces the key at path, for --force rotations.
// Rotation invalidates every token and sealed credential at once. The
// replacement lands atomically: temp file, fsync, rename. A crash
// never leaves a truncated live key.
func RotateSecretKey(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("sysops: secret key path is required")
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("sysops: create key dir: %w", err)
		}
	}
	key := make([]byte, SecretKeySize)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("sysops: randomize key: %w", err)
	}
	tmp, err := os.OpenFile(filepath.Clean(path+".tmp"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("sysops: write key temp file: %w", err)
	}
	if _, werr := tmp.Write(key); werr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("sysops: write key temp file: %w", werr)
	}
	if serr := tmp.Sync(); serr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("sysops: sync key temp file: %w", serr)
	}
	if cerr := tmp.Close(); cerr != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("sysops: close key temp file: %w", cerr)
	}
	if rerr := os.Rename(tmp.Name(), path); rerr != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("sysops: install rotated key: %w", rerr)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("sysops: chmod key file: %w", err)
	}
	return KeyID(key), nil
}

// LoadSecretKey reads and validates the 32-byte key file.
func LoadSecretKey(path string) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("sysops: read key file: %w", err)
	}
	if len(raw) != SecretKeySize {
		return nil, fmt.Errorf("sysops: key file %s holds %d bytes, want %d", path, len(raw), SecretKeySize)
	}
	return raw, nil
}
