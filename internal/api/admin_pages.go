package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
	"github.com/gosuda/rukie/internal/web/secheaders"
)

// adminPageHeaders locks a raw page to the same document policy as op
// pages: no scripts, no plugins, styles only from the bundled font host.
func adminPageHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", secheaders.CSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
}

// AdminRaw wraps the op mux with the page-only admin console and repository
// plumbing pages. These routes have no operation behind them: the console
// pages read the store directly and the plumbing pages call the daemon wire
// methods (op.log, claim.list, attest.list, refs.list) directly. Mutation
// forms on these pages carry the same CSRF contract as op pages, computed
// from the server key passed at wrap time.
func (d Deps) AdminRaw(next http.Handler, csrfKey []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if strings.HasPrefix(r.URL.Path, "/admin") {
				if d.serveAdminConsole(w, r, csrfKey) {
					return
				}
			}
			if d.serveRepoPlumbing(w, r) {
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// adminCSRFField mirrors the mux CSRF contract for hand-rendered pages: the
// _csrf field is the MAC of the csrf cookie under the server key.
func adminCSRFField(w http.ResponseWriter, r *http.Request, key []byte) string {
	if len(key) == 0 {
		return ""
	}
	if cookie, err := r.Cookie("__Host-rukie_csrf"); err == nil {
		return adminCSRFToken(key, cookie.Value)
	}
	var b [32]byte
	_, _ = rand.Read(b[:])
	value := base64.RawURLEncoding.EncodeToString(b[:])
	http.SetCookie(w, &http.Cookie{Name: "__Host-rukie_csrf", Value: value, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, Path: "/"})
	return adminCSRFToken(key, value)
}

func adminCSRFToken(key []byte, cookieValue string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("rukie-csrf-v1\x00"))
	mac.Write([]byte(cookieValue))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (d Deps) adminRawPrincipal(r *http.Request) op.Principal {
	return RawBytes{Deps: d}.principalFor(r)
}

// adminPageAuth enforces the same gate as the admin operations: the caller
// must carry the admin scope and belong to an active site admin. Scope first,
// like the op framework, so a scoped-down token fails closed.
func (d Deps) adminPageAuth(r *http.Request) (op.Principal, error) {
	p := d.adminRawPrincipal(r)
	if !p.Can(op.ScopeAdminWrite) {
		return op.Principal{}, op.Errorf(op.CodeForbidden, "site admin required")
	}
	admin, err := requireAdmin(r.Context(), d.Service, p)
	if err != nil {
		return op.Principal{}, err
	}
	_ = admin
	return p, nil
}

func writeAdminProblem(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if oerr, ok := errors.AsType[*op.Error](err); ok {
		switch oerr.Code {
		case op.CodeUnauthorized:
			status = http.StatusUnauthorized
		case op.CodeForbidden:
			status = http.StatusForbidden
		case op.CodeNotFound:
			status = http.StatusNotFound
		case op.CodeUnavailable:
			status = http.StatusServiceUnavailable
		}
	}
	adminPageHeaders(w)
	w.WriteHeader(status)
	_, _ = io.WriteString(w, pageShell("Admin", heading("Admin")+
		"<p>"+esc(firstLine(err.Error()))+"</p>\n"))
}

// adminPlumbProblem renders a daemon failure for a page-only surface. The
// daemon detail never reaches HTML: pages carry a fixed sentence plus the
// problem code, while JSON op errors keep the verbatim detail their contract
// already promises.
func adminPlumbProblem(title string, err error) (string, string) {
	code := op.CodeUnavailable
	if oerr, ok := errors.AsType[*op.Error](err); ok {
		code = oerr.Code
	} else if merr, ok := errors.AsType[*op.Error](mapRukError(err)); ok {
		code = merr.Code
	}
	var b strings.Builder
	b.WriteString(heading(title))
	b.WriteString("<p>The daemon request failed.</p>\n")
	b.WriteString("<p><code>" + esc(code.Slug()) + "</code></p>\n")
	return title, b.String()
}

func (d Deps) serveAdminConsole(w http.ResponseWriter, r *http.Request, csrfKey []byte) bool {
	trimmed := strings.TrimPrefix(r.URL.Path, "/admin")
	if trimmed == "" || trimmed == "/" {
		d.serveAdminDashboard(w, r)
		return true
	}
	if trimmed == "/orgs" {
		d.serveAdminOrgs(w, r)
		return true
	}
	if trimmed == "/monitor" {
		d.serveAdminMonitor(w, r)
		return true
	}
	if rest, ok := strings.CutPrefix(trimmed, "/users/"); ok && rest != "" && !strings.Contains(rest, "/") {
		d.serveAdminUserDetail(w, r, rest, csrfKey)
		return true
	}
	return false
}

func (d Deps) serveAdminDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := d.adminPageAuth(r); err != nil {
		writeAdminProblem(w, err)
		return
	}
	st, err := requireStore(d)
	if err != nil {
		writeAdminProblem(w, err)
		return
	}
	// Bounded counts only; the dashboard never loads whole tables.
	users, uerr := st.CountUsers(ctx)
	if uerr != nil {
		writeAdminProblem(w, mapAdminStore(uerr))
		return
	}
	repos, rerr := st.CountLiveRepos(ctx)
	if rerr != nil {
		writeAdminProblem(w, mapAdminStore(rerr))
		return
	}
	orgs, oerr := st.CountOrgs(ctx)
	if oerr != nil {
		writeAdminProblem(w, mapAdminStore(oerr))
		return
	}
	jobs, jerr := st.CountJobsByState(ctx)
	if jerr != nil {
		writeAdminProblem(w, mapAdminStore(jerr))
		return
	}
	var b strings.Builder
	b.WriteString(heading("Admin"))
	b.WriteString("<p>" + esc(strconv.FormatInt(users, 10)) + " users, " +
		esc(strconv.FormatInt(repos, 10)) + " live repositories, " +
		esc(strconv.FormatInt(orgs, 10)) + " orgs.</p>\n")
	b.WriteString("<ul>\n")
	for _, link := range []struct{ href, label string }{
		{"/admin/users", "Users"},
		{"/admin/orgs", "Orgs"},
		{"/admin/repos", "Repositories"},
		{"/admin/jobs", "Background jobs"},
		{"/admin/audit", "Audit log"},
		{"/admin/health", "Health"},
		{"/admin/monitor", "Monitor"},
	} {
		b.WriteString("<li><a href=\"" + esc(link.href) + "\">" + esc(link.label) + "</a></li>\n")
	}
	b.WriteString("</ul>\n")
	b.WriteString("<table>\n<thead><tr><th>Queue state</th><th>Depth</th></tr></thead>\n<tbody>\n")
	for _, state := range []string{"queued", "leased", "done", "dead"} {
		b.WriteString("<tr><td>" + esc(state) + "</td><td>" +
			esc(strconv.FormatInt(jobs[state], 10)) + "</td></tr>\n")
	}
	b.WriteString("</tbody>\n</table>\n")
	adminPageHeaders(w)
	_, _ = io.WriteString(w, pageShell("Admin", b.String()))
}

func (d Deps) serveAdminOrgs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := d.adminPageAuth(r); err != nil {
		writeAdminProblem(w, err)
		return
	}
	st, err := requireStore(d)
	if err != nil {
		writeAdminProblem(w, err)
		return
	}
	var orgs []store.OrgRow
	if rerr := st.Read(ctx, func(tx *store.ReadTx) error {
		var lerr error
		orgs, lerr = tx.ListAllOrgs("", 1000)
		return lerr
	}); rerr != nil {
		writeAdminProblem(w, mapAdminStore(rerr))
		return
	}
	var b strings.Builder
	b.WriteString(heading("Orgs"))
	b.WriteString("<table>\n<thead><tr><th>Name</th><th>Visibility</th></tr></thead>\n<tbody>\n")
	for _, org := range orgs {
		b.WriteString("<tr><td><a href=\"/" + esc(org.Name) + "\">" + esc(org.Name) + "</a></td><td>" +
			esc(org.Visibility) + "</td></tr>\n")
	}
	b.WriteString("</tbody>\n</table>\n")
	adminPageHeaders(w)
	_, _ = io.WriteString(w, pageShell("Orgs", b.String()))
}

