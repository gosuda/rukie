package api

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
)

// Inbox ops (F8). Every inbox handler addresses the caller only: the
// addressee is always p.UserID, so no target-user parameter exists and
// GetInboxThread(userID=p.UserID) enforces isolation. Anonymous and service
// principals are refused; the user must be active. Reads never auto-mark;
// the HTML thread page marks through the inbox.mark path before rendering.
const (
	inboxListRatePerSec   = 1.0
	inboxListBurst        = 5.0
	inboxCountRatePerSec  = 2.0
	inboxCountBurst       = 4.0
	inboxMarkRatePerSec   = 5.0
	inboxMarkBurst        = 10.0
	inboxListDefaultLimit = 50
	inboxListMaxLimit     = 100
	inboxMarkMaxThreads   = 100
	inboxGetItemsLimit    = 200
)

var (
	inboxRateMu      sync.Mutex
	inboxRateBuckets = map[string]*inboxRateBucket{}
)

type inboxRateBucket struct {
	last   time.Time
	tokens float64
}

// inboxRateAllow admits one event for userID under the named budget. Budgets
// follow the settled rates: list 1 qps burst 5, count 2 qps burst 4,
// mark/snooze 5 qps burst 10.
func inboxRateAllow(userID int64, budget string, now time.Time) (bool, time.Duration) {
	var rate, burst float64
	switch budget {
	case "list":
		rate, burst = inboxListRatePerSec, inboxListBurst
	case "count":
		rate, burst = inboxCountRatePerSec, inboxCountBurst
	case "mark", "snooze":
		rate, burst = inboxMarkRatePerSec, inboxMarkBurst
	default:
		return true, 0
	}
	key := strconv.FormatInt(userID, 10) + "\x00" + budget
	inboxRateMu.Lock()
	defer inboxRateMu.Unlock()
	bucket, ok := inboxRateBuckets[key]
	if !ok {
		bucket = &inboxRateBucket{tokens: burst, last: now}
		inboxRateBuckets[key] = bucket
	}
	if elapsed := now.Sub(bucket.last).Seconds(); elapsed > 0 {
		bucket.tokens += elapsed * rate
		if bucket.tokens > burst {
			bucket.tokens = burst
		}
		bucket.last = now
	}
	if bucket.tokens < 1 {
		return false, time.Duration((1 - bucket.tokens) / rate * float64(time.Second))
	}
	bucket.tokens--
	return true, 0
}

func inboxRateLimited(retry time.Duration) *op.Error {
	if retry < time.Second {
		retry = time.Second
	}
	err := op.Errorf(op.CodeRateLimited, "rate limit exceeded")
	err.RetryAfter = retry
	return err
}

// requireInboxUser gates every inbox op: human principal, non-zero user, and
// an active account. Scoped tokens on human accounts pass; sessions pass;
// anonymous and service principals do not.
func (d Deps) requireInboxUser(ctx context.Context, p op.Principal) (int64, error) {
	if p.Kind != op.PrincipalHuman || p.UserID == 0 {
		return 0, op.Errorf(op.CodeUnauthorized, "authentication is required")
	}
	user, uerr := d.Service.GetUserByID(ctx, p.UserID)
	if uerr != nil || !user.IsActive {
		return 0, op.Errorf(op.CodeUnauthorized, "authentication is required")
	}
	return p.UserID, nil
}

// InboxThreadJSON is one inbox thread.
type InboxThreadJSON struct {
	Snippet     string   `json:"snippet"`
	ID          string   `json:"id"`
	SubjectKind string   `json:"subject_kind"`
	SubjectID   string   `json:"subject_id"`
	Repo        string   `json:"repo,omitempty"`
	State       string   `json:"state"`
	Reasons     []string `json:"reasons"`
	UnreadItems int64    `json:"unread_items"`
	BumpedAt    int64    `json:"bumped_at"`
	SnoozeUntil int64    `json:"snooze_until,omitempty"`
	Muted       bool     `json:"muted"`
}

