package api

import (
	"cmp"
	"context"
	"database/sql"
	"strings"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/land"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/review"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

// LandPlanCreateBody carries one land plan request.
type LandPlanCreateBody struct {
	TargetRef string `json:"target_ref" minLength:"1" maxLength:"255"`
	Change    string `json:"change" minLength:"1" maxLength:"128"`
}

// LandPlanCreateIn carries one land plan create request.
type LandPlanCreateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Body           LandPlanCreateBody
}

// GapJSON is one machine-checkable land gap.
type GapJSON struct {
	Kind   string `json:"kind,omitempty"`
	Reason string `json:"reason"`
	Detail string `json:"detail"`
	NextOp string `json:"next_op"`
}

// LandPlanJSON is one land plan.
type LandPlanJSON struct {
	ID        string    `json:"id"`
	Target    string    `json:"target_ref"`
	State     string    `json:"state"`
	Candidate string    `json:"candidate"`
	Base      string    `json:"base"`
	Gaps      []GapJSON `json:"gaps"`
	CreatedAt int64     `json:"created_at"`
}

func landPlanJSON(row store.LandIntentRow, gaps []review.Gap) LandPlanJSON {
	out := LandPlanJSON{
		ID: row.PublicID, Target: row.TargetRef, State: row.State,
		Candidate: row.CandidateOid, Base: row.BaseOid,
		Gaps: []GapJSON{}, CreatedAt: row.CreatedAt,
	}
	for _, gap := range gaps {
		out.Gaps = append(out.Gaps, GapJSON{Kind: gap.Kind, Reason: gap.Reason, Detail: gap.Detail, NextOp: gap.NextOp})
	}
	return out
}

// LandPlanCreateOut carries the created plan.
type LandPlanCreateOut struct {
	Plan LandPlanJSON `json:"plan"`
}

func landPlanCreateHandler(d Deps) func(context.Context, op.Principal, LandPlanCreateIn) (LandPlanCreateOut, error) {
	return func(ctx context.Context, p op.Principal, in LandPlanCreateIn) (LandPlanCreateOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return LandPlanCreateOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return LandPlanCreateOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.push", ""); derr != nil {
			return LandPlanCreateOut{}, derr
		}
		branch, ok := strings.CutPrefix(in.Body.TargetRef, "refs/heads/")
		if !ok || branch == "" || strings.Contains(branch, "..") {
			return LandPlanCreateOut{}, op.Errorf(op.CodeValidation, "target ref must be a branch ref")
		}
		if !rq.authed || rq.user.ID == 0 {
			return LandPlanCreateOut{}, op.Errorf(op.CodeUnauthorized, "authentication is required")
		}
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return LandPlanCreateOut{}, oerr
		}
		rc.daemon = daemon
		baseResolved, berr := d.resolveRev(ctx, rc, branch)
		if berr != nil {
			return LandPlanCreateOut{}, berr
		}
		out := LandPlanCreateOut{}
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			row, gerr := tx.GetChangeByKey(rc.row.ID, in.Body.Change)
			if gerr != nil {
				return gerr
			}
			if row.State != "open" {
				return op.Errorf(op.CodeConflict, "change %s is %s", in.Body.Change, row.State)
			}
			if row.HeadOid == "" {
				return op.Errorf(op.CodeConflict, "change %s has a conflicted head", in.Body.Change)
			}
			gaps, gerr := d.liveGapsInTx(ctx, daemon, tx, rc.row.ID, row)
			if gerr != nil {
				return gerr
			}
			gapJSON, merr := marshalJSON(gaps)
			if merr != nil {
				return op.Wrap(op.CodeInternal, merr, "gap encoding failed")
			}
			ruleVersion := int64(1)
			resolver := access.NewSQLResolver(d.Service.DB(), access.NewStoreTeams(d.Service.DB(), nil))
			if rules := resolver.Rules(rc.row.ID); len(rules) > 0 {
				ruleVersion = rules[0].Version
			}
			plan, perr := tx.InsertLandIntent(d.now(), store.LandIntentRow{
				RepoID: row.RepoID, TargetRef: in.Body.TargetRef,
				ChangeID:         sql.NullInt64{Int64: row.ID, Valid: true},
				CandidateOid:     row.HeadOid,
				BaseOid:          baseResolved.commit,
				PolicyGeneration: ruleVersion,
				Gap:              string(gapJSON),
				CreatedBy:        rq.user.ID,
			})
			if perr != nil {
				return perr
			}
			createdBody, _ := marshalJSON(map[string]any{
				"owner": in.Owner, "repo": in.Repo, "plan": plan.PublicID, "target": in.Body.TargetRef,
			})
			if _, rerr := tx.Outbox().Emit(store.Event{
				Kind: "land.plan_created", Body: createdBody, OccurredAt: d.now(),
			}); rerr != nil {
				return rerr
			}
			out.Plan = landPlanJSON(plan, gaps)
			return nil
		})
		if werr != nil {
			return LandPlanCreateOut{}, storeToOp(werr, "change was not found")
		}
		return out, nil
	}
}

