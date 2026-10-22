package api

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"strconv"
	"strings"

	jsonv1 "encoding/json"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/ci"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

func (d Deps) ciController() (*ci.Controller, error) {
	if d.CI == nil {
		return nil, op.Wrap(op.CodeInternal, errCIUnconfigured, "CI control plane is not configured")
	}
	return d.CI, nil
}

var errCIUnconfigured = op.Errorf(op.CodeInternal, "CI control plane is not configured")

func ciToOp(err error, notFound string) error {
	if err == nil {
		return nil
	}
	switch ci.CodeOf(err) {
	case "E_RECOVERING", "E_CAPACITY", "E_OFFLINE":
		return op.Wrap(op.CodeUnavailable, err, "CI control plane is not ready")
	case "E_LEASE_LOST":
		return op.Wrap(op.CodeLeaseConflict, err, "attempt fence is stale")
	case "E_NOT_STARTED", "E_RESULT_CONFLICT", "E_LOG_INCOMPLETE", "E_BLOB_MISSING", "E_OFFSET_GAP", "E_SEQ_CONFLICT", "E_CONFLICT":
		return op.Wrap(op.CodeConflict, err, "CI state conflict")
	case "E_DIGEST_MISMATCH":
		return op.Wrap(op.CodeValidation, err, "digest mismatch")
	case "E_NOT_FOUND":
		return op.Errorf(op.CodeNotFound, "%s", notFound)
	case "E_UNAUTHENTICATED":
		return op.Wrap(op.CodeUnauthorized, err, "CI authentication failed")
	case "E_LIMIT":
		return op.Wrap(op.CodeLimitExceeded, err, "CI limit exceeded")
	case "E_CORRUPT":
		return op.Wrap(op.CodeCorrupt, err, "CI state is corrupt")
	case "E_FORBIDDEN", "E_POLICY":
		return op.Wrap(op.CodeForbidden, err, "CI access was refused")
	case "E_OBJECT_FORMAT":
		return op.Wrap(op.CodeUnsupported, err, "CI object format is unsupported")
	default:
		return storeToOp(err, notFound)
	}
}

type CIRunJSON struct {
	Class        string   `json:"class"`
	State        string   `json:"state"`
	Tree         string   `json:"tree"`
	Trust        string   `json:"trust"`
	TriggerKey   string   `json:"trigger_key"`
	Group        string   `json:"group"`
	Subject      string   `json:"subject"`
	CancelReason string   `json:"cancel_reason,omitempty"`
	ID           string   `json:"id"`
	Jobs         []string `json:"jobs"`
	CreatedAt    int64    `json:"created_at"`
	FinishedAt   int64    `json:"finished_at,omitempty"`
	Passed       int      `json:"passed"`
	Failed       int      `json:"failed"`
	Total        int      `json:"total"`
}

type CIJobJSON struct {
	ID           string   `json:"id"`
	Run          string   `json:"run"`
	Name         string   `json:"name"`
	SpecDigest   string   `json:"spec_digest"`
	ActionDigest string   `json:"action_digest,omitempty"`
	State        string   `json:"state"`
	WaitReason   string   `json:"wait_reason,omitempty"`
	Class        string   `json:"class"`
	RunsOn       []string `json:"runs_on"`
	ReadyAt      int64    `json:"ready_at"`
	AttemptsUsed int      `json:"attempts_used"`
	CurrentFence int64    `json:"current_fence,omitempty"`
}

func ciRunJSON(run ci.Run, jobs []ci.Job) CIRunJSON {
	out := CIRunJSON{
		ID: run.ID, Subject: run.Subject, Tree: run.Tree, Trust: run.Trust,
		TriggerKey: run.TriggerKey, Group: run.Group, Class: run.Class, State: run.State,
		Jobs: run.Jobs, CancelReason: run.CancelReason, CreatedAt: run.CreatedAt, FinishedAt: run.FinishedAt,
	}
	for _, job := range jobs {
		switch job.State {
		case "passed", "cached", "carried":
			out.Passed++
		case "failed":
			out.Failed++
		}
	}
	out.Total = len(jobs)
	return out
}

func ciJobJSON(run ci.Run, job ci.Job) CIJobJSON {
	return CIJobJSON{
		ID: job.ID, Run: job.RunID, Name: job.Spec.Name, SpecDigest: job.SpecDigest,
		ActionDigest: job.ActionDigest, State: job.State, WaitReason: job.WaitReason,
		RunsOn: job.Spec.RunsOn, Class: run.Class, ReadyAt: job.ReadyAt,
		AttemptsUsed: job.AttemptsUsed, CurrentFence: job.Fence,
	}
}

func ciCursorOffset(cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	offset, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil || offset < 0 {
		return 0, op.Errorf(op.CodeInvalid, "cursor is not valid")
	}
	return offset, nil
}

func ciNextCursor(offset, limit int64, more bool) string {
	if !more {
		return ""
	}
	return strconv.FormatInt(offset+limit, 10)
}

