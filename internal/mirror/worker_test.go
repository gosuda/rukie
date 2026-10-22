package mirror

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/store"
)

const testCanary = "CANARY-7f3a9c-mirror-secret"

func openWorker(t *testing.T) (*Worker, *store.Store, *queue.Queue, time.Time) {
	t.Helper()
	now := time.Unix(1700000000, 0).UTC()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	st, err := store.Open(ctx, store.Options{Path: t.TempDir() + "/mirror.sqlite"})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	current := now
	q := queue.Open(st, func() time.Time { return current })
	w := &Worker{
		Store:    st,
		Queue:    q,
		Ruk:      nil,
		SealKey:  bytesRepeat(32),
		Clock:    func() time.Time { return current },
		RepoRoot: t.TempDir(),
	}
	return w, st, q, now
}

func bytesRepeat(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = 7
	}
	return out
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func insertRepo(t *testing.T, st *store.Store, now time.Time, name string) int64 {
	t.Helper()
	var id int64
	err := st.Write(testCtx(t), func(tx *store.WriteTx) error {
		got, ierr := tx.InsertRepo(now, store.RepoInsert{Name: name, Visibility: "private", DefaultBranch: "main", RukPath: name})
		if ierr != nil {
			return ierr
		}
		id = got
		return nil
	})
	if err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	if id == 0 {
		t.Fatal("repo id is zero")
	}
	return id
}

func insertMirror(t *testing.T, st *store.Store, now time.Time, repoID int64, enabled bool) store.MirrorRow {
	t.Helper()
	var row store.MirrorRow
	err := st.Write(testCtx(t), func(tx *store.WriteTx) error {
		got, ierr := tx.InsertMirror(now, store.MirrorInsert{
			RepoID:                   repoID,
			Name:                     "origin",
			TargetURL:                "https://example.com/octo/repo.git",
			HostKind:                 "github",
			Username:                 "octo",
			Mode:                     "exact",
			RefSelection:             "all",
			Enabled:                  enabled,
			SyncOnRefUpdate:          true,
			DebounceSeconds:          10,
			ReconcileIntervalSeconds: 600,
		})
		if ierr != nil {
			return ierr
		}
		row = got
		return nil
	})
	if err != nil {
		t.Fatalf("insert mirror: %v", err)
	}
	if row.ID == 0 || row.PublicID == "" {
		t.Fatalf("mirror row incomplete: %+v", row)
	}
	return row
}

func insertRun(t *testing.T, st *store.Store, now time.Time, mirrorID int64, pub string) store.MirrorRunRow {
	t.Helper()
	var row store.MirrorRunRow
	err := st.Write(testCtx(t), func(tx *store.WriteTx) error {
		got, ierr := tx.InsertRun(now, store.MirrorRunRow{
			PublicID: pub,
			MirrorID: mirrorID,
			Kind:     "manual",
			State:    "queued",
			Trigger:  "manual",
			Counts:   "{}",
		})
		if ierr != nil {
			return ierr
		}
		row = got
		return nil
	})
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}
	return row
}

func putCanaryCredential(t *testing.T, w *Worker, st *store.Store, now time.Time, mirror store.MirrorRow) {
	t.Helper()
	sealed, err := Seal(w.SealKey, []byte(testCanary), credentialAAD(mirror.PublicID), 1, Last4(testCanary))
	if err != nil {
		t.Fatalf("seal canary: %v", err)
	}
	ms := now.UnixMilli()
	err = st.Write(testCtx(t), func(tx *store.WriteTx) error {
		return tx.PutCredential(store.CredentialRow{
			MirrorID:   mirror.ID,
			KeyVersion: int64(sealed.KeyVersion),
			Nonce:      sealed.Nonce,
			Ciphertext: sealed.Ciphertext,
			Last4:      sealed.Last4,
			CreatedAt:  ms,
		})
	})
	if err != nil {
		t.Fatalf("put credential: %v", err)
	}
}

