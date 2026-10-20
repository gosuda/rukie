package wire

import (
	"os"
	"reflect"
	"strings"
	"testing"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"
)

func modelDefs(t *testing.T) map[string]jsonv1.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("../testdata/ruk-model.json")
	if err != nil {
		t.Fatalf("read model: %v", err)
	}
	var doc struct {
		Defs map[string]jsonv1.RawMessage `json:"$defs"`
	}
	if err := jsonv2.Unmarshal(raw, &doc, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode model: %v", err)
	}
	return doc.Defs
}

func fieldNames(t *testing.T, value any) map[string]bool {
	t.Helper()
	names := make(map[string]bool)
	typ := reflect.TypeOf(value)
	for i := range typ.NumField() {
		tag := typ.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			t.Fatalf("field %s lacks a json name", typ.Field(i).Name)
		}
		names[name] = true
	}
	return names
}

func checkDef(t *testing.T, defs map[string]jsonv1.RawMessage, defName string, value any) {
	t.Helper()
	raw, ok := defs[defName]
	if !ok {
		t.Fatalf("model lacks $defs entry %s", defName)
	}
	var def struct {
		Properties map[string]jsonv1.RawMessage `json:"properties"`
		Required   []string                     `json:"required"`
	}
	if err := jsonv2.Unmarshal(raw, &def, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode %s: %v", defName, err)
	}
	got := fieldNames(t, value)
	for name := range got {
		if _, ok := def.Properties[name]; !ok {
			t.Fatalf("%s carries unknown field %s", defName, name)
		}
	}
	for _, name := range def.Required {
		if !got[name] {
			t.Fatalf("%s misses required field %s", defName, name)
		}
	}
}

func TestWireMatchesModel(t *testing.T) {
	defs := modelDefs(t)
	checkDef(t, defs, "SchemaArgs", SchemaArgs{})
	checkDef(t, defs, "SessionOpenArgs", SessionOpenArgs{})
	checkDef(t, defs, "SessionResumeArgs", SessionResumeArgs{})
	checkDef(t, defs, "SessionResumeReply", SessionResumeReply{})
	checkDef(t, defs, "Capabilities", Capabilities{})
	checkDef(t, defs, "IdentityClaim", IdentityClaim{})
	checkDef(t, defs, "ErrorReply", ErrorReply{})
	got := fieldNames(t, SessionOpened{})
	for _, name := range []string{"session", "reconnect_secret", "principal"} {
		if !got[name] {
			t.Fatalf("SessionOpened misses daemon field %s", name)
		}
	}
	if len(got) != 3 {
		t.Fatalf("SessionOpened carries %d fields, daemon sends 3", len(got))
	}
}