type CIRunListIn struct {
	Owner   string `path:"owner"`
	Repo    string `path:"repo"`
	State   string `query:"state"`
	Subject string `query:"subject"`
	Change  string `query:"change"`
	Ref     string `query:"ref"`
	Trigger string `query:"trigger"`
	Cursor  string `query:"cursor"`
	Limit   int    `query:"limit"`
}

type CIRunListOut struct {
	Next string      `json:"-"`
	Runs []CIRunJSON `json:"runs"`
}

func ciRunListHandler(d Deps) func(context.Context, op.Principal, CIRunListIn) (CIRunListOut, error) {
	return func(ctx context.Context, p op.Principal, in CIRunListIn) (CIRunListOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return CIRunListOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return CIRunListOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "ci.view", ""); derr != nil {
			return CIRunListOut{}, derr
		}
		ctrl, cerr := d.ciController()
		if cerr != nil {
			return CIRunListOut{}, cerr
		}
		offset, oerr := ciCursorOffset(in.Cursor)
		if oerr != nil {
			return CIRunListOut{}, oerr
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 100
		}
		if limit > 1000 {
			return CIRunListOut{}, op.Errorf(op.CodeInvalid, "limit must not exceed 1000")
		}
		runs, rerr := ctrl.Runs(ctx, rc.row.ID)
		if rerr != nil {
			return CIRunListOut{}, ciToOp(rerr, "CI runs were not found")
		}
		out := CIRunListOut{Runs: []CIRunJSON{}}
		var matched []ci.Run
		for _, run := range runs {
			if in.State != "" && run.State != in.State {
				continue
			}
			if in.Subject != "" && run.Subject != in.Subject {
				continue
			}
			if in.Trigger != "" && run.Class != in.Trigger {
				continue
			}
			matched = append(matched, run)
		}
		slices.SortFunc(matched, func(a, b ci.Run) int { return strings.Compare(a.ID, b.ID) })
		for i := offset; i < int64(len(matched)) && len(out.Runs) < limit; i++ {
			run := matched[i]
			_, jobs, jerr := ctrl.Run(ctx, run.ID)
			if jerr != nil {
				return CIRunListOut{}, ciToOp(jerr, "CI run was not found")
			}
			out.Runs = append(out.Runs, ciRunJSON(run, jobs))
		}
		out.Next = ciNextCursor(offset, int64(limit), offset+int64(limit) < int64(len(matched)))
		return out, nil
	}
}

type CIRunGetIn struct {
	Owner string `path:"owner"`
	Repo  string `path:"repo"`
	Run   string `path:"run"`
}

type CIRunGetOut struct {
	ETag string      `json:"-"`
	Jobs []CIJobJSON `json:"jobs"`
	Run  CIRunJSON   `json:"run"`
}

func ciRunGetHandler(d Deps) func(context.Context, op.Principal, CIRunGetIn) (CIRunGetOut, error) {
	return func(ctx context.Context, p op.Principal, in CIRunGetIn) (CIRunGetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return CIRunGetOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return CIRunGetOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "ci.view", ""); derr != nil {
			return CIRunGetOut{}, derr
		}
		ctrl, cerr := d.ciController()
		if cerr != nil {
			return CIRunGetOut{}, cerr
		}
		run, jobs, rerr := ctrl.Run(ctx, in.Run)
		if rerr != nil {
			return CIRunGetOut{}, ciToOp(rerr, "CI run was not found")
		}
		if run.RepoID != rc.row.ID {
			return CIRunGetOut{}, op.Errorf(op.CodeNotFound, "CI run was not found")
		}
		out := CIRunGetOut{Run: ciRunJSON(run, jobs), Jobs: []CIJobJSON{}}
		for _, job := range jobs {
			out.Jobs = append(out.Jobs, ciJobJSON(run, job))
		}
		out.ETag = "\"ci-run-" + run.ID + "-" + run.State + "\""
		return out, nil
	}
}

type CIRunCreateBody struct {
	Subject  string `json:"subject,omitempty" maxLength:"128"`
	Tree     string `json:"tree,omitempty" maxLength:"128"`
	Ref      string `json:"ref,omitempty" maxLength:"256"`
	Change   string `json:"change,omitempty" maxLength:"128"`
	SpecFrom string `json:"spec_from,omitempty" maxLength:"16"`
	Class    string `json:"class,omitempty" maxLength:"16"`
	Spec     string `json:"spec,omitempty" maxLength:"131072"`
}

type CIRunCreateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Body           CIRunCreateBody
}

type CIRunCreateOut struct {
	Run CIRunJSON `json:"run"`
}