func (d Deps) liveGaps(ctx context.Context, daemon ruk.Repo, repoID int64, row store.ChangeRow) ([]review.Gap, error) {
	resolver := access.NewSQLResolver(d.Service.DB(), access.NewStoreTeams(d.Service.DB(), nil))
	rules := resolver.Rules(repoID)
	if len(rules) == 0 {
		return nil, nil
	}
	reqs := make([]review.Requirement, 0, len(rules[0].Required))
	for _, req := range rules[0].Required {
		reqs = append(reqs, review.Requirement{Kind: req.Kind, Result: req.Result, MinSigners: req.MinSigners})
	}
	if len(reqs) == 0 {
		return nil, nil
	}
	var reply attestListReply
	if rerr := d.callRuk(ctx, daemon, wire.MethodAttestList,
		map[string]any{"subject": row.HeadOid, "page": map[string]any{"limit": 100}}, &reply); rerr != nil {
		return nil, rerr
	}
	facts := make([]review.AttestationFact, 0, len(reply.Records.Items))
	for _, record := range reply.Records.Items {
		facts = append(facts, review.AttestationFact{
			Kind: record.Kind, Result: record.Result, OID: row.HeadOid,
			Live: record.Status == "live",
		})
	}
	return review.GapsForChange(row.HeadOid, row.AuthorUserID, reqs, facts), nil
}

func (d Deps) liveGapsInTx(ctx context.Context, daemon ruk.Repo, tx *store.WriteTx, repoID int64, change store.ChangeRow) ([]review.Gap, error) {
	gaps, gerr := d.liveGaps(ctx, daemon, repoID, change)
	if gerr != nil {
		return nil, gerr
	}
	comments, cerr := tx.WholeChangeComments(change.ID, change.HeadOid)
	if cerr != nil {
		return nil, cerr
	}
	eligible := false
	for _, comment := range comments {
		if comment.AuthorUserID == change.AuthorUserID {
			continue
		}
		if comment.Body == "approve" || strings.HasPrefix(comment.Body, "approve:") {
			eligible = true
			break
		}
	}
	kept := make([]review.Gap, 0, len(gaps))
	keptApprove := false
	for _, gap := range gaps {
		if gap.Reason == "needs_review" && gap.Kind == "review/approve" {
			if eligible {
				continue
			}
			keptApprove = true
		}
		kept = append(kept, gap)
	}
	if !eligible && !keptApprove {
		for _, gap := range gaps {
			if gap.Kind == "review/approve" {
				_ = gap
			}
		}
		kept = append(kept, review.Gap{
			Kind: "review/approve", Reason: "needs_review",
			Detail: "review/approve wants 1 more", NextOp: "change.review.create",
		})
	}
	return kept, nil
}

// LandPlanGetIn carries one land plan read.
type LandPlanGetIn struct {
	Owner string `path:"owner"`
	Repo  string `path:"repo"`
	Plan  string `path:"plan"`
}

// LandPlanGetOut carries one land plan.
type LandPlanGetOut struct {
	Plan LandPlanJSON `json:"plan"`
}

func landPlanGetHandler(d Deps) func(context.Context, op.Principal, LandPlanGetIn) (LandPlanGetOut, error) {
	return func(ctx context.Context, p op.Principal, in LandPlanGetIn) (LandPlanGetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return LandPlanGetOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return LandPlanGetOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.push", ""); derr != nil {
			return LandPlanGetOut{}, derr
		}
		out := LandPlanGetOut{}
		rerr = d.Store.Read(ctx, func(tx *store.ReadTx) error {
			row, gerr := tx.ReadLandIntentByPublicID(rc.row.ID, in.Plan)
			if gerr != nil {
				return gerr
			}
			var gaps []review.Gap
			if uerr := unmarshalJSON([]byte(row.Gap), &gaps); uerr != nil {
				return uerr
			}
			out.Plan = landPlanJSON(row, gaps)
			return nil
		})
		if rerr != nil {
			return LandPlanGetOut{}, storeToOp(rerr, "land plan was not found")
		}
		return out, nil
	}
}

