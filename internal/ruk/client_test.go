package ruk

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/ruk/internal/fakedaemon"
	"github.com/gosuda/rukie/internal/ruk/wire"
)

func startFake(t *testing.T, cfg fakedaemon.Config) (string, *fakedaemon.Server) {
	t.Helper()
	dir := t.TempDir()
	if len(dir) > 90 {
		t.Fatalf("temp dir too long for unix socket: %s", dir)
	}
	socket := dir + "/ruk.sock"
	server, err := fakedaemon.Listen(socket, cfg)
	if err != nil {
		t.Fatalf("fake listen: %v", err)
	}
	t.Cleanup(server.Close)
	return socket, server
}

func dialTest(t *testing.T, socket string, opts ...Option) *Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := Dial(ctx, socket, opts...)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
	})
	return client
}

func callCtx(t *testing.T, d time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), d)
}

func TestClientHandshakeAndSchema(t *testing.T) {
	socket, _ := startFake(t, fakedaemon.Config{})
	client := dialTest(t, socket)
	ctx, cancel := callCtx(t, 5*time.Second)
	defer cancel()
	repo, err := client.OpenRepo(ctx, "/repo")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	if repo.Handle != "handle-/repo" {
		t.Fatalf("handle = %q", repo.Handle)
	}
	doc, err := Schema(ctx, client, repo, wire.SchemaArgs{})
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	if !strings.Contains(string(doc), "schema_version") {
		t.Fatalf("schema doc = %s", string(doc))
	}
}

func TestClientSessionFlows(t *testing.T) {
	socket, _ := startFake(t, fakedaemon.Config{RequireAuth: true})
	client := dialTest(t, socket)
	ctx, cancel := callCtx(t, 5*time.Second)
	defer cancel()
	repo, err := client.OpenRepo(ctx, "/repo")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	opened, err := SessionOpen(ctx, client, repo, wire.SessionOpenArgs{})
	if err != nil {
		t.Fatalf("session open: %v", err)
	}
	if opened.Session == "" || opened.Principal == "" {
		t.Fatalf("session opened = %+v", opened)
	}
	if opened.ReconnectSecret.String() != "<redacted>" {
		t.Fatal("secret prints raw")
	}
	resumed, err := SessionResume(ctx, client, repo, wire.SessionResumeArgs{
		Session:         opened.Session,
		ReconnectSecret: opened.ReconnectSecret,
	})
	if err != nil {
		t.Fatalf("session resume: %v", err)
	}
	if !resumed.Authority.IsActive() {
		t.Fatal("resumed authority inactive")
	}
	type echo struct {
		Echo bool `json:"echo"`
	}
	out, err := Call[map[string]any, echo](ctx, client, repo, wire.MethodStatus, map[string]any{})
	if err != nil {
		t.Fatalf("echo read: %v", err)
	}
	if !out.Echo {
		t.Fatal("echo missing")
	}
}

func TestClientRefusalBeforeAccept(t *testing.T) {
	socket, _ := startFake(t, fakedaemon.Config{
		ValidRepo: func(_ string) (string, string) {
			return "", "E_FORBIDDEN"
		},
	})
	var events []CallEvent
	client := dialTest(t, socket, WithHooks(Hooks{
		OnCall: func(e CallEvent) {
			events = append(events, e)
		},
	}))
	ctx, cancel := callCtx(t, 5*time.Second)
	defer cancel()
	_, err := Schema(ctx, client, Repo{Path: "/denied"}, wire.SchemaArgs{})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v", err)
	}
	rerr, ok := errors.AsType[*Error](err)
	if !ok {
		t.Fatalf("err type = %T", err)
	}
	if rerr.Outcome != NotCommitted {
		t.Fatalf("outcome = %v", rerr.Outcome)
	}
	if len(events) != 1 || events[0].Outcome != NotCommitted {
		t.Fatalf("events = %+v", events)
	}
}

func TestClientMalformedBody(t *testing.T) {
	socket, _ := startFake(t, fakedaemon.Config{Doc: jsonv1.RawMessage("bogus{{{")})
	client := dialTest(t, socket)
	ctx, cancel := callCtx(t, 5*time.Second)
	defer cancel()
	_, err := Schema(ctx, client, Repo{Path: "/repo"}, wire.SchemaArgs{})
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("err = %v", err)
	}
}