func enqueueMirrorJob(t *testing.T, q *queue.Queue, id string, repoID int64, payload string) {
	t.Helper()
	if _, err := q.Enqueue(testCtx(t), queue.EnqueueSpec{
		Payload: payload,
		ID:      id,
		Kind:    queue.KindMirrorRun,
		RepoID:  repoID,
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
}

func jobDone(t *testing.T, q *queue.Queue, id string) {
	t.Helper()
	job, err := q.Job(testCtx(t), id)
	if err != nil {
		t.Fatalf("job lookup: %v", err)
	}
	if job.State != "done" {
		t.Fatalf("job %q state = %q, want done", id, job.State)
	}
}

func mustMarshalPayload(t *testing.T, value map[string]any) string {
	t.Helper()
	raw, err := jsonv2.Marshal(value, jsonv1.DefaultOptionsV1())
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return string(raw)
}

func TestSplitPayload(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		kind   payloadKind
		runPub string
		repoID int64
	}{
		{"touch", `{"invocation":"evt-1","repo":7}`, payloadKindTouch, "", 7},
		{"run", `{"run":"pub-1","mirror":3}`, payloadKindRun, "pub-1", 0},
		{"run beats touch", `{"run":"pub-2","mirror":3,"invocation":"evt-1","repo":7}`, payloadKindRun, "pub-2", 0},
		{"unknown shape", `{"bogus":1}`, payloadKindUnknown, "", 0},
		{"empty object", `{}`, payloadKindUnknown, "", 0},
		{"empty string", ``, payloadKindUnknown, "", 0},
		{"malformed", `{nope`, payloadKindUnknown, "", 0},
		{"empty run", `{"run":"","mirror":3}`, payloadKindRun, "", 0},
		{"touch without repo", `{"invocation":"evt-1"}`, payloadKindUnknown, "", 0},
		{"touch repo zero", `{"invocation":"evt-1","repo":0}`, payloadKindTouch, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, runPub, repoID := splitPayload(tc.raw)
			if kind != tc.kind || runPub != tc.runPub || repoID != tc.repoID {
				t.Fatalf("splitPayload(%q) = (%d,%q,%d), want (%d,%q,%d)",
					tc.raw, kind, runPub, repoID, tc.kind, tc.runPub, tc.repoID)
			}
		})
	}
}

