package ruk

import (
	"os"
	"reflect"
	"strings"
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
	if doc.SchemaVersion.Major != 1 || doc.SchemaVersion.Minor != 3 {
		t.Fatalf("schema version = %d.%d, want 1.3", doc.SchemaVersion.Major, doc.SchemaVersion.Minor)
	}
	if len(doc.Methods) != 61 {
		t.Fatalf("methods = %d, want 61", len(doc.Methods))
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

func TestModelCoversFeedShapes(t *testing.T) {
	defs, _, _ := loadSnapshot(t)
	checkFeedDef(t, defs, "EventsTailReply", EventsTailReply{})
	checkFeedDef(t, defs, "TailedEvent", RefEvent{})
	checkFeedDef(t, defs, "RefsListReply", RefsListReply{})
	checkFeedDef(t, defs, "KeysetPage2", RefPage{})
	checkFeedDef(t, defs, "RefEntry", RefEntry{})
}

func TestModelFeedKindsMatch(t *testing.T) {
	defs, _, _ := loadSnapshot(t)
	checkFeedKind(t, defs, defs["EventsTailReply"], reflect.TypeOf(EventsTailReply{}), "EventsTailReply", map[string]bool{})
	checkFeedKind(t, defs, defs["TailedEvent"], reflect.TypeOf(RefEvent{}), "TailedEvent", map[string]bool{})
	checkFeedKind(t, defs, defs["RefsListReply"], reflect.TypeOf(RefsListReply{}), "RefsListReply", map[string]bool{})
}

func checkFeedKind(t *testing.T, defs map[string]jsonv1.RawMessage, raw jsonv1.RawMessage, typ reflect.Type, path string, seen map[string]bool) {
	t.Helper()
	if typ == reflect.TypeOf(jsonv1.RawMessage{}) {
		return
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() == reflect.Slice && typ.Elem().Kind() == reflect.Uint8 {
		return
	}
	schema := resolveFeedSchema(t, defs, raw, path, seen)
	if schema == nil {
		return
	}
	if typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
		items, ok := schema["items"]
		if !ok {
			t.Fatalf("%s: array without items schema", path)
		}
		var itemRaw jsonv1.RawMessage
		if err := jsonv2.Unmarshal(items, &itemRaw, jsonv1.DefaultOptionsV1()); err != nil {
			t.Fatalf("%s: items: %v", path, err)
		}
		checkFeedKind(t, defs, itemRaw, typ.Elem(), path+"[]", seen)
		return
	}
	if typ.Kind() != reflect.Struct {
		want := feedKindsOf(t, defs, schema, path, seen)
		got := feedGoKinds(typ)
		for kind := range got {
			if !want[kind] {
				t.Fatalf("%s: Go kind %s not in model kinds %v", path, kind, feedKindList(want))
			}
		}
		return
	}
	props := feedObjectProperties(t, defs, schema, path, seen)
	for i := range typ.NumField() {
		tag := typ.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			t.Fatalf("%s: field %s lacks a json name", path, typ.Field(i).Name)
		}
		prop, ok := props[name]
		if !ok {
			t.Fatalf("%s carries unknown field %s", path, name)
		}
		checkFeedKind(t, defs, prop, typ.Field(i).Type, path+"."+name, seen)
	}
}

func feedObjectProperties(t *testing.T, defs map[string]jsonv1.RawMessage, schema map[string]jsonv1.RawMessage, path string, seen map[string]bool) map[string]jsonv1.RawMessage {
	t.Helper()
	if props, ok := schema["properties"]; ok {
		var out map[string]jsonv1.RawMessage
		raw, err := jsonv2.Marshal(props, jsonv1.DefaultOptionsV1())
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if err = jsonv2.Unmarshal(raw, &out, jsonv1.DefaultOptionsV1()); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		return out
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		branches, ok := schema[key]
		if !ok {
			continue
		}
		var list []jsonv1.RawMessage
		if err := jsonv2.Unmarshal(branches, &list, jsonv1.DefaultOptionsV1()); err != nil {
			t.Fatalf("%s: %s: %v", path, key, err)
		}
		merged := map[string]jsonv1.RawMessage{}
		for _, branch := range list {
			resolved := resolveFeedSchema(t, defs, branch, path, seen)
			if kind, ok := resolved["type"]; ok && !feedTypeIsObject(t, kind, path) {
				continue
			}
			for name, prop := range feedObjectProperties(t, defs, resolved, path, seen) {
				if _, dup := merged[name]; !dup {
					merged[name] = prop
				}
			}
		}
		if len(merged) == 0 {
			t.Fatalf("%s: no object branch in %s", path, key)
		}
		return merged
	}
	if _, ok := schema["$ref"]; ok {
		resolved := resolveFeedSchema(t, defs, mustMarshalRaw(t, schema, path), path, seen)
		return feedObjectProperties(t, defs, resolved, path, seen)
	}
	t.Fatalf("%s: schema has no object shape", path)
	return nil
}

func feedTypeIsObject(t *testing.T, kind jsonv1.RawMessage, path string) bool {
	t.Helper()
	var name string
	if err := jsonv2.Unmarshal(kind, &name, jsonv1.DefaultOptionsV1()); err == nil {
		return name == "object"
	}
	var names []string
	if err := jsonv2.Unmarshal(kind, &names, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("%s: type: %v", path, err)
	}
	for _, entry := range names {
		if entry == "object" {
			return true
		}
	}
	return false
}

func resolveFeedSchema(t *testing.T, defs map[string]jsonv1.RawMessage, raw jsonv1.RawMessage, path string, seen map[string]bool) map[string]jsonv1.RawMessage {
	t.Helper()
	var schema map[string]jsonv1.RawMessage
	if err := jsonv2.Unmarshal(raw, &schema, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	refRaw, ok := schema["$ref"]
	if !ok {
		return schema
	}
	var ref string
	if err := jsonv2.Unmarshal(refRaw, &ref, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("%s: ref: %v", path, err)
	}
	name, found := strings.CutPrefix(ref, "#/$defs/")
	if !found {
		t.Fatalf("%s: non-local ref %s", path, ref)
	}
	if seen[name] {
		t.Fatalf("%s: schema cycle at %s", path, name)
	}
	seen[name] = true
	defer delete(seen, name)
	target, ok := defs[name]
	if !ok {
		t.Fatalf("%s: missing $defs entry %s", path, name)
	}
	return resolveFeedSchema(t, defs, target, path, seen)
}

func feedKindsOf(t *testing.T, defs map[string]jsonv1.RawMessage, schema map[string]jsonv1.RawMessage, path string, seen map[string]bool) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	if branch, ok := schema["type"]; ok {
		var name string
		if err := jsonv2.Unmarshal(branch, &name, jsonv1.DefaultOptionsV1()); err == nil {
			out[name] = true
			return out
		}
		var names []string
		if err := jsonv2.Unmarshal(branch, &names, jsonv1.DefaultOptionsV1()); err != nil {
			t.Fatalf("%s: type: %v", path, err)
		}
		for _, entry := range names {
			out[entry] = true
		}
		return out
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		branches, ok := schema[key]
		if !ok {
			continue
		}
		var list []jsonv1.RawMessage
		if err := jsonv2.Unmarshal(branches, &list, jsonv1.DefaultOptionsV1()); err != nil {
			t.Fatalf("%s: %s: %v", path, key, err)
		}
		for _, branch := range list {
			resolved := resolveFeedSchema(t, defs, branch, path, seen)
			for kind := range feedKindsOf(t, defs, resolved, path, seen) {
				out[kind] = true
			}
		}
		return out
	}
	if _, ok := schema["$ref"]; ok {
		resolved := resolveFeedSchema(t, defs, mustMarshalRaw(t, schema, path), path, seen)
		return feedKindsOf(t, defs, resolved, path, seen)
	}
	if _, ok := schema["properties"]; ok {
		out["object"] = true
		return out
	}
	t.Fatalf("%s: schema has no kind", path)
	return out
}

func mustMarshalRaw(t *testing.T, schema map[string]jsonv1.RawMessage, path string) jsonv1.RawMessage {
	t.Helper()
	raw, err := jsonv2.Marshal(schema, jsonv1.DefaultOptionsV1())
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return raw
}

var feedKindByReflect = map[reflect.Kind]string{
	reflect.Bool:    "boolean",
	reflect.String:  "string",
	reflect.Int:     "integer",
	reflect.Int8:    "integer",
	reflect.Int16:   "integer",
	reflect.Int32:   "integer",
	reflect.Int64:   "integer",
	reflect.Uint:    "integer",
	reflect.Uint8:   "integer",
	reflect.Uint16:  "integer",
	reflect.Uint32:  "integer",
	reflect.Uint64:  "integer",
	reflect.Float32: "number",
	reflect.Float64: "number",
	reflect.Slice:   "array",
	reflect.Array:   "array",
	reflect.Struct:  "object",
	reflect.Map:     "object",
}

func feedGoKinds(typ reflect.Type) map[string]bool {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	out := map[string]bool{}
	if name, ok := feedKindByReflect[typ.Kind()]; ok {
		out[name] = true
	}
	return out
}

func feedKindList(kinds map[string]bool) []string {
	out := make([]string, 0, len(kinds))
	for kind := range kinds {
		out = append(out, kind)
	}
	return out
}

func checkFeedDef(t *testing.T, defs map[string]jsonv1.RawMessage, defName string, value any) {
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
	got := feedFieldNames(t, value)
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

func feedFieldNames(t *testing.T, value any) map[string]bool {
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
