package mirror

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestSealOpen(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	token := []byte("CANARY-secret-credential")
	sealed, err := Seal(key, token, "context", 9, Last4(string(token)))
	if err != nil {
		t.Fatal(err)
	}
	if sealed.KeyVersion != 9 {
		t.Fatalf("version=%d", sealed.KeyVersion)
	}
	got, err := Open(key, sealed, "context")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, token) {
		t.Fatal("round trip mismatch")
	}
	Zero(got)
	if !bytes.Equal(got, make([]byte, len(got))) {
		t.Fatal("Zero did not erase bytes")
	}
	if _, err = Open(key, sealed, "other"); err == nil {
		t.Fatal("AAD mismatch accepted")
	}
	if _, err = Open(key, sealed, " "); err == nil {
		t.Fatal("empty/different AAD accepted")
	}
}
func TestSealRejectsInvalidInputs(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	s, err := Seal(key, []byte("CANARY-a"), "", 1, "a")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		edit func(*SealedCredential)
		key  []byte
	}{{"wrong key", nil, bytes.Repeat([]byte{2}, 32)}, {"empty key", nil, nil}, {"short key", nil, []byte{1}}, {"ciphertext", func(s *SealedCredential) { s.Ciphertext[0] ^= 1 }, key}, {"nonce", func(s *SealedCredential) { s.Nonce[0] ^= 1 }, key}, {"nonce length", func(s *SealedCredential) { s.Nonce = nil }, key}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			copyS := s
			if tc.edit != nil {
				copyS.Nonce = bytes.Clone(s.Nonce)
				copyS.Ciphertext = bytes.Clone(s.Ciphertext)
				tc.edit(&copyS)
			}
			if _, err := Open(tc.key, copyS, ""); err == nil {
				t.Fatal("Open accepted invalid input")
			}
		})
	}
	if _, err := Seal(nil, []byte("CANARY-a"), "", 1, ""); err == nil {
		t.Fatal("Seal accepted empty key")
	}
	if _, err := Seal([]byte{1}, nil, "", 1, ""); err == nil {
		t.Fatal("Seal accepted short key")
	}
}
func TestSealedCredentialJSONRedactsCanary(t *testing.T) {
	s, err := Seal(bytes.Repeat([]byte{3}, 32), []byte("CANARY-private-material"), "", 1, "ial")
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("CANARY-private-material")) || bytes.Contains(b, []byte("token")) {
		t.Fatalf("credential leaked: %s", b)
	}
}
func TestLast4(t *testing.T) {
	for in, want := range map[string]string{"": "", "abc": "abc", "abcdef": "cdef", "a界bcdef": "cdef"} {
		if got := Last4(in); got != want {
			t.Errorf("Last4(%q)=%q want %q", in, got, want)
		}
	}
}