// LandPlanListIn carries one land plan list request.
type LandPlanListIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	Target string `query:"target"`
	Limit  int    `query:"limit"`
}

// LandPlanListOut carries land plans for one target.
type LandPlanListOut struct {
	Plans []LandPlanJSON `json:"plans"`
}

func landPlanListHandler(d Deps) func(context.Context, op.Principal, LandPlanListIn) (LandPlanListOut, error) {
	return func(ctx context.Context, p op.Principal, in LandPlanListIn) (LandPlanListOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return LandPlanListOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return LandPlanListOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.push", ""); derr != nil {
			return LandPlanListOut{}, derr
		}
		if in.Target == "" {
			return LandPlanListOut{}, op.Errorf(op.CodeValidation, "target must not be empty")
		}
		limit := clampLimit(in.Limit, 30, 100)
		out := LandPlanListOut{Plans: []LandPlanJSON{}}
		rerr = d.Store.Read(ctx, func(tx *store.ReadTx) error {
			rows, lerr := tx.ListLandIntents(rc.row.ID, in.Target, limit)
			if lerr != nil {
				return lerr
			}
			for _, row := range rows {
				var gaps []review.Gap
				if uerr := unmarshalJSON([]byte(row.Gap), &gaps); uerr != nil {
					return uerr
				}
				out.Plans = append(out.Plans, landPlanJSON(row, gaps))
			}
			return nil
		})
		if rerr != nil {
			return LandPlanListOut{}, op.Wrap(op.CodeInternal, rerr, "land plan list failed")
		}
		return out, nil
	}
}

// LandRunCreateIn carries one land run request.
type LandRunCreateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Plan           string `path:"plan"`
	Stack          bool   `query:"stack"`
}

// LandRunCreateOut carries the created run.
type LandRunCreateOut struct {
	Run   LandRunJSON `json:"run"`
	Stale bool        `json:"stale,omitzero"`
}

// LandRunJSON is one land execution attempt.
type LandRunJSON struct {
	ID    string `json:"id"`
	Plan  string `json:"plan"`
	State string `json:"state"`
}

// LandRunListIn carries one land run list request.
type LandRunListIn struct {
	Owner string `path:"owner"`
	Repo  string `path:"repo"`
	Plan  string `path:"plan"`
}

// LandRunListOut carries runs of one plan.
type LandRunListOut struct {
	Runs []LandRunJSON `json:"runs"`
}

func landRunCreateHandler(d Deps) func(context.Context, op.Principal, LandRunCreateIn) (LandRunCreateOut, error) {
	return func(ctx context.Context, p op.Principal, in LandRunCreateIn) (LandRunCreateOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return LandRunCreateOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return LandRunCreateOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.push", ""); derr != nil {
			return LandRunCreateOut{}, derr
		}
		if !rq.authed || rq.user.ID == 0 {
			return LandRunCreateOut{}, op.Errorf(op.CodeUnauthorized, "authentication is required")
		}
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return LandRunCreateOut{}, oerr
		}
		out := LandRunCreateOut{}
		var planTarget string
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			plan, gerr := tx.GetLandIntentByPublicID(rc.row.ID, in.Plan)
			if gerr != nil {
				return gerr
			}
			planTarget = plan.TargetRef
			switch plan.State {
			case "landed":
				out.Stale = true
				return op.Errorf(op.CodeStale, "land plan %s already landed", in.Plan)
			case "executing":
				return op.Errorf(op.CodeConflict, "land plan %s is already executing", in.Plan)
			case "awaiting":
			case "failed":
				if serr := tx.SetLandIntentState(d.now(), plan.ID, "repreparing"); serr != nil {
					return serr
				}
			case "repreparing":
				if serr := tx.SetLandIntentState(d.now(), plan.ID, "awaiting"); serr != nil {
					return serr
				}
			default:
				return op.Errorf(op.CodeConflict, "land plan %s is %s", in.Plan, plan.State)
			}
			var change store.ChangeRow
			if plan.ChangeID.Valid {
				change, gerr = tx.GetChangeForUpdate(plan.ChangeID.Int64)
			} else {
				gerr = store.ErrNotFound
			}
			if gerr != nil {
				return gerr
			}
			gaps, gerr := d.liveGapsInTx(ctx, daemon, tx, rc.row.ID, change)
			if gerr != nil {
				return gerr
			}
			if len(gaps) > 0 {
				detail := "requirements-unmet:"
				for _, gap := range gaps {
					detail += " " + gap.Reason + "=" + gap.Detail + ";"
				}
				return &op.Error{
					Code:   op.CodeConflict,
					Detail: detail,
					Recovery: &op.Recovery{
						Next: "change.review.create",
						Docs: "meet every required proof, then run again",
					},
				}
			}
			run, rerr := tx.InsertLandRun(d.now(), store.LandRunRow{PlanID: plan.ID, State: "awaiting"})
			if rerr != nil {
				return rerr
			}
			out.Run = LandRunJSON{ID: run.PublicID, Plan: plan.PublicID, State: "awaiting"}
			return nil
		})
		if werr != nil {
			return LandRunCreateOut{}, storeToOp(werr, "land plan was not found")
		}
		if d.Queue != nil {
			worker := land.Worker{Queue: d.Queue}
			if _, qerr := worker.EnqueueRun(ctx, land.Key{RepoID: rc.row.ID, Target: planTarget}, out.Run.Plan, out.Run.ID); qerr != nil {
				return LandRunCreateOut{}, op.Wrap(op.CodeInternal, qerr, "land job was not queued")
			}
		}
		return out, nil
	}
}

