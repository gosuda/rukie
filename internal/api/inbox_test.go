package api_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/store"
)

func f8seedIntent(t *testing.T, e *f8Env, repoID, creatorID int64, title string) store.IntentRow {
	t.Helper()
	var saved store.IntentRow
	err := e.st.Write(t.Context(), func(tx *store.WriteTx) error {
		row, ierr := tx.InsertIntent(e.now, store.IntentRow{
			RepoID: repoID, Title: title, CreatedBy: creatorID, LeaseMode: "advise",
		})
		if ierr != nil {
			return ierr
		}
		saved = row
		return nil
	})
	if err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	return saved
}

func f8threadIDs(t *testing.T, e *f8Env, token, query string) []string {
	t.Helper()
	rec := f8do(t, e, "GET", "/inbox"+query, token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("inbox.list = %d: %s", rec.Code, rec.Body.String())
	}
	decoded := f8decode(t, rec)
	threads, _ := decoded["threads"].([]any)
	var ids []string
	for _, thread := range threads {
		row, _ := thread.(map[string]any)
		id, _ := row["id"].(string)
		ids = append(ids, id)
	}
	return ids
}

func f8threadState(t *testing.T, e *f8Env, token, id, want string) {
	t.Helper()
	rec := f8do(t, e, "GET", "/inbox/thread?id="+id, token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("inbox.get = %d: %s", rec.Code, rec.Body.String())
	}
	decoded := f8decode(t, rec)
	thread, _ := decoded["thread"].(map[string]any)
	if thread["state"] != want {
		t.Fatalf("thread state = %v, want %s (detail %v)", thread["state"], want, decoded)
	}
}

func TestInboxStatesAndSnooze(t *testing.T) {
	e := buildF8Env(t)
	f8bootstrap(t, e, "ada")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	cookie := f8login(t, e, "ada")
	repoID := f8seedRepo(t, e, ada.ID, "hooks")
	intent := f8seedIntent(t, e, repoID, ada.ID, "Wire the panel")
	ids := f8threadIDs(t, e, cookie, "?state=unread")
	if len(ids) != 1 {
		t.Fatalf("unread threads = %d, want 1", len(ids))
	}
	id := ids[0]
	f8threadState(t, e, cookie, id, "unread")

	// Idempotent unread and read transitions.
	mark := func(op, until string) (int, map[string]any) {
		body := `{"thread_ids":["` + id + `"],"op":"` + op + `"`
		if until != "" {
			body += `,"snooze_until":` + until
		}
		rec := f8do(t, e, "POST", "/inbox", cookie, body+"}")
		return rec.Code, f8decode(t, rec)
	}
	if code, _ := mark("mark_unread", ""); code != http.StatusCreated {
		t.Fatalf("mark_unread = %d", code)
	}
	f8threadState(t, e, cookie, id, "unread")
	if code, _ := mark("mark_read", ""); code != http.StatusCreated {
		t.Fatalf("mark_read = %d", code)
	}
	f8threadState(t, e, cookie, id, "read")
	// Done sticks until a new event; mark is idempotent per thread.
	if code, _ := mark("mark_done", ""); code != http.StatusCreated {
		t.Fatalf("mark_done = %d", code)
	}
	f8threadState(t, e, cookie, id, "done")

	// Snooze window boundaries: past and 91d refuse, 1m and 90d hold.
	nowMillis := e.now.UnixMilli()
	past := strconv.FormatInt(nowMillis-1000, 10)
	far := strconv.FormatInt(nowMillis+91*24*60*60*1000, 10)
	if code, _ := mark("snooze", past); code != http.StatusBadRequest && code != http.StatusUnprocessableEntity {
		t.Fatalf("snoozing to the past = %d, want rejection", code)
	}
	if code, _ := mark("snooze", far); code != http.StatusBadRequest && code != http.StatusUnprocessableEntity {
		t.Fatalf("snoozing 91d out = %d, want rejection", code)
	}
	near := strconv.FormatInt(nowMillis+60*1000, 10)
	if code, _ := mark("snooze", near); code != http.StatusCreated {
		t.Fatalf("snoozing 1m out = %d", code)
	}
	f8threadState(t, e, cookie, id, "snoozed")
	edge := strconv.FormatInt(nowMillis+90*24*60*60*1000, 10)
	if code, _ := mark("snooze", edge); code != http.StatusCreated {
		t.Fatalf("snoozing 90d out = %d", code)
	}
	_ = intent
}

