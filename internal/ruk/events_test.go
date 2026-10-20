package ruk

import (
	"strings"
	"testing"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"
)

func TestEventsTailLimitBoundaries(t *testing.T) {
	for _, limit := range []int{-1, 0} {
		args := EventsTailArgs{Limit: limit, WaitMs: 1000}
		if err := args.Validate(); err == nil {
			t.Fatalf("limit %d validated", limit)
		}
	}
	for _, limit := range []int{1, 999, 1000} {
		args := EventsTailArgs{Limit: limit, WaitMs: 1000}
		if err := args.Validate(); err != nil {
			t.Fatalf("limit %d rejected: %v", limit, err)
		}
	}
	args := EventsTailArgs{Limit: 1001, WaitMs: 1000}
	if err := args.Validate(); err == nil {
		t.Fatal("limit 1001 validated")
	}
}

func TestEventsTailWaitBoundaries(t *testing.T) {
	for _, wait := range []uint64{0, 1, 29999, 30000} {
		args := EventsTailArgs{Limit: 10, WaitMs: wait}
		if err := args.Validate(); err != nil {
			t.Fatalf("wait_ms %d rejected: %v", wait, err)
		}
	}
	args := EventsTailArgs{Limit: 10, WaitMs: 30001}
	if err := args.Validate(); err == nil {
		t.Fatal("wait_ms 30001 validated")
	}
}

func TestRefsListLimitBoundaries(t *testing.T) {
	args := RefsListArgs{Limit: -1}
	if err := args.Validate(); err == nil {
		t.Fatal("limit -1 validated")
	}
	for _, limit := range []int{0, 1, 1000} {
		args := RefsListArgs{Limit: limit}
		if err := args.Validate(); err != nil {
			t.Fatalf("limit %d rejected: %v", limit, err)
		}
	}
	over := RefsListArgs{Limit: 1001}
	if err := over.Validate(); err == nil {
		t.Fatal("limit 1001 validated")
	}
}

func TestEventsTailMarshalShape(t *testing.T) {
	args := EventsTailArgs{After: new(uint64(7)), Limit: 50, WaitMs: 500, Prefix: "refs/heads/"}
	raw, err := jsonv2.Marshal(args.marshal(), jsonv1.DefaultOptionsV1())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]jsonv1.RawMessage
	if err = jsonv2.Unmarshal(raw, &got, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"after", "limit", "wait_ms", "prefix"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("args miss %q: %s", key, string(raw))
		}
	}
	if len(got) != 4 {
		t.Fatalf("args carry %d keys: %s", len(got), string(raw))
	}
	bare := EventsTailArgs{Limit: 1, WaitMs: 0}
	raw, err = jsonv2.Marshal(bare.marshal(), jsonv1.DefaultOptionsV1())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "prefix") || strings.Contains(string(raw), "after") {
		t.Fatalf("absent after and prefix must be omitted: %s", string(raw))
	}
}

func TestRefsListMarshalShape(t *testing.T) {
	after := "refs/tags/v1"
	args := RefsListArgs{Prefix: "refs/tags/", After: &after, Limit: 25}
	raw, err := jsonv2.Marshal(args.marshal(), jsonv1.DefaultOptionsV1())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]jsonv1.RawMessage
	if err = jsonv2.Unmarshal(raw, &got, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"prefix", "after", "limit"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("args miss %q: %s", key, string(raw))
		}
	}
	empty := RefsListArgs{}
	raw, err = jsonv2.Marshal(empty.marshal(), jsonv1.DefaultOptionsV1())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != "{}" {
		t.Fatalf("empty args encode as %s, want {}", string(raw))
	}
}

func TestRefEventWireNames(t *testing.T) {
	operation := "receive-pack"
	request := "push-01"
	session := "s1"
	event := RefEvent{
		At: "2026-10-20T00:00:00Z", Name: "refs/heads/main",
		Old: RefTarget{}, New: DirectTarget("abc"),
		Operation: new(operation), RequestID: new(request), Session: new(session),
		Source: "receive_pack", Seq: 9,
	}
	raw, err := jsonv2.Marshal(event, jsonv1.DefaultOptionsV1())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]jsonv1.RawMessage
	if err = jsonv2.Unmarshal(raw, &got, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []string{"seq", "name", "old", "new", "at", "source", "operation", "request_id", "session"}
	if len(got) != len(want) {
		t.Fatalf("event carries %d keys, want %d: %s", len(got), len(want), string(raw))
	}
	for _, key := range want {
		if _, ok := got[key]; !ok {
			t.Fatalf("event misses %q: %s", key, string(raw))
		}
	}
	var fresh map[string]jsonv1.RawMessage
	if err = jsonv2.Unmarshal(got["new"], &fresh, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("new target: %v", err)
	}
	if _, ok := fresh["direct"]; !ok {
		t.Fatalf("new target misses direct: %s", string(got["new"]))
	}
	var back RefEvent
	if err = jsonv2.Unmarshal(raw, &back, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if back.Seq != event.Seq || back.Name != event.Name || back.New.Hex() != "abc" || back.Source != event.Source {
		t.Fatalf("round trip = %+v, want %+v", back, event)
	}
	if !back.Old.Null() {
		t.Fatalf("null old decoded as %+v", back.Old)
	}
}

func TestEventsTailReplyDecodesStatus(t *testing.T) {
	raw := []byte(`{"status": "expired", "head": 12, "oldest": 9, "events": [], "next_after": 12}`)
	var reply EventsTailReply
	if err := jsonv2.Unmarshal(raw, &reply, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reply.Expired() {
		t.Fatal("status expired decoded as live")
	}
	if reply.Head != 12 || reply.Oldest != 9 || reply.NextAfter != 12 {
		t.Fatalf("reply = %+v", reply)
	}
	raw = []byte(`{"status": "ok", "head": 12, "oldest": 9, "events": [{"seq": 12, "name": "refs/heads/main", "old": {"direct": "a"}, "new": {"direct": "b"}, "at": "t", "source": "receive_pack", "operation": null, "request_id": null, "session": null}], "next_after": 12}`)
	if err := jsonv2.Unmarshal(raw, &reply, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if reply.Expired() {
		t.Fatal("status ok decoded as expired")
	}
	if len(reply.Events) != 1 || reply.Events[0].Old.Hex() != "a" || reply.Events[0].New.Hex() != "b" {
		t.Fatalf("events = %+v", reply.Events)
	}
}

func TestRefsListReplyDecodesItems(t *testing.T) {
	raw := []byte(`{"as_of": 12, "refs": {"items": [{"name": "refs/heads/main", "target": {"direct": "abc"}}], "next_after": null}}`)
	var reply RefsListReply
	if err := jsonv2.Unmarshal(raw, &reply, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if reply.AsOf != 12 {
		t.Fatalf("as_of = %d, want 12", reply.AsOf)
	}
	if len(reply.Refs.Items) != 1 || reply.Refs.Items[0].Target.Hex() != "abc" {
		t.Fatalf("refs = %+v", reply.Refs)
	}
	if reply.Refs.NextAfter != nil {
		t.Fatalf("next_after = %+v, want null", reply.Refs.NextAfter)
	}
}

func TestTailMethodNames(t *testing.T) {
	if string(MethodEventsTail) != "events.tail" {
		t.Fatalf("tail method = %q", string(MethodEventsTail))
	}
	if string(MethodRefsList) != "refs.list" {
		t.Fatalf("refs method = %q", string(MethodRefsList))
	}
}