// InboxItemJSON is one thread history entry. At carries the event time in
// unix millis; the fan-out appends items synchronously so it equals the row
// creation time.
type InboxItemJSON struct {
	Title     string   `json:"title"`
	Actor     string   `json:"actor"`
	ID        string   `json:"id"`
	EventID   string   `json:"event_id"`
	Type      string   `json:"type"`
	Reasons   []string `json:"reasons"`
	ActorID   int64    `json:"actor_id"`
	At        int64    `json:"at"`
	CreatedAt int64    `json:"created_at"`
	Bumped    bool     `json:"bumped"`
}

// InboxWatchJSON is one repo watch level.
type InboxWatchJSON struct {
	Repo       string   `json:"repo"`
	Level      string   `json:"level"`
	CustomBits []string `json:"custom_bits"`
}

// InboxSettingsJSON carries the caller's inbox settings.
type InboxSettingsJSON struct {
	DefaultLevel        string `json:"default_level"`
	ClaimWarnWindowSecs int64  `json:"claim_warn_window_secs"`
}

// InboxListIn carries one inbox list.
type InboxListIn struct {
	State       string `query:"state"`
	SubjectKind string `query:"subject_kind"`
	Repo        string `query:"repo"`
	Reason      string `query:"reason"`
	Cursor      string `query:"cursor"`
	Limit       int    `query:"limit"`
}

// InboxListOut carries one thread page with the unread badge count.
// SnoozePreset is a handler-computed now+24h default so the zero-JS bulk
// snooze button always submits a valid integer. It stays out of the wire
// shape; the page renderer reads the struct field directly.
type InboxListOut struct {
	Next         string            `json:"-"`
	NextCursor   string            `json:"next_cursor,omitempty"`
	Threads      []InboxThreadJSON `json:"threads"`
	SnoozePreset int64             `json:"-"`
	Unread       int64             `json:"unread_count"`
}

// InboxGetIn carries one thread read. ThreadID is the canonical query key;
// ID stays as a document-convention alias for the same thread.
type InboxGetIn struct {
	ThreadID string `query:"thread_id"`
	ID       string `query:"id"`
}

// InboxGetOut carries one thread with its history.
type InboxGetOut struct {
	Items  []InboxItemJSON `json:"items"`
	Thread InboxThreadJSON `json:"thread"`
}

// InboxMarkBody carries one bulk mark. SnoozeUntil is required when Op implies
// snooze and ignored otherwise; times are unix millis.
type InboxMarkBody struct {
	Op          string   `json:"op"`
	ThreadIDs   []string `json:"thread_ids"`
	SnoozeUntil int64    `json:"snooze_until,omitzero"`
}

// InboxMarkIn carries one bulk mark with its idempotency key.
type InboxMarkIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           InboxMarkBody
}

// InboxMarkFailureJSON is one per-thread failure of a bulk mark.
type InboxMarkFailureJSON struct {
	Error string `json:"error"`
	ID    string `json:"id"`
}

// InboxMarkOut carries the per-thread mark outcome.
type InboxMarkOut struct {
	Updated []string               `json:"updated"`
	Failed  []InboxMarkFailureJSON `json:"failed"`
}

// InboxSnoozeBody carries one bulk snooze. Until is unix millis and must land
// in (now+1m, now+90d].
type InboxSnoozeBody struct {
	ThreadIDs []string `json:"thread_ids"`
	Until     int64    `json:"until"`
}

// InboxSnoozeIn carries one bulk snooze with its idempotency key.
type InboxSnoozeIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           InboxSnoozeBody
}

// InboxSnoozeOut carries the per-thread snooze outcome.
type InboxSnoozeOut struct {
	Updated []string               `json:"updated"`
	Failed  []InboxMarkFailureJSON `json:"failed"`
}

// InboxCountIn carries one badge count read.
type InboxCountIn struct{}

// InboxCountOut carries the badge count and the pending snooze wakeups.
type InboxCountOut struct {
	Unread              int64 `json:"unread"`
	SnoozedAwakePending int64 `json:"snoozed_awake_pending"`
}

// InboxWatchSetBody carries one watch level set. Confirm must be true exactly
// when Level is ignore; CustomBits is required non-empty exactly when Level
// is custom.
type InboxWatchSetBody struct {
	Repo       string   `json:"repo"`
	Level      string   `json:"level"`
	CustomBits []string `json:"custom_bits,omitempty"`
	Confirm    bool     `json:"confirm,omitzero"`
}

