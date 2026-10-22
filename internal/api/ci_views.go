package api

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gosuda/rukie/internal/ci"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
	"github.com/gosuda/rukie/internal/web/secheaders"
)

// latestRun picks the badge run: the highest creation time, breaking ties
// toward the highest run id to match the ci.run.list ordering.
func latestRun(runs []ci.Run) *ci.Run {
	if len(runs) == 0 {
		return nil
	}
	latest := &runs[0]
	for i := range runs[1:] {
		if run := &runs[i+1]; run.CreatedAt > latest.CreatedAt ||
			(run.CreatedAt == latest.CreatedAt && run.ID > latest.ID) {
			latest = run
		}
	}
	return latest
}

// ciContentSecurityPolicy is the shared policy with scripts fully off:
// CI pages are zero-JS surfaces, so they agree with htmlcheck instead of
// inheriting the application script allowance.
var ciContentSecurityPolicy = strings.Replace(secheaders.CSP, "script-src 'self'", "script-src 'none'", 1)

func setCIPageHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", ciContentSecurityPolicy)
}

func writeCIRunList(w io.Writer, c op.ViewCtx, out CIRunListOut) error {
	var b strings.Builder
	b.WriteString(heading("CI runs"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<table>\n<tr><th>Run</th><th>Subject</th><th>Trust</th><th>State</th><th>Passed</th><th>Failed</th><th>Total</th></tr>\n")
	for _, run := range out.Runs {
		b.WriteString("<tr><td><a href=\"" + esc(c.Path+"/runs/"+run.ID) + "\">" + esc(run.ID) + "</a></td>" +
			"<td>" + esc(run.Subject) + "</td><td>" + esc(run.Trust) + "</td><td>" + esc(run.State) + "</td>" +
			"<td>" + esc(strconv.Itoa(run.Passed)) + "</td><td>" + esc(strconv.Itoa(run.Failed)) + "</td>" +
			"<td>" + esc(strconv.Itoa(run.Total)) + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	if out.Next != "" {
		b.WriteString("<p>More results are available through the Link header.</p>\n")
	}
	_, err := io.WriteString(w, pageShell("CI runs", b.String()))
	return err
}

func writeCIRunGet(w io.Writer, c op.ViewCtx, out CIRunGetOut) error {
	var b strings.Builder
	b.WriteString(heading("CI run " + out.Run.ID))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<p>Subject " + esc(out.Run.Subject) + ", trust " + esc(out.Run.Trust) +
		", state " + esc(out.Run.State) + ", trigger " + esc(out.Run.TriggerKey) + ".</p>\n")
	b.WriteString("<table>\n<tr><th>Job</th><th>State</th><th>Wait</th><th>Spec digest</th><th>Cache</th><th>Attempts</th></tr>\n")
	for _, job := range out.Jobs {
		cache := job.ActionDigest
		if cache == "" {
			cache = "none"
		}
		b.WriteString("<tr><td><a href=\"" + esc(c.Path+"/jobs/"+job.ID) + "\">" + esc(job.Name) + "</a></td>" +
			"<td>" + esc(job.State) + "</td><td>" + esc(job.WaitReason) + "</td>" +
			"<td>" + esc(job.SpecDigest) + "</td><td>" + esc(cache) + "</td>" +
			"<td>" + esc(strconv.Itoa(job.AttemptsUsed)) + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	if !runTerminal(out.Run.State) {
		b.WriteString("<h2>Cancel</h2>\n" + formOpen(c.Path+"/cancel", c.CSRF) +
			hiddenField("state", "cancelled") + textField("reason", "Reason", "", false) + submitField("Cancel run"))
	}
	b.WriteString("<h2>Retry</h2>\n" + formOpen(c.Path+"/retry", c.CSRF) +
		checkField("acknowledge_side_effects", "Acknowledge side effects", false) + submitField("Retry run"))
	_, err := io.WriteString(w, pageShell("CI run "+out.Run.ID, b.String()))
	return err
}

func runTerminal(state string) bool {
	switch state {
	case "succeeded", "failed", "errored", "cancelled":
		return true
	}
	return false
}

func writeCIJobList(w io.Writer, c op.ViewCtx, out CIJobListOut) error {
	var b strings.Builder
	b.WriteString(heading("CI jobs"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<table>\n<tr><th>Job</th><th>Run</th><th>State</th><th>Wait</th><th>Runs on</th><th>Ready</th></tr>\n")
	for _, job := range out.Jobs {
		b.WriteString("<tr><td>" + esc(job.Name) + "</td><td>" + esc(job.Run) + "</td>" +
			"<td>" + esc(job.State) + "</td><td>" + esc(job.WaitReason) + "</td>" +
			"<td>" + esc(strings.Join(job.RunsOn, ", ")) + "</td>" +
			"<td>" + esc(strconv.FormatInt(job.ReadyAt, 10)) + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	if out.Next != "" {
		b.WriteString("<p>More results are available through the Link header.</p>\n")
	}
	_, err := io.WriteString(w, pageShell("CI jobs", b.String()))
	return err
}

func writeCILogGet(w io.Writer, c op.ViewCtx, out CILogGetOut) error {
	refresh := 0
	if raw := c.Input.Get("refresh_seconds"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= ciRefreshMinSeconds {
			refresh = parsed
		}
	}
	if _, err := io.WriteString(w, ciLogPage(c.Path, out.Data, out.AttemptState, refresh)); err != nil {
		return err
	}
	return nil
}

func writeCIArtifactList(w io.Writer, c op.ViewCtx, out CIArtifactListOut) error {
	var b strings.Builder
	b.WriteString(heading("CI artifacts"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<table>\n<tr><th>Job</th><th>Name</th><th>Digest</th><th>Size</th></tr>\n")
	for _, artifact := range out.Artifacts {
		b.WriteString("<tr><td>" + esc(artifact.Job) + "</td>" +
			"<td><a href=\"" + esc(c.Path+"/"+artifact.Name) + "\">" + esc(artifact.Name) + "</a></td>" +
			"<td>" + esc(artifact.Digest) + "</td>" +
			"<td>" + esc(strconv.FormatInt(artifact.Size, 10)) + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	_, err := io.WriteString(w, pageShell("CI artifacts", b.String()))
	return err
}

func writeCIArtifactGet(w io.Writer, c op.ViewCtx, out CIArtifactGetOut) error {
	var b strings.Builder
	b.WriteString(heading("CI artifact"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<p>Name " + esc(out.Name) + ", digest " + esc(out.Digest) +
		", size " + esc(strconv.FormatInt(out.Size, 10)) + ".</p>\n" +
		"<p><a href=\"" + esc(out.DownloadURL) + "\">Download</a></p>\n")
	_, err := io.WriteString(w, pageShell("CI artifact", b.String()))
	return err
}

func writeCISpecGet(w io.Writer, c op.ViewCtx, out CISpecGetOut) error {
	var b strings.Builder
	b.WriteString(heading("CI spec"))
	b.WriteString(errorList(c.Errors))
	raw, _ := marshalJSON(out.Effective)
	b.WriteString("<pre>" + esc(string(raw)) + "</pre>\n")
	b.WriteString("<table>\n<tr><th>Job</th><th>Digest</th></tr>\n")
	for name, digest := range out.Digests {
		b.WriteString("<tr><td>" + esc(name) + "</td><td>" + esc(digest) + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	if len(out.Required) > 0 {
		b.WriteString("<p>Required proofs: " + esc(strings.Join(out.Required, ", ")) + ".</p>\n")
	}
	_, err := io.WriteString(w, pageShell("CI spec", b.String()))
	return err
}

func writeCIRunnerList(w io.Writer, c op.ViewCtx, out CIRunnerListOut) error {
	var b strings.Builder
	b.WriteString(heading("CI runners"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<table>\n<tr><th>ID</th><th>Labels</th><th>Scope</th><th>Capacity</th><th>Status</th></tr>\n")
	for _, runner := range out.Runners {
		b.WriteString("<tr><td>" + esc(runner.ID) + "</td><td>" + esc(strings.Join(runner.Labels, ", ")) + "</td>" +
			"<td>" + esc(runner.Scope) + "</td><td>" + esc(strconv.Itoa(runner.Capacity)) + "</td>" +
			"<td>" + esc(runner.Status) + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	_, err := io.WriteString(w, pageShell("CI runners", b.String()))
	return err
}

// RegisterCIViews registers the zero-JS CI pages.
func RegisterCIViews(v *op.Views) {
	op.PageView(v, "ci.run.list", writeCIRunList)
	op.PageView(v, "ci.run.get", writeCIRunGet)
	op.PageView(v, "ci.job.list", writeCIJobList)
	op.PageView(v, "ci.log.get", writeCILogGet)
	op.PageView(v, "ci.artifact.list", writeCIArtifactList)
	op.PageView(v, "ci.artifact.get", writeCIArtifactGet)
	op.PageView(v, "ci.spec.get", writeCISpecGet)
	op.PageView(v, "ci.runner.list", writeCIRunnerList)
	v.Form("ci.run.create", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Start a CI run") + errorList(c.Errors) +
			formOpen(c.Path, c.CSRF) +
			textField("subject", "Subject commit", "", false) +
			textField("tree", "Tree", "", false) +
			textField("ref", "Ref", "", false) +
			"<label>Spec (TOML)\n<textarea name=\"spec\" rows=\"12\" cols=\"72\"></textarea>\n</label>\n" +
			submitField("Start run")
		_, err := io.WriteString(w, pageShell("Start a CI run", body))
		return err
	})
	v.Form("ci.run.update", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Cancel the CI run") + errorList(c.Errors) +
			formOpen(c.Path, c.CSRF) +
			hiddenField("state", "cancelled") +
			textField("reason", "Reason", "", false) +
			submitField("Cancel run")
		_, err := io.WriteString(w, pageShell("Cancel the CI run", body))
		return err
	})
	v.Form("ci.run.retry", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Retry the CI run") + errorList(c.Errors) +
			formOpen(c.Path, c.CSRF) +
			checkField("acknowledge_side_effects", "Acknowledge side effects", false) +
			submitField("Retry run")
		_, err := io.WriteString(w, pageShell("Retry the CI run", body))
		return err
	})
}

type ciPageData struct {
	owner string
	repo  string
	jobs  []ci.Job
	run   ci.Run
}

func (d Deps) ciPrincipal(r *http.Request) op.Principal {
	return RawBytes{Deps: d}.principalFor(r)
}

func (d Deps) ciPageRun(ctx context.Context, p op.Principal, owner, repo, runID string) (ciPageData, error) {
	rq, err := d.requester(ctx, p)
	if err != nil {
		return ciPageData{}, err
	}
	rc, err := d.resolveRepo(ctx, rq, owner, repo, false)
	if err != nil {
		return ciPageData{}, err
	}
	if verr := d.checkRepoAccess(rq, rc.row, "ci.view", ""); verr != nil {
		return ciPageData{}, verr
	}
	ctrl, err := d.ciController()
	if err != nil {
		return ciPageData{}, err
	}
	run, jobs, err := ctrl.Run(ctx, runID)
	if err != nil {
		return ciPageData{}, ciToOp(err, "CI run was not found")
	}
	if run.RepoID != rc.row.ID {
		return ciPageData{}, op.Errorf(op.CodeNotFound, "CI run was not found")
	}
	return ciPageData{owner: owner, repo: repo, run: run, jobs: jobs}, nil
}

func (d Deps) serveCITab(w http.ResponseWriter, r *http.Request, owner, repo string) {
	ctx := r.Context()
	principal := d.ciPrincipal(r)
	rq, err := d.requester(ctx, principal)
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	rc, err := d.resolveRepo(ctx, rq, owner, repo, false)
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	if verr := d.checkRepoAccess(rq, rc.row, "ci.view", ""); verr != nil {
		writeRunnerProblem(w, verr)
		return
	}
	ctrl, err := d.ciController()
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	runs, err := ctrl.Runs(ctx, rc.row.ID)
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	out := CIRunListOut{Runs: []CIRunJSON{}}
	for _, run := range runs {
		_, jobs, jerr := ctrl.Run(ctx, run.ID)
		if jerr != nil {
			writeRunnerProblem(w, jerr)
			return
		}
		out.Runs = append(out.Runs, ciRunJSON(run, jobs))
	}
	var body strings.Builder
	if err := writeCIRunList(&body, op.ViewCtx{Path: "/" + owner + "/" + repo + "/ci"}, out); err != nil {
		writeRunnerProblem(w, err)
		return
	}
	setCIPageHeaders(w)
	_, _ = io.WriteString(w, body.String())
}

func (d Deps) serveCIRunPage(w http.ResponseWriter, r *http.Request, owner, repo, runID string) {
	data, err := d.ciPageRun(r.Context(), d.ciPrincipal(r), owner, repo, runID)
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	out := CIRunGetOut{Run: ciRunJSON(data.run, data.jobs), Jobs: []CIJobJSON{}}
	for _, job := range data.jobs {
		out.Jobs = append(out.Jobs, ciJobJSON(data.run, job))
	}
	var body strings.Builder
	base := "/" + owner + "/" + repo + "/ci/runs/" + runID
	if err := writeCIRunGet(&body, op.ViewCtx{Path: base}, out); err != nil {
		writeRunnerProblem(w, err)
		return
	}
	setCIPageHeaders(w)
	_, _ = io.WriteString(w, body.String())
}

func (d Deps) serveCIJobPage(w http.ResponseWriter, r *http.Request, owner, repo, runID, jobID string) {
	data, err := d.ciPageRun(r.Context(), d.ciPrincipal(r), owner, repo, runID)
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	var found *ci.Job
	for i := range data.jobs {
		if data.jobs[i].ID == jobID {
			found = &data.jobs[i]
			break
		}
	}
	if found == nil {
		writeRunnerProblem(w, op.Errorf(op.CodeNotFound, "CI job was not found"))
		return
	}
	ctrl, err := d.ciController()
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	attempts := []ci.Attempt{}
	if found.CurrentAttempt != "" {
		if attempt, _, _, aerr := ctrl.GetAttempt(r.Context(), found.CurrentAttempt); aerr == nil {
			attempts = append(attempts, attempt)
		}
	}
	var b strings.Builder
	b.WriteString(heading("CI job " + found.Spec.Name))
	b.WriteString("<p>State " + esc(found.State) + ", spec " + esc(found.SpecDigest) + ".</p>\n")
	b.WriteString("<table>\n<tr><th>Step</th><th>State</th><th>Exit</th></tr>\n")
	for _, attempt := range attempts {
		for _, step := range attempt.Steps {
			b.WriteString("<tr><td>" + esc(step.Name) + "</td><td>" + esc(step.State) + "</td>" +
				"<td>" + esc(strconv.Itoa(step.ExitCode)) + "</td></tr>\n")
		}
	}
	b.WriteString("</table>\n")
	base := "/" + owner + "/" + repo + "/ci/runs/" + runID + "/jobs/" + jobID
	b.WriteString("<p><a href=\"" + esc(base+"/log") + "\">Log</a></p>\n")
	setCIPageHeaders(w)
	_, _ = io.WriteString(w, pageShell("CI job "+found.Spec.Name, b.String()))
}

func (d Deps) serveCILog(w http.ResponseWriter, r *http.Request, owner, repo, runID, jobID string) {
	data, err := d.ciPageRun(r.Context(), d.ciPrincipal(r), owner, repo, runID)
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	var found *ci.Job
	for i := range data.jobs {
		if data.jobs[i].ID == jobID {
			found = &data.jobs[i]
			break
		}
	}
	if found == nil || found.CurrentAttempt == "" {
		writeRunnerProblem(w, op.Errorf(op.CodeNotFound, "CI attempt was not found"))
		return
	}
	ctrl, err := d.ciController()
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	attempt, _, _, err := ctrl.GetAttempt(r.Context(), found.CurrentAttempt)
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	refresh := 0
	if raw := r.URL.Query().Get("refresh_seconds"); raw != "" {
		if parsed, perr := strconv.Atoi(raw); perr == nil && parsed >= ciRefreshMinSeconds {
			refresh = parsed
		}
	}
	acceptsHTML := strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html")
	base := "/" + owner + "/" + repo + "/ci/runs/" + runID + "/jobs/" + jobID + "/log"
	if refresh > 0 || acceptsHTML {
		html := ciLogPage(base, string(attempt.Log), attempt.State, refresh)
		setCIPageHeaders(w)
		_, _ = io.WriteString(w, html)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(attempt.Log)
}

func (d Deps) serveCIBadge(w http.ResponseWriter, r *http.Request, owner, repo string) {
	ctx := r.Context()
	principal := d.ciPrincipal(r)
	rq, err := d.requester(ctx, principal)
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	rc, err := d.resolveRepo(ctx, rq, owner, repo, false)
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	if verr := d.checkRepoAccess(rq, rc.row, "ci.view", ""); verr != nil {
		writeRunnerProblem(w, verr)
		return
	}
	ctrl, err := d.ciController()
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	runs, err := ctrl.Runs(ctx, rc.row.ID)
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	status := "unknown"
	if latest := latestRun(runs); latest != nil {
		status = latest.State
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write(ciBadgeSVG(status))
}

func (d Deps) serveCISettings(w http.ResponseWriter, r *http.Request, owner, repo string) {
	ctx := r.Context()
	principal := d.ciPrincipal(r)
	rq, err := d.requester(ctx, principal)
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	rc, err := d.resolveRepo(ctx, rq, owner, repo, false)
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	if err := d.checkRepoAccess(rq, rc.row, "ci.config", ""); err != nil {
		writeRunnerProblem(w, err)
		return
	}
	rules := d.Store.Resolver().Rules(rc.row.ID)
	var b strings.Builder
	b.WriteString(heading("CI settings"))
	b.WriteString("<p>Required proof kinds come from the bookmark policy.</p>\n")
	b.WriteString("<table>\n<tr><th>Pattern</th><th>Mode</th><th>Required</th></tr>\n")
	for _, rule := range rules {
		required := make([]string, 0, len(rule.Required))
		for _, req := range rule.Required {
			required = append(required, req.Kind+"="+req.Result)
		}
		b.WriteString("<tr><td>" + esc(rule.Pattern) + "</td><td>" + esc(rule.Mode) + "</td>" +
			"<td>" + esc(strings.Join(required, ", ")) + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	setCIPageHeaders(w)
	_, _ = io.WriteString(w, pageShell("CI settings", b.String()))
}

func (d Deps) serveCIQueue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	principal := d.ciPrincipal(r)
	rq, err := d.requester(ctx, principal)
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	ctrl, err := d.ciController()
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	runs, err := ctrl.Runs(ctx, 0)
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	var b strings.Builder
	b.WriteString(heading("CI queue"))
	shown := 0
	b.WriteString("<table>\n<tr><th>Run</th><th>Repo</th><th>State</th><th>Trust</th></tr>\n")
	for _, run := range runs {
		var row store.RepoRow
		accessErr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			var gerr error
			row, gerr = tx.GetRepo(run.RepoID)
			return gerr
		})
		if accessErr != nil {
			continue
		}
		if derr := d.checkRepoAccess(rq, row, "ci.view", ""); derr != nil {
			continue
		}
		_, jobs, jerr := ctrl.Run(ctx, run.ID)
		if jerr != nil {
			continue
		}
		b.WriteString("<tr><td>" + esc(run.ID) + "</td><td>" + esc(row.Name) + "</td>" +
			"<td>" + esc(run.State) + "</td><td>" + esc(run.Trust) + "</td>" +
			"<td>" + esc(strconv.Itoa(len(jobs))) + " jobs</td></tr>\n")
		shown++
		if shown >= 100 {
			break
		}
	}
	b.WriteString("</table>\n")
	setCIPageHeaders(w)
	_, _ = io.WriteString(w, pageShell("CI queue", b.String()))
}

func (d Deps) serveCIAdminQueue(w http.ResponseWriter, r *http.Request) {
	if _, err := requireAdmin(r.Context(), d.Service, d.ciPrincipal(r)); err != nil {
		writeRunnerProblem(w, err)
		return
	}
	ctrl, err := d.ciController()
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	runners, err := ctrl.Runners(r.Context())
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	runs, err := ctrl.Runs(r.Context(), 0)
	if err != nil {
		writeRunnerProblem(w, err)
		return
	}
	queued := 0
	for _, run := range runs {
		_, jobs, jerr := ctrl.Run(r.Context(), run.ID)
		if jerr != nil {
			writeRunnerProblem(w, jerr)
			return
		}
		for _, job := range jobs {
			if job.State == "queued" || job.State == "leased" || job.State == "running" {
				queued++
			}
		}
	}
	var b strings.Builder
	b.WriteString(heading("CI scheduler queue"))
	b.WriteString("<p>" + esc(strconv.Itoa(queued)) + " active jobs, " +
		esc(strconv.Itoa(len(runners))) + " runners.</p>\n")
	b.WriteString("<table>\n<tr><th>Runner</th><th>Scope</th><th>Capacity</th><th>Status</th></tr>\n")
	for _, runner := range runners {
		b.WriteString("<tr><td>" + esc(runner.ID) + "</td><td>" + esc(runner.Scope) + "</td>" +
			"<td>" + esc(strconv.Itoa(runner.Capacity)) + "</td><td>" + esc(runner.Status) + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	setCIPageHeaders(w)
	_, _ = io.WriteString(w, pageShell("CI scheduler queue", b.String()))
}

func (d Deps) serveCIPages(w http.ResponseWriter, r *http.Request) bool {
	trimmed := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) == 1 && parts[0] == "ci" && r.Method == http.MethodGet {
		d.serveCIQueue(w, r)
		return true
	}
	if len(parts) == 2 && parts[0] == "admin" && parts[1] == "queue" && r.Method == http.MethodGet {
		d.serveCIAdminQueue(w, r)
		return true
	}
	if len(parts) == 4 && parts[2] == "settings" && parts[3] == "ci" && r.Method == http.MethodGet {
		d.serveCISettings(w, r, parts[0], parts[1])
		return true
	}
	if len(parts) < 3 || parts[2] != "ci" {
		return false
	}
	owner, repo := parts[0], parts[1]
	rest := parts[3:]
	if r.Method != http.MethodGet {
		return false
	}
	switch {
	case len(rest) == 0:
		d.serveCITab(w, r, owner, repo)
		return true
	case len(rest) == 1 && rest[0] == "badge.svg":
		d.serveCIBadge(w, r, owner, repo)
		return true
	case len(rest) == 2 && rest[0] == "runs" && rest[1] != "":
		d.serveCIRunPage(w, r, owner, repo, rest[1])
		return true
	case len(rest) == 4 && rest[0] == "runs" && rest[1] != "" && rest[2] == "jobs" && rest[3] != "":
		d.serveCIJobPage(w, r, owner, repo, rest[1], rest[3])
		return true
	case len(rest) == 5 && rest[0] == "runs" && rest[1] != "" && rest[2] == "jobs" && rest[3] != "" && rest[4] == "log":
		d.serveCILog(w, r, owner, repo, rest[1], rest[3])
		return true
	case len(rest) == 4 && rest[0] == "runs" && rest[1] != "" && rest[2] == "artifacts" && rest[3] != "":
		d.serveCIBytes(w, r)
		return true
	}
	return false
}