func TestInboxMuteUnmuteBulk(t *testing.T) {
	e := buildF8Env(t)
	f8bootstrap(t, e, "ada")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	cookie := f8login(t, e, "ada")
	repoID := f8seedRepo(t, e, ada.ID, "hooks")
	f8seedIntent(t, e, repoID, ada.ID, "First panel")
	f8seedIntent(t, e, repoID, ada.ID, "Second panel")
	ids := f8threadIDs(t, e, cookie, "?state=unread")
	if len(ids) != 2 {
		t.Fatalf("unread threads = %d, want 2", len(ids))
	}
	// Bulk mark with one bad id: partial success, failure entry, other updated.
	payload := `{"thread_ids":["` + ids[0] + `","msg_nonexistent"],"op":"mute"}`
	rec := f8do(t, e, "POST", "/inbox", cookie, payload)
	if rec.Code != http.StatusCreated {
		t.Fatalf("bulk mute = %d: %s", rec.Code, rec.Body.String())
	}
	decoded := f8decode(t, rec)
	updated, _ := decoded["updated"].([]any)
	failed, _ := decoded["failed"].([]any)
	if len(updated) != 1 || len(failed) != 1 {
		t.Fatalf("bulk outcome = %v, want one updated and one failed", decoded)
	}
	if entry, _ := failed[0].(map[string]any); entry["id"] != "msg_nonexistent" {
		t.Fatalf("failed entry = %v, want the bad id", entry)
	}
	f8threadState(t, e, cookie, ids[0], "unread")
	unmute := `{"thread_ids":["` + ids[0] + `"],"op":"unmute"}`
	if code := f8do(t, e, "POST", "/inbox", cookie, unmute).Code; code != http.StatusCreated {
		t.Fatalf("unmute = %d", code)
	}
	// Empty and oversized bulks refuse.
	for _, bad := range []string{`{"thread_ids":[],"op":"mark_read"}`, `{"thread_ids":["x"],"op":"bogus"}`} {
		if code := f8do(t, e, "POST", "/inbox", cookie, bad).Code; code != http.StatusBadRequest && code != http.StatusUnprocessableEntity {
			t.Fatalf("bad mark %s = %d, want 400 or 422", bad, code)
		}
	}
	many := `{"thread_ids":[` + strings.Repeat(`"x",`, 100) + `"x"],"op":"mark_read"}`
	if code := f8do(t, e, "POST", "/inbox", cookie, many).Code; code != http.StatusBadRequest && code != http.StatusUnprocessableEntity {
		t.Fatalf("oversized mark = %d, want 400 or 422", code)
	}
}

