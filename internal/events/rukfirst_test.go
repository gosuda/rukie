package events

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"encoding/json/jsontext"

	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/store"
)

type stubCall struct {
	unknown    error
	fail       error
	stored     map[string]jsontext.Value
	receipt    jsontext.Value
	executions int
	replays    int
}

func (s *stubCall) call(id string) (jsontext.Value, error) {
	if s.stored == nil {
		s.stored = make(map[string]jsontext.Value)
	}
	if receipt, ok := s.stored[id]; ok {
		return receipt, nil
	}
	s.executions++
	if s.fail != nil {
		return nil, s.fail
	}
	if s.unknown != nil {
		err := s.unknown
		s.unknown = nil
		return nil, err
	}
	s.stored[id] = s.receipt
	return s.receipt, nil
}

func (s *stubCall) replay(id string) (jsontext.Value, error) {
	s.replays++
	if s.stored == nil {
		s.stored = make(map[string]jsontext.Value)
	}
	if receipt, ok := s.stored[id]; ok {
		return receipt, nil
	}
	s.stored[id] = s.receipt
	return s.receipt, nil
}

func commitTipRow(t *testing.T, key string) func(*store.WriteTx, jsontext.Value) error {
	t.Helper()
	return func(tx *store.WriteTx, receipt jsontext.Value) error {
		return tx.PutRefTip(1, key, string(receipt), feedTime())
	}
}

func tipOf(t *testing.T, st *store.Store, repo int64, name string) (string, bool) {
	t.Helper()
	var tips map[string]string
	err := st.Read(feedCtx(t), func(tx *store.ReadTx) error {
		got, rerr := tx.RefTips(repo)
		if rerr != nil {
			return rerr
		}
		tips = got
		return nil
	})
	if err != nil {
		t.Fatalf("tips: %v", err)
	}
	target, ok := tips[name]
	return target, ok
}

func TestStableRequestIDFormat(t *testing.T) {
	id := StableRequestID("land", "ada/hello", []byte("digest-1"))
	matched, err := regexp.MatchString(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, id)
	if err != nil {
		t.Fatalf("pattern: %v", err)
	}
	if !matched {
		t.Fatalf("id %q is not a lowercase version-5 UUID", id)
	}
	if strings.HasPrefix(id, "push-") {
		t.Fatalf("id %q collides with the push prefix", id)
	}
	if again := StableRequestID("land", "ada/hello", []byte("digest-1")); again != id {
		t.Fatalf("unstable: %q vs %q", again, id)
	}
	if other := StableRequestID("land", "ada/other", []byte("digest-1")); other == id {
		t.Fatalf("repo does not scope the id: %q", id)
	}
	if other := StableRequestID("mirror", "ada/hello", []byte("digest-1")); other == id {
		t.Fatalf("operation does not scope the id: %q", id)
	}
	if other := StableRequestID("land", "ada/hello", []byte("digest-2")); other == id {
		t.Fatalf("digest does not scope the id: %q", id)
	}
}

func TestWriteAfterRukCommitsAfterCall(t *testing.T) {
	st := openFeedStore(t)
	stub := &stubCall{receipt: jsontext.Value(`"tip-b"`)}
	id := StableRequestID("op", "r", []byte("d"))
	err := WriteAfterRuk(feedCtx(t), st, id, stub.call, stub.replay, commitTipRow(t, "refs/heads/main"))
	if err != nil {
		t.Fatalf("write-after: %v", err)
	}
	if stub.executions != 1 || stub.replays != 0 {
		t.Fatalf("executions = %d, replays = %d", stub.executions, stub.replays)
	}
	target, ok := tipOf(t, st, 1, "refs/heads/main")
	if !ok || target != `"tip-b"` {
		t.Fatalf("tip = %q, %v", target, ok)
	}
}

