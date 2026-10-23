package webhook

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

var pinnedEventTypes = []string{
	"ref.updated",
	"change.opened",
	"change.updated",
	"change.landed",
	"change.abandoned",
	"review.submitted",
	"attestation.added",
	"task.created",
	"task.claimed",
	"task.closed",
	"ci.run.state_changed",
	"release.published",
	"mirror.sync_failed",
	"repo.created",
	"repo.deleted",
}

func TestWebhookSchemasFrozen(t *testing.T) {
	if got := FreezeEventTypes(); !reflect.DeepEqual(got, pinnedEventTypes) {
		t.Fatalf("FreezeEventTypes = %q, want pinned catalogue", got)
	}
	catalogue := Catalogue()
	if len(catalogue) != len(pinnedEventTypes) {
		t.Fatalf("Catalogue has %d types, want %d", len(catalogue), len(pinnedEventTypes))
	}
	for _, typ := range pinnedEventTypes {
		fields, ok := catalogue[typ]
		if !ok {
			t.Fatalf("Catalogue missing %q", typ)
		}
		if len(fields) == 0 {
			t.Fatalf("Catalogue[%q] is empty", typ)
		}
	}
}

func validDataFor(fields []string) map[string]any {
	data := make(map[string]any, len(fields))
	for _, field := range fields {
		data[field] = "x"
	}
	return data
}

func TestWebhookSchemasValidate(t *testing.T) {
	at := time.Date(2026, 10, 20, 8, 14, 3, 120000000, time.UTC)
	catalogue := Catalogue()
	for typ, fields := range catalogue {
		raw, err := BuildEnvelope(goldenMsgID, typ, at, validDataFor(fields))
		if err != nil {
			t.Fatalf("BuildEnvelope(%s): %v", typ, err)
		}
		if err := ValidateEnvelope(raw); err != nil {
			t.Fatalf("ValidateEnvelope(%s) valid envelope: %v", typ, err)
		}
		for _, missing := range fields {
			data := validDataFor(fields)
			delete(data, missing)
			bad, err := BuildEnvelope(goldenMsgID, typ, at, data)
			if err != nil {
				t.Fatalf("BuildEnvelope(%s): %v", typ, err)
			}
			if err := ValidateEnvelope(bad); err == nil {
				t.Fatalf("ValidateEnvelope(%s) accepted data missing %q", typ, missing)
			}
		}
	}
	raw, err := BuildEnvelope(goldenMsgID, "ref.updated", at, validDataFor(catalogue["ref.updated"]))
	if err != nil {
		t.Fatalf("BuildEnvelope: %v", err)
	}
	badID := strings.Replace(string(raw), goldenMsgID, "bad-id", 1)
	if err := ValidateEnvelope([]byte(badID)); err == nil {
		t.Fatal("ValidateEnvelope accepted a bad id prefix")
	}
	badType := strings.Replace(string(raw), "ref.updated", "repo.starred", 1)
	if err := ValidateEnvelope([]byte(badType)); err == nil {
		t.Fatal("ValidateEnvelope accepted an unknown type")
	}
	badTime := strings.Replace(string(raw), "2026-10-20T08:14:03.12Z", "yesterday", 1)
	if err := ValidateEnvelope([]byte(badTime)); err == nil {
		t.Fatal("ValidateEnvelope accepted a bad timestamp")
	}
	for _, bad := range []string{`{"id":"` + goldenMsgID + `","type":"ref.updated","timestamp":"2026-10-20T08:14:03Z","data":null}`, `not json`, string(raw) + "x", `{"id":"` + goldenMsgID + `","type":"ref.updated","timestamp":"2026-10-20T08:14:03Z","data":[]}`} {
		if err := ValidateEnvelope([]byte(bad)); err == nil {
			t.Fatalf("ValidateEnvelope accepted %q", bad)
		}
	}
}

func TestWebhookBuildEnvelopeCanonical(t *testing.T) {
	at := time.Date(2026, 10, 20, 8, 14, 3, 0, time.UTC)
	raw, err := BuildEnvelope(goldenMsgID, "task.created", at, map[string]any{"title": "<b>hi</b>", "state": "open", "task_id": "1", "task_number": "2", "assignee": "a", "created_by": "c", "task_url": "u"})
	if err != nil {
		t.Fatalf("BuildEnvelope: %v", err)
	}
	if strings.HasSuffix(string(raw), "\n") {
		t.Fatal("BuildEnvelope emitted a trailing newline")
	}
	if !strings.Contains(string(raw), "<b>hi</b>") {
		t.Fatalf("BuildEnvelope escaped HTML: %s", raw)
	}
	if err := ValidateEnvelope(raw); err != nil {
		t.Fatalf("ValidateEnvelope(built) = %v", err)
	}
}
