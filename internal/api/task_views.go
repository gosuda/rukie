package api

import (
	"cmp"
	"io"
	"strconv"
	"strings"

	"github.com/gosuda/rukie/internal/op"
)

// RegisterIntentsViews registers the zero-JS intent and ask pages.
func RegisterIntentsViews(v *op.Views) {
	op.PageView(v, "intent.list", writeIntentList)
	op.PageView(v, "intent.get", writeIntent)
	op.PageView(v, "ask.list", writeAskList)
	op.PageView(v, "ask.get", writeAsk)
	v.Form("intent.create", func(w io.Writer, c op.ViewCtx) error {
		return writeIntentList(w, c, IntentListOut{})
	})
	v.Form("intent.update", func(w io.Writer, c op.ViewCtx) error {
		return writeIntent(w, c, IntentGetOut{})
	})
	v.Form("intent.state.set", func(w io.Writer, c op.ViewCtx) error {
		return writeIntent(w, c, IntentGetOut{})
	})
	v.Form("intent.lease.create", func(w io.Writer, c op.ViewCtx) error {
		return writeIntent(w, c, IntentGetOut{})
	})
	v.Form("intent.lease.delete", func(w io.Writer, c op.ViewCtx) error {
		return writeIntent(w, c, IntentGetOut{})
	})
	v.Form("ask.answer.create", func(w io.Writer, c op.ViewCtx) error {
		return writeAsk(w, c, AskGetOut{})
	})
	v.Form("intent.lease.update", func(w io.Writer, c op.ViewCtx) error {
		return writeIntent(w, c, IntentGetOut{})
	})
	v.Form("intent.change.add", func(w io.Writer, c op.ViewCtx) error {
		return writeIntent(w, c, IntentGetOut{})
	})
	v.Form("intent.change.remove", func(w io.Writer, c op.ViewCtx) error {
		return writeIntent(w, c, IntentGetOut{})
	})
	v.Form("ask.create", func(w io.Writer, c op.ViewCtx) error {
		return writeIntent(w, c, IntentGetOut{})
	})
	v.Form("ask.update", func(w io.Writer, c op.ViewCtx) error {
		return writeAsk(w, c, AskGetOut{})
	})
}

func intentNumber(n int64) string {
	return strconv.FormatInt(n, 10)
}

func intentRepoBase(path string, n int64) (string, bool) {
	return strings.CutSuffix(path, "/tasks/"+intentNumber(n))
}

func intentRowRef(in IntentJSON) string {
	num := intentNumber(in.Number)
	if in.Owner == "" || in.Repo == "" {
		return esc(num)
	}
	return "<a href=\"/" + esc(in.Owner) + "/" + esc(in.Repo) + "/tasks/" + esc(num) + "\">" + esc(num) + "</a>"
}

type radarEntry struct {
	holders []string
	tasks   []string
}

func appendUnique(in []string, v string) []string {
	for _, s := range in {
		if s == v {
			return in
		}
	}
	return append(in, v)
}