// InboxWatchSetIn carries one watch level set with its idempotency key.
type InboxWatchSetIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           InboxWatchSetBody
}

// InboxWatchSetOut carries the stored watch level.
type InboxWatchSetOut struct {
	Repo       string   `json:"repo"`
	Level      string   `json:"level"`
	CustomBits []string `json:"custom_bits"`
}

// InboxWatchListIn carries one watch list.
type InboxWatchListIn struct{}

// InboxWatchListOut carries every repo watch with the global default.
type InboxWatchListOut struct {
	DefaultLevel string           `json:"default_level"`
	Watches      []InboxWatchJSON `json:"watches"`
}

// InboxSettingsBody carries one settings update. Custom is forbidden as a
// default level. Claim windows are seconds in 300..86400. Absent fields keep
// their stored values.
type InboxSettingsBody struct {
	DefaultLevel        string `json:"default_level,omitempty"`
	ClaimWarnWindowSecs int64  `json:"claim_warn_window_secs,omitzero"`
}

// InboxSettingsIn carries one settings update with its idempotency key.
type InboxSettingsIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           InboxSettingsBody
}

// InboxSettingsOut carries the stored settings.
type InboxSettingsOut struct {
	Settings InboxSettingsJSON `json:"settings"`
}

func inboxThreadJSON(ctx context.Context, d Deps, row store.InboxThreadRow) (InboxThreadJSON, error) {
	out := InboxThreadJSON{
		ID: row.ID, SubjectKind: row.SubjectKind, SubjectID: row.SubjectID,
		State: row.State, Muted: row.Muted, Reasons: nonemptyStrings(row.Reasons),
		Snippet: row.Snippet, UnreadItems: row.UnreadItems,
		BumpedAt: row.BumpedAt, SnoozeUntil: row.SnoozeUntil,
	}
	if row.RepoID != 0 {
		var repo store.RepoRow
		serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			var gerr error
			repo, gerr = tx.GetRepo(row.RepoID)
			return gerr
		})
		if serr == nil {
			full, ferr := webhookRepoFullName(ctx, d, repo)
			if ferr == nil {
				out.Repo = full
			}
		} else if !errors.Is(serr, store.ErrNotFound) {
			return InboxThreadJSON{}, op.Wrap(op.CodeInternal, serr, "inbox thread render failed")
		}
	}
	return out, nil
}

func inboxItemJSON(row store.InboxItemRow) InboxItemJSON {
	return InboxItemJSON{
		ID: row.ID, EventID: row.EventID, Type: row.EventType,
		ActorID: row.ActorID, Actor: row.ActorName, At: row.CreatedAt,
		Reasons: nonemptyStrings(row.Reasons),
		Title:   row.Title, Bumped: row.Bumped, CreatedAt: row.CreatedAt,
	}
}

func mapInboxStore(err error, notFound string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrNotFound) {
		return op.Errorf(op.CodeNotFound, "%s", notFound)
	}
	if oerr, ok := errors.AsType[*op.Error](err); ok {
		return oerr
	}
	if errors.Is(err, store.ErrInvalid) {
		return op.Errorf(op.CodeValidation, "%s", "inbox input is not valid")
	}
	return op.Wrap(op.CodeInternal, err, "inbox store failed")
}

func validInboxState(state string) bool {
	switch state {
	case "unread", "read", "done", "snoozed", "all", "":
		return true
	default:
		return false
	}
}

func inboxMarkStoreOp(mark string) (string, bool) {
	switch mark {
	case "mark_read":
		return "read", true
	case "mark_done":
		return "done", true
	case "mark_unread":
		return "unread", true
	case "snooze":
		return "snooze", true
	case "mute":
		return "mute", true
	case "unmute":
		return "unmute", true
	default:
		return "", false
	}
}

func inboxMarkOut(updated []string, failed map[string]string) InboxMarkOut {
	out := InboxMarkOut{Updated: []string{}, Failed: []InboxMarkFailureJSON{}}
	out.Updated = append(out.Updated, updated...)
	for _, id := range slices.Sorted(maps.Keys(failed)) {
		out.Failed = append(out.Failed, InboxMarkFailureJSON{ID: id, Error: failed[id]})
	}
	return out
}

