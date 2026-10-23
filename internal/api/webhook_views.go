package api

import (
	"cmp"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
	"github.com/gosuda/rukie/internal/webhook"
)

// RegisterWebhookViews registers the zero-JS webhook settings pages.
func RegisterWebhookViews(v *op.Views) {
	op.PageView(v, "webhook.endpoint.list", writeWebhookEndpointList)
	op.PageView(v, "webhook.endpoint.get", writeWebhookEndpointDetail)
	op.PageView(v, "webhook.message.list", writeWebhookMessageList)
	op.PageView(v, "webhook.message.get", writeWebhookMessageDetail)
	op.PageView(v, "webhook.attempt.list", writeWebhookAttemptList)
}

// customCSRFToken reimplements the op layer token (internal/op/mount_huma.go
// csrfToken) for wrapper-rendered pages: the token is bound to the CSRF
// cookie value, not to the page, so tokens minted here validate on op form
// posts that share the server CSRF key.
func customCSRFToken(key []byte, cookieValue string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("rukie-csrf-v1\x00"))
	mac.Write([]byte(cookieValue))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// ensureCustomCSRF returns a form token for a wrapper-rendered page, setting
// the CSRF cookie when the browser has none yet. Mirrors the op layer cookie
// attributes.
func ensureCustomCSRF(w http.ResponseWriter, r *http.Request, key []byte) string {
	if cookie, err := r.Cookie("__Host-rukie_csrf"); err == nil && cookie.Value != "" {
		return customCSRFToken(key, cookie.Value)
	}
	var raw [32]byte
	_, _ = rand.Read(raw[:])
	value := base64.RawURLEncoding.EncodeToString(raw[:])
	http.SetCookie(w, &http.Cookie{Name: "__Host-rukie_csrf", Value: value, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, Path: "/"})
	return customCSRFToken(key, value)
}

// wantsCustomHTML reports whether the request prefers a hand-authored page
// over the op mux rendering. JSON clients fall through to the op table.
func wantsCustomHTML(r *http.Request) bool {
	accept := strings.ToLower(r.Header.Get("Accept"))
	if accept == "" || accept == "*/*" {
		return true
	}
	for item := range strings.SplitSeq(accept, ",") {
		media, _, _ := strings.Cut(strings.TrimSpace(item), ";")
		if strings.TrimSpace(media) == "application/json" {
			return false
		}
	}
	return true
}

func writeCustomProblem(w http.ResponseWriter, err error) {
	code := op.CodeInternal
	detail := "internal error"
	if oerr, ok := errors.AsType[*op.Error](err); ok {
		code, detail = oerr.Code, oerr.Detail
	}
	setCIPageHeaders(w)
	w.WriteHeader(problemStatus(code))
	_, _ = io.WriteString(w, pageShell("Error", heading("Error")+para(detail)))
}

func webhookScopeCell(m WebhookEndpointJSON) string {
	if m.Scope == "owner" {
		return "owner " + m.Owner
	}
	return "repo " + m.Repo
}