func TestWriteAfterRukCrashReplaysReceipt(t *testing.T) {
	st := openFeedStore(t)
	stub := &stubCall{receipt: jsontext.Value(`"tip-b"`)}
	id := StableRequestID("op", "r", []byte("d"))
	crashed := errors.New("crash between ruk and sqlite")
	err := WriteAfterRuk(feedCtx(t), st, id, stub.call, stub.replay, func(_ *store.WriteTx, _ jsontext.Value) error {
		return crashed
	})
	if !errors.Is(err, crashed) {
		t.Fatalf("first err = %v, want the crash", err)
	}
	err = WriteAfterRuk(feedCtx(t), st, id, stub.call, stub.replay, commitTipRow(t, "refs/heads/main"))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if stub.executions != 1 {
		t.Fatalf("executions = %d, want 1: the retry re-ran the call", stub.executions)
	}
	target, ok := tipOf(t, st, 1, "refs/heads/main")
	if !ok || target != `"tip-b"` {
		t.Fatalf("tip = %q, %v", target, ok)
	}
}

func TestWriteAfterRukUnknownUsesReplay(t *testing.T) {
	st := openFeedStore(t)
	stub := &stubCall{receipt: jsontext.Value(`"tip-c"`), unknown: ruk.ErrOutcomeUnknown}
	seen := []string{}
	call := func(id string) (jsontext.Value, error) {
		seen = append(seen, "call:"+id)
		return stub.call(id)
	}
	replay := func(id string) (jsontext.Value, error) {
		seen = append(seen, "replay:"+id)
		return stub.replay(id)
	}
	id := StableRequestID("op", "r", []byte("d"))
	err := WriteAfterRuk(feedCtx(t), st, id, call, replay, commitTipRow(t, "refs/heads/main"))
	if err != nil {
		t.Fatalf("write-after: %v", err)
	}
	if len(seen) != 2 || seen[0] != "call:"+id || seen[1] != "replay:"+id {
		t.Fatalf("order = %v, want call then replay with the same id", seen)
	}
	if stub.executions != 1 || stub.replays != 1 {
		t.Fatalf("executions = %d, replays = %d", stub.executions, stub.replays)
	}
}

func TestWriteAfterRukUnknownWithoutReplayCommitsNothing(t *testing.T) {
	st := openFeedStore(t)
	stub := &stubCall{unknown: ruk.ErrOutcomeUnknown}
	id := StableRequestID("op", "r", []byte("d"))
	err := WriteAfterRuk(feedCtx(t), st, id, stub.call, nil, commitTipRow(t, "refs/heads/main"))
	if !errors.Is(err, ruk.ErrOutcomeUnknown) {
		t.Fatalf("err = %v, want unknown", err)
	}
	if _, ok := tipOf(t, st, 1, "refs/heads/main"); ok {
		t.Fatal("unknown outcome committed a row")
	}
}

func TestWriteAfterRukRefusesToCommitOnCallError(t *testing.T) {
	st := openFeedStore(t)
	boom := errors.New("daemon refused")
	stub := &stubCall{fail: boom}
	id := StableRequestID("op", "r", []byte("d"))
	err := WriteAfterRuk(feedCtx(t), st, id, stub.call, stub.replay, commitTipRow(t, "refs/heads/main"))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the refusal", err)
	}
	if stub.replays != 0 {
		t.Fatalf("replays = %d, want 0 for a certain failure", stub.replays)
	}
	if _, ok := tipOf(t, st, 1, "refs/heads/main"); ok {
		t.Fatal("refused call committed a row")
	}
}

func TestWriteAfterRukValidation(t *testing.T) {
	st := openFeedStore(t)
	stub := &stubCall{receipt: jsontext.Value(`"x"`)}
	commit := commitTipRow(t, "k")
	if err := WriteAfterRuk(feedCtx(t), st, "", stub.call, stub.replay, commit); err == nil {
		t.Fatal("empty request id accepted")
	}
	if err := WriteAfterRuk(feedCtx(t), st, "id", nil, stub.replay, commit); err == nil {
		t.Fatal("nil call accepted")
	}
	if err := WriteAfterRuk(feedCtx(t), st, "id", stub.call, stub.replay, nil); err == nil {
		t.Fatal("nil commit accepted")
	}
}
