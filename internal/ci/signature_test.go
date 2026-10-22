package ci_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/gosuda/rukie/internal/ci"
)

func fixtureStatement() ci.Statement {
	return ci.Statement{
		Subject:  strings.Repeat("07", 20),
		Kind:     "a",
		Result:   "bc",
		SignedAt: "2026-10-17T12:00:00Z",
	}
}

func fieldStrings(t *testing.T, payload []byte, want int) []string {
	t.Helper()
	fields, err := ci.SplitPayload(payload)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(fields) != want {
		t.Fatalf("split %d fields, want %d", len(fields), want)
	}
	out := make([]string, len(fields))
	for i, field := range fields {
		out[i] = string(field)
	}
	return out
}

func TestSSHSIGFixtureParity(t *testing.T) {
	payload, err := ci.CanonicalPayloadV1(fixtureStatement())
	if err != nil {
		t.Fatalf("v1 payload: %v", err)
	}
	fields := fieldStrings(t, payload, 7)
	if fields[0] != "ruk-attest-payload-v1" || fields[1] != "sha1" {
		t.Fatalf("header fields = %q, %q", fields[0], fields[1])
	}
	if fields[2] != string(bytes.Repeat([]byte{0x07}, 20)) {
		t.Fatal("subject bytes do not match the sha1 fixture")
	}
	if fields[3] != "a" || fields[4] != "bc" {
		t.Fatalf("kind/result = %q, %q", fields[3], fields[4])
	}
	if fields[5] != "2026-10-17T12:00:00Z" {
		t.Fatalf("timestamp = %q", fields[5])
	}
	if fields[6] != "" {
		t.Fatal("supersedes must be empty when absent")
	}
	shifted := fixtureStatement()
	shifted.Kind = "ab"
	shifted.Result = "c"
	moved, err := ci.CanonicalPayloadV1(shifted)
	if err != nil {
		t.Fatalf("shifted payload: %v", err)
	}
	if bytes.Equal(payload, moved) {
		t.Fatal("moving a byte across the kind/result boundary did not change the payload")
	}

	withSupersedes := fixtureStatement()
	digest := strings.Repeat("01", 32)
	withSupersedes.Supersedes = &digest
	superseded, err := ci.CanonicalPayloadV1(withSupersedes)
	if err != nil {
		t.Fatalf("superseded payload: %v", err)
	}
	if bytes.Equal(payload, superseded) {
		t.Fatal("a superseded digest did not change the payload")
	}
	if got := fieldStrings(t, superseded, 7)[6]; got != string(bytes.Repeat([]byte{0x01}, 32)) {
		t.Fatal("superseded digest is not covered")
	}
	other := strings.Repeat("02", 32)
	withSupersedes.Supersedes = &other
	resuperseded, err := ci.CanonicalPayloadV1(withSupersedes)
	if err != nil {
		t.Fatalf("resuperseded payload: %v", err)
	}
	if bytes.Equal(superseded, resuperseded) {
		t.Fatal("distinct superseded digests share a payload")
	}

	v2payload, err := ci.CanonicalPayloadV2(fixtureStatement())
	if err != nil {
		t.Fatalf("v2 payload: %v", err)
	}
	v2fields := fieldStrings(t, v2payload, 8)
	if v2fields[0] != "ruk-attest-payload-v2" {
		t.Fatalf("v2 domain = %q", v2fields[0])
	}
	if v2fields[6] != "" || v2fields[7] != "" {
		t.Fatal("v2 absence must encode as empty fields")
	}
	if bytes.Equal(payload, v2payload) {
		t.Fatal("v1 and v2 payloads match; the domain bump must separate them")
	}
	bound := fixtureStatement()
	evidence := "sha256:" + strings.Repeat("09", 32)
	bound.Evidence = &evidence
	evident, err := ci.CanonicalPayloadV2(bound)
	if err != nil {
		t.Fatalf("evident payload: %v", err)
	}
	if bytes.Equal(v2payload, evident) {
		t.Fatal("evidence did not change the v2 payload")
	}
	if got := fieldStrings(t, evident, 8)[7]; got != string(bytes.Repeat([]byte{0x09}, 32)) {
		t.Fatal("evidence digest is not covered")
	}
	if _, verr := ci.CanonicalPayloadV1(bound); verr == nil {
		t.Fatal("v1 accepted a statement carrying evidence")
	}

	seed := [32]byte{9, 8, 7}
	key := ed25519.NewKeyFromSeed(seed[:])
	armor, err := ci.SignSSHSIG(payload, key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	inner, found := strings.CutPrefix(armor, "-----BEGIN SSH SIGNATURE-----\n")
	if !found {
		t.Fatalf("armor framing is wrong:\n%s", armor)
	}
	inner, found = strings.CutSuffix(inner, "-----END SSH SIGNATURE-----\n")
	if !found {
		t.Fatalf("armor framing is wrong:\n%s", armor)
	}
	for line := range strings.SplitSeq(inner, "\n") {
		if line == "" {
			continue
		}
		if len(line) > 70 {
			t.Fatalf("armor line has %d characters, want at most 70", len(line))
		}
	}
	public, err := ci.VerifySSHSIG(payload, armor)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !public.Equal(key.Public().(ed25519.PublicKey)) {
		t.Fatal("verified key is not the signing key")
	}
	tampered := bytes.Clone(payload)
	tampered[len(tampered)-1] ^= 0x01
	if _, verr := ci.VerifySSHSIG(tampered, armor); verr == nil {
		t.Fatal("a tampered payload verified")
	}
	framed, _ := strings.CutPrefix(armor, "-----BEGIN SSH SIGNATURE-----\n")
	framed, _ = strings.CutSuffix(framed, "-----END SSH SIGNATURE-----\n")
	raw, err := base64.StdEncoding.DecodeString(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, framed))
	if err != nil {
		t.Fatalf("armor decode: %v", err)
	}
	if len(raw) < 10 || string(raw[:6]) != "SSHSIG" || raw[6] != 0 || raw[7] != 0 || raw[8] != 0 || raw[9] != 1 {
		t.Fatal("armor blob must start with the raw SSHSIG magic and version 1, not a length-prefixed string")
	}
	legacy := append([]byte{0, 0, 0, 6}, []byte("SSHSIG")...)
	if bytes.HasPrefix(raw, legacy) {
		t.Fatal("armor blob carries a length-prefixed magic; OpenSSH and the daemon reject it")
	}
	renamed := bytes.Replace(raw[10:], []byte("ruk-attest"), []byte("other-ns!!"), 1)
	renamed = append(bytes.Clone(raw[:10]), renamed...)
	if bytes.Equal(raw, renamed) {
		t.Fatal("namespace bytes are missing from the armor")
	}
	var renamedArmor strings.Builder
	renamedArmor.WriteString("-----BEGIN SSH SIGNATURE-----\n")
	for len(renamed) > 0 {
		width := min(70, len(renamed))
		renamedArmor.WriteString(base64.StdEncoding.EncodeToString(renamed[:width]))
		renamedArmor.WriteByte('\n')
		renamed = renamed[width:]
	}
	renamedArmor.WriteString("-----END SSH SIGNATURE-----\n")
	if _, err := ci.VerifySSHSIG(payload, renamedArmor.String()); err == nil {
		t.Fatal("a foreign namespace verified")
	}
}

func TestSSHSIGRejectsWrongSubjectWidth(t *testing.T) {
	bad := fixtureStatement()
	bad.Subject = "abcd"
	if _, err := ci.CanonicalPayloadV1(bad); err == nil {
		t.Fatal("a 2-byte subject was accepted")
	}
}

func TestCIStatementDigestGolden(t *testing.T) {
	payload, err := ci.CanonicalPayloadV1(fixtureStatement())
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	digest := ci.Digest(payload)
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+64 {
		t.Fatalf("digest shape = %q", digest)
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	if err != nil || len(raw) != 32 {
		t.Fatalf("digest is not 32 raw bytes: %v", err)
	}
}
