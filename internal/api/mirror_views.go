package api

import (
	"cmp"
	"io"
	"strconv"
	"strings"

	"github.com/gosuda/rukie/internal/op"
)

// RegisterMirrorViews registers the zero-JS push mirror settings pages.
func RegisterMirrorViews(v *op.Views) {
	op.PageView(v, "mirror.list", writeMirrorList)
	op.PageView(v, "mirror.get", writeMirrorDetail)
	op.PageView(v, "mirror.run.list", writeMirrorRunList)
	op.PageView(v, "mirror.run.get", writeMirrorRunPage)
}

func mirrorMillis(ms int64) string {
	if ms == 0 {
		return "never"
	}
	return strconv.FormatInt(ms, 10)
}

// mirrorHasBacklog reports whether a mirror belongs on the console backlog
// panel: an unsettled state, or scheduled work on an enabled mirror.
func mirrorHasBacklog(m MirrorJSON) bool {
	switch m.State {
	case "ok", "idle", "disabled", "":
		return m.Flags.Enabled && m.NextRunAt != 0
	default:
		return true
	}
}

func mirrorTrueFlags(m MirrorJSON) string {
	var on []string
	if m.Flags.IncludeNotes {
		on = append(on, "include_notes")
	}
	if m.Flags.ProtectDefaultBranch {
		on = append(on, "protect_default_branch")
	}
	if m.Flags.Enabled {
		on = append(on, "enabled")
	}
	if m.Flags.SyncOnRefUpdate {
		on = append(on, "sync_on_ref_update")
	}
	if m.Flags.CreateIfMissing {
		on = append(on, "create_if_missing")
	}
	if m.Flags.PolicyAck {
		on = append(on, "policy_ack")
	}
	if m.Flags.AdoptExistingRefs {
		on = append(on, "adopt_existing_refs")
	}
	if m.Flags.Adopted {
		on = append(on, "adopted")
	}
	return strings.Join(on, ", ")
}