func (d Deps) resolveInboxRepo(ctx context.Context, full string) (store.RepoRow, error) {
	owner, name, ok := strings.Cut(strings.TrimSpace(full), "/")
	if !ok || strings.TrimSpace(owner) == "" || strings.TrimSpace(name) == "" {
		return store.RepoRow{}, op.Errorf(op.CodeInvalid, "inbox repo must look like owner/name")
	}
	// Existence only: threads are already scoped to the caller, so no access
	// check runs here. Unknown repos report not-found for a stable filter.
	var rq requester
	rc, cerr := d.resolveRepo(ctx, rq, strings.TrimSpace(owner), strings.TrimSpace(name), false)
	if cerr != nil {
		return store.RepoRow{}, cerr
	}
	return rc.row, nil
}

func inboxListHandler(d Deps) func(context.Context, op.Principal, InboxListIn) (InboxListOut, error) {
	return func(ctx context.Context, p op.Principal, in InboxListIn) (InboxListOut, error) {
		userID, uerr := d.requireInboxUser(ctx, p)
		if uerr != nil {
			return InboxListOut{}, uerr
		}
		now := d.now()
		if ok, retry := inboxRateAllow(userID, "list", now); !ok {
			return InboxListOut{}, inboxRateLimited(retry)
		}
		state := strings.TrimSpace(in.State)
		if state == "" {
			state = "unread"
		}
		if !validInboxState(state) {
			return InboxListOut{}, op.Errorf(op.CodeInvalid, "inbox state is not valid")
		}
		storeState := state
		if storeState == "all" {
			storeState = ""
		}
		limit := in.Limit
		if limit == 0 {
			limit = inboxListDefaultLimit
		}
		if limit < 0 || limit > inboxListMaxLimit {
			return InboxListOut{}, op.Errorf(op.CodeInvalid, "inbox limit must be 1..100")
		}
		var repoID int64
		if strings.TrimSpace(in.Repo) != "" {
			row, rerr := d.resolveInboxRepo(ctx, in.Repo)
			if rerr != nil {
				return InboxListOut{}, rerr
			}
			repoID = row.ID
		}
		var rows []store.InboxThreadRow
		var next string
		var unread int64
		serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			got, cursor, lerr := tx.ListInboxThreads(userID, storeState, strings.TrimSpace(in.SubjectKind), repoID, in.Cursor, limit)
			if lerr != nil {
				return lerr
			}
			rows, next = got, cursor
			count, uerr := tx.CountUnreadInbox(userID)
			if uerr != nil {
				return uerr
			}
			unread = count
			return nil
		})
		if serr != nil {
			return InboxListOut{}, mapInboxStore(serr, "inbox list failed")
		}
		out := InboxListOut{Threads: []InboxThreadJSON{}, Next: next, NextCursor: next, Unread: unread, SnoozePreset: now.Add(24 * time.Hour).UnixMilli()}
		for _, row := range rows {
			if strings.TrimSpace(in.Reason) != "" && !slices.Contains(row.Reasons, strings.TrimSpace(in.Reason)) {
				continue
			}
			rendered, rerr := inboxThreadJSON(ctx, d, row)
			if rerr != nil {
				return InboxListOut{}, rerr
			}
			out.Threads = append(out.Threads, rendered)
		}
		return out, nil
	}
}

func inboxGetHandler(d Deps) func(context.Context, op.Principal, InboxGetIn) (InboxGetOut, error) {
	return func(ctx context.Context, p op.Principal, in InboxGetIn) (InboxGetOut, error) {
		userID, uerr := d.requireInboxUser(ctx, p)
		if uerr != nil {
			return InboxGetOut{}, uerr
		}
		threadID := strings.TrimSpace(in.ThreadID)
		if threadID == "" {
			threadID = strings.TrimSpace(in.ID)
		}
		if threadID == "" {
			return InboxGetOut{}, op.Errorf(op.CodeInvalid, "inbox thread id is required")
		}
		var thread store.InboxThreadRow
		var items []store.InboxItemRow
		serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			got, terr := tx.GetInboxThread(userID, threadID)
			if terr != nil {
				return terr
			}
			thread = got
			listed, ierr := tx.ListInboxItems(thread.ID, inboxGetItemsLimit)
			if ierr != nil {
				return ierr
			}
			items = listed
			return nil
		})
		if serr != nil {
			return InboxGetOut{}, mapInboxStore(serr, "inbox thread was not found")
		}
		rendered, rerr := inboxThreadJSON(ctx, d, thread)
		if rerr != nil {
			return InboxGetOut{}, rerr
		}
		out := InboxGetOut{Items: []InboxItemJSON{}, Thread: rendered}
		for _, item := range items {
			out.Items = append(out.Items, inboxItemJSON(item))
		}
		return out, nil
	}
}