func ciRunCreateHandler(d Deps) func(context.Context, op.Principal, CIRunCreateIn) (CIRunCreateOut, error) {
	return func(ctx context.Context, p op.Principal, in CIRunCreateIn) (CIRunCreateOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return CIRunCreateOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return CIRunCreateOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "ci.trigger", ""); derr != nil {
			return CIRunCreateOut{}, derr
		}
		ctrl, cerr := d.ciController()
		if cerr != nil {
			return CIRunCreateOut{}, cerr
		}
		body := in.Body
		specFrom := body.SpecFrom
		if specFrom == "" {
			specFrom = "base"
		}
		if specFrom != "base" && specFrom != "tree" {
			return CIRunCreateOut{}, op.Errorf(op.CodeValidation, "spec_from must be base or tree")
		}
		class := body.Class
		if class == "" {
			class = "interactive"
		}
		var spec ci.Spec
		if body.Spec != "" {
			decoded, derr := ci.DecodeSpec([]byte(body.Spec))
			if derr != nil {
				return CIRunCreateOut{}, ciSpecError(derr)
			}
			spec = decoded
		} else {
			return CIRunCreateOut{}, op.Errorf(op.CodeUnavailable, "spec reads need the Ruk file method; pass an inline spec")
		}
		if specFrom == "tree" && !rq.caller.IsAdmin {
			return CIRunCreateOut{}, op.Errorf(op.CodeForbidden, "spec_from tree needs a site admin")
		}
		trust := "untrusted"
		if body.Change == "" && d.protectedRef(rc.row.ID, body.Ref) {
			trust = "trusted"
		}
		if specFrom == "tree" {
			trust = "untrusted"
		}
		subject := body.Subject
		if subject == "" {
			return CIRunCreateOut{}, op.Errorf(op.CodeValidation, "subject is required")
		}
		tree := body.Tree
		if tree == "" {
			tree = subject
		}
		trigger := "manual:" + rq.user.Name + ":" + in.IdempotencyKey
		if body.Change != "" {
			trigger = "change:" + body.Change
		} else if body.Ref != "" {
			trigger = "push:" + body.Ref
		}
		run, rerr := ctrl.CreateRun(ctx, ci.CreateRunRequest{
			RepoID: rc.row.ID, Owner: in.Owner, Repo: in.Repo, Principal: rq.user.Name,
			Subject: subject, Tree: tree,
			BaseCommit: subject, SpecFrom: specFrom, Trust: trust, TriggerKey: trigger,
			Class: class, Spec: spec,
		})
		if rerr != nil {
			return CIRunCreateOut{}, ciToOp(rerr, "CI run was not created")
		}
		_, jobs, jerr := ctrl.Run(ctx, run.ID)
		if jerr != nil {
			return CIRunCreateOut{}, ciToOp(jerr, "CI run was not found")
		}
		return CIRunCreateOut{Run: ciRunJSON(run, jobs)}, nil
	}
}

func (d Deps) protectedRef(repoID int64, ref string) bool {
	if d.Store == nil {
		return false
	}
	if ref == "" {
		return false
	}
	if !strings.HasPrefix(ref, "refs/heads/") {
		ref = "refs/heads/" + ref
	}
	rule := access.MatchRule(d.Store.Resolver().Rules(repoID), ref)
	return rule != nil && rule.Mode == "LandOnly"
}

func (d Deps) CIDeliver() func(context.Context, ci.Outbox, string) (string, error) {
	return func(ctx context.Context, row ci.Outbox, signature string) (string, error) {
		var repoRow store.RepoRow
		if err := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			fetched, ferr := tx.GetRepo(row.RepoID)
			if ferr != nil {
				return ferr
			}
			repoRow = fetched
			return nil
		}); err != nil {
			return "", ciToOp(err, "CI repo was not found")
		}
		daemon, oerr := d.openDaemonRepo(ctx, repoRow)
		if oerr != nil {
			return "", oerr
		}
		statement := map[string]any{
			"subject": row.Statement.Subject, "kind": row.Statement.Kind,
			"result": row.Statement.Result, "signed_at": row.Statement.SignedAt,
		}
		if row.Statement.Supersedes != nil {
			statement["supersedes"] = *row.Statement.Supersedes
		}
		if row.Statement.Evidence != nil {
			statement["evidence"] = *row.Statement.Evidence
		}
		args := map[string]any{"statement": statement, "signature": signature}
		raw, rerr := ruk.Retry[map[string]any, jsonv1.RawMessage](ctx, d.Ruk, daemon, wire.MethodAttestAdd, row.ID, args)
		if rerr != nil {
			return "", ciRelayError(rerr)
		}
		var reply struct {
			Record struct {
				PayloadDigest string `json:"payload_digest"`
				Status        string `json:"status"`
			} `json:"record"`
		}
		if uerr := unmarshalJSON([]byte(raw), &reply); uerr != nil {
			return "", uerr
		}
		if reply.Record.PayloadDigest == "" {
			return "", op.Wrap(op.CodeInternal, errAttestReplyShape, "attest.add reply lacks payload_digest")
		}
		if !strings.EqualFold(strings.TrimPrefix(reply.Record.PayloadDigest, "sha256:"), row.Digest) {
			return "", op.Wrap(op.CodeCorrupt, errAttestDigestMismatch, "attest.add stored a different payload")
		}
		return reply.Record.Status, nil
	}
}