func (d Deps) serveAdminMonitor(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := d.adminPageAuth(r); err != nil {
		writeAdminProblem(w, err)
		return
	}
	st, err := requireStore(d)
	if err != nil {
		writeAdminProblem(w, err)
		return
	}
	counts, cerr := st.CountJobsByState(ctx)
	if cerr != nil {
		writeAdminProblem(w, mapAdminStore(cerr))
		return
	}
	stats := st.Stats()
	var b strings.Builder
	b.WriteString(heading("Monitor"))
	b.WriteString("<p>Daemon: " + esc(adminDaemonState(d)) + "</p>\n")
	b.WriteString("<p>Store: " + esc(strconv.FormatUint(stats.Requests, 10)) + " requests, " +
		esc(strconv.FormatUint(stats.Batches, 10)) + " batches, " +
		esc(strconv.FormatUint(stats.RolledBack, 10)) + " rollbacks.</p>\n")
	b.WriteString("<table>\n<thead><tr><th>Queue state</th><th>Depth</th></tr></thead>\n<tbody>\n")
	for _, state := range []string{"queued", "leased", "done", "dead"} {
		b.WriteString("<tr><td>" + esc(state) + "</td><td>" +
			esc(strconv.FormatInt(counts[state], 10)) + "</td></tr>\n")
	}
	b.WriteString("</tbody>\n</table>\n")
	b.WriteString("<p><a href=\"/admin/health\">Health (JSON/HTML)</a> — " +
		"<a href=\"/admin/audit\">Audit log</a> — <a href=\"/admin/jobs\">Jobs</a></p>\n")
	adminPageHeaders(w)
	_, _ = io.WriteString(w, pageShell("Monitor", b.String()))
}