func landRunListHandler(d Deps) func(context.Context, op.Principal, LandRunListIn) (LandRunListOut, error) {
	return func(ctx context.Context, p op.Principal, in LandRunListIn) (LandRunListOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return LandRunListOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return LandRunListOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.push", ""); derr != nil {
			return LandRunListOut{}, derr
		}
		out := LandRunListOut{Runs: []LandRunJSON{}}
		rerr = d.Store.Read(ctx, func(tx *store.ReadTx) error {
			plan, gerr := tx.ReadLandIntentByPublicID(rc.row.ID, in.Plan)
			if gerr != nil {
				return gerr
			}
			runs, lerr := tx.ListLandRunsByPlanID(plan.ID)
			if lerr != nil {
				return lerr
			}
			for _, run := range runs {
				out.Runs = append(out.Runs, LandRunJSON{ID: run.PublicID, Plan: plan.PublicID, State: run.State})
			}
			return nil
		})
		if rerr != nil {
			return LandRunListOut{}, storeToOp(rerr, "land plan was not found")
		}
		return out, nil
	}
}

// AdminLandRunCreateBody carries one privileged land execution.
type AdminLandRunCreateBody struct {
	Plan   string `json:"plan" minLength:"1" maxLength:"64"`
	Reason string `json:"reason" minLength:"1" maxLength:"1024"`
}

// AdminLandRunCreateIn carries one admin land run request.
type AdminLandRunCreateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           AdminLandRunCreateBody
}

// AdminLandRunCreateOut carries the privileged run.
type AdminLandRunCreateOut struct {
	Run LandRunJSON `json:"run"`
}

func adminLandRunCreateHandler(d Deps) func(context.Context, op.Principal, AdminLandRunCreateIn) (AdminLandRunCreateOut, error) {
	return func(ctx context.Context, p op.Principal, in AdminLandRunCreateIn) (AdminLandRunCreateOut, error) {
		admin, aerr := requireAdmin(ctx, d.Service, p)
		if aerr != nil {
			return AdminLandRunCreateOut{}, aerr
		}
		if strings.TrimSpace(in.Body.Reason) == "" {
			return AdminLandRunCreateOut{}, op.Errorf(op.CodeValidation, "reason must not be empty")
		}
		var planID int64
		var planPublic string
		rerr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			plan, gerr := tx.ReadLandIntentByPublic(in.Body.Plan)
			if gerr != nil {
				return gerr
			}
			planID = plan.ID
			planPublic = plan.PublicID
			return nil
		})
		if rerr != nil {
			return AdminLandRunCreateOut{}, storeToOp(rerr, "land plan was not found")
		}
		out := AdminLandRunCreateOut{}
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			run, rerr := tx.InsertLandRun(d.now(), store.LandRunRow{
				PlanID: planID, State: "awaiting", Detail: "admin override by " + admin.Name + ": " + in.Body.Reason,
			})
			if rerr != nil {
				return rerr
			}
			createdBody, _ := marshalJSON(map[string]any{
				"plan": planPublic, "admin": admin.Name, "reason": in.Body.Reason,
			})
			if _, rerr := tx.Outbox().Emit(store.Event{
				Kind: "admin.land_run_created", Body: createdBody, OccurredAt: d.now(),
			}); rerr != nil {
				return rerr
			}
			out.Run = LandRunJSON{ID: run.PublicID, Plan: planPublic, State: "awaiting"}
			return nil
		})
		if werr != nil {
			return AdminLandRunCreateOut{}, storeToOp(werr, "land plan was not found")
		}
		return out, nil
	}
}