func inboxMarkHandler(d Deps) func(context.Context, op.Principal, InboxMarkIn) (InboxMarkOut, error) {
	return func(ctx context.Context, p op.Principal, in InboxMarkIn) (InboxMarkOut, error) {
		userID, uerr := d.requireInboxUser(ctx, p)
		if uerr != nil {
			return InboxMarkOut{}, uerr
		}
		now := d.now()
		if ok, retry := inboxRateAllow(userID, "mark", now); !ok {
			return InboxMarkOut{}, inboxRateLimited(retry)
		}
		storeOp, ok := inboxMarkStoreOp(strings.TrimSpace(in.Body.Op))
		if !ok {
			return InboxMarkOut{}, op.Errorf(op.CodeInvalid, "inbox mark op is not valid")
		}
		if len(in.Body.ThreadIDs) == 0 || len(in.Body.ThreadIDs) > inboxMarkMaxThreads {
			return InboxMarkOut{}, op.Errorf(op.CodeInvalid, "inbox mark needs 1..100 thread ids")
		}
		if storeOp == "snooze" && in.Body.SnoozeUntil == 0 {
			return InboxMarkOut{}, op.Errorf(op.CodeInvalid, "snooze_until is required to snooze")
		}
		nowMillis := now.UnixMilli()
		var updated []string
		failed := map[string]string{}
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			got, fail, merr := tx.MarkInboxThreads(userID, in.Body.ThreadIDs, storeOp, in.Body.SnoozeUntil, nowMillis)
			if merr != nil {
				return merr
			}
			updated = got
			for id, reason := range fail {
				failed[id] = reason
			}
			return nil
		})
		if werr != nil {
			return InboxMarkOut{}, mapInboxStore(werr, "inbox mark failed")
		}
		return inboxMarkOut(updated, failed), nil
	}
}

func inboxSnoozeHandler(d Deps) func(context.Context, op.Principal, InboxSnoozeIn) (InboxSnoozeOut, error) {
	return func(ctx context.Context, p op.Principal, in InboxSnoozeIn) (InboxSnoozeOut, error) {
		userID, uerr := d.requireInboxUser(ctx, p)
		if uerr != nil {
			return InboxSnoozeOut{}, uerr
		}
		now := d.now()
		if ok, retry := inboxRateAllow(userID, "snooze", now); !ok {
			return InboxSnoozeOut{}, inboxRateLimited(retry)
		}
		if len(in.Body.ThreadIDs) == 0 || len(in.Body.ThreadIDs) > inboxMarkMaxThreads {
			return InboxSnoozeOut{}, op.Errorf(op.CodeInvalid, "inbox snooze needs 1..100 thread ids")
		}
		if in.Body.Until == 0 {
			return InboxSnoozeOut{}, op.Errorf(op.CodeInvalid, "snooze until is required")
		}
		nowMillis := now.UnixMilli()
		var updated []string
		failed := map[string]string{}
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			got, fail, merr := tx.MarkInboxThreads(userID, in.Body.ThreadIDs, "snooze", in.Body.Until, nowMillis)
			if merr != nil {
				return merr
			}
			updated = got
			for id, reason := range fail {
				failed[id] = reason
			}
			return nil
		})
		if werr != nil {
			return InboxSnoozeOut{}, mapInboxStore(werr, "inbox snooze failed")
		}
		out := InboxSnoozeOut{Updated: []string{}, Failed: []InboxMarkFailureJSON{}}
		out.Updated = append(out.Updated, updated...)
		for _, id := range slices.Sorted(maps.Keys(failed)) {
			out.Failed = append(out.Failed, InboxMarkFailureJSON{ID: id, Error: failed[id]})
		}
		return out, nil
	}
}

