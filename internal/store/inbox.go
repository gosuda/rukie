package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gosuda/rukie/internal/access"
)

// WatchRow is one user's watch level for one repo. Times are Unix millis.
type WatchRow struct {
	Level      string
	CustomBits []string
	UserID     int64
	RepoID     int64
	UpdatedAt  int64
}

// InboxThreadRow is one user's thread for one subject. Times are Unix millis;
// SnoozeUntil is 0 unless state is snoozed.
type InboxThreadRow struct {
	ID          string
	SubjectKind string
	SubjectID   string
	State       string
	Snippet     string
	Reasons     []string
	UserID      int64
	RepoID      int64
	UnreadItems int64
	TotalItems  int64
	BumpedAt    int64
	SnoozeUntil int64
	UpdatedAt   int64
	Muted       bool
}

// InboxItemRow is one event appended to a thread. Times are Unix millis.
// EventID is the outbox row identity: distinct emissions append distinct items.
type InboxItemRow struct {
	ID        string
	ThreadID  string
	UserID    string
	EventID   string
	EventType string
	ActorName string
	Title     string
	Reasons   []string
	CreatedAt int64
	ActorID   int64
	Bumped    bool
}

// InboxSettingsRow carries one user's inbox preferences.
type InboxSettingsRow struct {
	DefaultLevel        string
	UserID              int64
	ClaimWarnWindowSecs int64
	UpdatedAt           int64
}

const inboxThreadColumns = `id, user_id, subject_kind, subject_id, repo_id, state,
muted, reasons, snippet, unread_items, total_items, bumped_at, snooze_until, updated_at`

const inboxItemColumns = `id, thread_id, user_id, event_id, event_type, actor_id,
actor_name, reasons, title, bumped, created_at`

var validWatchLevels = map[string]bool{
	"ignore": true, "participate": true, "all": true, "custom": true,
}

var validCustomBits = map[string]bool{
	"changes": true, "tasks": true, "releases": true, "ci": true,
}

var validThreadStates = map[string]bool{
	"unread": true, "read": true, "done": true, "snoozed": true,
}

var validSubjectKinds = map[string]bool{
	"change": true, "branch_view": true, "task": true, "release": true,
	"mirror": true, "repo": true, "webhook_endpoint": true, "ci_run_group": true,
}

func scanWatch(scan func(...any) error) (WatchRow, error) {
	var r WatchRow
	var bits string
	err := scan(&r.UserID, &r.RepoID, &r.Level, &bits, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return r, fmt.Errorf("scanWatch: %w", ErrNotFound)
		}
		return r, err
	}
	decoded, derr := decodeWebhookStrings(bits)
	if derr != nil {
		return r, fmt.Errorf("scanWatch: %w", derr)
	}
	r.CustomBits = decoded
	return r, nil
}

func scanInboxThread(scan func(...any) error) (InboxThreadRow, error) {
	var r InboxThreadRow
	var repoID sql.NullInt64
	var reasons string
	var muted int64
	err := scan(&r.ID, &r.UserID, &r.SubjectKind, &r.SubjectID, &repoID,
		&r.State, &muted, &reasons, &r.Snippet, &r.UnreadItems, &r.TotalItems,
		&r.BumpedAt, &r.SnoozeUntil, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return r, fmt.Errorf("scanInboxThread: %w", ErrNotFound)
		}
		return r, err
	}
	if repoID.Valid {
		r.RepoID = repoID.Int64
	}
	r.Muted = muted == 1
	decoded, derr := decodeWebhookStrings(reasons)
	if derr != nil {
		return r, fmt.Errorf("scanInboxThread: %w", derr)
	}
	r.Reasons = decoded
	return r, nil
}

func scanInboxItem(scan func(...any) error) (InboxItemRow, error) {
	var r InboxItemRow
	var userID int64
	var reasons string
	var bumped int64
	err := scan(&r.ID, &r.ThreadID, &userID, &r.EventID, &r.EventType, &r.ActorID,
		&r.ActorName, &reasons, &r.Title, &bumped, &r.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return r, fmt.Errorf("scanInboxItem: %w", ErrNotFound)
		}
		return r, err
	}
	r.UserID = fmt.Sprintf("%d", userID)
	decoded, derr := decodeWebhookStrings(reasons)
	if derr != nil {
		return r, fmt.Errorf("scanInboxItem: %w", derr)
	}
	r.Reasons = decoded
	r.Bumped = bumped == 1
	return r, nil
}