func adminDaemonState(d Deps) string {
	if d.Ruk == nil {
		return "not configured"
	}
	return "configured"
}

func (d Deps) serveAdminUserDetail(w http.ResponseWriter, r *http.Request, name string, csrfKey []byte) {
	ctx := r.Context()
	if _, err := d.adminPageAuth(r); err != nil {
		writeAdminProblem(w, err)
		return
	}
	target, terr := d.Service.GetUser(ctx, name)
	if terr != nil {
		writeAdminProblem(w, mapAuthError(terr))
		return
	}
	etag, terr := d.Service.ETagFor(ctx, target.ID)
	if terr != nil {
		writeAdminProblem(w, mapAuthError(terr))
		return
	}
	tokens, terr := d.Service.ListTokens(ctx, target.ID)
	if terr != nil {
		writeAdminProblem(w, mapAdminStore(terr))
		return
	}
	csrf := adminCSRFField(w, r, csrfKey)
	var b strings.Builder
	b.WriteString(heading("User " + target.Name))
	b.WriteString("<p>Admin: " + esc(strconv.FormatBool(target.IsAdmin)) + " — Active: " +
		esc(strconv.FormatBool(target.IsActive)) + " — Tokens: " +
		esc(strconv.Itoa(len(tokens))) + "</p>\n")
	b.WriteString("<h2>Reset password</h2>\n" +
		"<form method=\"post\" action=\"/admin/users/" + esc(target.Name) + "/reset-password\">\n" +
		"<input type=\"hidden\" name=\"_csrf\" value=\"" + esc(csrf) + "\">\n" +
		"<input type=\"hidden\" name=\"_idem\" value=\"" + esc(newIdem()) + "\">\n" +
		textField("password", "New password", "", true) +
		checkField("random", "Random temporary password", false) +
		checkField("keep_tokens", "Keep scoped tokens", false) +
		submitField("Reset password"))
	b.WriteString("<h2>Disable or enable</h2>\n" +
		"<form method=\"post\" action=\"/admin/users/" + esc(target.Name) + "\">\n" +
		"<input type=\"hidden\" name=\"_csrf\" value=\"" + esc(csrf) + "\">\n" +
		"<input type=\"hidden\" name=\"_idem\" value=\"" + esc(newIdem()) + "\">\n" +
		hiddenField("_etag", etag) +
		hiddenField("is_active", "0") +
		submitField("Disable account"))
	b.WriteString("<h2>Delete</h2>\n" +
		"<form method=\"post\" action=\"/admin/users/" + esc(target.Name) + "/delete\">\n" +
		"<input type=\"hidden\" name=\"_csrf\" value=\"" + esc(csrf) + "\">\n" +
		"<input type=\"hidden\" name=\"_idem\" value=\"" + esc(newIdem()) + "\">\n" +
		hiddenField("_etag", etag) +
		textField("confirm", "Confirm username", "", false) +
		submitField("Delete user"))
	adminPageHeaders(w)
	_, _ = io.WriteString(w, pageShell("User "+target.Name, b.String()))
}

// serveRepoPlumbing serves /{owner}/{repo}/-/ops|claims|attestations|refs.
// It reports whether the path belonged to the plumbing namespace.
func (d Deps) serveRepoPlumbing(w http.ResponseWriter, r *http.Request) bool {
	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")
	if len(parts) != 4 || parts[2] != "-" {
		return false
	}
	owner, repo, kind := parts[0], parts[1], parts[3]
	switch kind {
	case "ops", "claims", "attestations", "refs":
	default:
		return false
	}
	ctx := r.Context()
	p, err := d.adminPageAuth(r)
	if err != nil {
		writeAdminProblem(w, err)
		return true
	}
	rq, rerr := d.requester(ctx, p)
	if rerr != nil {
		writeAdminProblem(w, rerr)
		return true
	}
	rc, cerr := d.resolveRepo(ctx, rq, owner, repo, false)
	if cerr != nil {
		writeAdminProblem(w, cerr)
		return true
	}
	// Per-repo authorization applies to site admins too: a token scoped to
	// another repository must not read this one. checkRepoAccess folds the
	// token's repo restriction into the decision and answers 404 either way
	// so private repositories do not leak existence.
	if derr := d.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
		writeAdminProblem(w, derr)
		return true
	}
	daemon, oerr := d.openDaemonRepo(ctx, rc.row)
	if oerr != nil {
		writeAdminProblem(w, oerr)
		return true
	}
	var title, body string
	switch kind {
	case "ops":
		title, body = d.plumbOpLog(ctx, daemon, owner, repo)
	case "claims":
		title, body = d.plumbClaims(ctx, daemon, owner, repo)
	case "attestations":
		title, body = d.plumbAttestations(ctx, r, daemon, owner, repo)
	case "refs":
		title, body = d.plumbRefs(ctx, daemon, owner, repo)
	}
	adminPageHeaders(w)
	_, _ = io.WriteString(w, pageShell(title, body))
	return true
}