func writeIntentList(w io.Writer, c op.ViewCtx, out IntentListOut) error {
	var b strings.Builder
	b.WriteString(heading("Tasks"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<form method=\"get\" action=\"" + esc(c.Path) + "\">\n" +
		textField("state", "State", c.Input.Get("state"), false) +
		textField("assignee", "Assignee", c.Input.Get("assignee"), false) +
		textField("holder", "Holder", c.Input.Get("holder"), false) +
		checkField("unclaimed", "Unclaimed only", c.Input.Get("unclaimed") != "") +
		submitField("Filter tasks"))
	b.WriteString("<table>\n<tr><th>Task</th><th>Title</th><th>State</th><th>Lease mode</th><th>Holder</th><th>Expires</th></tr>\n")
	for _, in := range out.Intents {
		holder := in.Holder
		if holder == "" {
			holder = "unclaimed"
		}
		expires := "none"
		if in.ExpiresAt != 0 {
			expires = strconv.FormatInt(in.ExpiresAt, 10)
		}
		b.WriteString("<tr><td>" + intentRowRef(in) + "</td><td>" + esc(in.Title) +
			"</td><td>" + esc(in.State) + "</td><td>" + esc(in.LeaseMode) +
			"</td><td>" + esc(holder) + "</td><td>" + esc(expires) + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	if out.Next != "" {
		b.WriteString("<p>More results are available through the Link header.</p>\n")
	}
	if len(out.Intents) == 0 {
		b.WriteString("<p>No tasks match these filters.</p>\n")
	}
	listPath := c.Path
	if nb, ok := strings.CutSuffix(c.Path, "/tasks/new"); ok {
		listPath = nb
	}
	newPath := ""
	if base, ok := strings.CutSuffix(listPath, "/tasks"); ok && base != "" {
		newPath = listPath + "/new"
	}
	if newPath != "" {
		b.WriteString("<h2>New task</h2>\n" +
			formOpen(newPath, c.CSRF) +
			textField("title", "Title", c.Input.Get("title"), false) +
			textField("body", "Body", c.Input.Get("body"), false) +
			textField("lease_mode", "Lease mode (enforce, advise)", c.Input.Get("lease_mode"), false) +
			"<label>Claim paths (one per line)\n<textarea name=\"claim_paths\" rows=\"4\">" + esc(c.Input.Get("claim_paths")) + "</textarea>\n</label>\n" +
			submitField("Create task"))
	}
	b.WriteString("<h2>Active claims</h2>\n<table>\n<tr><th>Task</th><th>Title</th><th>Mode</th><th>Holder</th><th>Expires</th></tr>\n")
	claims := 0
	for _, in := range out.Intents {
		if in.Holder == "" {
			continue
		}
		claims++
		expires := "none"
		if in.ExpiresAt != 0 {
			expires = strconv.FormatInt(in.ExpiresAt, 10)
		}
		b.WriteString("<tr><td>" + intentRowRef(in) + "</td><td>" + esc(in.Title) +
			"</td><td>" + esc(in.LeaseMode) + "</td><td>" + esc(in.Holder) +
			"</td><td>" + esc(expires) + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	if claims == 0 {
		b.WriteString("<p>No active claims.</p>\n")
	}
	b.WriteString("<h2>Supersede radar</h2>\n<table>\n<tr><th>Path</th><th>Holders</th><th>Tasks</th><th>Next action</th></tr>\n")
	byPath := map[string]*radarEntry{}
	var order []string
	for _, in := range out.Intents {
		if in.Holder == "" {
			continue
		}
		for _, p := range in.ClaimPaths {
			e := byPath[p]
			if e == nil {
				e = &radarEntry{}
				byPath[p] = e
				order = append(order, p)
			}
			e.holders = appendUnique(e.holders, in.Holder)
			e.tasks = appendUnique(e.tasks, intentNumber(in.Number))
		}
	}
	contested := 0
	for _, p := range order {
		e := byPath[p]
		if len(e.holders) < 2 {
			continue
		}
		contested++
		b.WriteString("<tr><td>" + esc(p) + "</td><td>" + esc(strings.Join(e.holders, ", ")) +
			"</td><td>" + esc(strings.Join(e.tasks, ", ")) +
			"</td><td>Abandon the superseded plan.</td></tr>\n")
	}
	b.WriteString("</table>\n")
	if contested == 0 {
		b.WriteString("<p>No overlapping claims.</p>\n")
	}
	_, err := io.WriteString(w, pageShell("Tasks", b.String()))
	return err
}

func writeIntent(w io.Writer, c op.ViewCtx, out IntentGetOut) error {
	in := out.Intent
	num := intentNumber(in.Number)
	var b strings.Builder
	b.WriteString(heading("Task " + num + " " + in.Title))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<p>State: " + esc(in.State) + " Lease mode: " + esc(in.LeaseMode) +
		" Version: " + esc(intentNumber(in.Version)) + "</p>\n")
	b.WriteString(para(cmp.Or(in.Body, "No description.")))
	if in.Holder == "" {
		b.WriteString("<h2>Claim</h2>\n" +
			formOpen(c.Path+"/claim", c.CSRF) +
			textField("ttl_s", "TTL seconds", cmp.Or(c.Input.Get("ttl_s"), "3600"), false) +
			submitField("Claim task"))
	} else {
		expires := "none"
		if in.ExpiresAt != 0 {
			expires = strconv.FormatInt(in.ExpiresAt, 10)
		}
		b.WriteString("<h2>Lease</h2>\n<dl>\n" +
			"<dt>Holder</dt><dd>" + esc(in.Holder) + "</dd>\n" +
			"<dt>Expires</dt><dd>" + esc(expires) + "</dd>\n</dl>\n" +
			formOpen(c.Path+"/claim/renew", c.CSRF) +
			textField("ttl_s", "TTL seconds", cmp.Or(c.Input.Get("ttl_s"), "3600"), false) +
			submitField("Renew claim"))
		b.WriteString(formOpen(c.Path+"/release", c.CSRF) +
			submitField("Release claim"))
	}
	b.WriteString("<h2>State</h2>\n" +
		formOpen(c.Path+"/state", c.CSRF) +
		hiddenField("version", intentNumber(in.Version)) +
		textField("state", "State (open, blocked, closed)", c.Input.Get("state"), false) +
		textField("reason", "Reason (completed, duplicate, wontfix, abandoned)", c.Input.Get("reason"), false) +
		submitField("Set state"))
	b.WriteString("<h2>Edit</h2>\n" +
		formOpen(c.Path+"/edit", c.CSRF) +
		hiddenField("version", intentNumber(in.Version)) +
		textField("title", "Title", cmp.Or(c.Input.Get("title"), in.Title), false) +
		textField("body", "Body", cmp.Or(c.Input.Get("body"), in.Body), false) +
		submitField("Save changes"))
	b.WriteString("<h2>Linked changes</h2>\n<table>\n<tr><th>Change</th><th>Head</th><th>Relation</th><th>Unlink</th></tr>\n")
	repoBase, hasBase := intentRepoBase(c.Path, in.Number)
	for _, link := range out.Changes {
		ref := esc(link.ChangeID)
		if hasBase {
			ref = "<a href=\"" + esc(repoBase+"/changes/"+link.ChangeID) + "\">" + esc(link.ChangeID) + "</a>"
		}
		b.WriteString("<tr><td>" + ref + "</td><td>" + esc(link.HeadOid) + "</td><td>" + esc(link.Relation) +
			"</td><td>" + formOpen(c.Path+"/changes/"+esc(link.ChangeID)+"/remove", c.CSRF) +
			submitField("Unlink") + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	if len(out.Changes) == 0 {
		b.WriteString("<p>No linked changes.</p>\n")
	}
	b.WriteString("<h2>Link change</h2>\n" +
		formOpen(c.Path+"/changes", c.CSRF) +
		textField("change_id", "Change id", c.Input.Get("change_id"), false) +
		textField("relation", "Relation", c.Input.Get("relation"), false) +
		submitField("Link change"))
	if hasBase {
		for _, link := range out.Changes {
			b.WriteString("<h3>Comment on " + esc(link.ChangeID) + "</h3>\n" +
				formOpen(repoBase+"/changes/"+link.ChangeID+"/comments", c.CSRF) +
				textField("path", "Path (empty for whole-change)", c.Input.Get("path"), false) +
				textField("line", "Line", c.Input.Get("line"), false) +
				textField("body", "Body", c.Input.Get("body"), false) +
				submitField("Comment on change"))
		}
	}
	b.WriteString("<h2>Asks</h2>\n<ul>\n")
	for _, ask := range out.Asks {
		line := ask.Question + " " + ask.State
		if ask.Answer != "" {
			line += " " + ask.Answer
		}
		if hasBase {
			line = "<a href=\"" + esc(repoBase+"/asks/"+ask.PublicID) + "\">" + esc(line) + "</a>"
		} else {
			line = esc(line)
		}
		b.WriteString("<li>" + line + "</li>\n")
	}
	b.WriteString("</ul>\n")
	if len(out.Asks) == 0 {
		b.WriteString("<p>No asks.</p>\n")
	}
	_, err := io.WriteString(w, pageShell("Task "+num, b.String()))
	return err
}

func writeAskList(w io.Writer, c op.ViewCtx, out AskListOut) error {
	var b strings.Builder
	b.WriteString(heading("Asks"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<table>\n<tr><th>Ask</th><th>Question</th><th>State</th><th>Blocks claim</th><th>Deadline</th></tr>\n")
	for _, ask := range out.Asks {
		blocks := "no"
		if ask.BlocksClaim {
			blocks = "yes"
		}
		deadline := "none"
		if ask.DeadlineMs != 0 {
			deadline = strconv.FormatInt(ask.DeadlineMs, 10)
		}
		b.WriteString("<tr><td><a href=\"" + esc(c.Path+"/"+ask.PublicID) + "\">" + esc(ask.PublicID) + "</a></td><td>" +
			esc(ask.Question) + "</td><td>" + esc(ask.State) + "</td><td>" + esc(blocks) +
			"</td><td>" + esc(deadline) + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	if out.Next != "" {
		b.WriteString("<p>More results are available through the Link header.</p>\n")
	}
	if len(out.Asks) == 0 {
		b.WriteString("<p>No asks.</p>\n")
	}
	_, err := io.WriteString(w, pageShell("Asks", b.String()))
	return err
}

func writeAsk(w io.Writer, c op.ViewCtx, out AskGetOut) error {
	ask := out.Ask
	var b strings.Builder
	b.WriteString(heading("Ask " + ask.PublicID))
	b.WriteString(errorList(c.Errors))
	blocks := "no"
	if ask.BlocksClaim {
		blocks = "yes"
	}
	deadline := "none"
	if ask.DeadlineMs != 0 {
		deadline = strconv.FormatInt(ask.DeadlineMs, 10)
	}
	b.WriteString("<dl>\n" +
		"<dt>Question</dt><dd>" + esc(ask.Question) + "</dd>\n" +
		"<dt>State</dt><dd>" + esc(ask.State) + "</dd>\n" +
		"<dt>Blocks claim</dt><dd>" + esc(blocks) + "</dd>\n" +
		"<dt>Deadline</dt><dd>" + esc(deadline) + "</dd>\n")
	b.WriteString("<dt>Answer</dt><dd>" + esc(cmp.Or(ask.Answer, "Awaiting answer.")) + "</dd>\n")
	b.WriteString("</dl>\n")
	if len(ask.Options) > 0 {
		b.WriteString("<h2>Options</h2>\n<ul>\n")
		for _, opt := range ask.Options {
			b.WriteString("<li>" + esc(opt.Key) + " — " + esc(opt.Label) + "</li>\n")
		}
		b.WriteString("</ul>\n")
	}
	switch ask.State {
	case "asked":
		b.WriteString("<h2>Answer</h2>\n" +
			formOpen(c.Path+"/answer", c.CSRF) +
			textField("answer", "Answer (option key or text)", c.Input.Get("answer"), false) +
			submitField("Answer ask"))
	case "expired":
		b.WriteString("<p>Expired asks cannot be answered.</p>\n")
	}
	_, err := io.WriteString(w, pageShell("Ask "+ask.PublicID, b.String()))
	return err
}