// PolicyGetIn carries one policy read.
type PolicyGetIn struct {
	Owner string `path:"owner"`
	Repo  string `path:"repo"`
}

// PolicyRuleJSON is one bookmark rule.
type PolicyRuleJSON struct {
	Pattern    string `json:"pattern"`
	Mode       string `json:"mode"`
	Required   string `json:"required"`
	Version    int64  `json:"version"`
	Bypassable bool   `json:"bypass_admin,omitzero"`
}

// PolicyGetOut carries the bookmark policy of a repository.
type PolicyGetOut struct {
	Rules []PolicyRuleJSON `json:"rules"`
}

func policyListHandler(d Deps) func(context.Context, op.Principal, PolicyGetIn) (PolicyGetOut, error) {
	return func(ctx context.Context, p op.Principal, in PolicyGetIn) (PolicyGetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return PolicyGetOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return PolicyGetOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
			return PolicyGetOut{}, derr
		}
		resolver := access.NewSQLResolver(d.Service.DB(), access.NewStoreTeams(d.Service.DB(), nil))
		out := PolicyGetOut{Rules: []PolicyRuleJSON{}}
		for _, rule := range resolver.Rules(rc.row.ID) {
			required, _ := marshalJSON(rule.Required)
			out.Rules = append(out.Rules, PolicyRuleJSON{
				Pattern: rule.Pattern, Mode: rule.Mode,
				Required: string(required), Version: rule.Version, Bypassable: rule.BypassAdmin,
			})
		}
		return out, nil
	}
}

// PolicySetBody carries one policy update.
type PolicySetBody struct {
	Pattern  string `json:"pattern" minLength:"1" maxLength:"255"`
	Mode     string `json:"mode" minLength:"1" maxLength:"16"`
	Required string `json:"required,omitempty"`
}

// PolicySetIn carries one policy set request.
type PolicySetIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Body           PolicySetBody
}

// PolicySetOut carries the updated rule.
type PolicySetOut struct {
	Rule PolicyRuleJSON `json:"rule"`
}

func policySetHandler(d Deps) func(context.Context, op.Principal, PolicySetIn) (PolicySetOut, error) {
	return func(ctx context.Context, p op.Principal, in PolicySetIn) (PolicySetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return PolicySetOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return PolicySetOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.protect", ""); derr != nil {
			return PolicySetOut{}, derr
		}
		switch in.Body.Mode {
		case "Open", "LandOnly":
		default:
			return PolicySetOut{}, op.Errorf(op.CodeValidation, "mode must be Open or LandOnly")
		}
		required := in.Body.Required
		if required == "" {
			required = review.DefaultRequired()
		}
		var reqs []review.Requirement
		if uerr := unmarshalJSON([]byte(required), &reqs); uerr != nil {
			return PolicySetOut{}, op.Errorf(op.CodeValidation, "required is not valid requirement JSON")
		}
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return PolicySetOut{}, oerr
		}
		out := PolicySetOut{}
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			version, uerr := tx.UpdateBookmarkRule(rc.row.ID, in.Body.Pattern, in.Body.Mode, required)
			if uerr != nil {
				return uerr
			}
			var reply map[string]any
			args := map[string]any{"requirements": reqs}
			if perr := review.PendingPolicySet(ctx, reviewCaller{d}, daemon, args, &reply); perr != nil {
				return perr
			}
			out.Rule = PolicyRuleJSON{
				Pattern: in.Body.Pattern, Mode: in.Body.Mode,
				Required: required, Version: version,
			}
			return nil
		})
		if werr != nil {
			return PolicySetOut{}, storeToOp(werr, "bookmark rule was not found")
		}
		return out, nil
	}
}