type opLogPageReply struct {
	Entries struct {
		Items []map[string]any `json:"items"`
	} `json:"entries"`
}

func (d Deps) plumbOpLog(ctx context.Context, daemon ruk.Repo, owner, repo string) (string, string) {
	var reply opLogPageReply
	if rerr := d.callRuk(ctx, daemon, wire.MethodOpLog, map[string]any{"page": map[string]any{"limit": 100}}, &reply); rerr != nil {
		return adminPlumbProblem("Operations", rerr)
	}
	var b strings.Builder
	b.WriteString(heading("Operations in " + owner + " / " + repo))
	b.WriteString("<ol>\n")
	for _, item := range reply.Entries.Items {
		id, _ := item["id"].(string)
		kind, _ := item["kind"].(string)
		b.WriteString("<li><code>" + esc(id) + "</code> " + esc(kind) + "</li>\n")
	}
	b.WriteString("</ol>\n")
	return "Operations", b.String()
}

func (d Deps) plumbClaims(ctx context.Context, daemon ruk.Repo, owner, repo string) (string, string) {
	items, rerr := d.liveClaims(ctx, daemon)
	if rerr != nil {
		return adminPlumbProblem("Claims", rerr)
	}
	var b strings.Builder
	b.WriteString(heading("Claims in " + owner + " / " + repo))
	b.WriteString("<ul>\n")
	for _, item := range items {
		subject := ""
		if item.Subject != nil {
			subject = item.Subject.Task
		}
		b.WriteString("<li>" + esc(item.Holder) + " holds <code>" + esc(subject) + "</code> (expires " +
			esc(item.ExpiresAt) + ")</li>\n")
	}
	b.WriteString("</ul>\n")
	return "Claims", b.String()
}

func (d Deps) plumbAttestations(ctx context.Context, r *http.Request, daemon ruk.Repo, owner, repo string) (string, string) {
	subject := r.URL.Query().Get("subject")
	var b strings.Builder
	b.WriteString(heading("Attestations in " + owner + " / " + repo))
	b.WriteString("<form method=\"get\" action=\"/" + esc(owner) + "/" + esc(repo) + "/-/attestations\">\n" +
		"<label>Subject\n<input type=\"text\" name=\"subject\" value=\"" + esc(subject) + "\">\n</label>\n" +
		"<button type=\"submit\">Verify</button>\n</form>\n")
	if subject == "" {
		return "Attestations", b.String()
	}
	var reply attestListReply
	if rerr := d.callRuk(ctx, daemon, wire.MethodAttestList,
		map[string]any{"subject": subject, "page": map[string]any{"limit": 100}}, &reply); rerr != nil {
		return adminPlumbProblem("Attestations", rerr)
	}
	b.WriteString("<ul>\n")
	for _, record := range reply.Records.Items {
		b.WriteString("<li>" + esc(record.Kind) + " / " + esc(record.Result) + " by " +
			esc(record.Signer) + " (" + esc(record.Status) + ")</li>\n")
	}
	b.WriteString("</ul>\n")
	return "Attestations", b.String()
}

func (d Deps) plumbRefs(ctx context.Context, daemon ruk.Repo, owner, repo string) (string, string) {
	var reply refsListReply
	if rerr := d.callRuk(ctx, daemon, wire.MethodRefsList,
		map[string]any{"prefix": "refs/", "limit": 200}, &reply); rerr != nil {
		return adminPlumbProblem("Refs", rerr)
	}
	var b strings.Builder
	b.WriteString(heading("Refs in " + owner + " / " + repo))
	b.WriteString("<ul>\n")
	for _, entry := range reply.Refs.Items {
		target := ""
		if entry.Target.Direct != nil {
			target = *entry.Target.Direct
		}
		b.WriteString("<li><code>" + esc(entry.Name) + "</code> → <code>" + esc(target) + "</code></li>\n")
	}
	b.WriteString("</ul>\n")
	return "Refs", b.String()
}