func TestScrubSecrets(t *testing.T) {
	token := testCanary
	user := "octo"
	encoded := base64.StdEncoding.EncodeToString([]byte(user + ":" + token))
	got := scrubSecrets("push "+token+" then "+encoded+" end", user, token)
	if strings.Contains(got, token) {
		t.Fatalf("exact token survived: %q", got)
	}
	if strings.Contains(got, encoded) {
		t.Fatalf("encoded credential survived: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("no redaction marker: %q", got)
	}
	if out := scrubSecrets("clean", user, ""); out != "clean" {
		t.Fatalf("empty secret changed input: %q", out)
	}
	if out := scrubSecrets("has "+token, "", token); strings.Contains(out, token) {
		t.Fatalf("token survived without username: %q", out)
	}
}

func TestParseGlobs(t *testing.T) {
	if globs, err := parseGlobs(""); err != nil || globs != nil {
		t.Fatalf("empty = %v, %v", globs, err)
	}
	globs, err := parseGlobs(`["refs/heads/*"]`)
	if err != nil || len(globs) != 1 || globs[0] != "refs/heads/*" {
		t.Fatalf("json array = %v, %v", globs, err)
	}
	globs, err = parseGlobs("refs/heads/*")
	if err != nil || len(globs) != 1 {
		t.Fatalf("single = %v, %v", globs, err)
	}
	if _, err := parseGlobs(`["refs/heads/*"`); err == nil {
		t.Fatal("broken json accepted")
	}
}

func TestDriveUnknownPayloadCompletes(t *testing.T) {
	w, _, q, _ := openWorker(t)
	enqueueMirrorJob(t, q, "mirror-run:poison", 1, `{"bogus":1}`)
	again, err := w.DriveOnce(testCtx(t), "test-owner")
	if err != nil {
		t.Fatalf("DriveOnce: %v", err)
	}
	if !again {
		t.Fatal("DriveOnce reported idle on a claimed poison job")
	}
	jobDone(t, q, "mirror-run:poison")
	again, err = w.DriveOnce(testCtx(t), "test-owner")
	if err != nil {
		t.Fatalf("DriveOnce: %v", err)
	}
	if again {
		t.Fatal("DriveOnce reported work with an empty queue")
	}
}

func TestDriveMalformedPayloadCompletes(t *testing.T) {
	w, _, q, _ := openWorker(t)
	enqueueMirrorJob(t, q, "mirror-run:malformed", 1, `[1,2]`)
	again, err := w.DriveOnce(testCtx(t), "test-owner")
	if err != nil {
		t.Fatalf("DriveOnce: %v", err)
	}
	if !again {
		t.Fatal("DriveOnce reported idle on a malformed job")
	}
	jobDone(t, q, "mirror-run:malformed")
}

func TestDriveMissingRunCompletes(t *testing.T) {
	w, _, q, _ := openWorker(t)
	enqueueMirrorJob(t, q, "mirror-run:ghost", 1, mustMarshalPayload(t, map[string]any{"run": "ghost-pub", "mirror": 99}))
	again, err := w.DriveOnce(testCtx(t), "test-owner")
	if err != nil {
		t.Fatalf("DriveOnce: %v", err)
	}
	if !again {
		t.Fatal("DriveOnce reported idle on a missing run")
	}
	jobDone(t, q, "mirror-run:ghost")
}

func TestTouchSetsDirtySince(t *testing.T) {
	w, st, q, now := openWorker(t)
	repoID := insertRepo(t, st, now, "touched")
	mirror := insertMirror(t, st, now, repoID, true)
	enqueueMirrorJob(t, q, "mirror-run:evt-1", repoID, mustMarshalPayload(t, map[string]any{"invocation": "evt-1", "repo": repoID}))
	again, err := w.DriveOnce(testCtx(t), "test-owner")
	if err != nil {
		t.Fatalf("DriveOnce: %v", err)
	}
	if !again {
		t.Fatal("DriveOnce reported idle on a touch job")
	}
	jobDone(t, q, "mirror-run:evt-1")
	var dirty int64
	if rerr := st.Read(testCtx(t), func(tx *store.ReadTx) error {
		row, gerr := tx.GetMirror(mirror.ID)
		if gerr != nil {
			return gerr
		}
		dirty = row.DirtySince
		return nil
	}); rerr != nil {
		t.Fatalf("read mirror: %v", rerr)
	}
	if dirty <= 0 {
		t.Fatalf("dirty_since = %d, want positive", dirty)
	}
}

func TestRunRukNilIsUnavailable(t *testing.T) {
	w, st, q, now := openWorker(t)
	repoID := insertRepo(t, st, now, "noruk")
	mirror := insertMirror(t, st, now, repoID, true)
	putCanaryCredential(t, w, st, now, mirror)
	pub := "run-unavailable-1"
	insertRun(t, st, now, mirror.ID, pub)
	enqueueMirrorJob(t, q, "mirror-run:"+pub, repoID, mustMarshalPayload(t, map[string]any{"run": pub, "mirror": mirror.ID}))
	again, err := w.DriveOnce(testCtx(t), "test-owner")
	if err != nil {
		t.Fatalf("DriveOnce: %v", err)
	}
	if !again {
		t.Fatal("DriveOnce reported idle on a run job")
	}
	jobDone(t, q, "mirror-run:"+pub)
	var run store.MirrorRunRow
	if rerr := st.Read(testCtx(t), func(tx *store.ReadTx) error {
		row, gerr := tx.GetRunByPublicID(pub)
		if gerr != nil {
			return gerr
		}
		run = row
		return nil
	}); rerr != nil {
		t.Fatalf("read run: %v", rerr)
	}
	if run.State != "failed" || run.OutcomeClass != "unavailable" {
		t.Fatalf("run = %q/%q, want failed/unavailable", run.State, run.OutcomeClass)
	}
	assertCanaryAbsent(t, run.OutcomeSummary)
}

func TestRunDisabledMirror(t *testing.T) {
	w, st, q, now := openWorker(t)
	repoID := insertRepo(t, st, now, "off")
	mirror := insertMirror(t, st, now, repoID, false)
	pub := "run-disabled-1"
	insertRun(t, st, now, mirror.ID, pub)
	enqueueMirrorJob(t, q, "mirror-run:"+pub, repoID, mustMarshalPayload(t, map[string]any{"run": pub, "mirror": mirror.ID}))
	if _, err := w.DriveOnce(testCtx(t), "test-owner"); err != nil {
		t.Fatalf("DriveOnce: %v", err)
	}
	jobDone(t, q, "mirror-run:"+pub)
	var run store.MirrorRunRow
	if rerr := st.Read(testCtx(t), func(tx *store.ReadTx) error {
		row, gerr := tx.GetRunByPublicID(pub)
		if gerr != nil {
			return gerr
		}
		run = row
		return nil
	}); rerr != nil {
		t.Fatalf("read run: %v", rerr)
	}
	if run.State != "failed" || run.OutcomeClass != "disabled" {
		t.Fatalf("run = %q/%q, want failed/disabled", run.State, run.OutcomeClass)
	}
}

func TestRunCredentialDecryptFailsIsSecret(t *testing.T) {
	w, st, q, now := openWorker(t)
	repoID := insertRepo(t, st, now, "badcred")
	mirror := insertMirror(t, st, now, repoID, true)
	putCanaryCredential(t, w, st, now, mirror)
	wrong := bytesRepeat(32)
	for i := range wrong {
		wrong[i] = 9
	}
	w.SealKey = wrong
	pub := "run-badcred-1"
	insertRun(t, st, now, mirror.ID, pub)
	enqueueMirrorJob(t, q, "mirror-run:"+pub, repoID, mustMarshalPayload(t, map[string]any{"run": pub, "mirror": mirror.ID}))
	if _, err := w.DriveOnce(testCtx(t), "test-owner"); err != nil {
		t.Fatalf("DriveOnce: %v", err)
	}
	jobDone(t, q, "mirror-run:"+pub)
	var run store.MirrorRunRow
	if rerr := st.Read(testCtx(t), func(tx *store.ReadTx) error {
		row, gerr := tx.GetRunByPublicID(pub)
		if gerr != nil {
			return gerr
		}
		run = row
		return nil
	}); rerr != nil {
		t.Fatalf("read run: %v", rerr)
	}
	if run.State != "failed" || run.OutcomeClass != "secret" {
		t.Fatalf("run = %q/%q, want failed/secret", run.State, run.OutcomeClass)
	}
	assertCanaryAbsent(t, run.OutcomeSummary)
}

func TestRunCredentialMissingIsSecret(t *testing.T) {
	w, st, q, now := openWorker(t)
	repoID := insertRepo(t, st, now, "nocred")
	mirror := insertMirror(t, st, now, repoID, true)
	pub := "run-nocred-1"
	insertRun(t, st, now, mirror.ID, pub)
	enqueueMirrorJob(t, q, "mirror-run:"+pub, repoID, mustMarshalPayload(t, map[string]any{"run": pub, "mirror": mirror.ID}))
	if _, err := w.DriveOnce(testCtx(t), "test-owner"); err != nil {
		t.Fatalf("DriveOnce: %v", err)
	}
	jobDone(t, q, "mirror-run:"+pub)
	var run store.MirrorRunRow
	var mirrorRow store.MirrorRow
	if rerr := st.Read(testCtx(t), func(tx *store.ReadTx) error {
		row, gerr := tx.GetRunByPublicID(pub)
		if gerr != nil {
			return gerr
		}
		run = row
		mirrorRow, gerr = tx.GetMirror(mirror.ID)
		return gerr
	}); rerr != nil {
		t.Fatalf("read rows: %v", rerr)
	}
	if run.State != "failed" || run.OutcomeClass != "secret" {
		t.Fatalf("run = %q/%q, want failed/secret", run.State, run.OutcomeClass)
	}
	if mirrorRow.State != "needs_attention" {
		t.Fatalf("mirror state = %q, want needs_attention", mirrorRow.State)
	}
}

func TestCanaryAbsentFromStoredStrings(t *testing.T) {
	w, st, q, now := openWorker(t)
	repoID := insertRepo(t, st, now, "canary")
	mirror := insertMirror(t, st, now, repoID, true)
	putCanaryCredential(t, w, st, now, mirror)
	pub := "run-canary-1"
	insertRun(t, st, now, mirror.ID, pub)
	enqueueMirrorJob(t, q, "mirror-run:"+pub, repoID, mustMarshalPayload(t, map[string]any{"run": pub, "mirror": mirror.ID}))
	if _, err := w.DriveOnce(testCtx(t), "test-owner"); err != nil {
		t.Fatalf("DriveOnce: %v", err)
	}
	jobDone(t, q, "mirror-run:"+pub)
	var run store.MirrorRunRow
	var mirrorRow store.MirrorRow
	var bodies []string
	if rerr := st.Read(testCtx(t), func(tx *store.ReadTx) error {
		row, gerr := tx.GetRunByPublicID(pub)
		if gerr != nil {
			return gerr
		}
		run = row
		mirrorRow, gerr = tx.GetMirror(mirror.ID)
		if gerr != nil {
			return gerr
		}
		rows, gerr := tx.Outbox().After("mirror-test", 32)
		if gerr != nil {
			return gerr
		}
		for _, out := range rows {
			bodies = append(bodies, string(out.Body))
		}
		return nil
	}); rerr != nil {
		t.Fatalf("read rows: %v", rerr)
	}
	assertCanaryAbsent(t, run.OutcomeSummary, mirrorRow.LastError)
	assertCanaryAbsent(t, bodies...)
	if len(bodies) == 0 {
		t.Fatal("no outbox events recorded")
	}
	found := false
	for _, body := range bodies {
		if strings.Contains(body, `"job_id":"mirror-run:run-canary-1"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing run sync event in %v", bodies)
	}
}

func assertCanaryAbsent(t *testing.T, values ...string) {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString([]byte("octo:" + testCanary))
	for _, value := range values {
		if strings.Contains(value, testCanary) {
			t.Fatalf("canary token in stored string: %q", value)
		}
		if strings.Contains(value, encoded) {
			t.Fatalf("encoded credential in stored string: %q", value)
		}
	}
}

func TestRemoteDefaultBranch(t *testing.T) {
	main := "refs/heads/main"
	trunk := "refs/heads/trunk"
	cases := []struct {
		name    string
		entries []ruk.RefEntry
		want    string
	}{
		{"no refs", nil, "main"},
		{"direct only", []ruk.RefEntry{{Name: "refs/heads/main", Target: ruk.DirectTarget("abc")}}, "main"},
		{
			"symbolic head",
			[]ruk.RefEntry{
				{Name: "HEAD", Target: ruk.RefTarget{Symbolic: &trunk}},
				{Name: "refs/heads/trunk", Target: ruk.DirectTarget("abc")},
			},
			"trunk",
		},
		{
			"symbolic non-head ignored",
			[]ruk.RefEntry{{Name: "refs/heads/main", Target: ruk.RefTarget{Symbolic: &main}}},
			"main",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := remoteDefaultBranch(tc.entries); got != tc.want {
				t.Fatalf("remoteDefaultBranch = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMirrorSyncBodyRedaction(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("octo:" + testCanary))
	tracked := []*trackedRef{{
		plan:   RefPlan{Ref: "refs/heads/main", New: "abc"},
		status: "rejected",
		class:  "ref_protected",
		reason: "denied for " + testCanary + " and " + encoded,
	}}
	body, err := mirrorSyncBody(9, "mirror-run:x", "partial", tracked, func(s string) string {
		return scrubSecrets(s, "octo", testCanary)
	})
	if err != nil {
		t.Fatalf("mirrorSyncBody: %v", err)
	}
	assertCanaryAbsent(t, string(body))
	if !strings.Contains(string(body), `"ref_protected"`) {
		t.Fatalf("class lost: %s", string(body))
	}
}

func TestSummarizeTracked(t *testing.T) {
	if out := summarizeTracked(nil); out.runState != "succeeded" || out.mirror != "ok" {
		t.Fatalf("empty = %+v, want succeeded/ok", out)
	}
	rejected := []*trackedRef{{status: "rejected"}, {status: "ok"}}
	if out := summarizeTracked(rejected); out.runState != "partial" || out.mirror != "partial" {
		t.Fatalf("rejected = %+v, want partial", out)
	}
	diverged := []*trackedRef{{status: "diverged"}}
	if out := summarizeTracked(diverged); out.runState != "partial" {
		t.Fatalf("diverged = %+v, want partial", out)
	}
	uncertain := []*trackedRef{{status: "uncertain"}}
	if out := summarizeTracked(uncertain); out.runState != "succeeded" {
		t.Fatalf("uncertain alone = %+v, want succeeded from summarize (sync retries separately)", out)
	}
	if n := countUncertain(uncertain); n != 1 {
		t.Fatalf("countUncertain = %d, want 1", n)
	}
}

func TestReconcileInterval(t *testing.T) {
	if got := reconcileInterval(store.MirrorRow{ReconcileIntervalSeconds: 3600}); got != time.Hour {
		t.Fatalf("3600s = %v, want 1h", got)
	}
	if got := reconcileInterval(store.MirrorRow{}); got != 0 {
		t.Fatalf("zero interval = %v, want commit-only zero", got)
	}
}

func TestForceCheckedFor(t *testing.T) {
	prot := Mirror{Mode: ModeExact, ProtectDefaultBranch: true}
	plain := Mirror{Mode: ModeExact}
	rows := []struct {
		name string
		cfg  Mirror
		ref  string
		def  string
		want bool
	}{
		{"protected default skips force", prot, "refs/heads/main", "main", true},
		{"protected other ref still forces", prot, "refs/heads/feat", "main", false},
		{"protected tag still forces", prot, "refs/tags/v1", "main", false},
		{"unprotected default still forces", plain, "refs/heads/main", "main", false},
		{"empty default branch never matches", prot, "refs/heads/main", "", false},
	}
	for _, r := range rows {
		if got := forceCheckedFor(r.cfg, r.ref, r.def); got != r.want {
			t.Fatalf("%s = %v, want %v", r.name, got, r.want)
		}
	}
}

func TestReconcilePriorUncertain(t *testing.T) {
	w, st, _, now := openWorker(t)
	repoID := insertRepo(t, st, now, "pq")
	m := insertMirror(t, st, now, repoID, true)
	seed := []store.RefStateRow{
		{MirrorID: m.ID, Ref: "refs/heads/applied", LocalOid: "new-a", RemoteOid: "old-a", Status: "uncertain"},
		{MirrorID: m.ID, Ref: "refs/heads/gone", LocalOid: "", RemoteOid: "old-d", Status: "uncertain"},
		{MirrorID: m.ID, Ref: "refs/heads/stale", LocalOid: "new-s", RemoteOid: "old-s", Status: "uncertain"},
		{MirrorID: m.ID, Ref: "refs/heads/moved", LocalOid: "new-m", RemoteOid: "old-m", Status: "uncertain"},
		{MirrorID: m.ID, Ref: "refs/heads/steady", LocalOid: "x", RemoteOid: "x", Status: "ok"},
	}
	if err := st.Write(testCtx(t), func(tx *store.WriteTx) error {
		return tx.ReplaceRefStates(m.ID, seed)
	}); err != nil {
		t.Fatalf("seed refstates: %v", err)
	}
	remote := map[string]string{
		"refs/heads/applied": "new-a",
		"refs/heads/stale":   "old-s",
		"refs/heads/moved":   "third-party",
	}
	e := &execution{worker: *w, loaded: loadedRun{mirror: m}}
	if err := e.reconcilePriorUncertain(testCtx(t), remote); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(e.priorDiverged) != 1 {
		t.Fatalf("priorDiverged = %v, want only refs/heads/moved", e.priorDiverged)
	}
	if reason := e.priorDiverged["refs/heads/moved"]; reason == "" {
		t.Fatalf("moved ref lacks reason: %v", e.priorDiverged)
	}
}

func TestPriorDivergedOverridesPlan(t *testing.T) {
	w, _, _, _ := openWorker(t)
	e := &execution{worker: *w, cfg: Mirror{Mode: ModeExact}, priorDiverged: map[string]string{"refs/heads/main": "remote moved since the uncertain push"}}
	e.trackPlanned(Plan{Updates: []RefPlan{{Ref: "refs/heads/main", Old: "o", New: "n", Action: ActionForceUpdate}}})
	if len(e.tracked) != 1 {
		t.Fatalf("tracked = %+v, want one entry", e.tracked)
	}
	if e.tracked[0].status != "diverged" || e.tracked[0].class != string(ClassDiverged) {
		t.Fatalf("tracked = %+v, want diverged override", e.tracked[0])
	}
}
