//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	jsonv1 "encoding/json"
	"encoding/json/jsontext"

	"github.com/gosuda/rukie/internal/events"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/rukproc"
	"github.com/gosuda/rukie/internal/store"
)

func pushMain(t *testing.T, s *stack, owner, repo string, files map[string]string) string {
	t.Helper()
	work, oid := s.localRepo(t, files)
	pushURL := s.repoURL(owner, repo, owner, s.users[owner].token)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	if out, err := gitRun(ctx, t, work, "push", pushURL, "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	return oid
}

func openFeedRepo(t *testing.T, s *stack, owner, repo string) ruk.Repo {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	handle, err := s.client.OpenRepo(ctx, filepath.Join(s.roots, owner, repo+".git", ".ruk"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	return handle
}

func refUpdatedCount(t *testing.T, s *stack) int {
	t.Helper()
	var rows []store.OutboxRow
	err := s.st.Read(e2eCtx(t), func(tx *store.ReadTx) error {
		got, rerr := tx.Outbox().After("feedprobe", 1000)
		if rerr != nil {
			return rerr
		}
		rows = got
		return nil
	})
	if err != nil {
		t.Fatalf("outbox: %v", err)
	}
	n := 0
	for _, row := range rows {
		if row.Kind == "ref.updated" {
			n++
		}
	}
	return n
}

func e2eCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func feedTips(t *testing.T, s *stack, repoID int64) map[string]string {
	t.Helper()
	var tips map[string]string
	err := s.st.Read(e2eCtx(t), func(tx *store.ReadTx) error {
		got, rerr := tx.RefTips(repoID)
		if rerr != nil {
			return rerr
		}
		tips = got
		return nil
	})
	if err != nil {
		t.Fatalf("tips: %v", err)
	}
	return tips
}

func TestFollowerReplayDedupe(t *testing.T) {
	s := newStack(t, nil)
	s.addAdmin(t, "ada", "repo:read", "repo:write")
	repoID := s.addRepo(t, "ada", "hello", "private")
	oid := pushMain(t, s, "ada", "hello", map[string]string{"README.md": "hello\n"})
	repo := openFeedRepo(t, s, "ada", "hello")
	follower := events.OpenFollower(s.st, repoID, repo, s.client, time.Now)
	ctx := e2eCtx(t)
	if err := follower.PollOnce(ctx); err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if err := follower.PollOnce(ctx); err != nil {
		t.Fatalf("replay poll: %v", err)
	}
	if n := refUpdatedCount(t, s); n != 1 {
		t.Fatalf("ref.updated rows = %d, want 1", n)
	}
	cursor, err := follower.Cursor(ctx)
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if cursor == 0 {
		t.Fatal("cursor never advanced")
	}
	if tips := feedTips(t, s, repoID); tips["refs/heads/main"] != oid {
		t.Fatalf("tips = %+v, want main at %s", tips, oid)
	}
}

func TestSpoolSurvivesRukieCrash(t *testing.T) {
	s := newStack(t, nil)
	sp, err := events.OpenSpool(filepath.Join(s.dir, "crash"))
	if err != nil {
		t.Fatalf("spool: %v", err)
	}
	rec := events.PushRecord{
		Landed:       []events.LandedRef{{Name: "refs/heads/main", Old: strings.Repeat("0", 40), New: strings.Repeat("b", 40)}},
		InvocationID: "push-e2e-crash-1",
		RepoID:       777,
		ActorID:      1,
		AttemptedAt:  time.Now().UnixMilli(),
	}
	if err = sp.Store(rec); err != nil {
		t.Fatalf("crash store: %v", err)
	}
	restarted, err := events.OpenSpool(filepath.Join(s.dir, "crash"))
	if err != nil {
		t.Fatalf("reopen spool: %v", err)
	}
	loaded, err := restarted.Load("push-e2e-crash-1")
	if err != nil {
		t.Fatalf("fresh handle load: %v", err)
	}
	if len(loaded.Landed) != 1 || loaded.Landed[0].New != strings.Repeat("b", 40) || loaded.RepoID != 777 {
		t.Fatalf("reloaded = %+v", loaded)
	}
	ctx := e2eCtx(t)
	if err = restarted.Replay(ctx, s.st, events.EnqueuePush); err != nil {
		t.Fatalf("restart replay: %v", err)
	}
	if err = sp.Store(rec); err != nil {
		t.Fatalf("second crash store: %v", err)
	}
	if err = sp.Replay(ctx, s.st, events.EnqueuePush); err != nil {
		t.Fatalf("second replay: %v", err)
	}
	for _, id := range []string{"hook-delivery:push-e2e-crash-1", "mirror-run:push-e2e-crash-1", "ci-plan:push-e2e-crash-1"} {
		var job store.Job
		rerr := s.st.Read(ctx, func(tx *store.ReadTx) error {
			got, gerr := tx.GetJob(id)
			if gerr != nil {
				return gerr
			}
			job = got
			return nil
		})
		if rerr != nil {
			t.Fatalf("job %s: %v", id, rerr)
		}
		if job.State != "queued" {
			t.Fatalf("job %s = %+v", id, job)
		}
	}
	if n := pushReceivedCount(t, s); n != 1 {
		t.Fatalf("push.received rows = %d, want 1", n)
	}
	if tips := feedTips(t, s, 777); len(tips) != 0 {
		t.Fatalf("tips = %+v, want empty: the follower owns ref tips", tips)
	}
	pending, err := sp.Pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("spool kept %+v", pending)
	}
}

func TestPushProducesSpoolThenOutboxThenRemoves(t *testing.T) {
	s := newStack(t, nil)
	ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
	repoID := s.addRepo(t, "ada", "hello", "private")
	oid := pushMain(t, s, "ada", "hello", map[string]string{"README.md": "hello\n"})
	ctx := e2eCtx(t)
	var invocation string
	var enqueued int64
	rerr := s.db.QueryRowContext(ctx, `SELECT invocation_id, enqueued FROM pending_pushes WHERE repo_id = ?`, repoID).Scan(&invocation, &enqueued)
	if rerr != nil {
		t.Fatalf("pending row: %v", rerr)
	}
	if !strings.HasPrefix(invocation, "push-") {
		t.Fatalf("invocation = %q, want the push request id", invocation)
	}
	if enqueued != 1 {
		t.Fatal("post-receive left the push pending")
	}
	for _, id := range []string{"hook-delivery:" + invocation, "mirror-run:" + invocation, "ci-plan:" + invocation} {
		var job store.Job
		jerr := s.st.Read(ctx, func(tx *store.ReadTx) error {
			got, gerr := tx.GetJob(id)
			if gerr != nil {
				return gerr
			}
			job = got
			return nil
		})
		if jerr != nil {
			t.Fatalf("job %s: %v", id, jerr)
		}
		if job.State != "queued" || job.RepoID != repoID {
			t.Fatalf("job %s = %+v", id, job)
		}
	}
	var received int
	rerr = s.db.QueryRowContext(ctx, `SELECT count(*) FROM outbox WHERE kind = 'push.received'`).Scan(&received)
	if rerr != nil {
		t.Fatalf("push.received count: %v", rerr)
	}
	if received != 1 {
		t.Fatalf("push.received rows = %d, want 1", received)
	}
	if n := refUpdatedCount(t, s); n != 0 {
		t.Fatalf("ref.updated rows = %d, want 0 before the follower runs", n)
	}
	repo := openFeedRepo(t, s, "ada", "hello")
	follower := events.OpenFollower(s.st, repoID, repo, s.client, time.Now)
	if err := follower.PollOnce(ctx); err != nil {
		t.Fatalf("follower poll: %v", err)
	}
	if n := refUpdatedCount(t, s); n != 1 {
		t.Fatalf("ref.updated rows = %d, want 1 after the follower runs", n)
	}
	if tips := feedTips(t, s, repoID); tips["refs/heads/main"] != oid {
		t.Fatalf("tips = %+v, want main at %s", tips, oid)
	}
	entries, err := os.ReadDir(filepath.Join(s.dir, "spool"))
	if err != nil {
		t.Fatalf("spool dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("spool kept %d records after success", len(entries))
	}
	if ada.user.ID == 0 {
		t.Fatal("admin id missing")
	}
}

func TestSpoolKillBetweenFsyncAndCommit(t *testing.T) {
	if os.Getenv("RUKIE_SPOOL_CRASH_CHILD") == "1" {
		runSpoolCrashChild()
		return
	}
	dir := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run", "^TestSpoolKillBetweenFsyncAndCommit$", "-test.count=1")
	child.Env = append(os.Environ(), "RUKIE_SPOOL_CRASH_CHILD=1", "RUKIE_SPOOL_CRASH_DIR="+dir)
	var stderr bytes.Buffer
	child.Stderr = &stderr
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if err = child.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	scanned := make(chan error, 1)
	go func() {
		line, rerr := bufio.NewReader(stdout).ReadString('\n')
		if rerr != nil {
			scanned <- rerr
			return
		}
		if strings.TrimSpace(line) != "stored" {
			scanned <- fmt.Errorf("unexpected child line %q", line)
			return
		}
		scanned <- nil
	}()
	select {
	case serr := <-scanned:
		if serr != nil {
			_ = child.Process.Kill()
			_ = child.Wait()
			t.Fatalf("child signal: %v\n%s", serr, stderr.String())
		}
	case <-time.After(30 * time.Second):
		_ = child.Process.Kill()
		_ = child.Wait()
		t.Fatalf("child never stored\n%s", stderr.String())
	}
	if err = child.Process.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if err = child.Wait(); err == nil {
		t.Fatalf("killed child exited cleanly\n%s", stderr.String())
	}
	ctx := e2eCtx(t)
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(dir, "feed.sqlite")})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	sp, err := events.OpenSpool(dir)
	if err != nil {
		t.Fatalf("spool: %v", err)
	}
	if err = sp.Replay(ctx, st, events.EnqueuePush); err != nil {
		t.Fatalf("replay: %v", err)
	}
	before := pushReceivedCountOn(t, st)
	if before != 1 {
		t.Fatalf("push.received rows = %d, want 1", before)
	}
	if err = sp.Replay(ctx, st, events.EnqueuePush); err != nil {
		t.Fatalf("second replay: %v", err)
	}
	if after := pushReceivedCountOn(t, st); after != before {
		t.Fatalf("second replay changed rows from %d to %d", before, after)
	}
}

func runSpoolCrashChild() {
	dir := os.Getenv("RUKIE_SPOOL_CRASH_DIR")
	fail := func() {
		os.Exit(2)
	}
	sp, err := events.OpenSpool(dir)
	if err != nil {
		fail()
	}
	rec := events.PushRecord{
		Landed:       []events.LandedRef{{Name: "refs/heads/main", Old: strings.Repeat("0", 40), New: strings.Repeat("e", 40)}},
		InvocationID: "push-crash-kill-1",
		RepoID:       888,
		ActorID:      2,
		AttemptedAt:  time.Now().UnixMilli(),
	}
	if err = sp.Store(rec); err != nil {
		fail()
	}
	fmt.Println("stored")
	time.Sleep(60 * time.Second)
}

func pushReceivedCount(t *testing.T, s *stack) int {
	t.Helper()
	return pushReceivedCountOn(t, s.st)
}

func pushReceivedCountOn(t *testing.T, st *store.Store) int {
	t.Helper()
	var rows []store.OutboxRow
	err := st.Read(e2eCtx(t), func(tx *store.ReadTx) error {
		got, rerr := tx.Outbox().After("feedprobe", 1000)
		if rerr != nil {
			return rerr
		}
		rows = got
		return nil
	})
	if err != nil {
		t.Fatalf("outbox: %v", err)
	}
	n := 0
	for _, row := range rows {
		if row.Kind == "push.received" {
			n++
		}
	}
	return n
}

func TestReconcilerRepairsMissedPush(t *testing.T) {
	s := newStack(t, nil)
	s.addAdmin(t, "ada", "repo:read", "repo:write")
	repoID := s.addRepo(t, "ada", "hello", "private")
	work, oid := s.localRepo(t, map[string]string{"README.md": "hello\n"})
	pushURL := s.repoURL("ada", "hello", "ada", s.users["ada"].token)
	pushCtx, pushCancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer pushCancel()
	if out, err := gitRun(pushCtx, t, work, "push", pushURL, "HEAD:refs/heads/main", "HEAD:refs/tags/v1"); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	repo := openFeedRepo(t, s, "ada", "hello")
	ctx := e2eCtx(t)
	listed, err := ruk.RefsList(ctx, s.client, repo, ruk.RefsListArgs{})
	if err != nil {
		t.Fatalf("refs.list: %v", err)
	}
	consumer := events.FollowerConsumer(repoID)
	err = s.st.Write(ctx, func(tx *store.WriteTx) error {
		return tx.Outbox().Advance(consumer, 0, int64(listed.AsOf))
	})
	if err != nil {
		t.Fatalf("simulate consumed cursor: %v", err)
	}
	reconciler := events.OpenReconciler(s.st, repoID, func(ctx context.Context, prefix, after string) (ruk.RefsListReply, error) {
		args := ruk.RefsListArgs{Prefix: prefix}
		if after != "" {
			cursor := after
			args.After = &cursor
		}
		return ruk.RefsList(ctx, s.client, repo, args)
	}, time.Now)
	n, err := reconciler.ReconcileRepo(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 2 {
		t.Fatalf("synthesized = %d, want 2 (branch plus tag)", n)
	}
	if got := refUpdatedCount(t, s); got != 2 {
		t.Fatalf("ref.updated rows = %d, want 2", got)
	}
	tips := feedTips(t, s, repoID)
	if tips["refs/heads/main"] != oid || tips["refs/tags/v1"] != oid {
		t.Fatalf("tips = %+v, want main and v1 at %s", tips, oid)
	}
	again, err := reconciler.ReconcileRepo(ctx)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if again != 0 {
		t.Fatalf("second synthesized = %d, want 0", again)
	}
}

func TestRukFirstWriteOrderCrash(t *testing.T) {
	s := newStack(t, nil)
	s.addAdmin(t, "ada", "repo:read", "repo:write")
	repoID := s.addRepo(t, "ada", "hello", "private")
	oid := pushMain(t, s, "ada", "hello", map[string]string{"README.md": "hello\n"})
	repo := openFeedRepo(t, s, "ada", "hello")
	ctx := e2eCtx(t)
	args := map[string]any{"name": "refs/tags/rukfirst-probe", "target": oid, "expected": "absent"}
	id := events.StableRequestID("tag-set", "ada/hello", []byte("refs/tags/rukfirst-probe"+oid))
	call := func(rid string) (jsontext.Value, error) {
		raw, err := ruk.Retry[map[string]any, jsonv1.RawMessage](ctx, s.client, repo, wire.MethodTagSet, rid, args)
		if err != nil {
			return nil, err
		}
		return jsontext.Value(raw), nil
	}
	var first []byte
	crashed := context.DeadlineExceeded
	err := events.WriteAfterRuk(ctx, s.st, id, call, call, func(_ *store.WriteTx, receipt jsontext.Value) error {
		first = bytes.Clone(receipt)
		return crashed
	})
	if err == nil {
		t.Fatal("crashing commit succeeded")
	}
	restartDaemonKeepClient(t, s)
	repo = openFeedRepo(t, s, "ada", "hello")
	var second []byte
	err = events.WriteAfterRuk(ctx, s.st, id, call, call, func(tx *store.WriteTx, receipt jsontext.Value) error {
		second = bytes.Clone(receipt)
		sum := sha256.Sum256(receipt)
		return tx.PutRefTip(repoID, "feed/rukfirst", hex.EncodeToString(sum[:]), time.Now())
	})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	assertTagReplay(t, jsonv1.RawMessage(first), jsonv1.RawMessage(second))
	var tips map[string]string
	rerr := s.st.Read(ctx, func(tx *store.ReadTx) error {
		got, gerr := tx.RefTips(repoID)
		if gerr != nil {
			return gerr
		}
		tips = got
		return nil
	})
	if rerr != nil {
		t.Fatalf("tips: %v", rerr)
	}
	sum := sha256.Sum256(second)
	if tips["feed/rukfirst"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("tip = %q, want the receipt digest", tips["feed/rukfirst"])
	}
	if got := feedTagTarget(t, s, repo, "refs/tags/rukfirst-probe"); got != oid {
		t.Fatalf("tag target = %q, want %q: the mutation applied exactly once", got, oid)
	}
}

func feedTagTarget(t *testing.T, s *stack, repo ruk.Repo, name string) string {
	t.Helper()
	ctx := e2eCtx(t)
	reply, err := ruk.RefsList(ctx, s.client, repo, ruk.RefsListArgs{})
	if err != nil {
		t.Fatalf("refs.list: %v", err)
	}
	for _, ref := range reply.Refs.Items {
		if ref.Name == name {
			return ref.Target.Hex()
		}
	}
	t.Fatalf("tag %s missing from refs.list", name)
	return ""
}

func assertTagReplay(t *testing.T, first, second jsonv1.RawMessage) {
	t.Helper()
	var fmap map[string]any
	if err := jsonv1.Unmarshal([]byte(first), &fmap); err != nil {
		t.Fatalf("decode first: %v", err)
	}
	var smap map[string]any
	if err := jsonv1.Unmarshal([]byte(second), &smap); err != nil {
		t.Fatalf("decode second: %v", err)
	}
	freceipt, _ := fmap["outcome"].(map[string]any)["receipt"].(map[string]any)
	sreceipt, _ := smap["outcome"].(map[string]any)["receipt"].(map[string]any)
	if freplayed, _ := freceipt["replayed"].(bool); freplayed {
		t.Fatal("first replayed = true, want false for the initial commit")
	}
	if sreplayed, _ := sreceipt["replayed"].(bool); !sreplayed {
		t.Fatal("second replayed = false, want true: the retry did not replay the stored report")
	}
	if fmap["name"] != smap["name"] {
		t.Fatalf("name = %v vs %v, want equal", fmap["name"], smap["name"])
	}
	ftarget, _ := jsonv1.Marshal(fmap["target"])
	starget, _ := jsonv1.Marshal(smap["target"])
	if string(ftarget) != string(starget) {
		t.Fatalf("target = %s vs %s, want equal", string(ftarget), string(starget))
	}
	if freceipt["retained_until"] != sreceipt["retained_until"] {
		t.Fatal("retained_until changed across the kill; the retry re-executed instead of replaying")
	}
	freceipt["replayed"] = false
	sreceipt["replayed"] = false
	fnorm, _ := jsonv1.Marshal(fmap)
	snorm, _ := jsonv1.Marshal(smap)
	if string(fnorm) != string(snorm) {
		t.Fatal("receipt changed across the kill; the retry did not replay the stored report")
	}
}

func restartDaemon(t *testing.T, s *stack) {
	t.Helper()
	restartDaemonKeepClient(t, s)
	dialCtx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	client, err := ruk.Dial(dialCtx, s.socket)
	if err != nil {
		t.Fatalf("redial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	s.client = client
}

func restartDaemonKeepClient(t *testing.T, s *stack) {
	t.Helper()
	_ = s.child.Kill()
	configPath := filepath.Join(s.dir, "daemon.toml")
	var logs bytes.Buffer
	child, err := rukproc.StartServe(rukBin(t), configPath, os.Environ(), &logs, &logs)
	if err != nil {
		t.Fatalf("restart: %v\n%s", err, logs.String())
	}
	s.child = child
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, serr := os.Stat(s.socket); serr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("socket never reappeared\n%s", logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestServiceReceiptsSurviveDaemonKill(t *testing.T) {
	s := newStack(t, nil)
	s.addAdmin(t, "ada", "repo:read", "repo:write")
	s.addRepo(t, "ada", "hello", "private")
	oid := pushMain(t, s, "ada", "hello", map[string]string{"README.md": "hello\n"})
	repo := openFeedRepo(t, s, "ada", "hello")
	ctx := e2eCtx(t)
	args := map[string]any{"name": "refs/tags/receipt-probe", "target": oid, "expected": "absent"}
	const requestID = "123e4567-e89b-12d3-a456-426614174001"
	first, err := ruk.Retry[map[string]any, jsonv1.RawMessage](ctx, s.client, repo, wire.MethodTagSet, requestID, args)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	restartDaemonKeepClient(t, s)
	repo = openFeedRepo(t, s, "ada", "hello")
	second, err := ruk.Retry[map[string]any, jsonv1.RawMessage](ctx, s.client, repo, wire.MethodTagSet, requestID, args)
	if err != nil {
		t.Fatalf("retry after kill: %v", err)
	}
	assertTagReplay(t, first, second)
	if got := feedTagTarget(t, s, repo, "refs/tags/receipt-probe"); got != oid {
		t.Fatalf("tag target = %q, want %q: the mutation applied exactly once", got, oid)
	}
}

func TestFaultKillNine(t *testing.T) {
	t.Run("push", func(t *testing.T) {
		s := newStack(t, nil)
		s.addAdmin(t, "ada", "repo:read", "repo:write")
		repoID := s.addRepo(t, "ada", "hello", "private")
		big := make([]byte, 4<<20)
		for i := range big {
			big[i] = byte(i * 31)
		}
		work, oid := s.localRepo(t, map[string]string{"big.bin": string(big)})
		pushURL := s.repoURL("ada", "hello", "ada", s.users["ada"].token)
		ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
		defer cancel()
		pushed := make(chan error, 1)
		go func() {
			_, err := gitRun(ctx, t, work, "push", pushURL, "HEAD:refs/heads/main")
			pushed <- err
		}()
		time.Sleep(300 * time.Millisecond)
		_ = s.child.Kill()
		pushErr := <-pushed
		restartDaemon(t, s)
		repo := openFeedRepo(t, s, "ada", "hello")
		rctx := e2eCtx(t)
		follower := events.OpenFollower(s.st, repoID, repo, s.client, time.Now)
		if err := follower.PollOnce(rctx); err != nil {
			t.Fatalf("follower poll after kill: %v", err)
		}
		reconciler := events.OpenReconciler(s.st, repoID, func(ctx context.Context, prefix, after string) (ruk.RefsListReply, error) {
			args := ruk.RefsListArgs{Prefix: prefix}
			if after != "" {
				cursor := after
				args.After = &cursor
			}
			return ruk.RefsList(ctx, s.client, repo, args)
		}, time.Now)
		n, err := reconciler.ReconcileRepo(rctx)
		if err != nil {
			t.Fatalf("reconcile after kill: %v", err)
		}
		tips := feedTips(t, s, repoID)
		switch tips["refs/heads/main"] {
		case oid:
			if n != 0 {
				t.Fatalf("reconcile after follower ingest synthesized %d rows, want 0", n)
			}
			if got := refUpdatedCount(t, s); got != 1 {
				t.Fatalf("landed push produced %d ref.updated rows, want exactly 1", got)
			}
		case "":
			if n != 0 {
				t.Fatalf("unlanded push synthesized %d rows, want 0", n)
			}
		default:
			t.Fatalf("tips = %+v, want main at %s or absent", tips, oid)
		}
		if pushErr == nil && tips["refs/heads/main"] != oid {
			t.Fatalf("push reported success but main is %q", tips["refs/heads/main"])
		}
	})
}

func TestEventsTailExpiredResnapshot(t *testing.T) {
	s := newStackWithConfig(t, nil, "event_retention_seconds = 1\nretention_interval_ms = 500\n")
	s.addAdmin(t, "ada", "repo:read", "repo:write")
	repoID := s.addRepo(t, "ada", "hello", "private")
	oid := pushMain(t, s, "ada", "hello", map[string]string{"README.md": "hello\n"})
	repo := openFeedRepo(t, s, "ada", "hello")
	ctx := e2eCtx(t)
	live, err := ruk.EventsTail(ctx, s.client, repo, ruk.EventsTailArgs{After: 0, Limit: 100, WaitMs: 0})
	if err != nil {
		t.Fatalf("events.tail: %v", err)
	}
	if live.Expired {
		t.Fatal("fresh tail reported expired")
	}
	if len(live.Events) == 0 {
		t.Fatal("push produced no events")
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		stale, serr := ruk.EventsTail(ctx, s.client, repo, ruk.EventsTailArgs{After: 0, Limit: 100, WaitMs: 0})
		if serr != nil {
			t.Fatalf("retention tail: %v", serr)
		}
		if stale.Expired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("tail never expired: expired = %v, oldest = %d", stale.Expired, stale.Oldest)
		}
		time.Sleep(500 * time.Millisecond)
	}
	follower := events.OpenFollower(s.st, repoID, repo, s.client, time.Now)
	if err = follower.PollOnce(ctx); err != nil {
		t.Fatalf("expired poll: %v", err)
	}
	cursor, err := follower.Cursor(ctx)
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if cursor == 0 {
		t.Fatal("cursor never advanced past the snapshot")
	}
	if tips := feedTips(t, s, repoID); tips["refs/heads/main"] != oid {
		t.Fatalf("tips = %+v, want main at %s", tips, oid)
	}
}