func inboxCountHandler(d Deps) func(context.Context, op.Principal, InboxCountIn) (InboxCountOut, error) {
	return func(ctx context.Context, p op.Principal, _ InboxCountIn) (InboxCountOut, error) {
		userID, uerr := d.requireInboxUser(ctx, p)
		if uerr != nil {
			return InboxCountOut{}, uerr
		}
		now := d.now()
		if ok, retry := inboxRateAllow(userID, "count", now); !ok {
			return InboxCountOut{}, inboxRateLimited(retry)
		}
		out := InboxCountOut{}
		serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			unread, uerr := tx.CountUnreadInbox(userID)
			if uerr != nil {
				return uerr
			}
			awake, aerr := tx.CountSnoozedAwake(userID, now.UnixMilli())
			if aerr != nil {
				return aerr
			}
			out.Unread, out.SnoozedAwakePending = unread, awake
			return nil
		})
		if serr != nil {
			return InboxCountOut{}, mapInboxStore(serr, "inbox count failed")
		}
		return out, nil
	}
}

var inboxWatchLevels = map[string]bool{
	"ignore": true, "participate": true, "all": true, "custom": true,
}

var inboxCustomBits = map[string]bool{
	"changes": true, "tasks": true, "releases": true, "ci": true,
}

func inboxWatchSetHandler(d Deps) func(context.Context, op.Principal, InboxWatchSetIn) (InboxWatchSetOut, error) {
	return func(ctx context.Context, p op.Principal, in InboxWatchSetIn) (InboxWatchSetOut, error) {
		userID, uerr := d.requireInboxUser(ctx, p)
		if uerr != nil {
			return InboxWatchSetOut{}, uerr
		}
		level := strings.TrimSpace(in.Body.Level)
		if !inboxWatchLevels[level] {
			return InboxWatchSetOut{}, op.Errorf(op.CodeInvalid, "inbox watch level is not valid")
		}
		if level == "ignore" && !in.Body.Confirm {
			return InboxWatchSetOut{}, op.Errorf(op.CodeInvalid, "ignoring a repository needs explicit confirmation")
		}
		bits := []string{}
		if level == "custom" {
			if len(in.Body.CustomBits) == 0 {
				return InboxWatchSetOut{}, op.Errorf(op.CodeInvalid, "custom watches need at least one bit")
			}
			for _, bit := range in.Body.CustomBits {
				trimmed := strings.TrimSpace(bit)
				if !inboxCustomBits[trimmed] {
					return InboxWatchSetOut{}, op.Errorf(op.CodeInvalid, "unknown custom watch bit %q", trimmed)
				}
				if !slices.Contains(bits, trimmed) {
					bits = append(bits, trimmed)
				}
			}
			slices.Sort(bits)
		}
		if strings.TrimSpace(in.Body.Repo) == "" {
			return InboxWatchSetOut{}, op.Errorf(op.CodeInvalid, "inbox watch repo is required")
		}
		row, rerr := d.resolveInboxRepo(ctx, in.Body.Repo)
		if rerr != nil {
			return InboxWatchSetOut{}, rerr
		}
		nowMillis := d.now().UnixMilli()
		var stored store.WatchRow
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			var serr error
			stored, serr = tx.SetWatch(userID, row.ID, level, bits, nowMillis)
			return serr
		})
		if werr != nil {
			return InboxWatchSetOut{}, mapInboxStore(werr, "inbox watch set failed")
		}
		full, ferr := webhookRepoFullName(ctx, d, row)
		if ferr != nil {
			return InboxWatchSetOut{}, ferr
		}
		return InboxWatchSetOut{Repo: full, Level: stored.Level, CustomBits: nonemptyStrings(stored.CustomBits)}, nil
	}
}