// ViewCreateBody carries one saved query.
type ViewCreateBody struct {
	Name       string `json:"name" minLength:"1" maxLength:"100"`
	Revset     string `json:"revset,omitempty" maxLength:"4096"`
	Predicates string `json:"predicates,omitempty" maxLength:"4096"`
	Shared     bool   `json:"shared,omitzero"`
}

// ViewCreateIn carries one view create request.
type ViewCreateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Body           ViewCreateBody
}

// ViewJSON is one saved query head.
type ViewJSON struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version int64  `json:"version"`
	Shared  bool   `json:"shared,omitzero"`
}

// ViewCreateOut carries the created view.
type ViewCreateOut struct {
	View ViewJSON `json:"view"`
}

// ViewGetIn carries one view read.
type ViewGetIn struct {
	Owner string `path:"owner"`
	Repo  string `path:"repo"`
	View  string `path:"view"`
}

// ViewGetOut carries one view with its head version.
type ViewGetOut struct {
	Revset     string   `json:"revset,omitempty"`
	Predicates string   `json:"predicates,omitempty"`
	View       ViewJSON `json:"view"`
}

// ViewListIn carries one view list request.
type ViewListIn struct {
	Owner string `path:"owner"`
	Repo  string `path:"repo"`
}

// ViewListOut carries the views of a repository.
type ViewListOut struct {
	Views []ViewJSON `json:"views"`
}

// ViewUpdateBody carries one view update.
type ViewUpdateBody struct {
	Shared     *bool  `json:"shared,omitempty"`
	Revset     string `json:"revset,omitempty" maxLength:"4096"`
	Predicates string `json:"predicates,omitempty" maxLength:"4096"`
}

// ViewUpdateIn carries one view update request.
type ViewUpdateIn struct {
	Body           ViewUpdateBody
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	View           string `path:"view"`
}

// ViewUpdateOut carries the updated view.
type ViewUpdateOut struct {
	View ViewJSON `json:"view"`
}

// ViewDeleteIn carries one view delete request.
type ViewDeleteIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	View           string `path:"view"`
}

// ViewDeleteOut carries the delete receipt.
type ViewDeleteOut struct {
	Deleted bool `json:"deleted,omitzero"`
}

// ViewVisibilityBody carries one visibility update.
type ViewVisibilityBody struct {
	Shared bool `json:"shared,omitzero"`
}

// ViewVisibilityIn carries one view visibility request.
type ViewVisibilityIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	View           string `path:"view"`
	Body           ViewVisibilityBody
}

// ViewVisibilityOut carries the updated view.
type ViewVisibilityOut struct {
	View ViewJSON `json:"view"`
}

// ViewResultListIn carries one view evaluation request.
type ViewResultListIn struct {
	Owner      string `path:"owner"`
	Repo       string `path:"repo"`
	View       string `path:"view"`
	Predicates string `query:"predicates"`
	Cursor     string `query:"cursor"`
	Limit      int    `query:"limit"`
}

// ViewResultRow is one evaluated gap row.
type ViewResultRow struct {
	Subject string    `json:"subject"`
	Head    string    `json:"head"`
	Gaps    []GapJSON `json:"gaps"`
}

// ViewResultListOut carries evaluated view rows.
type ViewResultListOut struct {
	Next string          `json:"-"`
	Rows []ViewResultRow `json:"rows"`
}

var viewPredicateKeys = map[string]bool{
	"intent.state": true, "lease.holder": true, "assignee": true,
	"updated_since_ms": true, "limit": true,
}

func checkViewPredicates(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var predicates map[string]any
	if uerr := unmarshalJSON([]byte(raw), &predicates); uerr != nil {
		return op.Errorf(op.CodeValidation, "predicates must be a JSON object")
	}
	for key := range predicates {
		if viewPredicateKeys[key] {
			continue
		}
		if key == "revset" || strings.HasPrefix(key, "facet.") || strings.HasPrefix(key, "facet:") ||
			strings.HasPrefix(key, "gap.") || strings.HasPrefix(key, "gap:") {
			continue
		}
		return op.Errorf(op.CodeValidation, "predicate %q is not served", key)
	}
	return nil
}