// SetWatch creates or replaces one user's watch level for one repo. A custom
// level requires at least one bit from {changes,tasks,releases,ci}; any other
// level must carry no bits. Absent rows mean participate.
func (tx *WriteTx) SetWatch(userID, repoID int64, level string, bits []string, nowMillis int64) (WatchRow, error) {
	if userID <= 0 || repoID <= 0 {
		return WatchRow{}, fmt.Errorf("SetWatch: %w: user and repo ids must be positive", ErrInvalid)
	}
	if !validWatchLevels[level] {
		return WatchRow{}, fmt.Errorf("SetWatch: %w: unknown level %q", ErrInvalid, level)
	}
	if level == "custom" {
		if len(bits) == 0 {
			return WatchRow{}, fmt.Errorf("SetWatch: %w: custom level needs at least one bit", ErrInvalid)
		}
		seen := map[string]bool{}
		for _, b := range bits {
			if !validCustomBits[b] {
				return WatchRow{}, fmt.Errorf("SetWatch: %w: unknown custom bit %q", ErrInvalid, b)
			}
			if seen[b] {
				return WatchRow{}, fmt.Errorf("SetWatch: %w: duplicate custom bit %q", ErrInvalid, b)
			}
			seen[b] = true
		}
	} else if len(bits) > 0 {
		return WatchRow{}, fmt.Errorf("SetWatch: %w: only custom level takes bits", ErrInvalid)
	}
	inner, err := tx.live()
	if err != nil {
		return WatchRow{}, err
	}
	_, uerr := inner.ExecContext(tx.ctx, `INSERT INTO watches
(user_id, repo_id, level, custom_bits, updated_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (user_id, repo_id) DO UPDATE SET level = excluded.level,
custom_bits = excluded.custom_bits, updated_at = excluded.updated_at`,
		userID, repoID, level, encodeWebhookStrings(bits), nowMillis)
	if uerr != nil {
		return WatchRow{}, fmt.Errorf("SetWatch: %w", mapErr(uerr))
	}
	return tx.GetWatch(userID, repoID)
}

// GetWatch loads one watch row. Absent rows report ErrNotFound and mean
// participate, which the caller applies.
func (tx *ReadTx) GetWatch(userID, repoID int64) (WatchRow, error) {
	inner, err := tx.live()
	if err != nil {
		return WatchRow{}, err
	}
	return scanWatch(inner.QueryRowContext(tx.ctx,
		`SELECT user_id, repo_id, level, custom_bits, updated_at FROM watches WHERE user_id = ? AND repo_id = ?`,
		userID, repoID).Scan)
}

// GetWatch loads one watch row in a write transaction.
func (tx *WriteTx) GetWatch(userID, repoID int64) (WatchRow, error) {
	inner, err := tx.live()
	if err != nil {
		return WatchRow{}, err
	}
	return scanWatch(inner.QueryRowContext(tx.ctx,
		`SELECT user_id, repo_id, level, custom_bits, updated_at FROM watches WHERE user_id = ? AND repo_id = ?`,
		userID, repoID).Scan)
}

