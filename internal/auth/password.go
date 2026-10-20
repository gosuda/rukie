package auth

import (
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	argonMemoryKib  = 19456
	argonIterations = 2
	argonParallel   = 1
	argonVersion    = argon2.Version
	argonSaltLen    = 16
	argonKeyLen     = 32
	argonAlgo       = "argon2id-v19"

	PasswordMinChars = 12
	PasswordMaxBytes = 512
)

//go:embed blocklist.txt
var blocklistRaw string

var blocklist = func() map[string]bool {
	set := map[string]bool{}
	for line := range strings.Lines(strings.TrimSpace(blocklistRaw)) {
		if line != "" {
			set[line] = true
		}
	}
	return set
}()

func BlocklistSize() int {
	return len(blocklist)
}

func CheckPasswordPolicy(password, username string) error {
	if len(password) > PasswordMaxBytes {
		return errors.New("password exceeds 512 bytes")
	}
	if utf8.RuneCountInString(password) < PasswordMinChars {
		return errors.New("password must be at least 12 characters")
	}
	lowered := strings.ToLower(password)
	if blocklist[lowered] {
		return errors.New("password is too common")
	}
	if username != "" && strings.EqualFold(password, username) {
		return errors.New("password must not equal the username")
	}
	return nil
}

func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	return encodePHC(salt, argon2.IDKey([]byte(password), salt, argonIterations, argonMemoryKib, argonParallel, argonKeyLen)), nil
}

func encodePHC(salt, hash []byte) string {
	return "$argon2id$v=" + itoa(argonVersion) +
		"$m=" + itoa(argonMemoryKib) + ",t=" + itoa(argonIterations) + ",p=" + itoa(argonParallel) +
		"$" + base64.RawStdEncoding.EncodeToString(salt) +
		"$" + base64.RawStdEncoding.EncodeToString(hash)
}

func itoa(v uint32) string {
	return fmt.Sprintf("%d", v)
}

type phcParams struct {
	salt     []byte
	hash     []byte
	memory   uint32
	iters    uint32
	parallel uint8
}

func parsePHC(encoded string) (phcParams, error) {
	var p phcParams
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return p, errors.New("unsupported password hash format")
	}
	var version uint32
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argonVersion {
		return p, errors.New("unsupported argon2 version")
	}
	var m, t uint32
	var par uint32
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &par); err != nil {
		return p, errors.New("malformed argon2 parameters")
	}
	if m < 8 || m > 1048576 || t < 1 || t > 16 || par < 1 || par > 8 {
		return p, errors.New("argon2 parameters outside safe bounds")
	}
	p.memory = m
	p.iters = t
	p.parallel = uint8(par)
	var err error
	p.salt, err = decodeB64(parts[4])
	if err != nil {
		return p, errors.New("malformed argon2 salt")
	}
	p.hash, err = decodeB64(parts[5])
	if err != nil {
		return p, errors.New("malformed argon2 hash")
	}
	if len(p.salt) != argonSaltLen || len(p.hash) != argonKeyLen {
		return p, errors.New("unexpected argon2 salt or hash length")
	}
	return p, nil
}

func decodeB64(s string) ([]byte, error) {
	if out, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return out, nil
	}
	return base64.StdEncoding.DecodeString(s)
}

func VerifyPassword(password, encoded string) (bool, error) {
	p, err := parsePHC(encoded)
	if err != nil {
		return false, err
	}
	candidate := argon2.IDKey([]byte(password), p.salt, p.iters, p.memory, p.parallel, uint32(argonKeyLen))
	return subtle.ConstantTimeCompare(candidate, p.hash) == 1, nil
}

func NeedsRehash(encoded string) bool {
	p, err := parsePHC(encoded)
	if err != nil {
		return true
	}
	return p.memory != argonMemoryKib || p.iters != argonIterations || p.parallel != argonParallel
}

var dummySalt = []byte("rukie-dummy-salt")

func DummyVerify(password string) {
	argon2.IDKey([]byte(password), dummySalt, argonIterations, argonMemoryKib, argonParallel, argonKeyLen)
}