func (d Deps) renderInboxWatch(ctx context.Context, row store.WatchRow) (InboxWatchJSON, error) {
	var repo store.RepoRow
	serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
		var gerr error
		repo, gerr = tx.GetRepo(row.RepoID)
		return gerr
	})
	if serr != nil {
		return InboxWatchJSON{}, mapInboxStore(serr, "inbox watch was not found")
	}
	full, ferr := webhookRepoFullName(ctx, d, repo)
	if ferr != nil {
		return InboxWatchJSON{}, ferr
	}
	return InboxWatchJSON{Repo: full, Level: row.Level, CustomBits: nonemptyStrings(row.CustomBits)}, nil
}

func inboxWatchListHandler(d Deps) func(context.Context, op.Principal, InboxWatchListIn) (InboxWatchListOut, error) {
	return func(ctx context.Context, p op.Principal, _ InboxWatchListIn) (InboxWatchListOut, error) {
		userID, uerr := d.requireInboxUser(ctx, p)
		if uerr != nil {
			return InboxWatchListOut{}, uerr
		}
		var rows []store.WatchRow
		var settings store.InboxSettingsRow
		serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			listed, lerr := tx.ListWatches(userID)
			if lerr != nil {
				return lerr
			}
			rows = listed
			current, cerr := tx.GetInboxSettings(userID)
			if cerr != nil {
				return cerr
			}
			settings = current
			return nil
		})
		if serr != nil {
			return InboxWatchListOut{}, mapInboxStore(serr, "inbox watch list failed")
		}
		out := InboxWatchListOut{Watches: []InboxWatchJSON{}, DefaultLevel: "participate"}
		for _, row := range rows {
			rendered, rerr := d.renderInboxWatch(ctx, row)
			if rerr != nil {
				return InboxWatchListOut{}, rerr
			}
			out.Watches = append(out.Watches, rendered)
		}
		if strings.TrimSpace(settings.DefaultLevel) != "" {
			out.DefaultLevel = settings.DefaultLevel
		}
		return out, nil
	}
}

func inboxSettingsRender(settings store.InboxSettingsRow) InboxSettingsJSON {
	out := InboxSettingsJSON{DefaultLevel: "participate", ClaimWarnWindowSecs: 3600}
	if strings.TrimSpace(settings.DefaultLevel) != "" {
		out.DefaultLevel = settings.DefaultLevel
	}
	if settings.ClaimWarnWindowSecs != 0 {
		out.ClaimWarnWindowSecs = settings.ClaimWarnWindowSecs
	}
	return out
}

func inboxSettingsHandler(d Deps) func(context.Context, op.Principal, InboxSettingsIn) (InboxSettingsOut, error) {
	return func(ctx context.Context, p op.Principal, in InboxSettingsIn) (InboxSettingsOut, error) {
		userID, uerr := d.requireInboxUser(ctx, p)
		if uerr != nil {
			return InboxSettingsOut{}, uerr
		}
		level := strings.TrimSpace(in.Body.DefaultLevel)
		if level != "" && level != "ignore" && level != "participate" && level != "all" {
			return InboxSettingsOut{}, op.Errorf(op.CodeInvalid, "inbox default level is not valid")
		}
		window := in.Body.ClaimWarnWindowSecs
		if window != 0 && (window < 300 || window > 86400) {
			return InboxSettingsOut{}, op.Errorf(op.CodeInvalid, "claim warning window must be 300..86400 seconds")
		}
		var current store.InboxSettingsRow
		serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			var cerr error
			current, cerr = tx.GetInboxSettings(userID)
			return cerr
		})
		if serr != nil {
			return InboxSettingsOut{}, mapInboxStore(serr, "inbox settings failed")
		}
		if level == "" {
			level = current.DefaultLevel
			if level == "" {
				level = "participate"
			}
		}
		if window == 0 {
			window = current.ClaimWarnWindowSecs
			if window == 0 {
				window = 3600
			}
		}
		nowMillis := d.now().UnixMilli()
		var stored store.InboxSettingsRow
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			var serr error
			stored, serr = tx.SetInboxSettings(userID, level, window, nowMillis)
			return serr
		})
		if werr != nil {
			return InboxSettingsOut{}, mapInboxStore(werr, "inbox settings failed")
		}
		return InboxSettingsOut{Settings: inboxSettingsRender(stored)}, nil
	}
}