func writeWebhookEndpointList(w io.Writer, c op.ViewCtx, out WebhookEndpointListOut) error {
	base := c.Path
	var b strings.Builder
	b.WriteString(heading("Webhooks"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<table>\n<tr><th>ID</th><th>Scope</th><th>URL</th><th>Events</th><th>Active</th><th>Failures</th><th>Status</th></tr>\n")
	for _, m := range out.Endpoints {
		b.WriteString("<tr><td><a href=\"" + esc(base+"/"+m.ID) + "\">" + esc(m.ID) + "</a></td>" +
			"<td>" + esc(webhookScopeCell(m)) + "</td><td>" + esc(m.URL) + "</td>" +
			"<td>" + esc(strings.Join(m.EventTypes, ", ")) + "</td>" +
			"<td>" + esc(strconv.FormatBool(m.Active)) + "</td>" +
			"<td>" + esc(strconv.FormatInt(m.ConsecutiveFailures, 10)) + "</td>" +
			"<td>" + esc(m.LastStatus) + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	if out.Next != "" {
		b.WriteString("<p>More results are available through the Link header.</p>\n")
	}
	types := strings.Join(slices.Sorted(maps.Keys(webhook.Catalogue())), "\n")
	b.WriteString("<h2>New endpoint</h2>\n" +
		formOpen(base, c.CSRF) +
		textField("scope", "Scope (repo or owner)", cmp.Or(c.Input.Get("scope"), "repo"), false) +
		textField("owner_name", "Owner (owner scope only, defaults to path owner)", c.Input.Get("owner_name"), false) +
		textField("url", "URL (https)", c.Input.Get("url"), false) +
		"<label>Event types (one per line)<br>\n<textarea name=\"event_types\" rows=\"6\" cols=\"40\">" + esc(c.Input.Get("event_types")) + "</textarea></label>\n" +
		"<label>Ref globs, ref.updated only (one per line)<br>\n<textarea name=\"ref_globs\" rows=\"3\" cols=\"40\">" + esc(c.Input.Get("ref_globs")) + "</textarea></label>\n" +
		"<p>Known event types:</p>\n<pre>" + esc(types) + "</pre>\n" +
		checkField("active", "Active", c.Input.Get("active") == "" || c.Input.Get("active") != "0") +
		textField("concurrency", "Concurrency (1..16, default 4)", cmp.Or(c.Input.Get("concurrency"), "4"), false) +
		textField("timeout_seconds", "Timeout seconds (5..60, default 20)", cmp.Or(c.Input.Get("timeout_seconds"), "20"), false) +
		textField("note", "Note", c.Input.Get("note"), false) +
		submitField("Create endpoint"))
	_, err := io.WriteString(w, pageShell("Webhooks", b.String()))
	return err
}

func writeWebhookEndpointDetail(w io.Writer, c op.ViewCtx, out WebhookEndpointGetOut) error {
	m := out.Endpoint
	listBase := c.Path
	if m.ID != "" {
		listBase = strings.TrimSuffix(c.Path, "/"+m.ID)
	}
	var b strings.Builder
	b.WriteString(heading("Endpoint " + m.ID))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<dl>\n" +
		"<dt>ID</dt><dd>" + esc(m.ID) + "</dd>\n" +
		"<dt>Scope</dt><dd>" + esc(webhookScopeCell(m)) + "</dd>\n" +
		"<dt>URL</dt><dd>" + esc(m.URL) + "</dd>\n" +
		"<dt>Event types</dt><dd>" + esc(strings.Join(m.EventTypes, ", ")) + "</dd>\n" +
		"<dt>Ref globs</dt><dd>" + esc(strings.Join(m.RefGlobs, ", ")) + "</dd>\n" +
		"<dt>Active</dt><dd>" + esc(strconv.FormatBool(m.Active)) + "</dd>\n" +
		"<dt>Concurrency</dt><dd>" + esc(strconv.FormatInt(m.Concurrency, 10)) + "</dd>\n" +
		"<dt>Timeout seconds</dt><dd>" + esc(strconv.FormatInt(m.TimeoutSeconds, 10)) + "</dd>\n" +
		"<dt>Note</dt><dd>" + esc(m.Note) + "</dd>\n" +
		"<dt>Last status</dt><dd>" + esc(m.LastStatus) + "</dd>\n" +
		"<dt>Consecutive failures</dt><dd>" + esc(strconv.FormatInt(m.ConsecutiveFailures, 10)) + "</dd>\n" +
		"<dt>Disabled at</dt><dd>" + esc(strconv.FormatInt(m.DisabledAt, 10)) + "</dd>\n" +
		"<dt>Disable reason</dt><dd>" + esc(m.DisableReason) + "</dd>\n" +
		"</dl>\n")
	b.WriteString("<p>The secret shows once, on create and rotate, and is never stored in clear.</p>\n")
	b.WriteString("<p><a href=\"" + esc(c.Path+"/messages") + "\">Messages</a> — " +
		"<a href=\"" + esc(listBase) + "\">Endpoints</a></p>\n")
	b.WriteString("<h2>Update</h2>\n" +
		formOpen(c.Path, c.CSRF) +
		textField("url", "URL", m.URL, false) +
		"<label>Event types (one per line)<br>\n<textarea name=\"event_types\" rows=\"6\" cols=\"40\">" + esc(strings.Join(m.EventTypes, "\n")) + "</textarea></label>\n" +
		"<label>Ref globs (one per line)<br>\n<textarea name=\"ref_globs\" rows=\"3\" cols=\"40\">" + esc(strings.Join(m.RefGlobs, "\n")) + "</textarea></label>\n" +
		checkField("active", "Active", m.Active) +
		textField("concurrency", "Concurrency", strconv.FormatInt(m.Concurrency, 10), false) +
		textField("timeout_seconds", "Timeout seconds", strconv.FormatInt(m.TimeoutSeconds, 10), false) +
		textField("note", "Note", m.Note, false) +
		submitField("Update endpoint"))
	b.WriteString("<h2>Rotate secret</h2>\n" +
		formOpen(c.Path+"/rotate-secret", c.CSRF) +
		submitField("Rotate secret"))
	b.WriteString("<h2>Send test ping</h2>\n" +
		formOpen(c.Path+"/test", c.CSRF) +
		submitField("Send endpoint.tested ping"))
	b.WriteString("<h2>Delete endpoint</h2>\n" +
		formOpen(c.Path+"/delete", c.CSRF) +
		submitField("Delete endpoint "+m.ID))
	b.WriteString("<section>\n<h2>Verifying deliveries</h2>\n" +
		"<p>Standard Webhooks recipe. Verify against the raw request bytes; never parse or re-serialize the body first.</p>\n" +
		"<pre>input = ASCII(webhook-id) + \".\" + ASCII(webhook-timestamp) + \".\" + raw_body\n" +
		"for entry in split(webhook-signature, \" \"):\n" +
		"  if entry starts with \"v1,\":\n" +
		"    b64 = entry after \"v1,\"\n" +
		"    accept when HMAC-SHA256(secret, input) constant-time equals base64-decode(b64)</pre>\n" +
		"<p>Accept on any matching entry. Reject when the timestamp differs from now by more than 300 seconds. " +
		"Remember every webhook-id for at least 24 hours and drop repeats.</p>\n" +
		"</section>\n")
	_, err := io.WriteString(w, pageShell("Endpoint "+m.ID, b.String()))
	return err
}

func writeWebhookMessageList(w io.Writer, c op.ViewCtx, out WebhookMessageListOut) error {
	endpointBase := strings.TrimSuffix(c.Path, "/messages")
	var b strings.Builder
	b.WriteString(heading("Endpoint messages"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<table>\n<tr><th>ID</th><th>Type</th><th>State</th><th>Attempts</th><th>Last error</th><th>Status</th></tr>\n")
	for _, m := range out.Messages {
		b.WriteString("<tr><td><a href=\"" + esc(c.Path+"/"+m.ID) + "\">" + esc(m.ID) + "</a></td>" +
			"<td>" + esc(m.EventType) + "</td><td>" + esc(m.State) + "</td>" +
			"<td>" + esc(strconv.FormatInt(m.AttemptCount, 10)) + "</td>" +
			"<td>" + esc(m.LastError) + "</td>" +
			"<td>" + esc(strconv.FormatInt(m.LastStatusCode, 10)) + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	if out.Next != "" {
		b.WriteString("<p>More results are available through the Link header.</p>\n")
	}
	b.WriteString("<p><a href=\"" + esc(endpointBase) + "\">Endpoint</a></p>\n")
	_, err := io.WriteString(w, pageShell("Endpoint messages", b.String()))
	return err
}

func writeWebhookMessageDetail(w io.Writer, c op.ViewCtx, out WebhookMessageGetOut) error {
	m := out.Message
	messagesBase := c.Path
	if m.ID != "" {
		messagesBase = strings.TrimSuffix(c.Path, "/"+m.ID)
	}
	endpointBase := strings.TrimSuffix(messagesBase, "/messages")
	var b strings.Builder
	b.WriteString(heading("Message " + m.ID))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<dl>\n" +
		"<dt>ID</dt><dd>" + esc(m.ID) + "</dd>\n" +
		"<dt>Event type</dt><dd>" + esc(m.EventType) + "</dd>\n" +
		"<dt>State</dt><dd>" + esc(m.State) + "</dd>\n" +
		"<dt>Attempt count</dt><dd>" + esc(strconv.FormatInt(m.AttemptCount, 10)) + "</dd>\n" +
		"<dt>Next attempt</dt><dd>" + esc(strconv.FormatInt(m.NextAttemptAt, 10)) + "</dd>\n" +
		"<dt>Last error</dt><dd>" + esc(m.LastError) + "</dd>\n" +
		"<dt>Last status</dt><dd>" + esc(strconv.FormatInt(m.LastStatusCode, 10)) + "</dd>\n" +
		"<dt>Delivered at</dt><dd>" + esc(strconv.FormatInt(m.DeliveredAt, 10)) + "</dd>\n" +
		"<dt>Redelivery of</dt><dd>" + esc(m.RedeliveryOf) + "</dd>\n" +
		"</dl>\n")
	b.WriteString("<h2>Payload</h2>\n<pre>" + esc(string(m.Payload)) + "</pre>\n")
	b.WriteString("<p><a href=\"" + esc(c.Path+"/attempts") + "\">Attempts</a> — " +
		"<a href=\"" + esc(messagesBase) + "\">Messages</a> — " +
		"<a href=\"" + esc(endpointBase) + "\">Endpoint</a></p>\n")
	b.WriteString("<h2>Redeliver</h2>\n" +
		formOpen(c.Path+"/redeliver", c.CSRF) +
		submitField("Redeliver message "+m.ID))
	_, err := io.WriteString(w, pageShell("Message "+m.ID, b.String()))
	return err
}

func writeWebhookAttemptList(w io.Writer, c op.ViewCtx, out WebhookAttemptListOut) error {
	messageBase := strings.TrimSuffix(c.Path, "/attempts")
	var b strings.Builder
	b.WriteString(heading("Message attempts"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<table>\n<tr><th>No</th><th>Started</th><th>Finished</th><th>Status</th><th>Error</th><th>Duration ms</th></tr>\n")
	for _, a := range out.Attempts {
		b.WriteString("<tr><td>" + esc(strconv.FormatInt(a.AttemptNo, 10)) + "</td>" +
			"<td>" + esc(strconv.FormatInt(a.StartedAt, 10)) + "</td>" +
			"<td>" + esc(strconv.FormatInt(a.FinishedAt, 10)) + "</td>" +
			"<td>" + esc(strconv.FormatInt(a.HTTPStatus, 10)) + "</td>" +
			"<td>" + esc(a.Error) + "</td>" +
			"<td>" + esc(strconv.FormatInt(a.DurationMs, 10)) + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	for _, a := range out.Attempts {
		body := a.RespBody
		const attemptBodyCap = 2048
		truncated := false
		if len(body) > attemptBodyCap {
			body, truncated = body[:attemptBodyCap], true
		}
		b.WriteString("<h3>Attempt " + esc(strconv.FormatInt(a.AttemptNo, 10)) + "</h3>\n")
		if len(a.RespHeaders) > 0 {
			b.WriteString("<pre>" + esc(string(a.RespHeaders)) + "</pre>\n")
		}
		if body != "" {
			b.WriteString("<pre>" + esc(body) + "</pre>\n")
		}
		if truncated {
			b.WriteString("<p>Response body truncated for display; the full bytes stay in the store.</p>\n")
		}
	}
	b.WriteString("<p><a href=\"" + esc(messageBase) + "\">Message</a></p>\n")
	_, err := io.WriteString(w, pageShell("Message attempts", b.String()))
	return err
}

// serveWebhookOwnerHooks renders the owner hooks page: every owner-scope
// endpoint of the owner with a create form. Owner-admin gated, HTML only.
func (d Deps) serveWebhookOwnerHooks(w http.ResponseWriter, r *http.Request, owner string) {
	ctx := r.Context()
	principal := RawBytes{Deps: d}.principalFor(r)
	rq, err := d.requester(ctx, principal)
	if err != nil {
		writeCustomProblem(w, err)
		return
	}
	resolved, err := d.resolveWebhookOwner(ctx, owner)
	if err != nil {
		writeCustomProblem(w, err)
		return
	}
	if err := d.requireOwnerAdmin(rq, resolved); err != nil {
		writeCustomProblem(w, err)
		return
	}
	rows := []store.WebhookEndpointRow{}
	serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
		got, _, lerr := tx.ListWebhookEndpointsByOwner(resolved.id, "", 100)
		if lerr != nil {
			return lerr
		}
		rows = got
		return nil
	})
	if serr != nil {
		writeCustomProblem(w, mapWebhookStore(serr, "webhook endpoint list failed"))
		return
	}
	csrf := ensureCustomCSRF(w, r, d.CSRFKey)
	action := "/" + resolved.name + "/-/settings/hooks"
	var b strings.Builder
	b.WriteString(heading("Webhooks for " + resolved.name))
	b.WriteString("<table>\n<tr><th>ID</th><th>URL</th><th>Events</th><th>Active</th><th>Failures</th><th>Status</th></tr>\n")
	for _, row := range rows {
		b.WriteString("<tr><td>" + esc(row.ID) + "</td>" +
			"<td>" + esc(row.URL) + "</td>" +
			"<td>" + esc(strings.Join(row.EventTypes, ", ")) + "</td>" +
			"<td>" + esc(strconv.FormatBool(row.Active)) + "</td>" +
			"<td>" + esc(strconv.FormatInt(row.ConsecutiveFailures, 10)) + "</td>" +
			"<td>" + esc(row.LastStatus) + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	types := strings.Join(slices.Sorted(maps.Keys(webhook.Catalogue())), "\n")
	b.WriteString("<h2>New owner endpoint</h2>\n" +
		formOpen(action, csrf) +
		hiddenField("scope", "owner") +
		textField("url", "URL (https)", "", false) +
		"<label>Event types (one per line)<br>\n<textarea name=\"event_types\" rows=\"6\" cols=\"40\"></textarea></label>\n" +
		"<label>Ref globs, ref.updated only (one per line)<br>\n<textarea name=\"ref_globs\" rows=\"3\" cols=\"40\"></textarea></label>\n" +
		"<p>Known event types:</p>\n<pre>" + esc(types) + "</pre>\n" +
		checkField("active", "Active", true) +
		textField("concurrency", "Concurrency (1..16, default 4)", "4", false) +
		textField("timeout_seconds", "Timeout seconds (5..60, default 20)", "20", false) +
		textField("note", "Note", "", false) +
		submitField("Create owner endpoint"))
	setCIPageHeaders(w)
	_, _ = io.WriteString(w, pageShell("Webhooks for "+resolved.name, b.String()))
}

// WebhookPages serves the owner hooks page outside the op table. All other
// paths fall through to the op mux.
func (d Deps) WebhookPages(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			trimmed := strings.TrimPrefix(r.URL.Path, "/")
			parts := strings.Split(trimmed, "/")
			if len(parts) == 4 && parts[0] != "" && parts[1] == "-" && parts[2] == "settings" && parts[3] == "hooks" {
				if !wantsCustomHTML(r) {
					next.ServeHTTP(w, r)
					return
				}
				d.serveWebhookOwnerHooks(w, r, parts[0])
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