// ListWatches lists all of one user's watch rows.
func (tx *ReadTx) ListWatches(userID int64) ([]WatchRow, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	rows, err := inner.QueryContext(tx.ctx,
		`SELECT user_id, repo_id, level, custom_bits, updated_at FROM watches WHERE user_id = ? ORDER BY repo_id ASC`, userID)
	if err != nil {
		return nil, fmt.Errorf("ListWatches: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []WatchRow
	for rows.Next() {
		r, serr := scanWatch(rows.Scan)
		if serr != nil {
			return nil, serr
		}
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("ListWatches: %w", err)
	}
	return out, nil
}

func encodeThreadCursor(bumpedAt int64, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%020d\x1f%s", bumpedAt, id)))
}

func parseThreadCursor(raw string) (int64, string, error) {
	decoded, derr := base64.RawURLEncoding.DecodeString(raw)
	if derr != nil {
		return 0, "", fmt.Errorf("cursor: %w: malformed inbox cursor", ErrInvalid)
	}
	value, id, ok := strings.Cut(string(decoded), "\x1f")
	if !ok || id == "" {
		return 0, "", fmt.Errorf("cursor: %w: malformed inbox cursor", ErrInvalid)
	}
	var bumped int64
	for i := range len(value) {
		c := value[i]
		if c < '0' || c > '9' {
			return 0, "", fmt.Errorf("cursor: %w: malformed inbox cursor", ErrInvalid)
		}
		bumped = bumped*10 + int64(c-'0')
	}
	if bumped <= 0 {
		return 0, "", fmt.Errorf("cursor: %w: malformed inbox cursor", ErrInvalid)
	}
	return bumped, id, nil
}

// ListInboxThreads lists one user's threads, newest first. Empty state or
// subjectKind disables that filter; repoID 0 lists every repo.
func (tx *ReadTx) ListInboxThreads(userID int64, state, subjectKind string, repoID int64, cursor string, limit int) ([]InboxThreadRow, string, error) {
	if state != "" && !validThreadStates[state] {
		return nil, "", fmt.Errorf("ListInboxThreads: %w: unknown state %q", ErrInvalid, state)
	}
	if subjectKind != "" && !validSubjectKinds[subjectKind] {
		return nil, "", fmt.Errorf("ListInboxThreads: %w: unknown subject kind %q", ErrInvalid, subjectKind)
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	inner, err := tx.live()
	if err != nil {
		return nil, "", err
	}
	query := `SELECT ` + inboxThreadColumns + ` FROM inbox_threads WHERE user_id = ?`
	args := []any{userID}
	if state != "" {
		query += ` AND state = ?`
		args = append(args, state)
	}
	if subjectKind != "" {
		query += ` AND subject_kind = ?`
		args = append(args, subjectKind)
	}
	if repoID != 0 {
		query += ` AND repo_id = ?`
		args = append(args, repoID)
	}
	if cursor != "" {
		bumped, id, cerr := parseThreadCursor(cursor)
		if cerr != nil {
			return nil, "", cerr
		}
		query += ` AND (bumped_at < ? OR (bumped_at = ? AND id < ?))`
		args = append(args, bumped, bumped, id)
	}
	query += ` ORDER BY bumped_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := inner.QueryContext(tx.ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("ListInboxThreads: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []InboxThreadRow
	for rows.Next() {
		r, serr := scanInboxThread(rows.Scan)
		if serr != nil {
			return nil, "", serr
		}
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		return nil, "", fmt.Errorf("ListInboxThreads: %w", err)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = encodeThreadCursor(last.BumpedAt, last.ID)
	}
	return out, next, nil
}

// CountUnreadInbox counts one user's unread threads (the badge number).
func (tx *ReadTx) CountUnreadInbox(userID int64) (int64, error) {
	inner, err := tx.live()
	if err != nil {
		return 0, err
	}
	var n int64
	if serr := inner.QueryRowContext(tx.ctx,
		`SELECT count(*) FROM inbox_threads WHERE user_id = ? AND state = 'unread'`, userID).Scan(&n); serr != nil {
		return 0, fmt.Errorf("CountUnreadInbox: %w", serr)
	}
	return n, nil
}

// CountSnoozedAwake counts one user's snoozed threads whose deadline passed.
func (tx *ReadTx) CountSnoozedAwake(userID int64, nowMillis int64) (int64, error) {
	inner, err := tx.live()
	if err != nil {
		return 0, err
	}
	var n int64
	if serr := inner.QueryRowContext(tx.ctx, `SELECT count(*) FROM inbox_threads
WHERE user_id = ? AND state = 'snoozed' AND snooze_until <= ?`, userID, nowMillis).Scan(&n); serr != nil {
		return 0, fmt.Errorf("CountSnoozedAwake: %w", serr)
	}
	return n, nil
}

// InboxPromoteBatchLimit caps lazy snooze promotion per read so one inbox
// list or get never rewrites an unbounded backlog; the remainder wakes on
// later reads.
const InboxPromoteBatchLimit = 100

// PromoteAwakeSnoozes moves up to limit of the user's snoozed threads whose
// deadline passed back to unread, clearing the deadline, and returns the
// promoted count. Non-positive or over-cap limits clamp to
// InboxPromoteBatchLimit. CountSnoozedAwake on the same clock drops by the
// returned count while CountUnreadInbox rises by it.
func (tx *WriteTx) PromoteAwakeSnoozes(userID, nowMillis int64, limit int) (int64, error) {
	if limit <= 0 || limit > InboxPromoteBatchLimit {
		limit = InboxPromoteBatchLimit
	}
	inner, err := tx.live()
	if err != nil {
		return 0, err
	}
	res, uerr := inner.ExecContext(tx.ctx, `UPDATE inbox_threads SET state = 'unread',
snooze_until = 0, updated_at = ? WHERE id IN (SELECT id FROM inbox_threads
WHERE user_id = ? AND state = 'snoozed' AND snooze_until <= ?
ORDER BY snooze_until ASC, id ASC LIMIT ?)`, nowMillis, userID, nowMillis, limit)
	if uerr != nil {
		return 0, fmt.Errorf("PromoteAwakeSnoozes: %w", mapErr(uerr))
	}
	n, rerr := res.RowsAffected()
	if rerr != nil {
		return 0, fmt.Errorf("PromoteAwakeSnoozes: %w", rerr)
	}
	return n, nil
}

// GetInboxThread loads one thread. Threads of another user report ErrNotFound.
func (tx *ReadTx) GetInboxThread(userID int64, threadID string) (InboxThreadRow, error) {
	inner, err := tx.live()
	if err != nil {
		return InboxThreadRow{}, err
	}
	r, serr := scanInboxThread(inner.QueryRowContext(tx.ctx,
		`SELECT `+inboxThreadColumns+` FROM inbox_threads WHERE id = ? AND user_id = ?`,
		threadID, userID).Scan)
	if serr != nil {
		return InboxThreadRow{}, serr
	}
	return r, nil
}

// ListInboxItems lists one thread's history, oldest first.
func (tx *ReadTx) ListInboxItems(threadID string, limit int) ([]InboxItemRow, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	rows, err := inner.QueryContext(tx.ctx, `SELECT `+inboxItemColumns+` FROM inbox_items
WHERE thread_id = ? ORDER BY created_at ASC, id ASC LIMIT ?`, threadID, limit)
	if err != nil {
		return nil, fmt.Errorf("ListInboxItems: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []InboxItemRow
	for rows.Next() {
		r, serr := scanInboxItem(rows.Scan)
		if serr != nil {
			return nil, serr
		}
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("ListInboxItems: %w", err)
	}
	return out, nil
}

const (
	maxSnoozeWindow = int64(90 * 24 * 60 * 60 * 1000)
	minSnoozeWindow = int64(60 * 1000)
)

func normalizeMarkOp(op string) string {
	op = strings.TrimPrefix(op, "mark_")
	switch op {
	case "read", "done", "unread", "mute", "unmute", "snooze":
		return op
	}
	return ""
}

// MarkInboxThreads applies one op to a set of threads. Each thread commits or
// fails on its own; failures land in failed keyed by thread id. Unknown ops and
// out-of-window snooze deadlines fail the whole call. Snooze windows must fall in
// [now+1m, now+90d].
func (tx *WriteTx) MarkInboxThreads(userID int64, ids []string, op string, snoozeUntil int64, nowMillis int64) (updated []string, failed map[string]string, err error) {
	normalized := normalizeMarkOp(op)
	if normalized == "" {
		return nil, nil, fmt.Errorf("MarkInboxThreads: %w: unknown op %q", ErrInvalid, op)
	}
	if normalized == "snooze" {
		if snoozeUntil < nowMillis+minSnoozeWindow || snoozeUntil > nowMillis+maxSnoozeWindow {
			return nil, nil, fmt.Errorf("MarkInboxThreads: %w: snooze window must be 1 minute to 90 days out", ErrInvalid)
		}
	}
	inner, err := tx.live()
	if err != nil {
		return nil, nil, err
	}
	failed = map[string]string{}
	for _, id := range ids {
		var owner int64
		serr := inner.QueryRowContext(tx.ctx,
			`SELECT user_id FROM inbox_threads WHERE id = ?`, id).Scan(&owner)
		if serr != nil || owner != userID {
			failed[id] = "not found"
			continue
		}
		var query string
		var args []any
		switch normalized {
		case "read":
			query = `UPDATE inbox_threads SET state = 'read', snooze_until = 0, updated_at = ? WHERE id = ?`
			args = []any{nowMillis, id}
		case "done":
			query = `UPDATE inbox_threads SET state = 'done', snooze_until = 0, updated_at = ? WHERE id = ?`
			args = []any{nowMillis, id}
		case "unread":
			query = `UPDATE inbox_threads SET state = 'unread', snooze_until = 0, updated_at = ? WHERE id = ?`
			args = []any{nowMillis, id}
		case "mute":
			query = `UPDATE inbox_threads SET muted = 1, updated_at = ? WHERE id = ?`
			args = []any{nowMillis, id}
		case "unmute":
			query = `UPDATE inbox_threads SET muted = 0, updated_at = ? WHERE id = ?`
			args = []any{nowMillis, id}
		case "snooze":
			query = `UPDATE inbox_threads SET state = 'snoozed', snooze_until = ?, updated_at = ? WHERE id = ?`
			args = []any{snoozeUntil, nowMillis, id}
		}
		if _, uerr := inner.ExecContext(tx.ctx, query, args...); uerr != nil {
			failed[id] = uerr.Error()
			continue
		}
		updated = append(updated, id)
	}
	return updated, failed, nil
}

// GetInboxSettings loads one user's settings. Absent rows return the zero struct
// and nil error; the caller applies defaults.
func (tx *ReadTx) GetInboxSettings(userID int64) (InboxSettingsRow, error) {
	inner, err := tx.live()
	if err != nil {
		return InboxSettingsRow{}, err
	}
	var r InboxSettingsRow
	serr := inner.QueryRowContext(tx.ctx,
		`SELECT user_id, default_level, claim_warn_window_secs, updated_at FROM inbox_settings WHERE user_id = ?`,
		userID).Scan(&r.UserID, &r.DefaultLevel, &r.ClaimWarnWindowSecs, &r.UpdatedAt)
	if errors.Is(serr, sql.ErrNoRows) {
		return InboxSettingsRow{}, nil
	}
	if serr != nil {
		return InboxSettingsRow{}, fmt.Errorf("GetInboxSettings: %w", serr)
	}
	return r, nil
}

// SetInboxSettings creates or replaces one user's settings. Empty defaultLevel
// means participate; zero claim window means one hour.
func (tx *WriteTx) SetInboxSettings(userID int64, defaultLevel string, claimWarnWindowSecs int64, nowMillis int64) (InboxSettingsRow, error) {
	if userID <= 0 {
		return InboxSettingsRow{}, fmt.Errorf("SetInboxSettings: %w: user id must be positive", ErrInvalid)
	}
	if defaultLevel == "" {
		defaultLevel = "participate"
	}
	if !validWatchLevels[defaultLevel] {
		return InboxSettingsRow{}, fmt.Errorf("SetInboxSettings: %w: unknown level %q", ErrInvalid, defaultLevel)
	}
	if claimWarnWindowSecs == 0 {
		claimWarnWindowSecs = 3600
	}
	if claimWarnWindowSecs < 300 || claimWarnWindowSecs > 86400 {
		return InboxSettingsRow{}, fmt.Errorf("SetInboxSettings: %w: claim window must be 5 minutes to 24 hours", ErrInvalid)
	}
	inner, err := tx.live()
	if err != nil {
		return InboxSettingsRow{}, err
	}
	_, uerr := inner.ExecContext(tx.ctx, `INSERT INTO inbox_settings
(user_id, default_level, claim_warn_window_secs, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT (user_id) DO UPDATE SET default_level = excluded.default_level,
claim_warn_window_secs = excluded.claim_warn_window_secs, updated_at = excluded.updated_at`,
		userID, defaultLevel, claimWarnWindowSecs, nowMillis)
	if uerr != nil {
		return InboxSettingsRow{}, fmt.Errorf("SetInboxSettings: %w", mapErr(uerr))
	}
	inner2, err := tx.live()
	if err != nil {
		return InboxSettingsRow{}, err
	}
	var r InboxSettingsRow
	serr := inner2.QueryRowContext(tx.ctx,
		`SELECT user_id, default_level, claim_warn_window_secs, updated_at FROM inbox_settings WHERE user_id = ?`,
		userID).Scan(&r.UserID, &r.DefaultLevel, &r.ClaimWarnWindowSecs, &r.UpdatedAt)
	if serr != nil {
		return InboxSettingsRow{}, fmt.Errorf("SetInboxSettings: %w", serr)
	}
	return r, nil
}

type inboxRecipient struct {
	reasons []string
}

func unionReasons(dst []string, src ...string) []string {
	seen := map[string]bool{}
	for _, r := range dst {
		seen[r] = true
	}
	for _, r := range src {
		if !seen[r] {
			seen[r] = true
			dst = append(dst, r)
		}
	}
	return dst
}

// customBitForType maps an event type to its watch-custom bit. Repo lifecycle
// events map to no bit: only all-watchers (and participants, of which there are
// none) receive them.
func customBitForType(eventType string) string {
	switch eventType {
	case "task.created", "task.claimed", "task.closed":
		return "tasks"
	case "ref.updated":
		return "changes"
	default:
		return ""
	}
}

func fanoutInbox(core *txCore, row OutboxRow) error {
	parsed, err := parseFanoutBody(row)
	if err != nil {
		return err
	}
	if !parsed.known {
		return nil
	}
	tx, err := core.live()
	if err != nil {
		return err
	}
	eventMillis := msOf(row.OccurredAt)
	var subjectKind, subjectID string
	var title string
	// Actor exclusion is best-effort: current producers omit pusher, but any
	// event body carrying a pusher user name excludes that user from the
	// recipient set (the actor never notifies themselves).
	var actorID int64
	var actorName string
	if name, ok := parsed.body["pusher"].(string); ok && name != "" {
		var id int64
		var canonical string
		if serr := tx.QueryRowContext(core.ctx,
			`SELECT id, name FROM users WHERE lower_name = ?`, strings.ToLower(name)).Scan(&id, &canonical); serr == nil {
			actorID, actorName = id, canonical
		} else if !errors.Is(serr, sql.ErrNoRows) {
			return fmt.Errorf("fanoutInbox: %w", serr)
		}
	}
	// Item attribution: task.created names its creator; every other kind uses
	// the resolved pusher where one exists, else the zero actor.
	var itemActorID int64
	var itemActorName string
	recipients := map[int64]*inboxRecipient{}
	addRecipient := func(userID int64, reasons ...string) {
		if userID <= 0 {
			return
		}
		r, ok := recipients[userID]
		if !ok {
			r = &inboxRecipient{}
			recipients[userID] = r
		}
		r.reasons = unionReasons(r.reasons, reasons...)
	}
	switch row.Kind {
	case "task.created", "task.claimed", "task.closed":
		var reporter, assignee sql.NullInt64
		var taskTitle string
		serr := tx.QueryRowContext(core.ctx,
			`SELECT reporter_id, assignee_id, title FROM intents WHERE repo_id = ? AND public_id = ?`,
			parsed.repoID, parsed.body["intent_id"]).Scan(&reporter, &assignee, &taskTitle)
		if serr != nil {
			if errors.Is(serr, sql.ErrNoRows) {
				return fmt.Errorf("fanoutInbox: %w: intent for %s is gone", ErrInvalid, row.Kind)
			}
			return fmt.Errorf("fanoutInbox: %w", serr)
		}
		subjectKind, subjectID = "task", parsed.body["intent_id"].(string)
		title = taskTitle
		if reporter.Valid {
			addRecipient(reporter.Int64, "author")
			if row.Kind == "task.created" {
				itemActorID = reporter.Int64
				if nerr := tx.QueryRowContext(core.ctx,
					`SELECT name FROM users WHERE id = ?`, reporter.Int64).Scan(&itemActorName); nerr != nil {
					return fmt.Errorf("fanoutInbox: %w", nerr)
				}
			}
		}
		if assignee.Valid {
			addRecipient(assignee.Int64, "assigned")
		}
		parsed.title = taskTitle
		parsed.snippet = taskTitle
	case "ref.updated":
		if strings.HasPrefix(parsed.ref, "refs/heads/") {
			subjectKind = "branch_view"
			subjectID = fmt.Sprintf("%d:%s", parsed.repoID, parsed.ref)
		} else {
			subjectKind = "repo"
			subjectID = fmt.Sprintf("%d", parsed.repoID)
		}
		title = parsed.ref + " updated"
		// Participants come from watches only; a body.pusher name (when present)
		// excludes that user via the actor rule above.
		parsed.title = title
		parsed.snippet = title
	case "repo.created", "repo.deleted":
		subjectKind, subjectID = "repo", fmt.Sprintf("%d", parsed.repoID)
		owner, _ := parsed.body["owner"].(string)
		name, _ := parsed.body["name"].(string)
		if row.Kind == "repo.created" {
			title = "Repository " + owner + "/" + name + " created"
		} else {
			title = "Repository " + owner + "/" + name + " deleted"
		}
		parsed.title = title
		parsed.snippet = title
	}
	// Watchers: level all always joins; custom joins on the matching bit;
	// participate adds nothing beyond participants; ignore suppresses every row
	// for that user, participants included.
	wrows, err := tx.QueryContext(core.ctx,
		`SELECT user_id, level, custom_bits FROM watches WHERE repo_id = ?`, parsed.repoID)
	if err != nil {
		return fmt.Errorf("fanoutInbox: %w", err)
	}
	ignored := map[int64]bool{}
	bit := customBitForType(row.Kind)
	for wrows.Next() {
		var userID int64
		var level, bitsRaw string
		if serr := wrows.Scan(&userID, &level, &bitsRaw); serr != nil {
			_ = wrows.Close()
			return fmt.Errorf("fanoutInbox: %w", serr)
		}
		switch level {
		case "ignore":
			ignored[userID] = true
		case "all":
			addRecipient(userID, "watcher_all")
		case "custom":
			if bit == "" {
				continue
			}
			bits, derr := decodeWebhookStrings(bitsRaw)
			if derr != nil {
				_ = wrows.Close()
				return derr
			}
			for _, b := range bits {
				if b == bit {
					addRecipient(userID, "watcher_custom")
					break
				}
			}
		}
	}
	if cerr := wrows.Err(); cerr != nil {
		_ = wrows.Close()
		return fmt.Errorf("fanoutInbox: %w", cerr)
	}
	_ = wrows.Close()
	for userID := range ignored {
		delete(recipients, userID)
	}
	if actorID != 0 {
		delete(recipients, actorID)
	}
	if row.Kind != "task.created" {
		itemActorID, itemActorName = actorID, actorName
	}
	base := inboxFanoutItem{
		eventID: string(row.ID), title: title, snippet: parsed.snippet,
		eventType: row.Kind, actorName: itemActorName, actorID: itemActorID,
	}
	for userID, rec := range recipients {
		if !inboxRecipientCanView(core.ctx, tx, userID, parsed.repoID) {
			continue
		}
		base.reasons = rec.reasons
		if rerr := upsertInboxThread(core, tx, userID, parsed.repoID, subjectKind, subjectID,
			base, eventMillis); rerr != nil {
			return rerr
		}
	}
	return nil
}

func inboxRecipientCanView(ctx context.Context, tx *sql.Tx, userID, repoID int64) bool {
	var active int64
	if serr := tx.QueryRowContext(ctx, `SELECT is_active FROM users WHERE id = ?`, userID).Scan(&active); serr != nil || active == 0 {
		return false
	}
	var owner sql.NullInt64
	var orgID int64
	var visibility string
	if serr := tx.QueryRowContext(ctx, `SELECT owner_user_id, org_id, visibility FROM repos WHERE id = ? AND deleted_at IS NULL`,
		repoID).Scan(&owner, &orgID, &visibility); serr != nil {
		return errors.Is(serr, sql.ErrNoRows)
	}
	switch visibility {
	case "public", "internal":
		return true
	case "private":
	default:
		return false
	}
	if orgID == 0 && owner.Valid && owner.Int64 == userID {
		return true
	}
	var n int64
	if orgID != 0 {
		if serr := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM team_members m
JOIN teams t ON t.id = m.team_id
WHERE t.org_id = ? AND t.lower_name = 'owners' AND m.user_id = ?`, orgID, userID).Scan(&n); serr == nil && n > 0 {
			return true
		}
	}
	if orgID == 0 {
		var raw string
		if serr := tx.QueryRowContext(ctx, `SELECT role FROM repo_grants WHERE repo_id = ? AND user_id = ?`,
			repoID, userID).Scan(&raw); serr == nil {
			if role, ok := access.ParseRole(raw); ok && role >= access.RoleRead {
				return true
			}
		}
	}
	rows, qerr := tx.QueryContext(ctx, `SELECT t.permission FROM teams t
JOIN team_members m ON m.team_id = t.id
JOIN repos r ON r.id = ? AND r.deleted_at IS NULL
WHERE m.user_id = ? AND t.org_id = r.org_id
AND (t.repo_scope = 'all' OR EXISTS
 (SELECT 1 FROM team_repos tr WHERE tr.team_id = t.id AND tr.repo_id = r.id))`, repoID, userID)
	if qerr != nil {
		return false
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var perm string
		if serr := rows.Scan(&perm); serr != nil {
			return false
		}
		if role, ok := access.ParseRole(perm); ok && role >= access.RoleRead {
			_ = rows.Close()
			return true
		}
	}
	return false
}

// inboxFanoutItem carries one new event's item content: identity (the outbox
// row id, so distinct emissions append distinct items), presentation, and
// attribution. Only a same-row double fan-out dedupes.
type inboxFanoutItem struct {
	eventID, title, snippet, eventType, actorName string
	reasons                                       []string
	actorID                                       int64
}

// upsertInboxThread appends one item and bumps (or silently extends) its thread.
// Muted threads gain an unbumped item only. Snoozed threads keep their state and
// bumped_at while still counting the item. All other states bump; read and done
// threads reopen as unread.
func upsertInboxThread(core *txCore, tx *sql.Tx, userID, repoID int64, subjectKind, subjectID string,
	item inboxFanoutItem, eventMillis int64) error {
	var threadID, state string
	var muted int64
	var bumpedAt int64
	serr := tx.QueryRowContext(core.ctx, `SELECT id, state, muted, bumped_at FROM inbox_threads
WHERE user_id = ? AND subject_kind = ? AND subject_id = ?`, userID, subjectKind, subjectID).Scan(
		&threadID, &state, &muted, &bumpedAt)
	if serr != nil && !errors.Is(serr, sql.ErrNoRows) {
		return fmt.Errorf("upsertInboxThread: %w", serr)
	}
	if errors.Is(serr, sql.ErrNoRows) {
		threadID = "msg_" + string(NewID(time.UnixMilli(eventMillis)))
		var repoNull any
		if repoID != 0 {
			repoNull = repoID
		}
		_, ierr := tx.ExecContext(core.ctx, `INSERT INTO inbox_threads
(id, user_id, subject_kind, subject_id, repo_id, state, muted, reasons, snippet,
 unread_items, total_items, bumped_at, snooze_until, updated_at)
VALUES (?, ?, ?, ?, ?, 'unread', 0, ?, ?, 0, 0, ?, 0, ?)`,
			threadID, userID, subjectKind, subjectID, repoNull,
			encodeWebhookStrings(item.reasons), item.snippet, eventMillis, eventMillis)
		if ierr != nil {
			return fmt.Errorf("upsertInboxThread: %w", mapErr(ierr))
		}
		state = "unread"
		muted = 0
	}
	// Insert-first: a replayed event hits the (event_id, user_id) ignore and
	// changes nothing. Counters move only when the item row is new.
	inserted, ierr := insertInboxItem(core, tx, threadID, userID, item, state != "snoozed" && muted == 0, eventMillis)
	if ierr != nil {
		return ierr
	}
	if !inserted {
		return nil
	}
	if muted == 1 {
		if _, uerr := tx.ExecContext(core.ctx, `UPDATE inbox_threads SET
total_items = total_items + 1, updated_at = ? WHERE id = ?`, eventMillis, threadID); uerr != nil {
			return fmt.Errorf("upsertInboxThread: %w", uerr)
		}
		return nil
	}
	if state == "snoozed" {
		// Snoozed threads stay snoozed: count the item, refresh the snippet,
		// but never move bumped_at or state.
		if _, uerr := tx.ExecContext(core.ctx, `UPDATE inbox_threads SET
unread_items = unread_items + 1, total_items = total_items + 1, snippet = ?,
reasons = ?, updated_at = ? WHERE id = ?`, item.snippet, encodeWebhookStrings(unionReasons(
			currentThreadReasons(core, tx, threadID), item.reasons...)), eventMillis, threadID); uerr != nil {
			return fmt.Errorf("upsertInboxThread: %w", uerr)
		}
		return nil
	}
	newState := state
	if state == "read" || state == "done" || state == "unread" {
		newState = "unread"
	}
	if _, uerr := tx.ExecContext(core.ctx, `UPDATE inbox_threads SET
state = ?, unread_items = unread_items + 1, total_items = total_items + 1,
bumped_at = ?, snippet = ?, reasons = ?, updated_at = ? WHERE id = ?`,
		newState, eventMillis, item.snippet,
		encodeWebhookStrings(unionReasons(currentThreadReasons(core, tx, threadID), item.reasons...)),
		eventMillis, threadID); uerr != nil {
		return fmt.Errorf("upsertInboxThread: %w", uerr)
	}
	_ = bumpedAt
	return nil
}

func currentThreadReasons(core *txCore, tx *sql.Tx, threadID string) []string {
	var raw string
	if serr := tx.QueryRowContext(core.ctx,
		`SELECT reasons FROM inbox_threads WHERE id = ?`, threadID).Scan(&raw); serr != nil {
		return nil
	}
	decoded, derr := decodeWebhookStrings(raw)
	if derr != nil {
		return nil
	}
	return decoded
}

func insertInboxItem(core *txCore, tx *sql.Tx, threadID string, userID int64, item inboxFanoutItem, bumped bool, eventMillis int64) (bool, error) {
	itemID := "msg_" + string(NewID(time.UnixMilli(eventMillis)))
	res, ierr := tx.ExecContext(core.ctx, `INSERT OR IGNORE INTO inbox_items
(id, thread_id, user_id, event_id, event_type, actor_id, actor_name, reasons, title, bumped, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		itemID, threadID, userID, item.eventID, item.eventType, item.actorID, item.actorName,
		encodeWebhookStrings(item.reasons), item.title, boolInt(bumped), eventMillis)
	if ierr != nil {
		return false, fmt.Errorf("insertInboxItem: %w", mapErr(ierr))
	}
	n, rerr := res.RowsAffected()
	if rerr != nil {
		return false, fmt.Errorf("insertInboxItem: %w", rerr)
	}
	return n == 1, nil
}
