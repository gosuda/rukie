package wire

import (
	"testing"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"
)

func TestWindowCreditReadsDaemonBytesField(t *testing.T) {
	var grant WindowCredit
	if err := jsonv2.Unmarshal([]byte(`{"direction":"stdin","bytes":32768}`), &grant, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode grant: %v", err)
	}
	if grant.Credit() != 32768 {
		t.Fatalf("credit = %d, want 32768", grant.Credit())
	}
	raw, err := jsonv2.Marshal(WindowCredit{Direction: "stdout", CreditBytes: 4096})
	if err != nil {
		t.Fatalf("encode grant: %v", err)
	}
	var back WindowCredit
	if err := jsonv2.Unmarshal(raw, &back, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode grant: %v", err)
	}
	if back.Credit() != 4096 {
		t.Fatalf("round trip credit = %d, want 4096", back.Credit())
	}
}