func viewCreateHandler(d Deps) func(context.Context, op.Principal, ViewCreateIn) (ViewCreateOut, error) {
	return func(ctx context.Context, p op.Principal, in ViewCreateIn) (ViewCreateOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ViewCreateOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return ViewCreateOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "change.view", ""); derr != nil {
			return ViewCreateOut{}, derr
		}
		if !rq.authed || rq.user.ID == 0 {
			return ViewCreateOut{}, op.Errorf(op.CodeUnauthorized, "authentication is required")
		}
		if perr := checkViewPredicates(in.Body.Predicates); perr != nil {
			return ViewCreateOut{}, perr
		}
		out := ViewCreateOut{}
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			row, ierr := tx.InsertView(d.now(), store.ViewRow{
				RepoID: rc.row.ID, Name: in.Body.Name,
				OwnerUserID: sql.NullInt64{Int64: rq.user.ID, Valid: true},
				Shared:      in.Body.Shared,
				Revset:      in.Body.Revset, Predicates: in.Body.Predicates,
			})
			if ierr != nil {
				return ierr
			}
			out.View = ViewJSON{ID: row.PublicID, Name: row.Name, Version: row.Version, Shared: row.Shared}
			return nil
		})
		if werr != nil {
			return ViewCreateOut{}, storeToOp(werr, "view was not created")
		}
		return out, nil
	}
}

func viewGetHandler(d Deps) func(context.Context, op.Principal, ViewGetIn) (ViewGetOut, error) {
	return func(ctx context.Context, p op.Principal, in ViewGetIn) (ViewGetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ViewGetOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return ViewGetOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "change.view", ""); derr != nil {
			return ViewGetOut{}, derr
		}
		out := ViewGetOut{}
		rerr = d.Store.Read(ctx, func(tx *store.ReadTx) error {
			row, gerr := tx.ReadViewByPublicID(rc.row.ID, in.View)
			if gerr != nil {
				return gerr
			}
			out.View = ViewJSON{ID: row.PublicID, Name: row.Name, Version: row.Version, Shared: row.Shared}
			out.Revset = row.Revset
			out.Predicates = row.Predicates
			return nil
		})
		if rerr != nil {
			return ViewGetOut{}, storeToOp(rerr, "view was not found")
		}
		return out, nil
	}
}

func viewListHandler(d Deps) func(context.Context, op.Principal, ViewListIn) (ViewListOut, error) {
	return func(ctx context.Context, p op.Principal, in ViewListIn) (ViewListOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ViewListOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return ViewListOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "change.view", ""); derr != nil {
			return ViewListOut{}, derr
		}
		out := ViewListOut{Views: []ViewJSON{}}
		rerr = d.Store.Read(ctx, func(tx *store.ReadTx) error {
			rows, lerr := tx.ListViewsByRepo(rc.row.ID)
			if lerr != nil {
				return lerr
			}
			for _, row := range rows {
				out.Views = append(out.Views, ViewJSON{ID: row.PublicID, Name: row.Name, Version: row.Version, Shared: row.Shared})
			}
			return nil
		})
		if rerr != nil {
			return ViewListOut{}, op.Wrap(op.CodeInternal, rerr, "view list failed")
		}
		return out, nil
	}
}

func viewUpdateHandler(d Deps) func(context.Context, op.Principal, ViewUpdateIn) (ViewUpdateOut, error) {
	return func(ctx context.Context, p op.Principal, in ViewUpdateIn) (ViewUpdateOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ViewUpdateOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return ViewUpdateOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "change.view", ""); derr != nil {
			return ViewUpdateOut{}, derr
		}
		if !rq.authed || rq.user.ID == 0 {
			return ViewUpdateOut{}, op.Errorf(op.CodeUnauthorized, "authentication is required")
		}
		if perr := checkViewPredicates(in.Body.Predicates); perr != nil {
			return ViewUpdateOut{}, perr
		}
		out := ViewUpdateOut{}
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			row, gerr := tx.GetViewByPublicID(rc.row.ID, in.View)
			if gerr != nil {
				return gerr
			}
			if row.OwnerUserID.Valid && row.OwnerUserID.Int64 != rq.user.ID {
				return op.Errorf(op.CodeForbidden, "only the view owner edits a view")
			}
			revset := cmp.Or(in.Body.Revset, row.Revset)
			predicates := cmp.Or(in.Body.Predicates, row.Predicates)
			version, aerr := tx.AppendViewVersion(d.now(), row.ID, revset, predicates)
			if aerr != nil {
				return aerr
			}
			shared := row.Shared
			if in.Body.Shared != nil {
				if serr := tx.SetViewShared(d.now(), row.ID, *in.Body.Shared); serr != nil {
					return serr
				}
				shared = *in.Body.Shared
			}
			out.View = ViewJSON{ID: row.PublicID, Name: row.Name, Version: version, Shared: shared}
			return nil
		})
		if werr != nil {
			return ViewUpdateOut{}, storeToOp(werr, "view was not found")
		}
		return out, nil
	}
}