// writeMirrorList renders one repository's push mirrors. There is no console
// page in the tree; the console backlog panel lives here, on the mirrors
// list. MirrorListOut carries no run rows, so the panel surfaces per-mirror
// queue links for mirrors with pending work rather than individual runs.
func writeMirrorList(w io.Writer, c op.ViewCtx, out MirrorListOut) error {
	base := c.Path
	var b strings.Builder
	b.WriteString(heading("Push mirrors"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<table>\n<tr><th>Name</th><th>Mode</th><th>Refs</th><th>State</th><th>Failures</th><th>Last error</th><th>Last success</th><th>Credential</th></tr>\n")
	for _, m := range out.Mirrors {
		href := base + "/" + m.ID
		lastErr := m.LastError.Class
		if m.LastError.Message != "" {
			lastErr += ": " + m.LastError.Message
		}
		if lastErr == "" {
			lastErr = "none"
		}
		cred := "none"
		if m.Credential != nil {
			cred = "last4 " + m.Credential.Last4
		}
		b.WriteString("<tr><td><a href=\"" + esc(href) + "\">" + esc(m.Name) + "</a></td>" +
			"<td>" + esc(m.Mode) + "</td><td>" + esc(m.RefSelection) + "</td>" +
			"<td>" + esc(m.State) + "</td>" +
			"<td>" + esc(strconv.FormatInt(m.ConsecutiveFailures, 10)) + "</td>" +
			"<td>" + esc(lastErr) + "</td>" +
			"<td>" + esc(mirrorMillis(m.LastSuccessAt)) + "</td>" +
			"<td>" + esc(cred) + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	if out.Next != "" {
		b.WriteString("<p>More results are available through the Link header.</p>\n")
	}
	b.WriteString("<h2>Backlog</h2>\n<ul>\n")
	pending := 0
	for _, m := range out.Mirrors {
		if !mirrorHasBacklog(m) {
			continue
		}
		pending++
		b.WriteString("<li><a href=\"" + esc(base+"/"+m.ID) + "\">" + esc(m.Name) + "</a> — " +
			esc(m.State) + " (failures " + esc(strconv.FormatInt(m.ConsecutiveFailures, 10)) + ") " +
			"<a href=\"" + esc(base+"/"+m.ID+"/runs") + "\">Runs</a></li>\n")
	}
	if pending == 0 {
		b.WriteString("<li>Backlog is empty.</li>\n")
	}
	b.WriteString("</ul>\n")
	b.WriteString("<h2>New mirror</h2>\n" +
		formOpen(base, c.CSRF) +
		textField("name", "Name", c.Input.Get("name"), false) +
		textField("target_url", "Target URL", c.Input.Get("target_url"), false) +
		textField("host_kind", "Host kind (github, gitlab, forgejo, generic)", c.Input.Get("host_kind"), false) +
		textField("username", "Username", c.Input.Get("username"), false) +
		textField("token", "Token", "", true) +
		textField("mode", "Mode (exact, fast-forward-only)", c.Input.Get("mode"), false) +
		textField("ref_selection", "Ref selection (all, default_branch_tags, globs)", c.Input.Get("ref_selection"), false) +
		textField("globs", "Globs (one per line)", c.Input.Get("globs"), false) +
		textField("debounce_seconds", "Debounce seconds", cmp.Or(c.Input.Get("debounce_seconds"), "0"), false) +
		textField("max_delay_seconds", "Max delay seconds", cmp.Or(c.Input.Get("max_delay_seconds"), "0"), false) +
		textField("reconcile_interval_seconds", "Reconcile interval seconds", cmp.Or(c.Input.Get("reconcile_interval_seconds"), "0"), false) +
		checkField("include_notes", "Include notes", c.Input.Get("include_notes") != "") +
		checkField("protect_default_branch", "Protect default branch", c.Input.Get("protect_default_branch") != "") +
		checkField("enabled", "Enabled", c.Input.Get("enabled") == "" || c.Input.Get("enabled") != "0") +
		checkField("sync_on_ref_update", "Sync on ref update", c.Input.Get("sync_on_ref_update") == "" || c.Input.Get("sync_on_ref_update") != "0") +
		checkField("create_if_missing", "Create if missing", c.Input.Get("create_if_missing") != "") +
		checkField("policy_ack", "Policy ack", c.Input.Get("policy_ack") != "") +
		checkField("adopt_existing_refs", "Adopt existing refs", c.Input.Get("adopt_existing_refs") != "") +
		submitField("Create mirror"))
	_, err := io.WriteString(w, pageShell("Push mirrors", b.String()))
	return err
}

// writeMirrorDetail renders one push mirror: config, credential metadata
// (last4 and versions only, never secrets), ref state, and the sync,
// token-rotate, and delete forms.
func writeMirrorDetail(w io.Writer, c op.ViewCtx, out MirrorGetOut) error {
	m := out.Mirror
	listBase := c.Path
	if m.ID != "" {
		listBase = strings.TrimSuffix(c.Path, "/"+m.ID)
	}
	var b strings.Builder
	b.WriteString(heading("Mirror " + m.Name))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<dl>\n" +
		"<dt>Name</dt><dd>" + esc(m.Name) + "</dd>\n" +
		"<dt>Target URL</dt><dd>" + esc(m.TargetURL) + "</dd>\n" +
		"<dt>Host kind</dt><dd>" + esc(m.HostKind) + "</dd>\n" +
		"<dt>Username</dt><dd>" + esc(m.Username) + "</dd>\n" +
		"<dt>Mode</dt><dd>" + esc(m.Mode) + "</dd>\n" +
		"<dt>Ref selection</dt><dd>" + esc(m.RefSelection) + "</dd>\n" +
		"<dt>Globs</dt><dd>" + esc(strings.Join(m.Globs, ", ")) + "</dd>\n" +
		"<dt>Flags</dt><dd>" + esc(cmp.Or(mirrorTrueFlags(m), "none")) + "</dd>\n" +
		"<dt>State</dt><dd>" + esc(m.State) + "</dd>\n" +
		"<dt>Consecutive failures</dt><dd>" + esc(strconv.FormatInt(m.ConsecutiveFailures, 10)) + "</dd>\n" +
		"<dt>Last error</dt><dd>" + esc(m.LastError.Class+" "+m.LastError.Message) + " (" + esc(strconv.FormatInt(m.LastError.HTTPStatus, 10)) + ")</dd>\n" +
		"<dt>Last success</dt><dd>" + esc(mirrorMillis(m.LastSuccessAt)) + "</dd>\n" +
		"<dt>Last attempt</dt><dd>" + esc(mirrorMillis(m.LastAttemptAt)) + "</dd>\n" +
		"<dt>Next run</dt><dd>" + esc(mirrorMillis(m.NextRunAt)) + "</dd>\n" +
		"</dl>\n")
	b.WriteString("<h2>Credential</h2>\n")
	if m.Credential == nil {
		b.WriteString("<p>No credential on file.</p>\n")
	} else {
		b.WriteString("<dl>\n" +
			"<dt>Last4</dt><dd>" + esc(m.Credential.Last4) + "</dd>\n" +
			"<dt>Key version</dt><dd>" + esc(strconv.FormatInt(m.Credential.KeyVersion, 10)) + "</dd>\n" +
			"<dt>Created</dt><dd>" + esc(strconv.FormatInt(m.Credential.CreatedAt, 10)) + "</dd>\n" +
			"<dt>Rotated</dt><dd>" + esc(strconv.FormatInt(m.Credential.RotatedAt, 10)) + "</dd>\n" +
			"<dt>Last used</dt><dd>" + esc(strconv.FormatInt(m.Credential.LastUsedAt, 10)) + "</dd>\n" +
			"</dl>\n")
	}
	b.WriteString("<h2>Refs</h2>\n<table>\n<tr><th>Ref</th><th>Local</th><th>Remote</th><th>Status</th><th>Reason class</th><th>Reason</th></tr>\n")
	for _, r := range out.Refs {
		b.WriteString("<tr><td>" + esc(r.Ref) + "</td><td>" + esc(r.LocalOid) + "</td><td>" + esc(r.RemoteOid) + "</td>" +
			"<td>" + esc(r.Status) + "</td><td>" + esc(r.ReasonClass) + "</td><td>" + esc(r.Reason) + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	if out.Next != "" {
		b.WriteString("<p>More results are available through the Link header.</p>\n")
	}
	b.WriteString("<p><a href=\"" + esc(c.Path+"/runs") + "\">Runs</a> — " +
		"<a href=\"" + esc(listBase) + "\">Mirrors</a></p>\n")
	b.WriteString("<h2>Sync now</h2>\n" +
		formOpen(c.Path+"/sync", c.CSRF) +
		checkField("dry_run", "Dry run", false) +
		textField("confirm_digest", "Confirm digest", "", false) +
		textField("mode", "Mode (now, reconcile)", "", false) +
		submitField("Sync mirror"))
	b.WriteString("<h2>Rotate token</h2>\n" +
		formOpen(c.Path, c.CSRF) +
		textField("token", "New token", "", true) +
		submitField("Rotate token"))
	b.WriteString("<h2>Delete mirror</h2>\n" +
		formOpen(c.Path+"/delete", c.CSRF) +
		submitField("Delete mirror "+m.Name))
	_, err := io.WriteString(w, pageShell("Mirror "+m.Name, b.String()))
	return err
}

// writeMirrorRunList renders one mirror's sync run queue.
func writeMirrorRunList(w io.Writer, c op.ViewCtx, out MirrorRunListOut) error {
	mirrorBase := strings.TrimSuffix(c.Path, "/runs")
	var b strings.Builder
	b.WriteString(heading("Mirror runs"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<table>\n<tr><th>Run</th><th>Kind</th><th>State</th><th>Trigger</th><th>Attempt</th><th>Started</th></tr>\n")
	for _, r := range out.Runs {
		b.WriteString("<tr><td><a href=\"" + esc(c.Path+"/"+r.ID) + "\">" + esc(r.ID) + "</a></td>" +
			"<td>" + esc(r.Kind) + "</td><td>" + esc(r.State) + "</td><td>" + esc(r.Trigger) + "</td>" +
			"<td>" + esc(strconv.FormatInt(r.AttemptNo, 10)) + "</td>" +
			"<td>" + esc(mirrorMillis(r.StartedAt)) + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	if out.Next != "" {
		b.WriteString("<p>More results are available through the Link header.</p>\n")
	}
	b.WriteString("<p><a href=\"" + esc(mirrorBase) + "\">Mirror</a></p>\n")
	_, err := io.WriteString(w, pageShell("Mirror runs", b.String()))
	return err
}

// writeMirrorRunPage renders one sync run. Runs carry no ref snapshot, so
// per-ref state lives on the mirror detail page; the run page carries the
// run header and its outcome.
func writeMirrorRunPage(w io.Writer, c op.ViewCtx, out MirrorRunGetOut) error {
	r := out.Run
	mirrorBase := c.Path
	runsBase := c.Path
	if r.ID != "" {
		runsBase = strings.TrimSuffix(c.Path, "/"+r.ID)
		mirrorBase = strings.TrimSuffix(runsBase, "/runs")
	}
	var b strings.Builder
	b.WriteString(heading("Run " + r.ID))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<dl>\n" +
		"<dt>State</dt><dd>" + esc(r.State) + "</dd>\n" +
		"<dt>Kind</dt><dd>" + esc(r.Kind) + "</dd>\n" +
		"<dt>Trigger</dt><dd>" + esc(r.Trigger) + "</dd>\n" +
		"<dt>Attempt</dt><dd>" + esc(strconv.FormatInt(r.AttemptNo, 10)) + "</dd>\n" +
		"<dt>Plan digest</dt><dd>" + esc(r.PlanDigest) + "</dd>\n" +
		"<dt>Outcome</dt><dd>" + esc(r.Outcome.Class+" "+r.Outcome.Summary) + "</dd>\n" +
		"<dt>Counts</dt><dd>" + esc(r.Outcome.Counts) + "</dd>\n" +
		"<dt>Started</dt><dd>" + esc(mirrorMillis(r.StartedAt)) + "</dd>\n" +
		"<dt>Finished</dt><dd>" + esc(mirrorMillis(r.FinishedAt)) + "</dd>\n" +
		"</dl>\n")
	b.WriteString("<p>Per-ref state is tracked on the mirror page.</p>\n")
	b.WriteString("<p><a href=\"" + esc(runsBase) + "\">Runs</a> — " +
		"<a href=\"" + esc(mirrorBase) + "\">Mirror</a></p>\n")
	_, err := io.WriteString(w, pageShell("Run "+r.ID, b.String()))
	return err
}