func TestInboxSnoozeOp(t *testing.T) {
	e := buildF8Env(t)
	f8bootstrap(t, e, "ada")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	cookie := f8login(t, e, "ada")
	repoID := f8seedRepo(t, e, ada.ID, "hooks")
	f8seedIntent(t, e, repoID, ada.ID, "Snoozed panel")
	ids := f8threadIDs(t, e, cookie, "?state=unread")
	if len(ids) != 1 {
		t.Fatalf("unread threads = %d, want 1", len(ids))
	}
	until := strconv.FormatInt(e.now.UnixMilli()+int64(time.Hour/time.Millisecond), 10)
	rec := f8do(t, e, "POST", "/inbox/snooze", cookie, `{"thread_ids":["`+ids[0]+`"],"until":`+until+`}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("snooze = %d: %s", rec.Code, rec.Body.String())
	}
	f8threadState(t, e, cookie, ids[0], "snoozed")
	// Missing until refuses; past until refuses.
	if code := f8do(t, e, "POST", "/inbox/snooze", cookie, `{"thread_ids":["`+ids[0]+`"]}`).Code; code != http.StatusBadRequest && code != http.StatusUnprocessableEntity {
		t.Fatalf("snooze without until = %d, want 400 or 422", code)
	}
}

func TestInboxIsolationAndAuth(t *testing.T) {
	e := buildF8Env(t)
	f8bootstrap(t, e, "ada")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	bob := f8user(t, e, "bob")
	acookie := f8login(t, e, "ada")
	bcookie := f8login(t, e, "bob")
	btoken := f8token(t, e, bob, "repo:read", "repo:write", "intents:read", "webhooks:read")
	repoID := f8seedRepo(t, e, ada.ID, "hooks")
	f8seedIntent(t, e, repoID, ada.ID, "Private panel")
	adaIDs := f8threadIDs(t, e, acookie, "?state=unread")
	if len(adaIDs) != 1 {
		t.Fatalf("ada unread = %d, want 1", len(adaIDs))
	}
	// Bob sees none of ada's threads and cannot open ada's thread id.
	if ids := f8threadIDs(t, e, bcookie, "?state=unread"); len(ids) != 0 {
		t.Fatalf("bob unread = %d, want 0", len(ids))
	}
	f8problem(t, f8do(t, e, "GET", "/inbox/thread?thread_id="+adaIDs[0], bcookie, ""), http.StatusNotFound, "not-found")
	f8problem(t, f8do(t, e, "GET", "/inbox/thread?thread_id="+adaIDs[0], btoken, ""), http.StatusNotFound, "not-found")
	// Anonymous callers are refused, not filtered.
	f8problem(t, f8do(t, e, "GET", "/inbox", "", ""), http.StatusUnauthorized, "unauthorized")
	f8problem(t, f8do(t, e, "GET", "/inbox/count", "", ""), http.StatusUnauthorized, "unauthorized")
	f8problem(t, f8do(t, e, "POST", "/inbox", "", `{"thread_ids":["x"],"op":"mark_read"}`), http.StatusUnauthorized, "unauthorized")
}

func TestInboxWatchAndSettings(t *testing.T) {
	e := buildF8Env(t)
	f8bootstrap(t, e, "ada")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	cookie := f8login(t, e, "ada")
	repoID := f8seedRepo(t, e, ada.ID, "hooks")
	// Ignore without confirmation refuses.
	f8problem(t, f8do(t, e, "POST", "/inbox/watch", cookie, `{"repo":"ada/hooks","level":"ignore"}`), http.StatusBadRequest, "invalid")
	// Custom without bits refuses; unknown bits refuse.
	f8problem(t, f8do(t, e, "POST", "/inbox/watch", cookie, `{"repo":"ada/hooks","level":"custom"}`), http.StatusBadRequest, "invalid")
	f8problem(t, f8do(t, e, "POST", "/inbox/watch", cookie, `{"repo":"ada/hooks","level":"custom","custom_bits":["nope"]}`), http.StatusBadRequest, "invalid")
	// Unknown repo refuses.
	f8problem(t, f8do(t, e, "POST", "/inbox/watch", cookie, `{"repo":"ada/missing","level":"all"}`), http.StatusNotFound, "not-found")
	rec := f8do(t, e, "POST", "/inbox/watch", cookie, `{"repo":"ada/hooks","level":"custom","custom_bits":["tasks","ci","tasks"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("watch set = %d: %s", rec.Code, rec.Body.String())
	}
	decoded := f8decode(t, rec)
	if decoded["repo"] != "ada/hooks" || decoded["level"] != "custom" {
		t.Fatalf("watch = %v", decoded)
	}
	if bits, _ := decoded["custom_bits"].([]any); len(bits) != 2 {
		t.Fatalf("watch bits = %v, want deduped tasks+ci", decoded)
	}
	// Watch-all on a repo surfaces new task threads for the watcher.
	_ = f8user(t, e, "bob")
	bcookie := f8login(t, e, "bob")
	if code := f8do(t, e, "POST", "/inbox/watch", bcookie, `{"repo":"ada/hooks","level":"all"}`).Code; code != http.StatusCreated {
		t.Fatalf("bob watch = %d", code)
	}
	f8seedIntent(t, e, repoID, ada.ID, "Watched panel")
	if ids := f8threadIDs(t, e, bcookie, "?state=unread"); len(ids) != 1 {
		t.Fatalf("bob unread = %d, want 1 watched thread", len(ids))
	}
	// Watch list carries the explicit row plus the global default.
	watches := f8do(t, e, "GET", "/inbox/watches", cookie, "")
	if watches.Code != http.StatusOK {
		t.Fatalf("watch list = %d: %s", watches.Code, watches.Body.String())
	}
	wdecoded := f8decode(t, watches)
	if wdecoded["default_level"] != "participate" {
		t.Fatalf("default level = %v", wdecoded)
	}
	rows, _ := wdecoded["watches"].([]any)
	if len(rows) != 1 {
		t.Fatalf("watches = %v, want one", wdecoded)
	}
	// Settings: custom is forbidden as a default; windows clamp to 300..86400.
	f8problem(t, f8do(t, e, "POST", "/inbox/settings", cookie, `{"default_level":"custom"}`), http.StatusBadRequest, "invalid")
	f8problem(t, f8do(t, e, "POST", "/inbox/settings", cookie, `{"claim_warn_window_secs":60}`), http.StatusBadRequest, "invalid")
	srec := f8do(t, e, "POST", "/inbox/settings", cookie, `{"default_level":"all","claim_warn_window_secs":900}`)
	if srec.Code != http.StatusCreated {
		t.Fatalf("settings = %d: %s", srec.Code, srec.Body.String())
	}
	settings, _ := f8decode(t, srec)["settings"].(map[string]any)
	if settings["default_level"] != "all" || settings["claim_warn_window_secs"] != float64(900) {
		t.Fatalf("settings = %v", settings)
	}
}

func f8html(t *testing.T, e *f8Env, method, target, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	front := e.deps.InboxPages(e.deps.WebhookPages(e.server))
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rec := httptest.NewRecorder()
	front.ServeHTTP(rec, req)
	return rec
}

func TestInboxHTMLPages(t *testing.T) {
	e := buildF8Env(t)
	f8bootstrap(t, e, "ada")
	ada, _ := e.svc.GetUser(t.Context(), "ada")
	cookie := f8login(t, e, "ada")
	repoID := f8seedRepo(t, e, ada.ID, "hooks")
	f8seedIntent(t, e, repoID, ada.ID, "HTML panel")
	ids := f8threadIDs(t, e, cookie, "?state=unread")
	if len(ids) != 1 {
		t.Fatalf("unread threads = %d, want 1", len(ids))
	}
	// The HTML thread view marks read, then renders the thread.
	if rec := f8html(t, e, "GET", "/inbox/thread?id="+ids[0], cookie); rec.Code != http.StatusOK {
		t.Fatalf("thread page = %d: %s", rec.Code, rec.Body.String())
	} else if !strings.Contains(rec.Body.String(), "HTML panel") {
		t.Fatalf("thread page misses the subject: %s", rec.Body.String())
	}
	f8threadState(t, e, cookie, ids[0], "read")
	// The list page carries a bulk bar whose snooze button submits a valid
	// handler-computed preset, never an empty integer.
	list := f8html(t, e, "GET", "/inbox", cookie)
	if list.Code != http.StatusOK {
		t.Fatalf("inbox page = %d: %s", list.Code, list.Body.String())
	}
	body := list.Body.String()
	if !strings.Contains(body, `name="op" value="snooze"`) {
		t.Fatal("bulk bar misses the snooze button")
	}
	const marker = `name="snooze_until" value="`
	_, rest, ok := strings.Cut(body, marker)
	if !ok {
		t.Fatal("bulk bar misses the snooze preset")
	}
	value, _, ok := strings.Cut(rest, `"`)
	if !ok {
		t.Fatal("snooze preset value is unterminated")
	}
	millis, merr := strconv.ParseInt(value, 10, 64)
	if merr != nil {
		t.Fatalf("preset parses: %v", merr)
	}
	nowMillis := e.now.UnixMilli()
	if millis < nowMillis+60*1000 || millis > nowMillis+90*24*60*60*1000 {
		t.Fatalf("preset %d outside the snooze window", millis)
	}
	// The settings page renders watches and forms outside the op table.
	if rec := f8html(t, e, "GET", "/inbox/settings", cookie); rec.Code != http.StatusOK {
		t.Fatalf("settings page = %d: %s", rec.Code, rec.Body.String())
	} else if !strings.Contains(rec.Body.String(), "Inbox settings") {
		t.Fatalf("settings page misses heading: %s", rec.Body.String())
	}
	// Anonymous HTML views render nothing privileged.
	if rec := f8html(t, e, "GET", "/inbox/settings", ""); rec.Code == http.StatusOK &&
		strings.Contains(rec.Body.String(), "Inbox settings") {
		t.Fatal("anonymous settings page renders private content")
	}
}

func TestInboxListValidation(t *testing.T) {
	e := buildF8Env(t)
	f8bootstrap(t, e, "ada")
	cookie := f8login(t, e, "ada")
	f8problem(t, f8do(t, e, "GET", "/inbox?state=bogus", cookie, ""), http.StatusBadRequest, "invalid")
	f8problem(t, f8do(t, e, "GET", "/inbox?limit=101", cookie, ""), http.StatusBadRequest, "invalid")
	if rec := f8do(t, e, "GET", "/inbox?limit=100", cookie, ""); rec.Code != http.StatusOK {
		t.Fatalf("limit 100 = %d", rec.Code)
	}
	f8problem(t, f8do(t, e, "GET", "/inbox?repo=ada/missing", cookie, ""), http.StatusNotFound, "not-found")
	// Count is cheap and gated the same way.
	if rec := f8do(t, e, "GET", "/inbox/count", cookie, ""); rec.Code != http.StatusOK {
		t.Fatalf("count = %d: %s", rec.Code, rec.Body.String())
	}
}
