package webhook

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SecretPrefix prefixes every displayed endpoint secret.
const SecretPrefix = "whsec_"

// SecretRawLen is the decoded length of every endpoint secret in bytes.
const SecretRawLen = 32

// VerifyToleranceSeconds bounds |now-tsUnix| for an accepted signature.
const VerifyToleranceSeconds = 300

// GenerateSecret mints 32 random bytes and returns the display form
// alongside the raw bytes. The display form keeps base64 padding.
func GenerateSecret() (display string, raw []byte, err error) {
	raw = make([]byte, SecretRawLen)
	if _, err = rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("GenerateSecret: %w", err)
	}
	return SecretPrefix + base64.StdEncoding.EncodeToString(raw), raw, nil
}

// IsValidSecretDisplay reports whether s parses as a whsec_ secret
// holding exactly SecretRawLen decoded bytes.
func IsValidSecretDisplay(s string) bool {
	rest, ok := strings.CutPrefix(s, SecretPrefix)
	if !ok {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(rest)
	return err == nil && len(raw) == SecretRawLen
}

// Sign computes the Standard Webhooks v1 signature over the canonical
// bytes msgID + "." + tsUnix + "." + body and returns v1,<base64>.
func Sign(secret []byte, msgID string, tsUnix int64, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(msgID))
	mac.Write([]byte{'.'})
	mac.Write([]byte(strconv.FormatInt(tsUnix, 10)))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// SignHeader renders the webhook-signature header: one v1 entry, or two
// space-separated entries while a previous secret overlaps rotation.
func SignHeader(current, prev []byte, msgID string, tsUnix int64, body []byte) string {
	header := Sign(current, msgID, tsUnix, body)
	if len(prev) == 0 {
		return header
	}
	return header + " " + Sign(prev, msgID, tsUnix, body)
}

// Verify checks each v1,<base64> entry of a space-separated header against
// each candidate secret in turn and enforces the timestamp tolerance.
// Malformed entries are skipped; an empty match set fails.
func Verify(secrets [][]byte, msgID string, tsUnix int64, body []byte, header string, now time.Time) bool {
	skew := now.Unix() - tsUnix
	if skew > VerifyToleranceSeconds || skew < -VerifyToleranceSeconds {
		return false
	}
	canon := make([]byte, 0, len(msgID)+len(body)+32)
	canon = append(canon, msgID...)
	canon = append(canon, '.')
	canon = append(canon, strconv.FormatInt(tsUnix, 10)...)
	canon = append(canon, '.')
	canon = append(canon, body...)
	for entry := range strings.FieldsSeq(header) {
		sig, ok := strings.CutPrefix(entry, "v1,")
		if !ok || sig == "" {
			continue
		}
		want, err := base64.StdEncoding.DecodeString(sig)
		if err != nil {
			continue
		}
		for _, secret := range secrets {
			if len(secret) == 0 {
				continue
			}
			mac := hmac.New(sha256.New, secret)
			mac.Write(canon)
			if subtle.ConstantTimeCompare(mac.Sum(nil), want) == 1 {
				return true
			}
		}
	}
	return false
}

// SealSecret encrypts raw with AES-256-GCM under sealKey and returns the
// raw nonce-prefixed ciphertext. Callers base64-encode it for storage.
func SealSecret(sealKey []byte, raw []byte, aad string) ([]byte, error) {
	gcm, err := sealGCM(sealKey, "SealSecret")
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("SealSecret: %w", err)
	}
	return gcm.Seal(nonce, nonce, raw, []byte(aad)), nil
}

// OpenSecret decrypts enc, the base64 form of nonce-prefixed ciphertext,
// under sealKey. The aad must equal the value used at seal time.
func OpenSecret(sealKey, enc, aad string) ([]byte, error) {
	gcm, err := sealGCM([]byte(sealKey), "OpenSecret")
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil, fmt.Errorf("OpenSecret: %w", err)
	}
	if len(raw) < gcm.NonceSize() {
		return nil, fmt.Errorf("OpenSecret: ciphertext too short")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, []byte(aad))
	if err != nil {
		return nil, fmt.Errorf("OpenSecret: authenticate secret: %w", err)
	}
	return plain, nil
}

func sealGCM(sealKey []byte, op string) (cipher.AEAD, error) {
	if len(sealKey) != 32 {
		return nil, fmt.Errorf("%s: AES-256 key must be 32 bytes", op)
	}
	block, err := aes.NewCipher(sealKey)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	return gcm, nil
}
