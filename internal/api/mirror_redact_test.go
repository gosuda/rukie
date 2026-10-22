package api

import (
	"reflect"
	"strings"
	"testing"
)

func mirrorJSONTags(t reflect.Type, out map[string]bool) {
	for i := range t.NumField() {
		field := t.Field(i)
		tag, ok := field.Tag.Lookup("json")
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		out[name] = true
		if field.Type.Kind() == reflect.Struct {
			mirrorJSONTags(field.Type, out)
		}
		if field.Type.Kind() == reflect.Pointer && field.Type.Elem().Kind() == reflect.Struct {
			mirrorJSONTags(field.Type.Elem(), out)
		}
	}
}

func TestMirrorJSONRedactsSecrets(t *testing.T) {
	const canary = "CANARY-9f3a-mirror-token-secret"
	for _, typ := range []reflect.Type{
		reflect.TypeFor[MirrorJSON](),
		reflect.TypeFor[RefStateJSON](),
		reflect.TypeFor[RunJSON](),
	} {
		names := map[string]bool{}
		mirrorJSONTags(typ, names)
		for _, banned := range []string{"token", "ciphertext", "nonce", "secret"} {
			if names[banned] {
				t.Fatalf("%s serializes banned field %q", typ.Name(), banned)
			}
		}
	}
	full := MirrorJSON{
		ID: "01MIRROR", Repo: 7, Name: "edge", TargetURL: "https://mirror.example.test/edge.git",
		HostKind: "github", Username: "ada", Mode: "exact", RefSelection: "all",
		Globs: []string{"refs/heads/*"},
		Flags: MirrorFlagsJSON{Enabled: true},
		State: "ok", ConsecutiveFailures: 1,
		LastError:     MirrorErrorJSON{Class: "network", Message: "timeout", HTTPStatus: 502},
		LastSuccessAt: 3, LastAttemptAt: 4, NextRunAt: 5,
		Credential: &MirrorCredentialJSON{
			Last4: "cret", KeyVersion: 2, CreatedAt: 6, RotatedAt: 7, LastUsedAt: 8,
		},
	}
	raw, merr := marshalJSON(full)
	if merr != nil {
		t.Fatalf("marshal mirror: %v", merr)
	}
	if strings.Contains(string(raw), canary) {
		t.Fatalf("MirrorJSON carries the canary token")
	}
	run := RunJSON{
		ID: "01RUN", MirrorID: 9, Kind: "sync", State: "succeeded", Trigger: "manual",
		AttemptNo: 1, PlanDigest: "abc",
		Outcome:   RunOutcomeJSON{Class: "ok", Summary: "done", Counts: "{}"},
		StartedAt: 1, FinishedAt: 2,
	}
	raw, merr = marshalJSON(run)
	if merr != nil {
		t.Fatalf("marshal run: %v", merr)
	}
	if strings.Contains(string(raw), canary) {
		t.Fatalf("RunJSON carries the canary token")
	}
}
