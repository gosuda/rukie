package api

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gosuda/rukie/internal/op"
)

// RegisterReview registers the F2 review and land operations.
func RegisterReview(t *op.Table, deps Deps) error {
	if err := op.Register(t, op.Op[CompareGetIn, CompareGetOut]{
		Name: "code.compare.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/compare/{range}",
		Scope: op.ScopeRepoRead, Summary: "Compare two revisions",
		Description: "Merge-base, ahead, and behind bounded by the daemon compare cap. " +
			"The a...b alias normalizes to a::b.",
		Problems: []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out CompareGetOut) string {
			return fmt.Sprintf("\"compare-%s-%s-%d-%d\"", out.Base, out.Head, out.Ahead, out.Behind)
		},
		Handler: compareGetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ChangeListIn, ChangeListOut]{
		Name: "change.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/changes",
		Scope: op.ScopeRepoRead, Summary: "List changes",
		Description: "Open, landed, abandoned, or conflicted changes of one repository, newest first.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out ChangeListOut) string {
			return fmt.Sprintf("\"changes-%d-%s\"", len(out.Changes), out.Next)
		},
		Pagination: &op.Pagination[ChangeListOut]{Default: 30, Max: 100, Next: func(out ChangeListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: changeListHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ChangeGetIn, ChangeGetOut]{
		Name: "change.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/changes/{change}",
		Scope: op.ScopeRepoRead, Summary: "Show a change",
		Description: "One change with every recorded patchset head. The head is an exact OID.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out ChangeGetOut) string {
			return fmt.Sprintf("\"change-%s-%s\"", out.Change.Key, out.Change.Head)
		},
		Handler: changeGetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ChangeDiffIn, ChangeDiffOut]{
		Name: "change.diff.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/changes/{change}/diff/{range}",
		Scope: op.ScopeRepoRead, Summary: "Diff two patchsets",
		Description: "Unified patches between two patchset ordinals or OIDs of one change.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out ChangeDiffOut) string {
			return fmt.Sprintf("\"changediff-%s-%s-%d\"", out.Left, out.Right, len(out.Files))
		},
		Pagination: &op.Pagination[ChangeDiffOut]{Default: 100, Max: 1000, Next: func(out ChangeDiffOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: changeDiffGetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ChangeBlameIn, ChangeBlameOut]{
		Name: "change.blame.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/changes/{change}/blame",
		Scope: op.ScopeRepoRead, Summary: "Blame a file at a change head",
		Description: "Line hunks with introducing commits at the current head of a change.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out ChangeBlameOut) string {
			return fmt.Sprintf("\"changeblame-%s-%s-%d\"", out.Head, out.Path, len(out.Hunks))
		},
		Pagination: &op.Pagination[ChangeBlameOut]{Default: 100, Max: 1000, Next: func(out ChangeBlameOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: changeBlameGetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ChangeStackIn, ChangeStackOut]{
		Name: "change.stack.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/changes/{change}/stack",
		Scope: op.ScopeRepoRead, Summary: "List the stack of a change",
		Description: "Open changes on the same target branch, ordered bottom to top of stack.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out ChangeStackOut) string {
			return fmt.Sprintf("\"stack-%s-%d\"", out.Target, len(out.Changes))
		},
		Handler: changeStackHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[BranchChangeListIn, BranchChangeListOut]{
		Name: "branch.change.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/branches/{branch}/changes",
		Scope: op.ScopeRepoRead, Summary: "Show the branch view",
		Description: "Tip, recent log, and open stacked changes of one branch with the land-queue position.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out BranchChangeListOut) string {
			return fmt.Sprintf("\"branchchanges-%s-%d\"", out.Target, len(out.Changes))
		},
		Handler: branchChangeListHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[CommentListIn, CommentListOut]{
		Name: "change.comment.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/changes/{change}/comments",
		Scope: op.ScopeRepoRead, Summary: "List change comments",
		Description: "Live comments anchored to the current head. Unmatched fallback rows arrive only on request.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out CommentListOut) string {
			return fmt.Sprintf("\"comments-%s-%d\"", out.Head, len(out.Comments))
		},
		Handler: commentListHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[CommentCreateIn, CommentCreateOut]{
		Name: "change.comment.create", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/changes/{change}/comments",
		Scope: op.ScopeRepoWrite, Summary: "Comment on a change",
		Description: "Anchor a comment to the current head at a path and line, or to the whole change.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeValidation, op.CodeForbidden, op.CodeConflict},
		Location:    func(CommentCreateOut) string { return "" },
		Handler:     commentCreateHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ReviewCreateIn, ReviewCreateOut]{
		Name: "change.review.create", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/changes/{change}/reviews",
		Scope: op.ScopeRepoWrite, Summary: "Submit a review",
		Description: "Approve, block, or comment. Model reviews never satisfy a human-approval requirement. " +
			"Self-approval is refused.",
		Problems: []op.Code{op.CodeNotFound, op.CodeValidation, op.CodeForbidden, op.CodeConflict},
		Location: func(ReviewCreateOut) string { return "" },
		Handler:  reviewCreateHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ChangeStateIn, ChangeStateOut]{
		Name: "change.state.set", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/changes/{change}/state",
		Scope: op.ScopeRepoWrite, Summary: "Open or abandon a change",
		Description: "Landed changes never reopen.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeValidation, op.CodeForbidden, op.CodeConflict},
		Location:    func(ChangeStateOut) string { return "" },
		Handler:     changeStateHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ProofListIn, ProofListOut]{
		Name: "proof.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/changes/{change}/proofs",
		Scope: op.ScopeRepoRead, Summary: "List change proofs",
		Description: "Live attestations of the current head read from Ruk at evaluation time.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out ProofListOut) string {
			return fmt.Sprintf("\"proofs-%s-%d\"", out.Head, len(out.Proofs))
		},
		Handler: proofListHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[LandPlanCreateIn, LandPlanCreateOut]{
		Name: "land.plan.create", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/land/plans",
		Scope: op.ScopeRepoWrite, Summary: "Prepare a land plan",
		Description: "Snapshot the candidate, the base, and the evaluated gaps. Requirements unmet later fail the run.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeValidation, op.CodeForbidden, op.CodeConflict},
		Location:    func(LandPlanCreateOut) string { return "" },
		Handler:     landPlanCreateHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[LandPlanGetIn, LandPlanGetOut]{
		Name: "land.plan.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/land/plans/{plan}",
		Scope: op.ScopeRepoRead, Summary: "Show a land plan",
		Description: "One land plan with the gaps recorded at prepare time.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeForbidden},
		ETag: func(out LandPlanGetOut) string {
			return fmt.Sprintf("\"landplan-%s-%s\"", out.Plan.ID, out.Plan.State)
		},
		Handler: landPlanGetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[LandPlanListIn, LandPlanListOut]{
		Name: "land.plan.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/land/plans",
		Scope: op.ScopeRepoRead, Summary: "List land plans",
		Description: "Land plans for one target ref, newest first.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeValidation, op.CodeForbidden},
		ETag: func(out LandPlanListOut) string {
			return fmt.Sprintf("\"landplans-%d\"", len(out.Plans))
		},
		Handler: landPlanListHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[LandRunCreateIn, LandRunCreateOut]{
		Name: "land.run.create", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/land/plans/{plan}/runs",
		Scope: op.ScopeRepoWrite, Summary: "Execute a land plan",
		Description: "Unmet requirements fail conflict with the gap list. A second execution after drift fails stale.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeValidation, op.CodeForbidden, op.CodeConflict, op.CodeStale},
		Location:    func(LandRunCreateOut) string { return "" },
		Handler:     landRunCreateHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[LandRunListIn, LandRunListOut]{
		Name: "land.run.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/land/plans/{plan}/runs",
		Scope: op.ScopeRepoRead, Summary: "List land runs",
		Description: "Every recorded execution attempt of one land plan.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeForbidden},
		ETag: func(out LandRunListOut) string {
			return fmt.Sprintf("\"landruns-%d\"", len(out.Runs))
		},
		Handler: landRunListHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[AdminLandRunCreateIn, AdminLandRunCreateOut]{
		Name: "admin.land.run", Kind: op.KindMutation, Method: http.MethodPost, Path: "/admin/land/runs",
		Scope: op.ScopeAdminWrite, Summary: "Execute a land plan as site admin",
		Description: "Waives the bookmark requirements with a non-empty reason. Audit-logged with the waived requirements.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeValidation, op.CodeForbidden},
		Location:    func(AdminLandRunCreateOut) string { return "" },
		Handler:     adminLandRunCreateHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[PolicyGetIn, PolicyGetOut]{
		Name: "repo.policy.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/settings/protected",
		Scope: op.ScopeRepoRead, Summary: "List the bookmark policy",
		Description: "Protected ref patterns with required attestation kinds.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeForbidden},
		ETag: func(out PolicyGetOut) string {
			return fmt.Sprintf("\"policy-%d\"", len(out.Rules))
		},
		Handler: policyListHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[PolicySetIn, PolicySetOut]{
		Name: "repo.policy.set", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/settings/protected",
		Scope: op.ScopeRepoWrite, Summary: "Set the bookmark policy",
		Description: "Replace one bookmark rule and bump the policy generation. Bypass admin is rejected in v1.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeValidation, op.CodeForbidden},
		Location:    func(PolicySetOut) string { return "" },
		Handler:     policySetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ViewListIn, ViewListOut]{
		Name: "view.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/views",
		Scope: op.ScopeRepoRead, Summary: "List saved views",
		Description: "Named saved queries of one repository.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeForbidden},
		ETag: func(out ViewListOut) string {
			return fmt.Sprintf("\"views-%d\"", len(out.Views))
		},
		Handler: viewListHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ViewCreateIn, ViewCreateOut]{
		Name: "view.create", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/views",
		Scope: op.ScopeIntentsWrite, Summary: "Save a view",
		Description: "Name a closed-predicate query. Unknown predicate keys are rejected.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeValidation, op.CodeForbidden},
		Location:    func(ViewCreateOut) string { return "" },
		Handler:     viewCreateHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ViewGetIn, ViewGetOut]{
		Name: "view.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/views/{view}",
		Scope: op.ScopeRepoRead, Summary: "Show a saved view",
		Description: "One saved view with its head version.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeForbidden},
		ETag: func(out ViewGetOut) string {
			return fmt.Sprintf("\"view-%s-%d\"", out.View.ID, out.View.Version)
		},
		Handler: viewGetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ViewUpdateIn, ViewUpdateOut]{
		Name: "view.update", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/views/{view}/update",
		Scope: op.ScopeIntentsWrite, Summary: "Update a saved view",
		Description: "Pin one more immutable version. Only the view owner edits.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeValidation, op.CodeForbidden},
		Location:    func(ViewUpdateOut) string { return "" },
		Handler:     viewUpdateHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ViewDeleteIn, ViewDeleteOut]{
		Name: "view.delete", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/views/{view}/delete",
		Scope: op.ScopeIntentsWrite, Summary: "Delete a saved view",
		Description: "Remove a saved view and its versions. Only the view owner deletes.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeForbidden},
		Location:    func(ViewDeleteOut) string { return "" },
		Handler:     viewDeleteHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ViewVisibilityIn, ViewVisibilityOut]{
		Name: "view.visibility.set", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/views/{view}/visibility",
		Scope: op.ScopeIntentsWrite, Summary: "Share a saved view",
		Description: "Flip the sharing of a saved view. Only the view owner shares.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeForbidden},
		Location:    func(ViewVisibilityOut) string { return "" },
		Handler:     viewVisibilityHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ViewResultListIn, ViewResultListOut]{
		Name: "view.result.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/views/{view}/results",
		Scope: op.ScopeRepoRead, Summary: "Evaluate a saved view",
		Description: "Gap work-list rows computed from live attestations, never cached.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeValidation, op.CodeForbidden},
		ETag: func(out ViewResultListOut) string {
			return fmt.Sprintf("\"viewresults-%d\"", len(out.Rows))
		},
		Handler: viewResultListHandler(deps),
	}); err != nil {
		return err
	}
	return nil
}

// RegisterReviewViews registers the zero-JS review, land, and policy pages.
func RegisterReviewViews(v *op.Views) {
	op.PageView(v, "code.compare.get", writeCompare)
	op.PageView(v, "change.list", writeChanges)
	op.PageView(v, "change.get", writeChange)
	op.PageView(v, "change.diff.get", writeChangeDiff)
	op.PageView(v, "change.blame.get", writeChangeBlame)
	op.PageView(v, "change.stack.list", writeChangeStack)
	op.PageView(v, "branch.change.list", writeBranchChanges)
	op.PageView(v, "change.comment.list", writeComments)
	op.PageView(v, "proof.list", writeProofs)
	op.PageView(v, "land.plan.get", writeLandPlan)
	op.PageView(v, "land.plan.list", writeLandPlans)
	op.PageView(v, "land.run.list", writeLandRuns)
	op.PageView(v, "repo.policy.list", writePolicy)
	op.PageView(v, "view.list", writeViews)
	op.PageView(v, "view.get", writeView)
	op.PageView(v, "view.result.list", writeViewResults)
	v.Form("change.comment.create", func(w io.Writer, c op.ViewCtx) error {
		return writeChange(w, c, ChangeGetOut{})
	})
	v.Form("change.review.create", func(w io.Writer, c op.ViewCtx) error {
		return writeChange(w, c, ChangeGetOut{})
	})
	v.Form("change.state.set", func(w io.Writer, c op.ViewCtx) error {
		return writeChange(w, c, ChangeGetOut{})
	})
	v.Form("land.plan.create", func(w io.Writer, c op.ViewCtx) error {
		return writeLandPlans(w, c, LandPlanListOut{})
	})
	v.Form("land.run.create", func(w io.Writer, c op.ViewCtx) error {
		return writeLandPlan(w, c, LandPlanGetOut{})
	})
	v.Form("admin.land.run", func(w io.Writer, c op.ViewCtx) error {
		return writeLandPlan(w, c, LandPlanGetOut{})
	})
	v.Form("repo.policy.set", func(w io.Writer, c op.ViewCtx) error {
		return writePolicy(w, c, PolicyGetOut{})
	})
	v.Form("view.create", func(w io.Writer, c op.ViewCtx) error {
		return writeViews(w, c, ViewListOut{})
	})
	v.Form("view.update", func(w io.Writer, c op.ViewCtx) error {
		return writeView(w, c, ViewGetOut{})
	})
	v.Form("view.delete", func(w io.Writer, c op.ViewCtx) error {
		return writeViews(w, c, ViewListOut{})
	})
	v.Form("view.visibility.set", func(w io.Writer, c op.ViewCtx) error {
		return writeView(w, c, ViewGetOut{})
	})
}

func writeCompare(w io.Writer, c op.ViewCtx, out CompareGetOut) error {
	var b strings.Builder
	b.WriteString(heading("Compare " + out.Base + " and " + out.Head))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<p>Ahead: " + esc(strconv.FormatInt(out.Ahead, 10)) + " Behind: " + esc(strconv.FormatInt(out.Behind, 10)) + "</p>\n")
	_, err := io.WriteString(w, pageShell("Compare", b.String()))
	return err
}

func writeChanges(w io.Writer, c op.ViewCtx, out ChangeListOut) error {
	var b strings.Builder
	b.WriteString(heading("Changes"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<ul>\n")
	for _, change := range out.Changes {
		href := c.Path + "/" + change.Key
		b.WriteString("<li><a href=\"" + esc(href) + "\">" + esc(change.Key) + "</a> — " +
			esc(change.TargetBranch) + " " + esc(change.State) + " " + esc(change.Head) + "</li>\n")
	}
	b.WriteString("</ul>\n")
	_, err := io.WriteString(w, pageShell("Changes", b.String()))
	return err
}

func writeChange(w io.Writer, c op.ViewCtx, out ChangeGetOut) error {
	var b strings.Builder
	b.WriteString(heading("Change " + out.Change.Key))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<p>Target: " + esc(out.Change.TargetBranch) + " State: " + esc(out.Change.State) + "</p>\n")
	b.WriteString("<p>Head: " + esc(out.Change.Head) + "</p>\n")
	b.WriteString("<h2>Patchsets</h2>\n<ul>\n")
	for i, head := range out.Heads {
		b.WriteString("<li>Patchset " + esc(strconv.Itoa(i+1)) + " — " + esc(head) + "</li>\n")
	}
	b.WriteString("</ul>\n")
	b.WriteString(pendingReviewBanner())
	b.WriteString("<h2>Comment</h2>\n" +
		formOpen(c.Path+"/comments", c.CSRF) +
		textField("path", "Path (empty for whole-change)", c.Input.Get("path"), false) +
		textField("line", "Line", c.Input.Get("line"), false) +
		textField("body", "Body", c.Input.Get("body"), false) +
		submitField("Add comment"))
	b.WriteString("<h2>Review</h2>\n" +
		formOpen(c.Path+"/reviews", c.CSRF) +
		textField("verdict", "Verdict (approve, block, comment)", c.Input.Get("verdict"), false) +
		textField("body", "Body", c.Input.Get("body"), false) +
		submitField("Submit review"))
	_, err := io.WriteString(w, pageShell("Change", b.String()))
	return err
}

func pendingReviewBanner() string {
	return "<p role=\"status\">Pending reviews are held server-side until submitted.</p>\n"
}

func writeChangeDiff(w io.Writer, c op.ViewCtx, out ChangeDiffOut) error {
	var b strings.Builder
	b.WriteString(heading("Change diff " + out.Left + ".." + out.Right))
	b.WriteString(errorList(c.Errors))
	for _, file := range out.Files {
		b.WriteString("<details><summary>" + esc(file.Path) + "</summary>\n<pre>" + esc(file.Patch) + "</pre>\n")
		b.WriteString(formOpen(c.Path+"/comments", c.CSRF) +
			hiddenField("path", file.Path) +
			textField("line", "Line", "", false) +
			textField("body", "Body", "", false) +
			submitField("Comment on a line"))
		b.WriteString("</details>\n")
	}
	_, err := io.WriteString(w, pageShell("Change diff", b.String()))
	return err
}

func writeChangeBlame(w io.Writer, c op.ViewCtx, out ChangeBlameOut) error {
	var b strings.Builder
	b.WriteString(heading("Blame " + out.Path + " at " + out.Head))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<ul>\n")
	for _, hunk := range out.Hunks {
		b.WriteString("<li>" + esc(strconv.FormatUint(uint64(hunk.StartLine), 10)) + ": " + esc(hunk.Author) + " " + esc(hunk.Head) + "</li>\n")
	}
	b.WriteString("</ul>\n")
	_, err := io.WriteString(w, pageShell("Change blame", b.String()))
	return err
}

func writeComments(w io.Writer, c op.ViewCtx, out CommentListOut) error {
	var b strings.Builder
	b.WriteString(heading("Comments at " + out.Head))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<ul>\n")
	for _, comment := range out.Comments {
		line := ""
		if comment.Line != nil {
			line = strconv.FormatInt(*comment.Line, 10)
		}
		path := ""
		if comment.Path != nil {
			path = *comment.Path
		}
		b.WriteString("<li>" + esc(path) + " " + esc(line) + " " + esc(comment.Body) + "</li>\n")
	}
	b.WriteString("</ul>\n")
	_, err := io.WriteString(w, pageShell("Comments", b.String()))
	return err
}

func writeProofs(w io.Writer, c op.ViewCtx, out ProofListOut) error {
	var b strings.Builder
	b.WriteString(heading("Proofs at " + out.Head))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<ul>\n")
	for _, proof := range out.Proofs {
		b.WriteString("<li>" + esc(proof.Kind) + " " + esc(proof.Result) + "</li>\n")
	}
	b.WriteString("</ul>\n")
	_, err := io.WriteString(w, pageShell("Proofs", b.String()))
	return err
}

func writeChangeStack(w io.Writer, c op.ViewCtx, out ChangeStackOut) error {
	var b strings.Builder
	b.WriteString(heading("Stack on " + out.Target))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<ol>\n")
	for _, change := range out.Changes {
		b.WriteString("<li>" + esc(change.Key) + " " + esc(change.State) + " " + esc(change.Head) + "</li>\n")
	}
	b.WriteString("</ol>\n")
	_, err := io.WriteString(w, pageShell("Stack", b.String()))
	return err
}

func writeBranchChanges(w io.Writer, c op.ViewCtx, out BranchChangeListOut) error {
	var b strings.Builder
	b.WriteString(heading("Branch " + out.Target))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<ul>\n")
	for _, change := range out.Changes {
		b.WriteString("<li>" + esc(change.Key) + " " + esc(change.State) + " " + esc(change.Head) + "</li>\n")
	}
	b.WriteString("</ul>\n")
	_, err := io.WriteString(w, pageShell("Branch changes", b.String()))
	return err
}

func writeLandPlans(w io.Writer, c op.ViewCtx, out LandPlanListOut) error {
	var b strings.Builder
	b.WriteString(heading("Land plans"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<ul>\n")
	for _, plan := range out.Plans {
		b.WriteString("<li>" + esc(plan.ID) + " " + esc(plan.Target) + " " + esc(plan.State) + "</li>\n")
	}
	b.WriteString("</ul>\n")
	_, err := io.WriteString(w, pageShell("Land plans", b.String()))
	return err
}

func writeLandRuns(w io.Writer, c op.ViewCtx, out LandRunListOut) error {
	var b strings.Builder
	b.WriteString(heading("Land runs"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<ul>\n")
	for _, run := range out.Runs {
		b.WriteString("<li>" + esc(run.ID) + " " + esc(run.State) + "</li>\n")
	}
	b.WriteString("</ul>\n")
	_, err := io.WriteString(w, pageShell("Land runs", b.String()))
	return err
}

func writeLandPlan(w io.Writer, c op.ViewCtx, out LandPlanGetOut) error {
	var b strings.Builder
	b.WriteString(heading("Land plan " + out.Plan.ID))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<p>Target: " + esc(out.Plan.Target) + " State: " + esc(out.Plan.State) + "</p>\n")
	b.WriteString("<p>Candidate: " + esc(out.Plan.Candidate) + " Base: " + esc(out.Plan.Base) + "</p>\n")
	for _, gap := range out.Plan.Gaps {
		b.WriteString("<p>Gap: " + esc(gap.Reason) + " — " + esc(gap.Detail) + "</p>\n")
	}
	b.WriteString(formOpen(c.Path+"/runs", c.CSRF) + submitField("Execute plan"))
	_, err := io.WriteString(w, pageShell("Land plan", b.String()))
	return err
}

func writePolicy(w io.Writer, c op.ViewCtx, out PolicyGetOut) error {
	var b strings.Builder
	b.WriteString(heading("Protected bookmarks"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<ul>\n")
	for _, rule := range out.Rules {
		b.WriteString("<li>" + esc(rule.Pattern) + " " + esc(rule.Mode) + " v" + esc(strconv.FormatInt(rule.Version, 10)) + "</li>\n")
	}
	b.WriteString("</ul>\n")
	_, err := io.WriteString(w, pageShell("Policy", b.String()))
	return err
}

func writeViews(w io.Writer, c op.ViewCtx, out ViewListOut) error {
	var b strings.Builder
	b.WriteString(heading("Views"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<ul>\n")
	for _, view := range out.Views {
		href := c.Path + "/" + view.ID
		b.WriteString("<li><a href=\"" + esc(href) + "\">" + esc(view.Name) + "</a> v" + esc(strconv.FormatInt(view.Version, 10)) + "</li>\n")
	}
	b.WriteString("</ul>\n")
	b.WriteString("<h2>Compose a view</h2>\n" +
		formOpen(c.Path, c.CSRF) +
		textField("name", "Name", c.Input.Get("name"), false) +
		textField("predicates", "Predicates (JSON)", c.Input.Get("predicates"), false) +
		submitField("Save view"))
	_, err := io.WriteString(w, pageShell("Views", b.String()))
	return err
}

func writeView(w io.Writer, c op.ViewCtx, out ViewGetOut) error {
	var b strings.Builder
	b.WriteString(heading("View " + out.View.Name))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<p>Version: " + esc(strconv.FormatInt(out.View.Version, 10)) + "</p>\n")
	if out.Predicates != "" {
		b.WriteString("<p>Predicates: " + esc(out.Predicates) + "</p>\n")
	}
	_, err := io.WriteString(w, pageShell("View", b.String()))
	return err
}

func writeViewResults(w io.Writer, c op.ViewCtx, out ViewResultListOut) error {
	var b strings.Builder
	b.WriteString(heading("View results"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<ul>\n")
	for _, row := range out.Rows {
		b.WriteString("<li>" + esc(row.Subject) + " " + esc(row.Head) + "\n<ul>\n")
		for _, gap := range row.Gaps {
			b.WriteString("<li>" + esc(gap.Reason) + " — " + esc(gap.Detail) + "</li>\n")
		}
		b.WriteString("</ul>\n</li>\n")
	}
	b.WriteString("</ul>\n")
	_, err := io.WriteString(w, pageShell("View results", b.String()))
	return err
}