func viewDeleteHandler(d Deps) func(context.Context, op.Principal, ViewDeleteIn) (ViewDeleteOut, error) {
	return func(ctx context.Context, p op.Principal, in ViewDeleteIn) (ViewDeleteOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ViewDeleteOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return ViewDeleteOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "change.view", ""); derr != nil {
			return ViewDeleteOut{}, derr
		}
		if !rq.authed || rq.user.ID == 0 {
			return ViewDeleteOut{}, op.Errorf(op.CodeUnauthorized, "authentication is required")
		}
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			row, gerr := tx.GetViewByPublicID(rc.row.ID, in.View)
			if gerr != nil {
				return gerr
			}
			if row.OwnerUserID.Valid && row.OwnerUserID.Int64 != rq.user.ID {
				return op.Errorf(op.CodeForbidden, "only the view owner deletes a view")
			}
			return tx.DeleteView(row.ID)
		})
		if werr != nil {
			return ViewDeleteOut{}, storeToOp(werr, "view was not found")
		}
		return ViewDeleteOut{Deleted: true}, nil
	}
}

func viewVisibilityHandler(d Deps) func(context.Context, op.Principal, ViewVisibilityIn) (ViewVisibilityOut, error) {
	return func(ctx context.Context, p op.Principal, in ViewVisibilityIn) (ViewVisibilityOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ViewVisibilityOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return ViewVisibilityOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "change.view", ""); derr != nil {
			return ViewVisibilityOut{}, derr
		}
		if !rq.authed || rq.user.ID == 0 {
			return ViewVisibilityOut{}, op.Errorf(op.CodeUnauthorized, "authentication is required")
		}
		out := ViewVisibilityOut{}
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			row, gerr := tx.GetViewByPublicID(rc.row.ID, in.View)
			if gerr != nil {
				return gerr
			}
			if row.OwnerUserID.Valid && row.OwnerUserID.Int64 != rq.user.ID {
				return op.Errorf(op.CodeForbidden, "only the view owner shares a view")
			}
			if serr := tx.SetViewShared(d.now(), row.ID, in.Body.Shared); serr != nil {
				return serr
			}
			out.View = ViewJSON{ID: row.PublicID, Name: row.Name, Version: row.Version, Shared: in.Body.Shared}
			return nil
		})
		if werr != nil {
			return ViewVisibilityOut{}, storeToOp(werr, "view was not found")
		}
		return out, nil
	}
}

func viewResultListHandler(d Deps) func(context.Context, op.Principal, ViewResultListIn) (ViewResultListOut, error) {
	return func(ctx context.Context, p op.Principal, in ViewResultListIn) (ViewResultListOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ViewResultListOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return ViewResultListOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "change.view", ""); derr != nil {
			return ViewResultListOut{}, derr
		}
		if !strings.Contains(in.Predicates, "gap:") {
			return ViewResultListOut{}, op.Errorf(op.CodeValidation, "only gap predicates are served")
		}
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return ViewResultListOut{}, oerr
		}
		limit := clampLimit(in.Limit, 30, 100)
		out := ViewResultListOut{Rows: []ViewResultRow{}}
		rerr = d.Store.Read(ctx, func(tx *store.ReadTx) error {
			rows, lerr := tx.ListChanges(rc.row.ID, "open", limit)
			if lerr != nil {
				return lerr
			}
			for _, row := range rows {
				gaps, gerr := d.liveGaps(ctx, daemon, rc.row.ID, row)
				if gerr != nil {
					return gerr
				}
				if len(gaps) == 0 {
					continue
				}
				entry := ViewResultRow{Subject: row.ChangeKey, Head: row.HeadOid, Gaps: []GapJSON{}}
				for _, gap := range gaps {
					entry.Gaps = append(entry.Gaps, GapJSON{Kind: gap.Kind, Reason: gap.Reason, Detail: gap.Detail, NextOp: gap.NextOp})
				}
				out.Rows = append(out.Rows, entry)
			}
			return nil
		})
		if rerr != nil {
			return ViewResultListOut{}, storeToOp(rerr, "view evaluation failed")
		}
		return out, nil
	}
}
