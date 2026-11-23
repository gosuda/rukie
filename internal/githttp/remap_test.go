package githttp

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/review"
)

func diffPage(t *testing.T, raw string) diffReplyJSON {
	t.Helper()
	var page diffReplyJSON
	if err := jsonv2.Unmarshal([]byte(raw), &page, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestCollectChangeSpansConsumesEveryPage(t *testing.T) {
	first := diffPage(t, `{"files":{"items":[{"path":"changed.txt","body":{"unified":{"patch":{"inline":{"data":"@@ -1 +1,2 @@\n+new\n old\n"}}}}}],"next":{"position":"next"}}}`)
	last := diffPage(t, `{"files":{"items":[{"path":"binary.dat","body":"binary"}],"next":null}}`)
	pages := 0
	spans, err := collectChangeSpans(t.Context(), func(_ context.Context, cursor jsonv1.RawMessage) (diffReplyJSON, error) {
		pages++
		switch pages {
		case 1:
			if len(cursor) != 0 {
				return diffReplyJSON{}, fmt.Errorf("initial cursor = %s", cursor)
			}
			return first, nil
		case 2:
			if string(cursor) != `{"position":"next"}` {
				return diffReplyJSON{}, fmt.Errorf("continuation = %s", cursor)
			}
			return last, nil
		default:
			return diffReplyJSON{}, fmt.Errorf("unexpected page %d", pages)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if pages != 2 {
		t.Fatalf("pages = %d, want 2", pages)
	}
	if line, ok := review.RemapLine(1, spans["changed.txt"]); !ok || line != 2 {
		t.Fatalf("changed line = (%d, %v), want (2, true)", line, ok)
	}
	if binary, present := spans["binary.dat"]; !present || len(binary) != 0 {
		t.Fatalf("binary change lost its explicit unmatched marker: %v", spans)
	}
	if _, changed := spans["stable.txt"]; changed {
		t.Fatal("unchanged path appeared in changed paths")
	}
}

func TestCollectChangeSpansDiscardsIncompleteDiff(t *testing.T) {
	first := diffPage(t, `{"files":{"items":[{"path":"binary.dat","body":"binary"}],"next":{"position":"next"}}}`)
	unavailable := errors.New("next page unavailable")
	pages := 0
	spans, err := collectChangeSpans(t.Context(), func(context.Context, jsonv1.RawMessage) (diffReplyJSON, error) {
		pages++
		if pages == 1 {
			return first, nil
		}
		return diffReplyJSON{}, unavailable
	})
	if !errors.Is(err, unavailable) || spans != nil {
		t.Fatalf("incomplete diff = (%v, %v), want no mappings", spans, err)
	}
}

func TestCollectChangeSpansRejectsStalledCursor(t *testing.T) {
	page := diffPage(t, `{"files":{"items":[],"next":{"position":"same"}}}`)
	spans, err := collectChangeSpans(t.Context(), func(context.Context, jsonv1.RawMessage) (diffReplyJSON, error) {
		return page, nil
	})
	if err == nil || spans != nil {
		t.Fatalf("stalled diff = (%v, %v), want refusal", spans, err)
	}
}

func TestUnifiedPatchBytesDecodesDaemonHex(t *testing.T) {
	patch := "@@ -1 +1,2 @@\n+new\n old\n"
	raw := jsonv1.RawMessage(`{"unified":{"patch":{"inline":{"data":{"hex":"` + hex.EncodeToString([]byte(patch)) + `"}}}}}`)
	got, text, err := unifiedPatchBytes(raw)
	if err != nil || !text || got != patch {
		t.Fatalf("hex patch = (%q, %v, %v)", got, text, err)
	}
}

func TestUnifiedPatchBytesRejectsUnsupportedBodies(t *testing.T) {
	for _, raw := range []string{
		`{"unified":{"patch":{"streamed":{"stream":"pending","size":10}}}}`,
		`{"unified":{"patch":{"inline":{"data":{"hex":"zz"}}}}}`,
		`{"unified":{"patch":{"inline":{}}}}`,
		`{}`,
	} {
		if _, _, err := unifiedPatchBytes(jsonv1.RawMessage(raw)); err == nil {
			t.Fatalf("accepted invalid patch %s", raw)
		}
	}
}

func TestCollectChangeSpansRejectsMissingFileList(t *testing.T) {
	for _, raw := range []string{`{}`, `{"files":{}}`, `{"files":{"items":null}}`} {
		page := diffPage(t, raw)
		spans, err := collectChangeSpans(t.Context(), func(context.Context, jsonv1.RawMessage) (diffReplyJSON, error) {
			return page, nil
		})
		if err == nil || spans != nil {
			t.Fatalf("incomplete reply %s yielded (%v, %v)", raw, spans, err)
		}
	}
}