func ciRelayError(err error) error {
	re, ok := errors.AsType[*ruk.Error](err)
	if !ok {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return ciProblem("E_CAPACITY")
		}
		if oerr, ok := errors.AsType[*op.Error](err); ok {
			switch oerr.Code {
			case op.CodeNotFound:
				return ciProblem("E_NOT_FOUND")
			case op.CodeForbidden:
				return ciProblem("E_FORBIDDEN")
			case op.CodeUnauthorized:
				return ciProblem("E_UNAUTHENTICATED")
			case op.CodeInvalid, op.CodeValidation:
				return ciProblem("E_INVALID")
			case op.CodeUnavailable:
				return ciProblem("E_CAPACITY")
			}
		}
		return ciProblem("E_CAPACITY")
	}
	switch re.Code {
	case wire.CodeForbidden:
		return ciProblem("E_FORBIDDEN")
	case wire.CodePolicy:
		return ciProblem("E_POLICY")
	case wire.CodeObjectFormat:
		return ciProblem("E_OBJECT_FORMAT")
	case wire.CodeNotFound:
		return ciProblem("E_NOT_FOUND")
	case wire.CodeInvalid:
		return ciProblem("E_INVALID")
	case wire.CodeLeaseLost:
		return ciProblem("E_LEASE_LOST")
	case wire.CodeCapacity, wire.CodeOffline, wire.CodeCoordinatorUnresponsive, wire.CodeHosted:
		return ciProblem("E_CAPACITY")
	default:
		return ciProblem("E_CAPACITY")
	}
}

var errAttestReplyShape = op.Errorf(op.CodeInternal, "attest.add reply lacks payload_digest")
var errAttestDigestMismatch = op.Errorf(op.CodeCorrupt, "attest.add stored a different payload")

func ciSpecError(err error) error {
	return op.Wrap(op.CodeValidation, err, "CI spec is not valid")
}

type CIRunUpdateBody struct {
	State  string `json:"state" minLength:"1" maxLength:"32"`
	Reason string `json:"reason,omitempty" maxLength:"200"`
}

type CIRunUpdateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Run            string `path:"run"`
	Body           CIRunUpdateBody
}

type CIRunUpdateOut struct {
	State             string   `json:"state"`
	CancelledJobs     []string `json:"cancelled_jobs"`
	SignalledAttempts []string `json:"signalled_attempts"`
	ExclusiveRunning  []string `json:"exclusive_running"`
	Noop              bool     `json:"noop"`
}

func ciRunUpdateHandler(d Deps) func(context.Context, op.Principal, CIRunUpdateIn) (CIRunUpdateOut, error) {
	return func(ctx context.Context, p op.Principal, in CIRunUpdateIn) (CIRunUpdateOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return CIRunUpdateOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return CIRunUpdateOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "ci.trigger", ""); derr != nil {
			return CIRunUpdateOut{}, derr
		}
		if in.Body.State != "cancelled" {
			return CIRunUpdateOut{}, op.Errorf(op.CodeValidation, "state must be cancelled")
		}
		ctrl, cerr := d.ciController()
		if cerr != nil {
			return CIRunUpdateOut{}, cerr
		}
		run, jobs, rerr := ctrl.Run(ctx, in.Run)
		if rerr != nil {
			return CIRunUpdateOut{}, ciToOp(rerr, "CI run was not found")
		}
		if run.RepoID != rc.row.ID {
			return CIRunUpdateOut{}, op.Errorf(op.CodeNotFound, "CI run was not found")
		}
		out := CIRunUpdateOut{State: run.State, CancelledJobs: []string{}, SignalledAttempts: []string{}, ExclusiveRunning: []string{}}
		terminal := true
		for _, job := range jobs {
			switch job.State {
			case "passed", "failed", "errored", "timed_out", "cancelled", "skipped", "cached", "carried":
			default:
				terminal = false
			}
		}
		if terminal {
			out.Noop = true
			return out, nil
		}
		reason := in.Body.Reason
		if reason == "" {
			reason = "cancelled"
		}
		if cerr := ctrl.Cancel(ctx, run.ID, reason); cerr != nil {
			return CIRunUpdateOut{}, ciToOp(cerr, "CI run was not cancelled")
		}
		updated, ujobs, uerr := ctrl.Run(ctx, run.ID)
		if uerr != nil {
			return CIRunUpdateOut{}, ciToOp(uerr, "CI run was not found")
		}
		out.State = updated.State
		for _, job := range ujobs {
			switch job.State {
			case "cancelled":
				out.CancelledJobs = append(out.CancelledJobs, job.Spec.Name)
			case "cancelling":
				out.SignalledAttempts = append(out.SignalledAttempts, job.Spec.Name)
			case "running":
				out.ExclusiveRunning = append(out.ExclusiveRunning, job.Spec.Name)
			}
		}
		return out, nil
	}
}

type CIRunRetryBody struct {
	Jobs                   []string `json:"jobs,omitempty"`
	AcknowledgeSideEffects bool     `json:"acknowledge_side_effects,omitempty"`
}

type CIRunRetryIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Run            string `path:"run"`
	Body           CIRunRetryBody
}

type CIRunRetryOut struct {
	Carried []string  `json:"carried"`
	Rerun   []string  `json:"rerun"`
	Run     CIRunJSON `json:"run"`
}

func ciRunRetryHandler(d Deps) func(context.Context, op.Principal, CIRunRetryIn) (CIRunRetryOut, error) {
	return func(ctx context.Context, p op.Principal, in CIRunRetryIn) (CIRunRetryOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return CIRunRetryOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return CIRunRetryOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "ci.trigger", ""); derr != nil {
			return CIRunRetryOut{}, derr
		}
		ctrl, cerr := d.ciController()
		if cerr != nil {
			return CIRunRetryOut{}, cerr
		}
		run, _, rerr := ctrl.Run(ctx, in.Run)
		if rerr != nil {
			return CIRunRetryOut{}, ciToOp(rerr, "CI run was not found")
		}
		if run.RepoID != rc.row.ID {
			return CIRunRetryOut{}, op.Errorf(op.CodeNotFound, "CI run was not found")
		}
		retry, rerr := ctrl.RetryRun(ctx, run.ID, in.Body.Jobs, in.Body.AcknowledgeSideEffects)
		if rerr != nil {
			return CIRunRetryOut{}, ciToOp(rerr, "CI run was not retried")
		}
		_, jobs, jerr := ctrl.Run(ctx, retry.ID)
		if jerr != nil {
			return CIRunRetryOut{}, ciToOp(jerr, "CI run was not found")
		}
		out := CIRunRetryOut{Run: ciRunJSON(retry, jobs), Carried: []string{}, Rerun: []string{}}
		for _, job := range jobs {
			if job.State == "carried" {
				out.Carried = append(out.Carried, job.Spec.Name)
			} else {
				out.Rerun = append(out.Rerun, job.Spec.Name)
			}
		}
		return out, nil
	}
}

type CIJobListIn struct {
	Owner      string `path:"owner"`
	Repo       string `path:"repo"`
	State      string `query:"state"`
	WaitReason string `query:"wait_reason"`
	Run        string `query:"run"`
	Cursor     string `query:"cursor"`
	Limit      int    `query:"limit"`
}

type CIJobListOut struct {
	Next string      `json:"-"`
	Jobs []CIJobJSON `json:"jobs"`
}

func ciJobListHandler(d Deps) func(context.Context, op.Principal, CIJobListIn) (CIJobListOut, error) {
	return func(ctx context.Context, p op.Principal, in CIJobListIn) (CIJobListOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return CIJobListOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return CIJobListOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "ci.view", ""); derr != nil {
			return CIJobListOut{}, derr
		}
		ctrl, cerr := d.ciController()
		if cerr != nil {
			return CIJobListOut{}, cerr
		}
		offset, oerr := ciCursorOffset(in.Cursor)
		if oerr != nil {
			return CIJobListOut{}, oerr
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 100
		}
		if limit > 1000 {
			return CIJobListOut{}, op.Errorf(op.CodeInvalid, "limit must not exceed 1000")
		}
		runs, rerr := ctrl.Runs(ctx, rc.row.ID)
		if rerr != nil {
			return CIJobListOut{}, ciToOp(rerr, "CI jobs were not found")
		}
		out := CIJobListOut{Jobs: []CIJobJSON{}}
		var matched []CIJobJSON
		for _, run := range runs {
			if in.Run != "" && run.ID != in.Run {
				continue
			}
			_, jobs, jerr := ctrl.Run(ctx, run.ID)
			if jerr != nil {
				return CIJobListOut{}, ciToOp(jerr, "CI run was not found")
			}
			for _, job := range jobs {
				if in.State != "" && job.State != in.State {
					continue
				}
				if in.WaitReason != "" && job.WaitReason != in.WaitReason {
					continue
				}
				matched = append(matched, ciJobJSON(run, job))
			}
		}
		slices.SortFunc(matched, func(a, b CIJobJSON) int { return strings.Compare(a.ID, b.ID) })
		for i := offset; i < int64(len(matched)) && len(out.Jobs) < limit; i++ {
			out.Jobs = append(out.Jobs, matched[i])
		}
		out.Next = ciNextCursor(offset, int64(limit), offset+int64(limit) < int64(len(matched)))
		return out, nil
	}
}

type CILogGetIn struct {
	Owner    string `path:"owner"`
	Repo     string `path:"repo"`
	Attempt  string `path:"attempt"`
	Encoding string `query:"encoding"`
	Offset   int64  `query:"offset"`
	Limit    int    `query:"limit"`
	WaitMS   int    `query:"wait_ms"`
}

type CILogGetOut struct {
	Data         string     `json:"data"`
	AttemptState string     `json:"attempt_state"`
	Elided       [][2]int64 `json:"elided"`
	Offset       int64      `json:"offset"`
	NextOffset   int64      `json:"next_offset"`
	EndOffset    int64      `json:"end_offset"`
	EOF          bool       `json:"eof"`
}

