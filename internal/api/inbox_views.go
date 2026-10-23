package api

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
)

// RegisterInboxViews registers the zero-JS inbox pages.
func RegisterInboxViews(v *op.Views) {
	op.PageView(v, "inbox.list", writeInboxList)
	op.PageView(v, "inbox.get", writeInboxDetail)
}

func inboxFilterLink(base string, state, label, current string) string {
	if state == current {
		return "<strong>" + esc(label) + "</strong>"
	}
	return "<a href=\"" + esc(base+"?state="+state) + "\">" + esc(label) + "</a>"
}

func writeInboxList(w io.Writer, c op.ViewCtx, out InboxListOut) error {
	base := c.Path
	state := c.Input.Get("state")
	if state == "" {
		state = "unread"
	}
	var b strings.Builder
	b.WriteString(heading("Inbox"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<p>" +
		inboxFilterLink(base, "unread", "Unread", state) + " · " +
		inboxFilterLink(base, "read", "Read", state) + " · " +
		inboxFilterLink(base, "done", "Done", state) + " · " +
		inboxFilterLink(base, "snoozed", "Snoozed", state) + " · " +
		inboxFilterLink(base, "all", "All", state) +
		" — unread " + esc(strconv.FormatInt(out.Unread, 10)) + "</p>\n")
	b.WriteString("<table>\n<tr><th></th><th></th><th>Subject</th><th>Reason</th><th>Repo</th><th>Snippet</th><th>Bumped</th><th></th></tr>\n")
	for _, thread := range out.Threads {
		dot := "○"
		if thread.State == "unread" {
			dot = "●"
		}
		if thread.Muted {
			dot += " muted"
		}
		subject := thread.SubjectKind + ":" + thread.SubjectID
		b.WriteString("<tr><td><input type=\"checkbox\" form=\"inboxbulk\" name=\"thread_ids\" value=\"" + esc(thread.ID) + "\"></td>" +
			"<td>" + esc(dot) + "</td>" +
			"<td><a href=\"" + esc("/inbox/thread?id="+thread.ID) + "\">" + esc(subject) + "</a></td>" +
			"<td>" + esc(strings.Join(thread.Reasons, ", ")) + "</td>" +
			"<td>" + esc(thread.Repo) + "</td>" +
			"<td>" + esc(thread.Snippet) + "</td>" +
			"<td>" + esc(strconv.FormatInt(thread.BumpedAt, 10)) + "</td>" +
			"<td>" + formOpen(base, c.CSRF) +
			hiddenField("thread_ids", thread.ID) +
			"<button type=\"submit\" name=\"op\" value=\"mark_read\">Read</button>\n" +
			"<button type=\"submit\" name=\"op\" value=\"mark_done\">Done</button>\n" +
			"</form></td>" +
			"</tr>\n")
	}
	b.WriteString("</table>\n")
	if out.Next != "" {
		b.WriteString("<p>More results are available through the Link header.</p>\n")
	}
	b.WriteString("<h2>Bulk</h2>\n" +
		"<form method=\"post\" action=\"" + esc(base) + "\" id=\"inboxbulk\">\n" +
		"<input type=\"hidden\" name=\"_csrf\" value=\"" + esc(c.CSRF) + "\">\n" +
		"<input type=\"hidden\" name=\"_idem\" value=\"" + esc(newIdem()) + "\">\n" +
		hiddenField("snooze_until", strconv.FormatInt(out.SnoozePreset, 10)) +
		"<p><button type=\"submit\" name=\"op\" value=\"mark_read\">Mark read</button>\n" +
		"<button type=\"submit\" name=\"op\" value=\"mark_done\">Mark done</button>\n" +
		"<button type=\"submit\" name=\"op\" value=\"mark_unread\">Mark unread</button>\n" +
		"<button type=\"submit\" name=\"op\" value=\"snooze\">Snooze 1d</button>\n" +
		"<button type=\"submit\" name=\"op\" value=\"mute\">Mute</button>\n" +
		"<button type=\"submit\" name=\"op\" value=\"unmute\">Unmute</button></p>\n" +
		"</form>\n" +
		"<p>Bulk snooze hides checked threads for one day; open a thread for a custom snooze time.</p>\n")
	b.WriteString("<p><a href=\"/inbox/settings\">Watch and notification settings</a></p>\n")
	_, err := io.WriteString(w, pageShell("Inbox", b.String()))
	return err
}

func writeInboxDetail(w io.Writer, c op.ViewCtx, out InboxGetOut) error {
	thread := out.Thread
	var b strings.Builder
	b.WriteString(heading(thread.SubjectKind + ":" + thread.SubjectID))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<dl>\n" +
		"<dt>State</dt><dd>" + esc(thread.State) + "</dd>\n" +
		"<dt>Muted</dt><dd>" + esc(strconv.FormatBool(thread.Muted)) + "</dd>\n" +
		"<dt>Reasons</dt><dd>" + esc(strings.Join(thread.Reasons, ", ")) + "</dd>\n" +
		"<dt>Repo</dt><dd>" + esc(thread.Repo) + "</dd>\n" +
		"<dt>Snippet</dt><dd>" + esc(thread.Snippet) + "</dd>\n" +
		"</dl>\n")
	b.WriteString("<table>\n<tr><th>Event</th><th>Title</th><th>Reasons</th><th>At</th></tr>\n")
	for _, item := range out.Items {
		b.WriteString("<tr><td>" + esc(item.EventID) + "</td>" +
			"<td>" + esc(item.Title) + "</td>" +
			"<td>" + esc(strings.Join(item.Reasons, ", ")) + "</td>" +
			"<td>" + esc(strconv.FormatInt(item.CreatedAt, 10)) + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	b.WriteString(formOpen("/inbox", c.CSRF) +
		hiddenField("thread_ids", thread.ID) +
		"<p><button type=\"submit\" name=\"op\" value=\"mark_read\">Mark read</button>\n" +
		"<button type=\"submit\" name=\"op\" value=\"mark_done\">Mark done</button>\n" +
		"<button type=\"submit\" name=\"op\" value=\"mark_unread\">Mark unread</button>\n" +
		"<button type=\"submit\" name=\"op\" value=\"mute\">Mute</button>\n" +
		"<button type=\"submit\" name=\"op\" value=\"unmute\">Unmute</button></p>\n" +
		"</form>\n")
	b.WriteString(formOpen("/inbox/snooze", c.CSRF) +
		hiddenField("thread_ids", thread.ID) +
		textField("until", "Snooze until (unix millis, 1m..90d ahead)", "", false) +
		submitField("Snooze thread"))
	b.WriteString("<p><a href=\"/inbox\">Inbox</a></p>\n")
	_, err := io.WriteString(w, pageShell("Thread "+thread.ID, b.String()))
	return err
}

// serveInboxSettings renders the settings page: default level, claim window,
// and every explicit watch with per-row forms. HTML only, human gated.
func (d Deps) serveInboxSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	principal := RawBytes{Deps: d}.principalFor(r)
	userID, uerr := d.requireInboxUser(ctx, principal)
	if uerr != nil {
		writeCustomProblem(w, uerr)
		return
	}
	settings := InboxSettingsJSON{DefaultLevel: "participate", ClaimWarnWindowSecs: 3600}
	var watchRows []store.WatchRow
	serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
		current, cerr := tx.GetInboxSettings(userID)
		if cerr != nil {
			return cerr
		}
		settings = inboxSettingsRender(current)
		rows, lerr := tx.ListWatches(userID)
		if lerr != nil {
			return lerr
		}
		watchRows = rows
		return nil
	})
	if serr != nil {
		writeCustomProblem(w, mapInboxStore(serr, "inbox settings failed"))
		return
	}
	watches := []InboxWatchJSON{}
	for _, row := range watchRows {
		rendered, rerr := d.renderInboxWatch(ctx, row)
		if rerr != nil {
			writeCustomProblem(w, rerr)
			return
		}
		watches = append(watches, rendered)
	}
	csrf := ensureCustomCSRF(w, r, d.CSRFKey)
	var b strings.Builder
	b.WriteString(heading("Inbox settings"))
	b.WriteString("<h2>Defaults</h2>\n" +
		formOpen("/inbox/settings", csrf) +
		"<label>Default level for new repositories<br>\n" +
		"<select name=\"default_level\">\n" +
		inboxLevelOption("participate", settings.DefaultLevel) +
		inboxLevelOption("all", settings.DefaultLevel) +
		inboxLevelOption("ignore", settings.DefaultLevel) +
		"</select></label>\n" +
		textField("claim_warn_window_secs", "Claim warning window (seconds, 300..86400)", strconv.FormatInt(settings.ClaimWarnWindowSecs, 10), false) +
		submitField("Save settings"))
	b.WriteString("<h2>Watches</h2>\n<table>\n<tr><th>Repo</th><th>Level</th><th>Bits</th><th>Change</th></tr>\n")
	for _, watch := range watches {
		b.WriteString("<tr><td>" + esc(watch.Repo) + "</td>" +
			"<td>" + esc(watch.Level) + "</td>" +
			"<td>" + esc(strings.Join(watch.CustomBits, ", ")) + "</td>" +
			"<td>" + formOpen("/inbox/watch", csrf) +
			hiddenField("repo", watch.Repo) +
			"<label>Level<br>\n<select name=\"level\">\n" +
			inboxLevelOption("participate", watch.Level) +
			inboxLevelOption("all", watch.Level) +
			inboxLevelOption("custom", watch.Level) +
			inboxLevelOption("ignore", watch.Level) +
			"</select></label>\n" +
			textField("custom_bits", "Custom bits (comma separated: changes, tasks, releases, ci)", strings.Join(watch.CustomBits, ","), false) +
			checkField("confirm", "Confirm ignore", false) +
			submitField("Save watch") + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	b.WriteString("<h2>New watch</h2>\n" +
		formOpen("/inbox/watch", csrf) +
		textField("repo", "Repository (owner/name)", "", false) +
		"<label>Level<br>\n<select name=\"level\">\n" +
		inboxLevelOption("participate", "participate") +
		inboxLevelOption("all", "") +
		inboxLevelOption("custom", "") +
		inboxLevelOption("ignore", "") +
		"</select></label>\n" +
		textField("custom_bits", "Custom bits (comma separated)", "", false) +
		checkField("confirm", "Confirm ignore", false) +
		submitField("Add watch"))
	b.WriteString("<p><a href=\"/inbox\">Inbox</a></p>\n")
	setCIPageHeaders(w)
	_, _ = io.WriteString(w, pageShell("Inbox settings", b.String()))
}

func inboxLevelOption(value, current string) string {
	selected := ""
	if value == current {
		selected = " selected"
	}
	return "<option value=\"" + esc(value) + "\"" + selected + ">" + esc(value) + "</option>\n"
}

// InboxPages serves the inbox custom pages outside the op table: the thread
// page marks read on HTML view before the op renders it, and the settings
// page renders outside the table. All other paths fall through.
func (d Deps) InboxPages(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/inbox/thread" && wantsCustomHTML(r):
			d.serveInboxThread(w, r, next)
			return
		case r.Method == http.MethodGet && r.URL.Path == "/inbox/settings":
			if !wantsCustomHTML(r) {
				next.ServeHTTP(w, r)
				return
			}
			d.serveInboxSettings(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// serveInboxThread marks the thread read for HTML views, then delegates to
// the op mux so the op stays pure and its PageView renders with a fresh CSRF
// token. Anonymous views delegate without marking.
func (d Deps) serveInboxThread(w http.ResponseWriter, r *http.Request, next http.Handler) {
	ctx := r.Context()
	principal := RawBytes{Deps: d}.principalFor(r)
	if userID, uerr := d.requireInboxUser(ctx, principal); uerr == nil {
		query := r.URL.Query()
		id := strings.TrimSpace(query.Get("id"))
		if id == "" {
			id = strings.TrimSpace(query.Get("thread_id"))
		}
		if id != "" {
			nowMillis := d.now().UnixMilli()
			_ = d.Store.Write(ctx, func(tx *store.WriteTx) error {
				_, _, merr := tx.MarkInboxThreads(userID, []string{id}, "read", 0, nowMillis)
				return merr
			})
		}
	}
	next.ServeHTTP(w, r)
}