func TestDecodeTypedBodyTable(t *testing.T) {
	var out map[string]any
	good := `{"schema_version":{"major":1,"minor":1},"request_id":"r","body":{"success":{"status":{"echo":true}}}}`
	if err := decodeTypedBody(wire.MethodStatus, "r", []byte(good), &out); err != nil {
		t.Fatalf("good envelope: %v", err)
	}
	mismatch := `{"schema_version":{"major":1,"minor":1},"request_id":"other","body":{"success":{"status":{}}}}`
	if err := decodeTypedBody(wire.MethodStatus, "r", []byte(mismatch), &out); !errors.Is(err, ErrProtocol) {
		t.Fatalf("id mismatch err = %v", err)
	}
	wrongKey := `{"schema_version":{"major":1,"minor":1},"body":{"success":{"log":{}}}}`
	if err := decodeTypedBody(wire.MethodStatus, "r", []byte(wrongKey), &out); !errors.Is(err, ErrProtocol) {
		t.Fatalf("wrong key err = %v", err)
	}
	both := `{"schema_version":{"major":1,"minor":1},"body":{"success":{"status":{}},"failure":{"code":"E_STALE","message":"x"}}}`
	if err := decodeTypedBody(wire.MethodStatus, "r", []byte(both), &out); !errors.Is(err, ErrProtocol) {
		t.Fatalf("both keys err = %v", err)
	}
	failure := `{"schema_version":{"major":1,"minor":1},"body":{"failure":{"code":"E_STALE","message":"moved"}}}`
	err := decodeTypedBody(wire.MethodStatus, "r", []byte(failure), &out)
	rerr, ok := errors.AsType[*refusalError](err)
	if !ok {
		t.Fatalf("failure err type = %T", err)
	}
	if rerr.err.Code != wire.CodeStale {
		t.Fatalf("failure code = %s", string(rerr.err.Code))
	}
	bare := `{"session":"s","reconnect_secret":"x","principal":"p"}`
	var opened wire.SessionOpened
	if err := decodeTypedBody(wire.MethodSessionOpen, "r", []byte(bare), &opened); err != nil {
		t.Fatalf("bare decode: %v", err)
	}
	if opened.Session != "s" {
		t.Fatalf("bare session = %s", opened.Session)
	}
}

