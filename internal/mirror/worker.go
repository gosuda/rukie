// Package mirror drives push-mirror sync: it claims mirror queue jobs,
// touches dirty mirrors on ref updates, and executes reconcile and manual
// runs through the Ruk daemon without ever persisting credentials.
package mirror

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	cryptorand "crypto/rand"
	"crypto/sha256"

	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

const (
	mirrorClaimLease = time.Minute
	mirrorCallBudget = 60 * time.Second

	maxStoredErrorLen = 512

	outboxKindMirrorSyncCompleted = "mirror.sync.completed"
)

// Worker claims mirror jobs and executes their runs. A nil Ruk records the
// run as failed without dialing; poison jobs complete in place and never
// spin. Mirror scheduling state lives in the store mirror rows owned by the
// store slice; this worker only consumes that surface.
type Worker struct {
	Store       *store.Store
	Queue       *queue.Queue
	Ruk         *ruk.Client
	Clock       func() time.Time
	RepoRoot    string
	SealKey     []byte
	WorkerCount uint
}

func (w Worker) now() time.Time {
	if w.Clock != nil {
		return w.Clock()
	}
	return time.Now()
}

type jobPayload struct {
	Run    *string `json:"run"`
	Mirror *int64  `json:"mirror"`
	Invoke *string `json:"invocation"`
	Repo   *int64  `json:"repo"`
}

func credentialAAD(mirrorPublicID string) string {
	return "mirror:" + mirrorPublicID
}

func daemonPath(root, rel string) string {
	return filepath.Join(root, filepath.Clean("/"+rel))
}

