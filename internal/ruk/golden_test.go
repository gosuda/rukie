package ruk

import (
	"os"
	"testing"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/ruk/internal/rukgen"
	"github.com/gosuda/rukie/internal/ruk/wire"
)

func loadSnapshot(t *testing.T) (map[string]jsonv1.RawMessage, []wire.Method, []wire.Code) {
	t.Helper()
	raw, err := os.ReadFile("testdata/ruk-model.json")
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var doc struct {
		Defs    map[string]jsonv1.RawMessage `json:"$defs"`
		Methods []struct {
			Kind   string `json:"kind"`
			Method string `json:"method"`
		} `json:"methods"`
		ErrorCodes []struct {
			Code       string `json:"code"`
			ExitStatus int    `json:"exit_status"`
		} `json:"error_codes"`
		SchemaVersion struct {
			Major uint32 `json:"major"`
			Minor uint32 `json:"minor"`
		} `json:"schema_version"`
	}
	if err := jsonv2.Unmarshal(raw, &doc, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if doc.SchemaVersion.Major != 1 || doc.SchemaVersion.Minor != 1 {
		t.Fatalf("schema version = %d.%d, want 1.1", doc.SchemaVersion.Major, doc.SchemaVersion.Minor)
	}
	if len(doc.Methods) != 47 {
		t.Fatalf("methods = %d, want 47", len(doc.Methods))
	}
	if len(doc.ErrorCodes) != 30 {
		t.Fatalf("error codes = %d, want 30", len(doc.ErrorCodes))
	}
	methods := wire.AllMethods()
	if len(methods) != len(doc.Methods) {
		t.Fatalf("generated methods = %d, snapshot has %d", len(methods), len(doc.Methods))
	}
	for i, entry := range doc.Methods {
		if string(methods[i]) != entry.Method {
			t.Fatalf("method %d = %s, snapshot has %s", i, string(methods[i]), entry.Method)
		}
		if methods[i].Kind().String() != entry.Kind {
			t.Fatalf("method %s kind = %s, snapshot has %s", entry.Method, methods[i].Kind(), entry.Kind)
		}
	}
	codes := wire.AllCodes()
	if len(codes) != len(doc.ErrorCodes) {
		t.Fatalf("generated codes = %d, snapshot has %d", len(codes), len(doc.ErrorCodes))
	}
	for i, entry := range doc.ErrorCodes {
		if string(codes[i]) != entry.Code {
			t.Fatalf("code %d = %s, snapshot has %s", i, string(codes[i]), entry.Code)
		}
		if codes[i].ExitStatus() != entry.ExitStatus {
			t.Fatalf("code %s exit = %d, snapshot has %d", entry.Code, codes[i].ExitStatus(), entry.ExitStatus)
		}
	}
	return doc.Defs, methods, codes
}

func TestModelGolden(t *testing.T) {
	loadSnapshot(t)
}

func TestRukgenDrift(t *testing.T) {
	raw, err := os.ReadFile("testdata/ruk-model.json")
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	freshWire, freshSentinels, err := rukgen.Generate(raw)
	if err != nil {
		t.Fatalf("rukgen: %v", err)
	}
	committedWire, err := os.ReadFile("wire/closed_gen.go")
	if err != nil {
		t.Fatalf("read committed wire: %v", err)
	}
	if freshWire != string(committedWire) {
		t.Fatal("drift in wire/closed_gen.go: regenerate with rukgen")
	}
	committedSentinels, err := os.ReadFile("sentinels_gen.go")
	if err != nil {
		t.Fatalf("read committed sentinels: %v", err)
	}
	if freshSentinels != string(committedSentinels) {
		t.Fatal("drift in sentinels_gen.go: regenerate with rukgen")
	}
}