func TestClientCancelOrphan(t *testing.T) {
	socket, _ := startFake(t, fakedaemon.Config{StreamDelay: 150 * time.Millisecond})
	var outcomes []Outcome
	client := dialTest(t, socket, WithHooks(Hooks{
		OnCall: func(e CallEvent) {
			outcomes = append(outcomes, e.Outcome)
		},
	}))
	setup, setupCancel := callCtx(t, 5*time.Second)
	defer setupCancel()
	type echo struct {
		Echo bool `json:"echo"`
	}
	if _, err := Call[map[string]any, echo](setup, client, Repo{Path: "/repo"}, wire.MethodStatus, map[string]any{}); err != nil {
		t.Fatalf("session setup: %v", err)
	}
	outcomes = nil
	ctx, cancel := callCtx(t, 50*time.Millisecond)
	defer cancel()
	_, err := Call[map[string]any, map[string]any](ctx, client, Repo{Path: "/repo"}, wire.MethodWsNew, map[string]any{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if len(outcomes) != 1 || outcomes[0] != Unknown {
		t.Fatalf("outcomes = %v", outcomes)
	}
	readCtx, readCancel := callCtx(t, 50*time.Millisecond)
	defer readCancel()
	_, err = Call[map[string]any, map[string]any](readCtx, client, Repo{Path: "/repo"}, wire.MethodStatus, map[string]any{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read err = %v", err)
	}
	if len(outcomes) != 2 || outcomes[1] != NotCommitted {
		t.Fatalf("outcomes = %v", outcomes)
	}
}

func TestClientLateExitIgnored(t *testing.T) {
	socket, _ := startFake(t, fakedaemon.Config{StreamDelay: 150 * time.Millisecond})
	var violations atomic.Int32
	client := dialTest(t, socket, WithHooks(Hooks{
		OnProtocolViolation: func(_ error) {
			violations.Add(1)
		},
	}))
	ctx, cancel := callCtx(t, 50*time.Millisecond)
	defer cancel()
	_, err := Call[map[string]any, map[string]any](ctx, client, Repo{Path: "/repo"}, wire.MethodWsNew, map[string]any{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	if violations.Load() != 0 {
		t.Fatalf("violations = %d", violations.Load())
	}
	again, cancel2 := callCtx(t, 5*time.Second)
	defer cancel2()
	if _, err := Schema(again, client, Repo{Path: "/repo"}, wire.SchemaArgs{}); err != nil {
		t.Fatalf("reuse after cancel: %v", err)
	}
}

func TestClientCreditGrants(t *testing.T) {
	big, _ := jsonv2.Marshal(map[string]any{
		"schema_version": map[string]any{"major": 1, "minor": 1},
		"padding":        strings.Repeat("x", 300<<10),
	})
	var directions []string
	socket, server := startFake(t, fakedaemon.Config{Doc: big, Directions: &directions})
	client := dialTest(t, socket)
	ctx, cancel := callCtx(t, 10*time.Second)
	defer cancel()
	doc, err := Schema(ctx, client, Repo{Path: "/repo"}, wire.SchemaArgs{})
	if err != nil {
		t.Fatalf("big schema: %v", err)
	}
	if len(doc) != len(big) {
		t.Fatalf("doc len = %d, want %d", len(doc), len(big))
	}
	seen := server.SnapshotDirections()
	for _, dir := range seen {
		if dir != "stdout" {
			t.Fatalf("grant direction = %q", dir)
		}
	}
	if len(seen) == 0 {
		t.Fatal("no grants sent")
	}
}

func TestClientPlacement(t *testing.T) {
	socket, _ := startFake(t, fakedaemon.Config{StreamDelay: 300 * time.Millisecond})
	var mu sync.Mutex
	conns := make(map[uint64]int)
	queued := 0
	client := dialTest(t, socket,
		WithPool(2, 1),
		WithHooks(Hooks{
			OnAccept: func(e AcceptEvent) {
				mu.Lock()
				conns[e.Conn]++
				if e.Queued {
					queued++
				}
				mu.Unlock()
			},
		}))
	ctx, cancel := callCtx(t, 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			_, _ = Schema(ctx, client, Repo{Path: "/repo"}, wire.SchemaArgs{})
		})
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(conns) != 2 {
		t.Fatalf("conns = %v, want two", conns)
	}
	if queued != 0 {
		t.Fatalf("queued = %d", queued)
	}
}

func TestClientPlacementQueuesAtCap(t *testing.T) {
	socket, _ := startFake(t, fakedaemon.Config{StreamDelay: 300 * time.Millisecond})
	var mu sync.Mutex
	conns := make(map[uint64]int)
	queued := 0
	client := dialTest(t, socket,
		WithPool(1, 2),
		WithHooks(Hooks{
			OnAccept: func(e AcceptEvent) {
				mu.Lock()
				conns[e.Conn]++
				if e.Queued {
					queued++
				}
				mu.Unlock()
			},
		}))
	ctx, cancel := callCtx(t, 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			_, _ = Schema(ctx, client, Repo{Path: "/repo"}, wire.SchemaArgs{})
		})
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(conns) != 1 {
		t.Fatalf("conns = %v, want one", conns)
	}
	if queued != 1 {
		t.Fatalf("queued = %d, want one", queued)
	}
}

func TestClientHookStray(t *testing.T) {
	socket, _ := startFake(t, fakedaemon.Config{InjectHook: true})
	var violations atomic.Int32
	client := dialTest(t, socket, WithHooks(Hooks{
		OnProtocolViolation: func(_ error) {
			violations.Add(1)
		},
	}))
	ctx, cancel := callCtx(t, 5*time.Second)
	defer cancel()
	if _, err := Schema(ctx, client, Repo{Path: "/repo"}, wire.SchemaArgs{}); err != nil {
		t.Fatalf("call with hook: %v", err)
	}
	if violations.Load() != 1 {
		t.Fatalf("violations = %d, want one", violations.Load())
	}
}

func TestClientSessionRecovery(t *testing.T) {
	refused := false
	socket, _ := startFake(t, fakedaemon.Config{
		ValidRepo: func(repo string) (string, string) {
			if repo == "handle-/recover" && !refused {
				refused = true
				return "", "E_NOT_FOUND"
			}
			return "handle-" + repo, ""
		},
	})
	var attempts []uint8
	client := dialTest(t, socket, WithHooks(Hooks{
		OnCall: func(e CallEvent) {
			attempts = append(attempts, e.Attempts)
		},
	}))
	ctx, cancel := callCtx(t, 5*time.Second)
	defer cancel()
	repo, err := client.OpenRepo(ctx, "/recover")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	type echo struct {
		Echo bool `json:"echo"`
	}
	out, err := Call[map[string]any, echo](ctx, client, repo, wire.MethodStatus, map[string]any{})
	if err != nil {
		t.Fatalf("recovered call: %v", err)
	}
	if !out.Echo {
		t.Fatal("echo missing")
	}
	if len(attempts) != 5 || attempts[4] != 2 {
		t.Fatalf("attempts = %v", attempts)
	}
}

func TestClientResumeAfterForbidden(t *testing.T) {
	socket, _ := startFake(t, fakedaemon.Config{
		RequireAuth:       true,
		RefuseAfterAccept: map[string]string{"status": "E_FORBIDDEN"},
		RefuseOnce:        true,
	})
	var attempts []uint8
	client := dialTest(t, socket, WithHooks(Hooks{
		OnCall: func(e CallEvent) {
			attempts = append(attempts, e.Attempts)
		},
	}))
	ctx, cancel := callCtx(t, 5*time.Second)
	defer cancel()
	repo, err := client.OpenRepo(ctx, "/repo")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	type echo struct {
		Echo bool `json:"echo"`
	}
	out, err := Call[map[string]any, echo](ctx, client, repo, wire.MethodStatus, map[string]any{})
	if err != nil {
		t.Fatalf("resumed call: %v", err)
	}
	if !out.Echo {
		t.Fatal("echo missing")
	}
	if len(attempts) < 2 {
		t.Fatalf("attempts = %v, want resume and retry", attempts)
	}
}

func TestClientUnknownMethod(t *testing.T) {
	socket, _ := startFake(t, fakedaemon.Config{
		Refuse: map[string]string{"nope.nope": "E_INVALID"},
	})
	var outcomes []Outcome
	client := dialTest(t, socket, WithHooks(Hooks{
		OnCall: func(e CallEvent) {
			outcomes = append(outcomes, e.Outcome)
		},
	}))
	ctx, cancel := callCtx(t, 5*time.Second)
	defer cancel()
	_, err := Call[map[string]any, map[string]any](ctx, client, Repo{Path: "/repo"}, wire.Method("nope.nope"), map[string]any{})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	rerr, ok := errors.AsType[*Error](err)
	if !ok {
		t.Fatalf("err type = %T", err)
	}
	if rerr.Outcome != NotCommitted {
		t.Fatalf("outcome = %v", rerr.Outcome)
	}
	if len(outcomes) == 0 || outcomes[len(outcomes)-1] != NotCommitted {
		t.Fatalf("outcomes = %v", outcomes)
	}
}

func TestFakeSerialPending(t *testing.T) {
	socket, _ := startFake(t, fakedaemon.Config{Serial: true, StreamDelay: 50 * time.Millisecond})
	var mu sync.Mutex
	conns := make(map[uint64]int)
	client := dialTest(t, socket,
		WithPool(1, 8),
		WithHooks(Hooks{
			OnAccept: func(e AcceptEvent) {
				mu.Lock()
				conns[e.Conn]++
				mu.Unlock()
			},
		}))
	ctx, cancel := callCtx(t, 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	failed := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			_, err := Schema(ctx, client, Repo{Path: "/repo"}, wire.SchemaArgs{})
			if err != nil {
				failed <- err
			}
		})
	}
	wg.Wait()
	close(failed)
	for err := range failed {
		t.Fatalf("serial call: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(conns) != 1 {
		t.Fatalf("conns = %v, want one", conns)
	}
}

func TestFakeConcurrentInterleave(t *testing.T) {
	socket, _ := startFake(t, fakedaemon.Config{StreamDelay: 50 * time.Millisecond})
	client := dialTest(t, socket, WithPool(2, 4))
	ctx, cancel := callCtx(t, 15*time.Second)
	defer cancel()
	type echo struct {
		Echo bool `json:"echo"`
	}
	var wg sync.WaitGroup
	failed := make(chan error, 4)
	for range 4 {
		wg.Go(func() {
			out, err := Call[map[string]any, echo](ctx, client, Repo{Path: "/repo"}, wire.MethodStatus, map[string]any{})
			if err != nil {
				failed <- err
				return
			}
			if !out.Echo {
				failed <- errors.New("echo missing")
			}
		})
	}
	wg.Wait()
	close(failed)
	for err := range failed {
		t.Fatalf("concurrent call: %v", err)
	}
}

func TestOpenRepoRelativePath(t *testing.T) {
	socket, _ := startFake(t, fakedaemon.Config{})
	client := dialTest(t, socket)
	ctx, cancel := callCtx(t, 5*time.Second)
	defer cancel()
	if _, err := client.OpenRepo(ctx, "relative/path"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("err = %v", err)
	}
}

func TestClientGoroutineStability(t *testing.T) {
	socket, _ := startFake(t, fakedaemon.Config{})
	before := settledGoroutines(t)
	client := dialTest(t, socket)
	ctx, cancel := callCtx(t, 5*time.Second)
	defer cancel()
	if _, err := Schema(ctx, client, Repo{Path: "/repo"}, wire.SchemaArgs{}); err != nil {
		t.Fatalf("schema: %v", err)
	}
	_ = client.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if settledGoroutines(t) <= before {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("goroutines = %d, baseline %d", settledGoroutines(t), before)
}

func settledGoroutines(t *testing.T) int {
	t.Helper()
	return runtime.NumGoroutine()
}