// Scheduler ticks DueMirrors into reconcile runs and drains the queue until
// idle. It returns when ctx ends.
func (w Worker) Scheduler(ctx context.Context, interval time.Duration, owner string) error {
	if w.Store == nil || w.Queue == nil {
		return fmt.Errorf("Scheduler: worker is not configured")
	}
	if interval <= 0 {
		return fmt.Errorf("Scheduler: interval must be positive")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	if serr := w.scheduleOnce(ctx); serr != nil {
		return serr
	}
	for {
		if derr := w.drain(ctx, owner); derr != nil {
			return derr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		if serr := w.scheduleOnce(ctx); serr != nil {
			return serr
		}
	}
}

func (w Worker) drain(ctx context.Context, owner string) error {
	workers := int(w.WorkerCount)
	if workers <= 0 {
		workers = MaxMirrorWorkers
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for {
				again, derr := w.DriveOnce(workCtx, owner)
				if derr != nil {
					select {
					case errs <- derr:
					default:
					}
					cancel()
					return
				}
				if !again {
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (w Worker) scheduleOnce(ctx context.Context) error {
	now := w.now()
	var due []store.MirrorRow
	if rerr := w.Store.Read(ctx, func(tx *store.ReadTx) error {
		rows, derr := tx.DueMirrors(now.UnixMilli(), MaxMirrorWorkers*4)
		if derr != nil {
			return derr
		}
		due = rows
		return nil
	}); rerr != nil {
		return fmt.Errorf("scheduleOnce: %w", rerr)
	}
	for _, row := range due {
		if !row.Enabled {
			continue
		}
		pub := string(store.NewID(now))
		mirrorID := row.ID
		if werr := w.Store.Write(ctx, func(tx *store.WriteTx) error {
			_, ierr := tx.InsertRun(now, store.MirrorRunRow{
				PublicID: pub,
				MirrorID: row.ID,
				Kind:     "reconcile",
				State:    "queued",
				Trigger:  "schedule",
				Counts:   "{}",
			})
			return ierr
		}); werr != nil {
			return fmt.Errorf("scheduleOnce: %w", werr)
		}
		payload, merr := jsonv2.Marshal(jobPayload{Run: &pub, Mirror: &mirrorID}, jsonv1.DefaultOptionsV1())
		if merr != nil {
			return fmt.Errorf("scheduleOnce: %w", merr)
		}
		if _, eerr := w.Queue.Enqueue(ctx, queue.EnqueueSpec{
			Payload: string(payload),
			ID:      "mirror-run:" + pub,
			Kind:    queue.KindMirrorRun,
			RepoID:  row.RepoID,
		}); eerr != nil {
			return fmt.Errorf("scheduleOnce: %w", eerr)
		}
	}
	return nil
}

// DriveOnce claims one mirror_run job and executes it. It returns false
// when no job is ready. Unknown payloads complete in place so poison jobs
// never spin the writer.
func (w Worker) DriveOnce(ctx context.Context, owner string) (bool, error) {
	if w.Store == nil || w.Queue == nil {
		return false, fmt.Errorf("DriveOnce: worker is not configured")
	}
	claimed, cerr := w.Queue.ClaimKinds(ctx, owner, []queue.Kind{queue.KindMirrorRun}, mirrorClaimLease, 1)
	if cerr != nil {
		return false, fmt.Errorf("DriveOnce: %w", cerr)
	}
	if len(claimed) == 0 {
		return false, nil
	}
	ectx, cancel := context.WithTimeout(ctx, mirrorCallBudget)
	defer cancel()
	if derr := w.drive(ectx, owner, claimed[0]); derr != nil {
		return true, derr
	}
	return true, nil
}

func (w Worker) drive(ctx context.Context, owner string, job store.Job) error {
	switch kind, runPub, repoID := splitPayload(job.Payload); kind {
	case payloadKindRun:
		e := &execution{worker: w, owner: owner, job: job, start: w.now()}
		return e.run(ctx, runPub)
	case payloadKindTouch:
		return w.touch(ctx, owner, job, repoID)
	case payloadKindUnknown:
		return w.complete(ctx, owner, job)
	default:
		return w.complete(ctx, owner, job)
	}
}

type payloadKind int

const (
	payloadKindUnknown payloadKind = iota
	payloadKindRun
	payloadKindTouch
)

func splitPayload(raw string) (payloadKind, string, int64) {
	var payload jobPayload
	if uerr := jsonv2.Unmarshal([]byte(raw), &payload, jsonv1.DefaultOptionsV1()); uerr != nil {
		return payloadKindUnknown, "", 0
	}
	if payload.Run != nil {
		return payloadKindRun, *payload.Run, 0
	}
	if payload.Invoke != nil && *payload.Invoke != "" && payload.Repo != nil {
		return payloadKindTouch, "", *payload.Repo
	}
	return payloadKindUnknown, "", 0
}

func (w Worker) complete(ctx context.Context, owner string, job store.Job) error {
	if cerr := w.Queue.Complete(ctx, owner, job.ID, job.LeaseToken); cerr != nil {
		return fmt.Errorf("complete: %w", cerr)
	}
	return nil
}

func (w Worker) touch(ctx context.Context, owner string, job store.Job, repoID int64) error {
	if repoID <= 0 {
		return w.complete(ctx, owner, job)
	}
	now := w.now()
	if werr := w.Store.Write(ctx, func(tx *store.WriteTx) error {
		_, terr := tx.TouchMirrorDirty(now, repoID)
		return terr
	}); werr != nil {
		return fmt.Errorf("touch: %w", werr)
	}
	return w.complete(ctx, owner, job)
}

type trackedRef struct {
	confirmed *string
	plan      RefPlan
	status    string
	reason    string
	class     string
	planID    string
	requestID string
}

type execution struct {
	start         time.Time
	priorDiverged map[string]string
	auth          wire.TransportAuth
	repo          ruk.Repo
	remoteDefault string
	username      string
	owner         string
	plans         []string
	token         []byte
	tracked       []*trackedRef
	worker        Worker
	cfg           Mirror
	job           store.Job
	loaded        loadedRun
	observed      bool
}

type loadedRun struct {
	run    store.MirrorRunRow
	repo   store.RepoRow
	mirror store.MirrorRow
}

type runOutcome struct {
	runState string
	class    string
	summary  string
	mirror   string
	http     int64
	delay    time.Duration
	requeue  bool
}

func (e *execution) run(ctx context.Context, runPub string) error {
	done, err := e.load(ctx, runPub)
	if err != nil || done {
		return err
	}
	done, err = e.guard(ctx)
	if err != nil || done {
		return err
	}
	done, err = e.sync(ctx)
	if err != nil || done {
		return err
	}
	return nil
}

func (e *execution) load(ctx context.Context, runPub string) (bool, error) {
	var stored loadedRun
	var missing bool
	if rerr := e.worker.Store.Read(ctx, func(tx *store.ReadTx) error {
		run, gerr := tx.GetRunByPublicID(runPub)
		if gerr != nil {
			if errors.Is(gerr, store.ErrNotFound) {
				missing = true
				return nil
			}
			return gerr
		}
		mirrorRow, gerr := tx.GetMirror(run.MirrorID)
		if gerr != nil {
			if errors.Is(gerr, store.ErrNotFound) {
				missing = true
				return nil
			}
			return gerr
		}
		repo, gerr := tx.GetRepo(mirrorRow.RepoID)
		if gerr != nil {
			if errors.Is(gerr, store.ErrNotFound) {
				missing = true
				return nil
			}
			return gerr
		}
		stored = loadedRun{run: run, mirror: mirrorRow, repo: repo}
		return nil
	}); rerr != nil {
		return false, fmt.Errorf("load: %w", rerr)
	}
	if missing {
		return true, e.worker.complete(ctx, e.owner, e.job)
	}
	e.loaded = stored
	e.username = stored.mirror.Username
	switch e.loaded.run.State {
	case "succeeded", "partial", "failed":
		return true, e.worker.complete(ctx, e.owner, e.job)
	}
	if e.loaded.run.MirrorID == 0 {
		return false, fmt.Errorf("load: run %q names no mirror", runPub)
	}
	return false, nil
}

func (e *execution) guard(ctx context.Context) (bool, error) {
	if !e.loaded.mirror.Enabled {
		return e.finish(ctx, runOutcome{
			runState: "failed", class: "disabled", summary: "mirror is disabled", mirror: "disabled",
		}, nil, nil)
	}
	cfg, cerr := mirrorConfigOf(e.loaded.mirror)
	if cerr != nil {
		return e.finish(ctx, runOutcome{
			runState: "failed", class: "policy", summary: cerr.Error(), mirror: "needs_attention",
		}, nil, nil)
	}
	if verr := cfg.Validate(e.start); verr != nil {
		return e.finish(ctx, runOutcome{
			runState: "failed", class: "policy",
			summary: e.scrub(verr.Error()),
			mirror:  "needs_attention",
		}, nil, nil)
	}
	e.cfg = cfg
	var cred store.CredentialRow
	var credMissing bool
	if rerr := e.worker.Store.Read(ctx, func(tx *store.ReadTx) error {
		row, gerr := tx.GetCredential(e.loaded.mirror.ID)
		if gerr != nil {
			if errors.Is(gerr, store.ErrNotFound) {
				credMissing = true
				return nil
			}
			return gerr
		}
		cred = row
		return nil
	}); rerr != nil {
		return false, fmt.Errorf("guard: %w", rerr)
	}
	if credMissing {
		return e.finish(ctx, runOutcome{
			runState: "failed", class: "secret",
			summary: "mirror credential is missing", mirror: "needs_attention",
		}, nil, nil)
	}
	keyVersion, kerr := CheckedUint32(cred.KeyVersion)
	if kerr != nil {
		return e.finish(ctx, runOutcome{
			runState: "failed", class: "secret",
			summary: "mirror credential version is not usable", mirror: "needs_attention",
		}, nil, nil)
	}
	token, oerr := Open(e.worker.SealKey, SealedCredential{
		KeyVersion: keyVersion,
		Nonce:      cred.Nonce,
		Ciphertext: cred.Ciphertext,
		Last4:      cred.Last4,
	}, credentialAAD(e.loaded.mirror.PublicID))
	if oerr != nil {
		return e.finish(ctx, runOutcome{
			runState: "failed", class: "secret",
			summary: "mirror credential cannot be decrypted", mirror: "needs_attention",
		}, nil, nil)
	}
	e.token = token
	if e.worker.Ruk == nil {
		delay := reconcileInterval(e.loaded.mirror)
		if delay <= 0 {
			delay = BaseBackoff
		}
		return e.finish(ctx, runOutcome{
			runState: "failed", class: "unavailable",
			summary: "mirror daemon client is unavailable", mirror: "failing",
			delay: delay,
		}, nil, nil)
	}
	if e.loaded.repo.RukPath == "" {
		return e.finish(ctx, runOutcome{
			runState: "failed", class: "policy",
			summary: "repository has no daemon path", mirror: "needs_attention",
		}, nil, nil)
	}
	auth, aerr := wire.AuthForMirror(cfg.Username, string(token), cfg.TargetURL)
	if aerr != nil {
		return e.finish(ctx, runOutcome{
			runState: "failed", class: "policy",
			summary: e.scrub(aerr.Error()), mirror: "needs_attention",
		}, nil, nil)
	}
	e.auth = auth
	repo, oerr := e.worker.Ruk.OpenRepo(ctx, daemonPath(e.worker.RepoRoot, e.loaded.repo.RukPath))
	if oerr != nil {
		return e.finishCall(ctx, oerr, nil, nil)
	}
	e.repo = repo
	return false, nil
}

func (e *execution) sync(ctx context.Context) (bool, error) {
	local, lerr := e.collectLocal(ctx)
	if lerr != nil {
		return e.finishCall(ctx, lerr, nil, nil)
	}
	remote, rerr := e.collectRemote(ctx)
	if rerr != nil {
		return e.finishCall(ctx, rerr, nil, nil)
	}
	e.remoteDefault = remote.defaultBranch
	if rerr := e.reconcilePriorUncertain(ctx, remote.refs); rerr != nil {
		return e.finishCall(ctx, rerr, nil, &remote.count)
	}
	plan, perr := ComputePlan(local, remote.refs, e.cfg, remote.defaultBranch, e.loaded.run.ConfirmedDigest)
	if perr != nil {
		if errors.Is(perr, ErrNeedsConfirmation) {
			count := remote.count
			return e.finish(ctx, runOutcome{
				runState: "partial", class: "needs_attention",
				summary: "plan digest " + plan.Digest + " needs confirmation (adopted_required)",
				mirror:  "needs_attention",
			}, &plan, &count)
		}
		return e.finishCall(ctx, perr, &plan, nil)
	}
	e.trackPlanned(plan)
	e.observed = true
	if perr := e.pushDeletesFirst(ctx); perr != nil {
		return e.finishCall(ctx, sendUncertain(perr), &plan, nil)
	}
	if perr := e.resolveUncertain(ctx); perr != nil {
		return e.finishCall(ctx, sendUncertain(perr), &plan, nil)
	}
	outcome := summarizeTracked(e.tracked)
	if countUncertain(e.tracked) > 0 {
		outcome = runOutcome{
			runState: "failed",
			class:    "uncertain",
			summary:  "some refs uncertain; will re-observe",
			mirror:   "failing",
			delay:    BackoffDelay(int(e.loaded.mirror.ConsecutiveFailures)+1, BaseBackoff, MaxBackoff, jitterFraction()),
		}
	}
	count := remote.count
	return e.finish(ctx, outcome, &plan, &count)
}

func (e *execution) collectLocal(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	var after *string
	for {
		reply, rerr := ruk.RefsList(ctx, e.worker.Ruk, e.repo, ruk.RefsListArgs{After: after, Limit: ruk.MaxRefsLimit})
		if rerr != nil {
			return nil, fmt.Errorf("collectLocal: %w", rerr)
		}
		for _, entry := range reply.Refs.Items {
			if entry.Target.Direct == nil {
				continue
			}
			out[entry.Name] = *entry.Target.Direct
		}
		if reply.Refs.NextAfter == nil || *reply.Refs.NextAfter == "" {
			return out, nil
		}
		cursor := *reply.Refs.NextAfter
		after = &cursor
	}
}

type remoteSnapshot struct {
	refs          map[string]string
	defaultBranch string
	count         int
}

func (e *execution) collectRemote(ctx context.Context) (remoteSnapshot, error) {
	reply, rerr := ruk.RemoteRefs(ctx, e.worker.Ruk, e.repo, ruk.RemoteRefsArgs{URL: e.cfg.TargetURL}, e.auth)
	if rerr != nil {
		return remoteSnapshot{}, fmt.Errorf("collectRemote: %w", rerr)
	}
	refs := make(map[string]string, len(reply.Refs))
	for _, entry := range reply.Refs {
		if entry.Target.Direct == nil {
			continue
		}
		refs[entry.Name] = *entry.Target.Direct
	}
	return remoteSnapshot{refs: refs, count: len(reply.Refs), defaultBranch: remoteDefaultBranch(reply.Refs)}, nil
}

func remoteDefaultBranch(entries []ruk.RefEntry) string {
	for _, entry := range entries {
		if entry.Name == "HEAD" && entry.Target.Symbolic != nil {
			if branch, ok := strings.CutPrefix(*entry.Target.Symbolic, "refs/heads/"); ok && branch != "" {
				return branch
			}
		}
	}
	return "main"
}

func (e *execution) trackPlanned(plan Plan) {
	add := func(rp RefPlan, status, class, reason string) {
		e.tracked = append(e.tracked, &trackedRef{plan: rp, status: status, class: class, reason: reason})
	}
	for _, rp := range plan.Ignored {
		add(rp, "ignored_hidden", string(ClassRefHidden), rp.Reason)
	}
	for _, rp := range plan.Diverged {
		add(rp, "diverged", string(ClassDiverged), orDefault(rp.Reason, "remote diverged"))
	}
	for _, rp := range plan.Updates {
		switch rp.Action {
		case ActionInSync:
			add(rp, "in_sync", string(ClassOK), "")
		case ActionBlockedDF:
			add(rp, "blocked_df", string(ClassRefOther), orDefault(rp.Reason, "file/directory conflict"))
		case ActionCreate, ActionForceUpdate:
			e.tracked = append(e.tracked, &trackedRef{plan: rp})
		case ActionUpdate, ActionDelete, ActionPrune, ActionDiverged, ActionIgnoredHidden:
			add(rp, "blocked_df", string(ClassRefOther), orDefault(rp.Reason, "unsupported plan action"))
		default:
			add(rp, "blocked_df", string(ClassRefOther), orDefault(rp.Reason, "unsupported plan action"))
		}
	}
	for _, rp := range plan.Deletes {
		e.tracked = append(e.tracked, &trackedRef{plan: rp})
	}
	byRef := make(map[string]*trackedRef, len(e.tracked))
	for _, t := range e.tracked {
		byRef[t.plan.Ref] = t
	}
	for ref, reason := range e.priorDiverged {
		if t, ok := byRef[ref]; ok {
			t.status = "diverged"
			t.class = string(ClassDiverged)
			t.reason = reason
			continue
		}
		e.tracked = append(e.tracked, &trackedRef{
			plan:   RefPlan{Ref: ref, Action: ActionDiverged, Reason: reason},
			status: "diverged",
			class:  string(ClassDiverged),
			reason: reason,
		})
	}
}

func (e *execution) reconcilePriorUncertain(ctx context.Context, remote map[string]string) error {
	var rows []store.RefStateRow
	if rerr := e.worker.Store.Read(ctx, func(tx *store.ReadTx) error {
		after := ""
		for {
			page, next, lerr := tx.ListRefStates(e.loaded.mirror.ID, after, 500)
			if lerr != nil {
				return lerr
			}
			rows = append(rows, page...)
			if next == "" {
				return nil
			}
			after = next
		}
	}); rerr != nil {
		return fmt.Errorf("reconcilePriorUncertain: %w", rerr)
	}
	e.priorDiverged = map[string]string{}
	for _, row := range rows {
		if row.Status != "uncertain" {
			continue
		}
		observed, ok := remote[row.Ref]
		want := row.LocalOid
		switch {
		case !ok && want == "":
		case ok && want != "" && observed == want:
		case (ok && observed == row.RemoteOid) || (!ok && want != ""):
		default:
			e.priorDiverged[row.Ref] = "remote moved since the uncertain push"
		}
	}
	return nil
}

func orDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func updateFor(rp RefPlan, force bool) ruk.PushUpdate {
	switch rp.Action {
	case ActionDelete, ActionPrune:
		expect := ruk.PresentExpect(rp.Old)
		return ruk.PushUpdate{RemoteRef: rp.Ref, Source: ruk.DeleteSource(), ExpectedOld: &expect}
	case ActionForceUpdate:
		expect := ruk.PresentExpect(rp.Old)
		return ruk.PushUpdate{RemoteRef: rp.Ref, Source: ruk.CommitSource(rp.New), ExpectedOld: &expect, Force: force}
	case ActionCreate, ActionUpdate:
		absent := ruk.AbsentExpect()
		return ruk.PushUpdate{RemoteRef: rp.Ref, Source: ruk.CommitSource(rp.New), ExpectedOld: &absent}
	case ActionDiverged, ActionIgnoredHidden, ActionBlockedDF, ActionInSync:
		absent := ruk.AbsentExpect()
		return ruk.PushUpdate{RemoteRef: rp.Ref, Source: ruk.CommitSource(rp.New), ExpectedOld: &absent}
	default:
		absent := ruk.AbsentExpect()
		return ruk.PushUpdate{RemoteRef: rp.Ref, Source: ruk.CommitSource(rp.New), ExpectedOld: &absent}
	}
}

func updateForExact(rp RefPlan) ruk.PushUpdate {
	return updateFor(rp, true)
}

func jitterFraction() float64 {
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return 1
	}
	const unit = 1 << 20
	return float64(binary.BigEndian.Uint64(b[:])%unit) / unit
}

func mirrorRequestID(runPub string, attempt int64, seq int) string {
	sum := sha256.Sum256([]byte("mirror-push\x00" + runPub + "\x00" + strconv.FormatInt(attempt, 10) + "\x00" + strconv.Itoa(seq)))
	sum[6] = sum[6]&0x0f | 0x50
	sum[8] = sum[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

func forceCheckedFor(cfg Mirror, ref, defaultBranch string) bool {
	return cfg.ProtectDefaultBranch && defaultBranch != "" && ref == "refs/heads/"+defaultBranch
}

func updateForChecked(rp RefPlan) ruk.PushUpdate {
	return updateFor(rp, false)
}

func (e *execution) pushDeletesFirst(ctx context.Context) error {
	var deletes, upserts []ruk.PushUpdate
	var deleteRefs, upsertRefs []*trackedRef
	for _, t := range e.tracked {
		if t.status != "" {
			continue
		}
		switch t.plan.Action {
		case ActionDelete, ActionPrune:
			deletes = append(deletes, updateForChecked(t.plan))
			deleteRefs = append(deleteRefs, t)
		case ActionCreate, ActionForceUpdate:
			if e.cfg.Mode == ModeExact && !forceCheckedFor(e.cfg, t.plan.Ref, e.remoteDefault) {
				upserts = append(upserts, updateForExact(t.plan))
			} else {
				upserts = append(upserts, updateForChecked(t.plan))
			}
			upsertRefs = append(upsertRefs, t)
		case ActionUpdate, ActionDiverged, ActionIgnoredHidden, ActionBlockedDF, ActionInSync:
			t.status = "blocked_df"
			t.class = string(ClassRefOther)
			t.reason = orDefault(t.reason, "unsupported plan action")
		default:
			t.status = "blocked_df"
			t.class = string(ClassRefOther)
			t.reason = orDefault(t.reason, "unsupported plan action")
		}
	}
	seq := 0
	push := func(updates []ruk.PushUpdate, refs []*trackedRef) error {
		if len(updates) == 0 {
			return nil
		}
		seq++
		id := mirrorRequestID(e.loaded.run.PublicID, e.loaded.run.AttemptNo+1, seq)
		reply, perr := ruk.RetryAuth[ruk.PushArgs, ruk.PushReply](ctx, e.worker.Ruk, e.repo, wire.MethodPush, id, ruk.PushArgs{RemoteURL: e.cfg.TargetURL, Updates: updates}, e.auth)
		if perr != nil {
			return perr
		}
		e.plans = append(e.plans, reply.Plan)
		byRef := make(map[string]ruk.PushRefOutcome, len(reply.Refs))
		for _, outcome := range reply.Refs {
			byRef[outcome.RemoteRef] = outcome
		}
		for _, t := range refs {
			outcome, ok := byRef[t.plan.Ref]
			if !ok {
				t.status = "uncertain"
				t.class = string(ClassUncertain)
				t.reason = "no outcome reported"
				t.planID = reply.Plan
				t.requestID = id
				continue
			}
			switch outcome.Status.Kind {
			case "confirmed":
				t.confirmed = outcome.Status.New
				if outcome.Status.New == nil {
					t.status = "deleted"
				} else {
					t.status = "ok"
				}
				t.class = string(ClassOK)
				t.reason = ""
			case "rejected":
				t.status = "rejected"
				t.class = string(Classify("", 0, "", outcome.Status.Reason).Class)
				if t.class == string(ClassOK) {
					t.class = string(ClassRefOther)
				}
				t.reason = outcome.Status.Reason
			default:
				t.status = "uncertain"
				t.class = string(ClassUncertain)
				t.reason = "remote outcome unknown"
				t.planID = reply.Plan
				t.requestID = id
			}
		}
		return nil
	}
	if perr := push(deletes, deleteRefs); perr != nil {
		return perr
	}
	return push(upserts, upsertRefs)
}

func (e *execution) resolveUncertain(ctx context.Context) error {
	byPlan := map[string][]*trackedRef{}
	for _, t := range e.tracked {
		if t.status == "uncertain" {
			byPlan[t.planID] = append(byPlan[t.planID], t)
		}
	}
	for planID, refs := range byPlan {
		reply, rerr := ruk.CallAuth[ruk.PushReconcileArgs, ruk.PushReconcileReply](ctx, e.worker.Ruk, e.repo, wire.MethodPushReconcile, ruk.PushReconcileArgs{Plan: planID}, e.auth)
		if rerr != nil {
			return rerr
		}
		observed := make(map[string]ruk.ReconcileObservation, len(reply.Observations))
		for _, ob := range reply.Observations {
			observed[ob.RemoteRef] = ob
		}
		var resend []*trackedRef
		for _, t := range refs {
			ob, ok := observed[t.plan.Ref]
			if !ok {
				continue
			}
			switch ob.Verdict {
			case "equal_new":
				t.confirmed = ob.Observed
				if t.plan.Action == ActionDelete || t.plan.Action == ActionPrune {
					if ob.Observed == nil {
						t.status = "deleted"
					} else {
						t.status = "diverged"
						t.class = string(ClassDiverged)
						t.reason = "remote moved"
						continue
					}
				} else {
					t.status = "ok"
				}
				t.class = string(ClassOK)
				t.reason = ""
			case "equal_old":
				resend = append(resend, t)
			case "moved":
				t.status = "diverged"
				t.class = string(ClassDiverged)
				t.reason = "remote moved"
			}
		}
		if len(resend) > 0 {
			if rerr := e.resendOnce(ctx, resend[0].requestID, resend); rerr != nil {
				return rerr
			}
		}
	}
	return nil
}

func (e *execution) resendOnce(ctx context.Context, requestID string, refs []*trackedRef) error {
	updates := make([]ruk.PushUpdate, 0, len(refs))
	for _, t := range refs {
		if e.cfg.Mode == ModeExact {
			updates = append(updates, updateForExact(t.plan))
		} else {
			updates = append(updates, updateForChecked(t.plan))
		}
	}
	reply, rerr := ruk.RetryAuth[ruk.PushArgs, ruk.PushReply](ctx, e.worker.Ruk, e.repo, wire.MethodPush, requestID, ruk.PushArgs{RemoteURL: e.cfg.TargetURL, Updates: updates}, e.auth)
	if rerr != nil {
		return rerr
	}
	byRef := make(map[string]ruk.PushRefOutcome, len(reply.Refs))
	for _, outcome := range reply.Refs {
		byRef[outcome.RemoteRef] = outcome
	}
	for _, t := range refs {
		outcome, ok := byRef[t.plan.Ref]
		if !ok {
			continue
		}
		switch outcome.Status.Kind {
		case "confirmed":
			t.confirmed = outcome.Status.New
			if outcome.Status.New == nil {
				t.status = "deleted"
			} else {
				t.status = "ok"
			}
			t.class = string(ClassOK)
			t.reason = ""
		case "rejected":
			t.status = "rejected"
			t.class = string(Classify("", 0, "", outcome.Status.Reason).Class)
			if t.class == string(ClassOK) {
				t.class = string(ClassRefOther)
			}
			t.reason = outcome.Status.Reason
		}
	}
	return nil
}

func countUncertain(tracked []*trackedRef) int {
	var uncertain int
	for _, t := range tracked {
		if t.status == "uncertain" {
			uncertain++
		}
	}
	return uncertain
}

func summarizeTracked(tracked []*trackedRef) runOutcome {
	var rejected, diverged int
	for _, t := range tracked {
		switch t.status {
		case "rejected":
			rejected++
		case "diverged":
			diverged++
		}
	}
	if rejected > 0 || diverged > 0 {
		return runOutcome{runState: "partial", class: "partial", summary: "some refs were not synced", mirror: "partial"}
	}
	return runOutcome{runState: "succeeded", class: "ok", summary: "all refs synced", mirror: "ok"}
}

func (e *execution) finishCall(ctx context.Context, callErr error, plan *Plan, remoteCount *int) (bool, error) {
	if outcome, ok := sendUncertainOutcome(callErr); ok {
		outcome.summary = e.scrub(callErr.Error())
		outcome.delay = BackoffDelay(int(e.loaded.mirror.ConsecutiveFailures)+1, BaseBackoff, MaxBackoff, jitterFraction())
		return e.finish(ctx, outcome, plan, remoteCount)
	}
	code, detail, reason := describeCallError(callErr)
	verdict := Classify(code, int(detail.Status), retryAfterString(detail), reason)
	if (detail.Status == 401 || detail.Status == 403) && verdict.Class != ClassRateLimited {
		verdict = Verdict{Class: ClassAuth, Retry: false, State: "auth_failed"}
	}
	summary := e.scrub(callErr.Error())
	consecutive := int(e.loaded.mirror.ConsecutiveFailures)
	switch verdict.Class {
	case ClassAuth, ClassRepoMoved, ClassTooLarge, ClassPolicy, ClassSecret, ClassRefPolicy:
		return e.finish(ctx, runOutcome{
			runState: "failed", class: string(verdict.Class), summary: summary,
			mirror: verdict.State, http: int64(detail.Status),
		}, plan, remoteCount)
	case ClassRateLimited:
		delay := RateLimitDelay(retryAfterDuration(detail), retryAfterPresent(detail), jitterFraction())
		return e.finish(ctx, runOutcome{
			runState: "failing", class: string(verdict.Class), summary: summary,
			mirror: "failing", http: int64(detail.Status), delay: delay, requeue: true,
		}, plan, remoteCount)
	case ClassRepoMissing, ClassServer, ClassNetwork, ClassUncertain, ClassRefHidden,
		ClassRefProtected, ClassRefHook, ClassRefOther, ClassProtocol, ClassDiverged,
		ClassBusy, ClassOK:
	}
	retry, state := ShouldRetry(verdict, consecutive, protectedRepeat(e.loaded.mirror, verdict))
	if !retry {
		return e.finish(ctx, runOutcome{
			runState: "failed", class: string(verdict.Class), summary: summary,
			mirror: state, http: int64(detail.Status),
		}, plan, remoteCount)
	}
	return e.finish(ctx, runOutcome{
		runState: "failed", class: string(verdict.Class), summary: summary,
		mirror: "failing", http: int64(detail.Status),
		delay: BackoffDelay(consecutive+1, BaseBackoff, MaxBackoff, jitterFraction()),
	}, plan, remoteCount)
}

func protectedRepeat(row store.MirrorRow, verdict Verdict) int {
	if verdict.Class == ClassRefProtected && row.LastErrorClass == string(ClassRefProtected) {
		return int(row.ConsecutiveFailures)
	}
	return 0
}

type sendUncertainError struct{ err error }

func (e *sendUncertainError) Error() string { return e.err.Error() }

func (e *sendUncertainError) Unwrap() error { return e.err }

func sendUncertain(err error) error {
	if err == nil {
		return nil
	}
	return &sendUncertainError{err: err}
}

func sendUncertainOutcome(callErr error) (runOutcome, bool) {
	if _, ok := errors.AsType[*sendUncertainError](callErr); !ok {
		return runOutcome{}, false
	}
	if _, ok := errors.AsType[*ruk.Error](callErr); ok {
		return runOutcome{}, false
	}
	return runOutcome{
		runState: "failed", class: string(ClassUncertain),
		summary: "push outcome unknown; will re-observe", mirror: "failing",
	}, true
}

func describeCallError(err error) (string, ruk.HTTPDetail, string) {
	var detail ruk.HTTPDetail
	callErr, ok := errors.AsType[*ruk.Error](err)
	if !ok || callErr == nil {
		return "", detail, err.Error()
	}
	detail, _ = ruk.HTTPDetailOf(err)
	return string(callErr.Code), detail, callErr.Message
}

func retryAfterString(detail ruk.HTTPDetail) string {
	if detail.RetryAfterSeconds == nil {
		return ""
	}
	return strconv.FormatUint(*detail.RetryAfterSeconds, 10)
}

func retryAfterPresent(detail ruk.HTTPDetail) bool {
	return detail.RetryAfterSeconds != nil
}

func retryAfterDuration(detail ruk.HTTPDetail) time.Duration {
	if detail.RetryAfterSeconds == nil {
		return 0
	}
	d, err := durationSeconds(*detail.RetryAfterSeconds)
	if err != nil {
		return RateLimitMax
	}
	return d
}

func durationSeconds(s uint64) (time.Duration, error) {
	d, err := time.ParseDuration(strconv.FormatUint(s, 10) + "s")
	if err != nil {
		return 0, fmt.Errorf("mirror schedule: invalid second count: %w", err)
	}
	return d, nil
}

func (e *execution) finish(ctx context.Context, outcome runOutcome, plan *Plan, remoteCount *int) (bool, error) {
	now := e.worker.now()
	endMs := now.UnixMilli()
	for _, t := range e.tracked {
		t.reason = truncateMiddle(e.scrub(t.reason), maxStoredErrorLen)
	}
	summary := truncateMiddle(e.scrub(outcome.summary), maxStoredErrorLen)
	countsJSON := countsOf(e.tracked)
	attemptNo := e.loaded.run.AttemptNo + 1
	run := e.loaded.run
	run.State = outcome.runState
	run.OutcomeClass = outcome.class
	run.OutcomeSummary = summary
	run.Counts = countsJSON
	run.AttemptNo = attemptNo
	run.StartedAt = e.start.UnixMilli()
	run.FinishedAt = endMs
	if plan != nil {
		run.PlanDigest = plan.Digest
	}
	if len(e.plans) > 0 {
		run.RukPlan = strings.Join(e.plans, ",")
	}
	mirrorRow := e.loaded.mirror
	consecutive := mirrorRow.ConsecutiveFailures
	if outcome.mirror == "ok" {
		consecutive = 0
	} else {
		consecutive++
	}
	nextRunAt := endMs
	adopted := false
	if outcome.mirror == "ok" || outcome.mirror == "partial" {
		if iv := reconcileInterval(mirrorRow); iv > 0 {
			nextRunAt = endMs + iv.Milliseconds()
		} else {
			nextRunAt = 0
		}
	}
	if outcome.delay > 0 {
		nextRunAt = endMs + outcome.delay.Milliseconds()
	}
	if outcome.mirror == "needs_attention" || outcome.mirror == "disabled" || outcome.mirror == "auth_failed" {
		nextRunAt = 0
	}
	eventCursor := mirrorRow.EventCursor
	if remoteCount != nil {
		eventCursor = int64(*remoteCount)
	}
	confirmed := plan != nil && e.loaded.run.ConfirmedDigest != "" && e.loaded.run.ConfirmedDigest == plan.Digest
	if plan != nil && plan.TargetNonEmpty && (e.cfg.AdoptExistingRefs || confirmed) {
		adopted = true
	}
	result := store.MirrorResult{
		State:               outcome.mirror,
		ConsecutiveFailures: consecutive,
		LastHTTPStatus:      outcome.http,
		LastAttemptAt:       endMs,
		LastSuccessAt:       mirrorRow.LastSuccessAt,
		NextRunAt:           nextRunAt,
		EventCursor:         eventCursor,
		LastErrorClass:      outcome.class,
		LastError:           summary,
		Adopted:             adopted,
		ClearDirty:          outcome.mirror == "ok" || outcome.mirror == "partial" || outcome.mirror == "needs_attention" || outcome.mirror == "disabled" || outcome.mirror == "auth_failed",
	}
	if outcome.mirror == "ok" {
		result.LastSuccessAt = endMs
	}
	attempt := store.AttemptRow{
		RunID:      run.ID,
		AttemptNo:  attemptNo,
		RukPlan:    run.RukPlan,
		Outcome:    outcome.class,
		Summary:    summary,
		StartedAt:  e.start.UnixMilli(),
		FinishedAt: endMs,
	}
	refStates := refStatesOf(mirrorRow.ID, endMs, e.tracked)
	outboxBody, berr := mirrorSyncBody(mirrorRow.ID, e.job.ID, outcome.mirror, e.tracked, e.scrub)
	if berr != nil {
		return false, fmt.Errorf("finish: %w", berr)
	}
	if e.token != nil {
		Zero(e.token)
		e.token = nil
	}
	if werr := e.worker.Store.Write(ctx, func(tx *store.WriteTx) error {
		if uerr := tx.UpdateRun(now, run.ID, run); uerr != nil {
			return uerr
		}
		if _, ierr := tx.InsertAttempt(attempt); ierr != nil {
			return ierr
		}
		if len(refStates) > 0 || e.observed {
			if rerr := tx.ReplaceRefStates(mirrorRow.ID, refStates); rerr != nil {
				return rerr
			}
		}
		if serr := tx.SetMirrorResult(now, mirrorRow.ID, result); serr != nil {
			return serr
		}
		_, eerr := tx.Outbox().Emit(store.Event{
			OccurredAt: now,
			Kind:       outboxKindMirrorSyncCompleted,
			Body:       outboxBody,
		})
		return eerr
	}); werr != nil {
		return false, fmt.Errorf("finish: %w", werr)
	}
	if outcome.requeue {
		if rerr := e.requeue(ctx, now, run, attemptNo+1, outcome.delay); rerr != nil {
			return false, rerr
		}
	}
	return true, e.worker.complete(ctx, e.owner, e.job)
}

func (e *execution) scrub(s string) string {
	if e.token != nil {
		return scrubSecrets(s, e.username, string(e.token))
	}
	return scrubSecrets(s, e.username)
}

func (e *execution) requeue(ctx context.Context, now time.Time, run store.MirrorRunRow, nextAttempt int64, delay time.Duration) error {
	pub := run.PublicID
	mirrorID := run.MirrorID
	payload, merr := jsonv2.Marshal(jobPayload{Run: &pub, Mirror: &mirrorID}, jsonv1.DefaultOptionsV1())
	if merr != nil {
		return fmt.Errorf("requeue: %w", merr)
	}
	at := now.Add(delay)
	if werr := e.worker.Store.Write(ctx, func(tx *store.WriteTx) error {
		_, eerr := tx.EnqueueJob(store.JobSpec{
			Payload: string(payload),
			ID:      "mirror-run:" + pub + ":" + strconv.FormatInt(nextAttempt, 10),
			Kind:    string(queue.KindMirrorRun),
			RepoID:  e.loaded.mirror.RepoID,
			At:      at,
		})
		return eerr
	}); werr != nil {
		return fmt.Errorf("requeue: %w", werr)
	}
	updated := run
	updated.AttemptNo = nextAttempt
	if werr := e.worker.Store.Write(ctx, func(tx *store.WriteTx) error {
		return tx.UpdateRun(now, run.ID, updated)
	}); werr != nil {
		return fmt.Errorf("requeue: %w", werr)
	}
	return nil
}

func refStatesOf(mirrorID, nowMs int64, tracked []*trackedRef) []store.RefStateRow {
	out := make([]store.RefStateRow, 0, len(tracked))
	for _, t := range tracked {
		row := store.RefStateRow{
			MirrorID:      mirrorID,
			Ref:           t.plan.Ref,
			LocalOid:      t.plan.New,
			RemoteOid:     t.plan.Old,
			Status:        t.status,
			Reason:        t.reason,
			ReasonClass:   t.class,
			FirstFailedAt: nowMs,
			LastAttemptAt: nowMs,
		}
		if t.confirmed != nil {
			row.RemoteOid = *t.confirmed
		}
		if t.status == "ok" || t.status == "in_sync" || t.status == "deleted" {
			row.FirstFailedAt = 0
			row.ConsecutiveFailures = 0
			row.LastSuccessAt = nowMs
		} else {
			row.ConsecutiveFailures = 1
		}
		out = append(out, row)
	}
	return out
}

func countsOf(tracked []*trackedRef) string {
	var counts syncCounts
	for _, t := range tracked {
		switch t.status {
		case "ok", "in_sync":
			counts.OK++
		case "rejected":
			counts.Rejected++
		case "diverged":
			counts.Diverged++
		case "deleted":
			counts.Deleted++
		}
	}
	raw, _ := jsonv2.Marshal(counts, jsonv1.DefaultOptionsV1())
	return string(raw)
}

type syncCompletedBody struct {
	JobID    string     `json:"job_id"`
	State    string     `json:"state"`
	Errors   []syncErr  `json:"errors"`
	MirrorID int64      `json:"mirror_id"`
	Counts   syncCounts `json:"counts"`
}

type syncCounts struct {
	OK       int `json:"ok"`
	Rejected int `json:"rejected"`
	Diverged int `json:"diverged"`
	Deleted  int `json:"deleted"`
}

type syncErr struct {
	Ref     string `json:"ref"`
	Class   string `json:"class"`
	Message string `json:"message"`
}

func mirrorSyncBody(mirrorID int64, jobID, state string, tracked []*trackedRef, scrub func(string) string) (jsontext.Value, error) {
	var counts syncCounts
	errs := []syncErr{}
	for _, t := range tracked {
		switch t.status {
		case "ok", "in_sync":
			counts.OK++
		case "rejected":
			counts.Rejected++
		case "diverged":
			counts.Diverged++
		case "deleted":
			counts.Deleted++
		}
		if t.status == "rejected" || t.status == "diverged" || t.status == "uncertain" {
			errs = append(errs, syncErr{
				Ref:     t.plan.Ref,
				Class:   t.class,
				Message: truncateMiddle(scrub(t.reason), maxStoredErrorLen),
			})
		}
	}
	raw, merr := jsonv2.Marshal(syncCompletedBody{
		MirrorID: mirrorID,
		JobID:    jobID,
		State:    state,
		Counts:   counts,
		Errors:   errs,
	}, jsonv1.DefaultOptionsV1())
	if merr != nil {
		return nil, fmt.Errorf("mirrorSyncBody: %w", merr)
	}
	return jsontext.Value(raw), nil
}

func truncateMiddle(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

func scrubSecrets(s, username string, secrets ...string) string {
	out := s
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		out = strings.ReplaceAll(out, secret, "[REDACTED]")
		if username != "" {
			encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + secret))
			out = strings.ReplaceAll(out, encoded, "[REDACTED]")
		}
	}
	return out
}

func mirrorConfigOf(row store.MirrorRow) (Mirror, error) {
	globs, gerr := parseGlobs(row.Globs)
	if gerr != nil {
		return Mirror{}, fmt.Errorf("mirrorConfigOf: %w", gerr)
	}
	cfg := Mirror{
		Name:                     row.Name,
		TargetURL:                row.TargetURL,
		HostKind:                 HostKind(row.HostKind),
		Username:                 row.Username,
		Mode:                     Mode(row.Mode),
		RefSelection:             RefSelection(row.RefSelection),
		Globs:                    globs,
		IncludeNotes:             row.IncludeNotes,
		ProtectDefaultBranch:     row.ProtectDefaultBranch,
		Enabled:                  row.Enabled,
		SyncOnRefUpdate:          row.SyncOnRefUpdate,
		DebounceSeconds:          row.DebounceSeconds,
		MaxDelaySeconds:          row.MaxDelaySeconds,
		ReconcileIntervalSeconds: row.ReconcileIntervalSeconds,
		CreateIfMissing:          row.CreateIfMissing,
		PolicyAck:                row.PolicyAck,
		AdoptExistingRefs:        row.AdoptExistingRefs,
	}
	return cfg, nil
}

func reconcileInterval(row store.MirrorRow) time.Duration {
	d, err := durationSeconds(row.ReconcileIntervalSeconds)
	if err != nil {
		return 0
	}
	return d
}

func parseGlobs(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	var globs []string
	if uerr := jsonv2.Unmarshal([]byte(trimmed), &globs, jsonv1.DefaultOptionsV1()); uerr == nil {
		return globs, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		return nil, fmt.Errorf("parseGlobs: invalid glob list")
	}
	return []string{trimmed}, nil
}