func ciLogGetHandler(d Deps) func(context.Context, op.Principal, CILogGetIn) (CILogGetOut, error) {
	return func(ctx context.Context, p op.Principal, in CILogGetIn) (CILogGetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return CILogGetOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return CILogGetOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "ci.view", ""); derr != nil {
			return CILogGetOut{}, derr
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 64 << 10
		}
		if limit > 1<<20 {
			return CILogGetOut{}, op.Errorf(op.CodeInvalid, "limit must not exceed 1048576")
		}
		if in.WaitMS < 0 || in.WaitMS > 25000 {
			return CILogGetOut{}, op.Errorf(op.CodeValidation, "wait_ms must be 0 to 25000")
		}
		encoding := in.Encoding
		if encoding == "" {
			encoding = "text"
		}
		if encoding != "text" && encoding != "base64" {
			return CILogGetOut{}, op.Errorf(op.CodeValidation, "encoding must be text or base64")
		}
		ctrl, cerr := d.ciController()
		if cerr != nil {
			return CILogGetOut{}, cerr
		}
		attempt, job, run, rerr := ctrl.GetAttempt(ctx, in.Attempt)
		if rerr != nil {
			return CILogGetOut{}, ciToOp(rerr, "CI attempt was not found")
		}
		if run.RepoID != rc.row.ID {
			return CILogGetOut{}, op.Errorf(op.CodeNotFound, "CI attempt was not found")
		}
		_ = job
		start := in.Offset
		if start < attempt.LogBase {
			start = attempt.LogBase
		}
		end := attempt.LogBase + int64(len(attempt.Log))
		if start > end {
			start = end
		}
		stop := start + int64(limit)
		if stop > end {
			stop = end
		}
		window := attempt.Log[start-attempt.LogBase : stop-attempt.LogBase]
		data := string(window)
		if encoding == "base64" {
			data = base64.StdEncoding.EncodeToString(window)
		}
		terminal := true
		switch attempt.State {
		case "granted", "started", "suspect":
			terminal = false
		}
		elided := attempt.Elided
		if elided == nil {
			elided = [][2]int64{}
		}
		return CILogGetOut{
			Data: data, Offset: start, NextOffset: stop, EndOffset: end,
			EOF: terminal, AttemptState: attempt.State, Elided: elided,
		}, nil
	}
}

