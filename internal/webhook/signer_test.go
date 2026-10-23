package webhook

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func encodeB64(t *testing.T, raw []byte) string {
	t.Helper()
	return base64.StdEncoding.EncodeToString(raw)
}

const (
	goldenSecret = "0123456789abcdef0123456789abcdef"
	goldenMsgID  = "msg_01J9Z8X7Q6W5E4R3T2Y179A68G"
	goldenTS     = 1729450000
	goldenSig    = "v1,HyfYhy32/KGV8ceffl1OqWkVETREouCzU0kMNGkZ6Qk="
)

var goldenBody = []byte(`{"id":"msg_01J9Z8X7Q6W5E4R3T2Y179A68G","type":"ref.updated","timestamp":"2026-10-20T08:14:03.12Z","data":{}}`)

func TestWebhookSignatureStandardRecipe(t *testing.T) {
	secret := []byte(goldenSecret)
	if got := Sign(secret, goldenMsgID, goldenTS, goldenBody); got != goldenSig {
		t.Fatalf("Sign = %q, want %q", got, goldenSig)
	}
	now := time.Unix(goldenTS, 0)
	if !Verify([][]byte{secret}, goldenMsgID, goldenTS, goldenBody, goldenSig, now) {
		t.Fatal("Verify rejected the golden vector")
	}
	if Verify([][]byte{secret}, goldenMsgID, goldenTS, []byte(`{"tampered":true}`), goldenSig, now) {
		t.Fatal("Verify accepted a tampered body")
	}
	if Verify([][]byte{[]byte("0123456789abcdef0123456789abcde0")}, goldenMsgID, goldenTS, goldenBody, goldenSig, now) {
		t.Fatal("Verify accepted a wrong secret")
	}
	if Verify([][]byte{secret}, goldenMsgID, goldenTS, goldenBody, "", now) {
		t.Fatal("Verify accepted an empty header")
	}
	if Verify([][]byte{secret}, goldenMsgID, goldenTS, goldenBody, "v0,gnf+MaFDIZZ2eoiMaJ5ulcMJsoGg6LSA+zfTwyq0Osg=", now) {
		t.Fatal("Verify accepted an unknown scheme")
	}
	rotated := [][]byte{[]byte("wrong-secret-00000000000000000000"), secret}
	if !Verify(rotated, goldenMsgID, goldenTS, goldenBody, goldenSig, now) {
		t.Fatal("Verify failed rotation to the second secret")
	}
	for _, skew := range []int64{-300, -299, 299, 300} {
		if !Verify([][]byte{secret}, goldenMsgID, goldenTS, goldenBody, goldenSig, now.Add(time.Duration(skew)*time.Second)) {
			t.Fatalf("Verify rejected skew %+ds", skew)
		}
	}
	for _, skew := range []int64{-301, 301} {
		if Verify([][]byte{secret}, goldenMsgID, goldenTS, goldenBody, goldenSig, now.Add(time.Duration(skew)*time.Second)) {
			t.Fatalf("Verify accepted skew %+ds", skew)
		}
	}
	empty := Sign(secret, goldenMsgID, goldenTS, nil)
	if !Verify([][]byte{secret}, goldenMsgID, goldenTS, nil, empty, now) {
		t.Fatal("Verify rejected an empty-body signature")
	}
}

func TestWebhookSignatureRotationHeaders(t *testing.T) {
	second := []byte("fedcba9876543210fedcba9876543210")
	secret := []byte(goldenSecret)
	now := time.Unix(goldenTS, 0)
	if got := SignHeader(secret, nil, goldenMsgID, goldenTS, goldenBody); got != goldenSig {
		t.Fatalf("single SignHeader = %q, want %q", got, goldenSig)
	}
	if got := SignHeader(secret, []byte{}, goldenMsgID, goldenTS, goldenBody); got != goldenSig {
		t.Fatalf("empty-prev SignHeader = %q, want %q", got, goldenSig)
	}
	dual := SignHeader(secret, second, goldenMsgID, goldenTS, goldenBody)
	parts := strings.Split(dual, " ")
	if len(parts) != 2 {
		t.Fatalf("dual SignHeader has %d entries, want 2", len(parts))
	}
	if !Verify([][]byte{secret}, goldenMsgID, goldenTS, goldenBody, dual, now) {
		t.Fatal("dual header rejected under current secret")
	}
	if !Verify([][]byte{second}, goldenMsgID, goldenTS, goldenBody, dual, now) {
		t.Fatal("dual header rejected under previous secret")
	}
	withNoise := "  v0,bogus " + dual + " v1,!!!  "
	if !Verify([][]byte{secret}, goldenMsgID, goldenTS, goldenBody, withNoise, now) {
		t.Fatal("valid entry among garbage rejected")
	}
	for _, bad := range []string{"", "v0,bogus v1,!!!", "v1,"} {
		if Verify([][]byte{secret}, goldenMsgID, goldenTS, goldenBody, bad, now) {
			t.Fatalf("header %q accepted", bad)
		}
	}
}

func TestWebhookSecretGenerate(t *testing.T) {
	display, raw, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	if !strings.HasPrefix(display, "whsec_") {
		t.Fatalf("display %q lacks prefix", display)
	}
	if len(raw) != SecretRawLen {
		t.Fatalf("raw length = %d, want %d", len(raw), SecretRawLen)
	}
	if !IsValidSecretDisplay(display) {
		t.Fatalf("IsValidSecretDisplay rejected %q", display)
	}
	display2, raw2, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	if display == display2 || string(raw) == string(raw2) {
		t.Fatal("GenerateSecret repeated output")
	}
	for _, bad := range []string{"", "whsec_", "whsec_!!!", "not-a-secret", strings.TrimPrefix(display, "whsec_")} {
		if IsValidSecretDisplay(bad) {
			t.Fatalf("IsValidSecretDisplay accepted %q", bad)
		}
	}
}

func TestWebhookSecretSealRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	raw := []byte("endpoint-secret-material-32bytes!!")
	aad := "webhook-endpoint-secret:42"
	sealed, err := SealSecret(key, raw, aad)
	if err != nil {
		t.Fatalf("SealSecret: %v", err)
	}
	opened, err := OpenSecret(string(key), encodeB64(t, sealed), aad)
	if err != nil {
		t.Fatalf("OpenSecret: %v", err)
	}
	if string(opened) != string(raw) {
		t.Fatal("round trip mismatch")
	}
	if _, err := OpenSecret(string([]byte("fedcba9876543210fedcba9876543210")), encodeB64(t, sealed), aad); err == nil {
		t.Fatal("OpenSecret accepted a wrong key")
	}
	if _, err := OpenSecret(string(key), encodeB64(t, sealed), "webhook-endpoint-secret:43"); err == nil {
		t.Fatal("OpenSecret accepted a wrong aad")
	}
	if _, err := OpenSecret(string(key), "!!!not-base64!!!", aad); err == nil {
		t.Fatal("OpenSecret accepted garbage")
	}
	if _, err := SealSecret([]byte("short"), raw, aad); err == nil {
		t.Fatal("SealSecret accepted a short key")
	}
}
