package mirror

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"math"
)

type SealedCredential struct {
	Last4      string `json:"last4"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
	KeyVersion uint32 `json:"key_version"`
}

func Seal(key, token []byte, aad string, version uint32, last4 string) (SealedCredential, error) {
	if len(key) != 32 {
		return SealedCredential{}, fmt.Errorf("Seal: AES-256 key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return SealedCredential{}, fmt.Errorf("Seal: create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return SealedCredential{}, fmt.Errorf("Seal: create GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return SealedCredential{}, fmt.Errorf("Seal: generate nonce: %w", err)
	}
	return SealedCredential{KeyVersion: version, Nonce: nonce, Ciphertext: gcm.Seal(nil, nonce, token, []byte(aad)), Last4: last4}, nil
}

// Open returns plaintext that the caller must erase with Zero when no longer needed.
func Open(key []byte, s SealedCredential, aad string) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("Open: AES-256 key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("Open: create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("Open: create GCM: %w", err)
	}
	if len(s.Nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("Open: invalid nonce length")
	}
	plain, err := gcm.Open(nil, s.Nonce, s.Ciphertext, []byte(aad))
	if err != nil {
		return nil, fmt.Errorf("Open: authenticate credential: %w", err)
	}
	return plain, nil
}

func Zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// CheckedUint32 converts a stored key version, failing closed instead of
// wrapping negatives or overflows into credential state.
func CheckedUint32(v int64) (uint32, error) {
	if v < 0 || v > math.MaxUint32 {
		return 0, fmt.Errorf("mirror seal: key version out of range")
	}
	return uint32(v), nil
}

func Last4(token string) string {
	r := []rune(token)
	if len(r) > 4 {
		r = r[len(r)-4:]
	}
	return string(r)
}