type CIArtifactJSON struct {
	Job    string `json:"job"`
	Name   string `json:"name"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type CIArtifactListIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	Run    string `path:"run"`
	Job    string `query:"job"`
	Cursor string `query:"cursor"`
}

type CIArtifactListOut struct {
	Next      string           `json:"-"`
	Artifacts []CIArtifactJSON `json:"artifacts"`
}

func ciArtifactListHandler(d Deps) func(context.Context, op.Principal, CIArtifactListIn) (CIArtifactListOut, error) {
	return func(ctx context.Context, p op.Principal, in CIArtifactListIn) (CIArtifactListOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return CIArtifactListOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return CIArtifactListOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "ci.view", ""); derr != nil {
			return CIArtifactListOut{}, derr
		}
		ctrl, cerr := d.ciController()
		if cerr != nil {
			return CIArtifactListOut{}, cerr
		}
		run, jobs, rerr := ctrl.Run(ctx, in.Run)
		if rerr != nil {
			return CIArtifactListOut{}, ciToOp(rerr, "CI run was not found")
		}
		if run.RepoID != rc.row.ID {
			return CIArtifactListOut{}, op.Errorf(op.CodeNotFound, "CI run was not found")
		}
		out := CIArtifactListOut{Artifacts: []CIArtifactJSON{}}
		for _, job := range jobs {
			if in.Job != "" && job.Spec.Name != in.Job {
				continue
			}
			artifacts, aerr := ctrl.Artifacts(ctx, job.ID)
			if aerr != nil {
				return CIArtifactListOut{}, ciToOp(aerr, "CI artifacts were not found")
			}
			for _, artifact := range artifacts {
				out.Artifacts = append(out.Artifacts, CIArtifactJSON{Job: job.Spec.Name, Name: artifact.Name, Digest: artifact.Digest, Size: artifact.Size})
			}
		}
		slices.SortFunc(out.Artifacts, func(a, b CIArtifactJSON) int {
			if a.Job != b.Job {
				return strings.Compare(a.Job, b.Job)
			}
			return strings.Compare(a.Name, b.Name)
		})
		return out, nil
	}
}

type CIArtifactGetIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	Digest string `path:"digest"`
}

type CIArtifactGetOut struct {
	Name        string `json:"name"`
	Digest      string `json:"digest"`
	ContentType string `json:"content_type"`
	DownloadURL string `json:"download_url"`
	Size        int64  `json:"size"`
}

func ciArtifactGetHandler(d Deps) func(context.Context, op.Principal, CIArtifactGetIn) (CIArtifactGetOut, error) {
	return func(ctx context.Context, p op.Principal, in CIArtifactGetIn) (CIArtifactGetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return CIArtifactGetOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return CIArtifactGetOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "ci.view", ""); derr != nil {
			return CIArtifactGetOut{}, derr
		}
		ctrl, cerr := d.ciController()
		if cerr != nil {
			return CIArtifactGetOut{}, cerr
		}
		blob, ok := ctrl.RepoBlob(ctx, rc.row.ID, in.Digest)
		if !ok {
			return CIArtifactGetOut{}, op.Errorf(op.CodeNotFound, "CI blob was not found")
		}
		return CIArtifactGetOut{
			Name: in.Digest, Digest: in.Digest, Size: int64(len(blob)),
			ContentType: sniffRawContentType(blob),
			DownloadURL: "/" + in.Owner + "/" + in.Repo + "/ci/blobs/" + in.Digest + "?download=1",
		}, nil
	}
}

type CISpecVerifyBody struct {
	Ref  string `json:"ref,omitempty" maxLength:"256"`
	Tree string `json:"tree,omitempty" maxLength:"128"`
	Spec string `json:"spec,omitempty" maxLength:"131072"`
}

type CISpecVerifyIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Body           CISpecVerifyBody
}

type CISpecErrorJSON struct {
	Pointer string `json:"pointer"`
	Message string `json:"message"`
	Line    int    `json:"line,omitempty"`
	Col     int    `json:"col,omitempty"`
}

type CISpecVerifyOut struct {
	Digests map[string]string `json:"digests"`
	Errors  []CISpecErrorJSON `json:"errors"`
	Valid   bool              `json:"valid"`
}

func ciSpecVerifyHandler(d Deps) func(context.Context, op.Principal, CISpecVerifyIn) (CISpecVerifyOut, error) {
	return func(ctx context.Context, p op.Principal, in CISpecVerifyIn) (CISpecVerifyOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return CISpecVerifyOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return CISpecVerifyOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "ci.trigger", ""); derr != nil {
			return CISpecVerifyOut{}, derr
		}
		out := CISpecVerifyOut{Digests: map[string]string{}, Errors: []CISpecErrorJSON{}}
		if in.Body.Spec == "" {
			return CISpecVerifyOut{}, op.Errorf(op.CodeUnavailable, "spec reads need the Ruk file method; pass an inline spec")
		}
		spec, serr := ci.DecodeSpec([]byte(in.Body.Spec))
		if serr != nil {
			out.Errors = append(out.Errors, ciSpecErrorJSON(serr))
			return out, nil
		}
		for _, job := range spec.Jobs {
			digest, derr := ci.JobSpecDigest(job)
			if derr != nil {
				out.Errors = append(out.Errors, ciSpecErrorJSON(derr))
				return out, nil
			}
			out.Digests[job.Name] = digest
		}
		out.Valid = true
		return out, nil
	}
}

func ciSpecErrorJSON(err error) CISpecErrorJSON {
	specErr, ok := errors.AsType[*ci.SpecError](err)
	if !ok {
		return CISpecErrorJSON{Message: err.Error()}
	}
	return CISpecErrorJSON{Pointer: specErr.Pointer, Line: specErr.Line, Col: specErr.Col, Message: specErr.Message}
}

type CISpecGetIn struct {
	Owner string `path:"owner"`
	Repo  string `path:"repo"`
	Ref   string `query:"ref"`
	Run   string `query:"run"`
}

type CISpecGetOut struct {
	Effective map[string]any    `json:"effective"`
	Digests   map[string]string `json:"digests"`
	Required  []string          `json:"required"`
}

func ciSpecGetHandler(d Deps) func(context.Context, op.Principal, CISpecGetIn) (CISpecGetOut, error) {
	return func(ctx context.Context, p op.Principal, in CISpecGetIn) (CISpecGetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return CISpecGetOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return CISpecGetOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "ci.view", ""); derr != nil {
			return CISpecGetOut{}, derr
		}
		if in.Run == "" {
			return CISpecGetOut{}, op.Errorf(op.CodeUnavailable, "spec reads need the Ruk file method; pass a run")
		}
		ctrl, cerr := d.ciController()
		if cerr != nil {
			return CISpecGetOut{}, cerr
		}
		run, _, rerr := ctrl.Run(ctx, in.Run)
		if rerr != nil {
			return CISpecGetOut{}, ciToOp(rerr, "CI run was not found")
		}
		if run.RepoID != rc.row.ID {
			return CISpecGetOut{}, op.Errorf(op.CodeNotFound, "CI run was not found")
		}
		raw, merr := marshalJSON(run.Spec)
		if merr != nil {
			return CISpecGetOut{}, merr
		}
		var effective map[string]any
		if uerr := unmarshalJSON(raw, &effective); uerr != nil {
			return CISpecGetOut{}, uerr
		}
		out := CISpecGetOut{Effective: effective, Digests: map[string]string{}, Required: []string{}}
		for _, job := range run.Spec.Jobs {
			digest, derr := ci.JobSpecDigest(job)
			if derr != nil {
				return CISpecGetOut{}, ciToOp(derr, "CI spec digest failed")
			}
			out.Digests[job.Name] = digest
		}
		proofs, perr := ci.RequiredProofs(run.Spec)
		if perr == nil {
			out.Required = proofs
		}
		return out, nil
	}
}

type CIRunnerCreateBody struct {
	Scope          string   `json:"scope" minLength:"1" maxLength:"16"`
	Pubkey         string   `json:"pubkey,omitempty" maxLength:"64"`
	SandboxProfile string   `json:"sandbox_profile,omitempty" maxLength:"64"`
	Labels         []string `json:"labels" minItems:"1" maxItems:"8"`
	Capacity       int      `json:"capacity"`
}

type CIRunnerCreateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           CIRunnerCreateBody
}

type CIRunnerJSON struct {
	ID             string   `json:"id"`
	Scope          string   `json:"scope"`
	Status         string   `json:"status"`
	SandboxProfile string   `json:"sandbox_profile"`
	Labels         []string `json:"labels"`
	Capacity       int      `json:"capacity"`
}

type CIRunnerCreateOut struct {
	Token  string       `json:"token"`
	Runner CIRunnerJSON `json:"runner"`
}

func ciRunnerJSON(runner ci.Runner) CIRunnerJSON {
	return CIRunnerJSON{
		ID: runner.ID, Labels: runner.Labels, Scope: runner.Scope,
		Capacity: runner.Capacity, Status: runner.Status, SandboxProfile: runner.SandboxProfile,
	}
}

func ciRunnerCreateHandler(d Deps) func(context.Context, op.Principal, CIRunnerCreateIn) (CIRunnerCreateOut, error) {
	return func(ctx context.Context, p op.Principal, in CIRunnerCreateIn) (CIRunnerCreateOut, error) {
		if _, aerr := requireAdmin(ctx, d.Service, p); aerr != nil {
			return CIRunnerCreateOut{}, aerr
		}
		ctrl, cerr := d.ciController()
		if cerr != nil {
			return CIRunnerCreateOut{}, cerr
		}
		var pubkey []byte
		if in.Body.Pubkey != "" {
			decoded, derr := base64.StdEncoding.DecodeString(in.Body.Pubkey)
			if derr != nil {
				return CIRunnerCreateOut{}, op.Errorf(op.CodeValidation, "pubkey must be base64")
			}
			pubkey = decoded
		}
		runner, token, rerr := ctrl.CreateRunner(ctx, in.Body.Labels, in.Body.Scope, in.Body.Capacity, in.Body.SandboxProfile, pubkey)
		if rerr != nil {
			return CIRunnerCreateOut{}, ciToOp(rerr, "CI runner was not created")
		}
		return CIRunnerCreateOut{Runner: ciRunnerJSON(runner), Token: token}, nil
	}
}

type CIRunnerListIn struct{}

type CIRunnerListOut struct {
	Runners []CIRunnerJSON `json:"runners"`
}

func ciRunnerListHandler(d Deps) func(context.Context, op.Principal, CIRunnerListIn) (CIRunnerListOut, error) {
	return func(ctx context.Context, p op.Principal, _ CIRunnerListIn) (CIRunnerListOut, error) {
		if _, aerr := requireAdmin(ctx, d.Service, p); aerr != nil {
			return CIRunnerListOut{}, aerr
		}
		ctrl, cerr := d.ciController()
		if cerr != nil {
			return CIRunnerListOut{}, cerr
		}
		runners, rerr := ctrl.Runners(ctx)
		if rerr != nil {
			return CIRunnerListOut{}, ciToOp(rerr, "CI runners were not found")
		}
		out := CIRunnerListOut{Runners: []CIRunnerJSON{}}
		for _, runner := range runners {
			out.Runners = append(out.Runners, ciRunnerJSON(runner))
		}
		return out, nil
	}
}

type CIRunnerRevokeIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	ID             string `path:"id"`
}

type CIRunnerRevokeOut struct {
	Status string `json:"status"`
}

func ciRunnerRevokeHandler(d Deps) func(context.Context, op.Principal, CIRunnerRevokeIn) (CIRunnerRevokeOut, error) {
	return func(ctx context.Context, p op.Principal, in CIRunnerRevokeIn) (CIRunnerRevokeOut, error) {
		if _, aerr := requireAdmin(ctx, d.Service, p); aerr != nil {
			return CIRunnerRevokeOut{}, aerr
		}
		ctrl, cerr := d.ciController()
		if cerr != nil {
			return CIRunnerRevokeOut{}, cerr
		}
		if rerr := ctrl.RevokeRunner(ctx, in.ID); rerr != nil {
			return CIRunnerRevokeOut{}, ciToOp(rerr, "CI runner was not found")
		}
		return CIRunnerRevokeOut{Status: "revoked"}, nil
	}
}
