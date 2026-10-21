package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ChangeRow is one review change anchored to a repository.
type ChangeRow struct {
	ChangeKey     string
	TargetBranch  string
	State         string
	HeadOid       string
	PublicID      string
	RepoID        int64
	AuthorUserID  int64
	PatchsetCount int64
	ID            int64
	CreatedAt     int64
	UpdatedAt     int64
}

// PatchsetRow is one immutable revision of a change.
type PatchsetRow struct {
	HeadOid  string
	ChangeID int64
	Ordinal  int64
	ID       int64
	Created  int64
}

// CommentRow is one review comment anchored to a change head.
type CommentRow struct {
	HeadOid      string
	PublicID     string
	Body         string
	RemappedFrom string
	Path         sql.NullString
	IntentID     sql.NullInt64
	Line         sql.NullInt64
	ChangeID     int64
	AuthorUserID int64
	ID           int64
	CreatedAt    int64
	Unmatched    bool
}

// LandIntentRow is one row of the land state machine.
type LandIntentRow struct {
	TargetRef        string
	CandidateOid     string
	BaseOid          string
	State            string
	Gap              string
	PublicID         string
	ChangeID         sql.NullInt64
	RepoID           int64
	PolicyGeneration int64
	Reprepares       int64
	CreatedBy        int64
	ID               int64
	CreatedAt        int64
	UpdatedAt        int64
}

// LandRunRow is one execution attempt of a land plan.
type LandRunRow struct {
	State     string
	Detail    string
	PublicID  string
	PlanID    int64
	ID        int64
	CreatedAt int64
}

// IntentRow is one task object with an acceptance contract.
type IntentRow struct {
	Title          string
	Body           string
	State          string
	Contract       string
	PublicID       string
	AssigneeUserID sql.NullInt64
	RepoID         int64
	Number         int64
	CreatedBy      int64
	ID             int64
	CreatedAt      int64
	UpdatedAt      int64
}

// AskRow is one structured agent-to-human question.
type AskRow struct {
	Question    string
	Options     string
	State       string
	Answer      string
	PublicID    string
	AnsweredBy  sql.NullInt64
	ExpiresAt   sql.NullInt64
	IntentID    int64
	CreatedBy   int64
	ID          int64
	CreatedAt   int64
	BlocksClaim bool
}

// ViewRow is one named saved query head.
type ViewRow struct {
	Name        string
	PublicID    string
	Revset      string
	Predicates  string
	OwnerUserID sql.NullInt64
	RepoID      int64
	Version     int64
	ID          int64
	Shared      bool
}

const sqlChangeColumns = `id, public_id, repo_id, change_key, target_branch, author_user_id,
	state, head_oid, patchset_count, created_at, updated_at`

func scanChangeRow(scan func(dest ...any) error) (ChangeRow, error) {
	var row ChangeRow
	err := scan(
		&row.ID, &row.PublicID, &row.RepoID, &row.ChangeKey, &row.TargetBranch,
		&row.AuthorUserID, &row.State, &row.HeadOid, &row.PatchsetCount,
		&row.CreatedAt, &row.UpdatedAt,
	)
	return row, err
}

// InsertChange creates an open change with its first patchset already counted.
func (tx *WriteTx) InsertChange(now time.Time, row ChangeRow) (ChangeRow, error) {
	inner, err := tx.live()
	if err != nil {
		return ChangeRow{}, err
	}
	if row.PublicID == "" {
		row.PublicID = string(NewID(now))
	}
	ms := msOf(now)
	res, ierr := inner.ExecContext(tx.ctx, `INSERT INTO changes
(public_id, repo_id, change_key, target_branch, author_user_id, state, head_oid, patchset_count, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, 'open', ?, 1, ?, ?)`,
		row.PublicID, row.RepoID, row.ChangeKey, row.TargetBranch, row.AuthorUserID,
		row.HeadOid, ms, ms)
	if ierr != nil {
		return ChangeRow{}, ierr
	}
	id, ierr := res.LastInsertId()
	if ierr != nil {
		return ChangeRow{}, ierr
	}
	if _, ierr := inner.ExecContext(tx.ctx, `INSERT INTO patchsets (change_id, ordinal, head_oid, created_at)
VALUES (?, 1, ?, ?)`, id, row.HeadOid, ms); ierr != nil {
		return ChangeRow{}, ierr
	}
	return tx.GetChangeForUpdate(id)
}

// GetChangeForUpdate loads a change inside a write transaction.
func (tx *WriteTx) GetChangeForUpdate(id int64) (ChangeRow, error) {
	inner, err := tx.live()
	if err != nil {
		return ChangeRow{}, err
	}
	row, serr := scanChangeRow(inner.QueryRowContext(tx.ctx,
		`SELECT `+sqlChangeColumns+` FROM changes WHERE id = ?`, id).Scan)
	if errors.Is(serr, sql.ErrNoRows) {
		return ChangeRow{}, fmt.Errorf("GetChangeForUpdate: %w: change %d", ErrNotFound, id)
	}
	return row, serr
}

// GetChangeByKey loads a change by its stable key inside a write transaction.
func (tx *WriteTx) GetChangeByKey(repoID int64, key string) (ChangeRow, error) {
	inner, err := tx.live()
	if err != nil {
		return ChangeRow{}, err
	}
	row, serr := scanChangeRow(inner.QueryRowContext(tx.ctx,
		`SELECT `+sqlChangeColumns+` FROM changes WHERE repo_id = ? AND change_key = ?`, repoID, key).Scan)
	if errors.Is(serr, sql.ErrNoRows) {
		return ChangeRow{}, fmt.Errorf("GetChangeByKey: %w: %s", ErrNotFound, key)
	}
	return row, serr
}

// ReadChangeByKey loads a change by its stable key for reads.
func (tx *ReadTx) ReadChangeByKey(repoID int64, key string) (ChangeRow, error) {
	inner, err := tx.live()
	if err != nil {
		return ChangeRow{}, err
	}
	row, serr := scanChangeRow(inner.QueryRowContext(tx.ctx,
		`SELECT `+sqlChangeColumns+` FROM changes WHERE repo_id = ? AND change_key = ?`, repoID, key).Scan)
	if errors.Is(serr, sql.ErrNoRows) {
		return ChangeRow{}, fmt.Errorf("ReadChangeByKey: %w: %s", ErrNotFound, key)
	}
	return row, serr
}

// ListChanges lists changes of one repository, newest first.
func (tx *ReadTx) ListChanges(repoID int64, state string, limit int) ([]ChangeRow, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	query := `SELECT ` + sqlChangeColumns + ` FROM changes WHERE repo_id = ?`
	args := []any{repoID}
	if state != "" {
		query += ` AND state = ?`
		args = append(args, state)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, qerr := inner.QueryContext(tx.ctx, query, args...)
	if qerr != nil {
		return nil, qerr
	}
	defer func() { _ = rows.Close() }()
	var out []ChangeRow
	for rows.Next() {
		row, serr := scanChangeRow(rows.Scan)
		if serr != nil {
			return nil, serr
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// AppendPatchset records a new head for a change and bumps its head.
func (tx *WriteTx) AppendPatchset(now time.Time, changeID int64, headOid string) (ChangeRow, error) {
	inner, err := tx.live()
	if err != nil {
		return ChangeRow{}, err
	}
	row, gerr := tx.GetChangeForUpdate(changeID)
	if gerr != nil {
		return ChangeRow{}, gerr
	}
	next := row.PatchsetCount + 1
	if _, ierr := inner.ExecContext(tx.ctx, `INSERT INTO patchsets (change_id, ordinal, head_oid, created_at)
VALUES (?, ?, ?, ?)`, changeID, next, headOid, msOf(now)); ierr != nil {
		return ChangeRow{}, ierr
	}
	if _, uerr := inner.ExecContext(tx.ctx, `UPDATE changes
SET head_oid = ?, patchset_count = ?, updated_at = ? WHERE id = ?`,
		headOid, next, msOf(now), changeID); uerr != nil {
		return ChangeRow{}, uerr
	}
	return tx.GetChangeForUpdate(changeID)
}

// SetChangeState moves a change to landed, abandoned, or back to open.
func (tx *WriteTx) SetChangeState(now time.Time, changeID int64, state string) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	switch state {
	case "open", "landed", "abandoned", "conflicted":
	default:
		return fmt.Errorf("SetChangeState: unknown state %q", state)
	}
	moved, merr := execAffected(tx.ctx, inner,
		`UPDATE changes SET state = ?, updated_at = ? WHERE id = ?`, state, msOf(now), changeID)
	if merr != nil {
		return merr
	}
	if moved == 0 {
		return fmt.Errorf("SetChangeState: %w: change %d", ErrNotFound, changeID)
	}
	return nil
}

// ListPatchsets lists every recorded head of a change in ordinal order.
func (tx *ReadTx) ListPatchsets(changeID int64) ([]PatchsetRow, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	rows, qerr := inner.QueryContext(tx.ctx, `SELECT id, change_id, ordinal, head_oid, created_at
FROM patchsets WHERE change_id = ? ORDER BY ordinal ASC`, changeID)
	if qerr != nil {
		return nil, qerr
	}
	defer func() { _ = rows.Close() }()
	var out []PatchsetRow
	for rows.Next() {
		var row PatchsetRow
		if serr := rows.Scan(&row.ID, &row.ChangeID, &row.Ordinal, &row.HeadOid, &row.Created); serr != nil {
			return nil, serr
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// InsertComment anchors a comment to a change head.
func (tx *WriteTx) InsertComment(now time.Time, row CommentRow) (CommentRow, error) {
	inner, err := tx.live()
	if err != nil {
		return CommentRow{}, err
	}
	if row.PublicID == "" {
		row.PublicID = string(NewID(now))
	}
	res, ierr := inner.ExecContext(tx.ctx, `INSERT INTO change_comments
(public_id, change_id, intent_id, head_oid, path, line, body, author_user_id, remapped_from, unmatched, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.PublicID, row.ChangeID, nullInt(row.IntentID), row.HeadOid,
		nullStr(row.Path), nullInt(row.Line), row.Body, row.AuthorUserID,
		row.RemappedFrom, boolInt(row.Unmatched), msOf(now))
	if ierr != nil {
		return CommentRow{}, ierr
	}
	id, ierr := res.LastInsertId()
	if ierr != nil {
		return CommentRow{}, ierr
	}
	return tx.GetComment(id)
}

// GetComment loads one comment inside a write transaction.
func (tx *WriteTx) GetComment(id int64) (CommentRow, error) {
	inner, err := tx.live()
	if err != nil {
		return CommentRow{}, err
	}
	var row CommentRow
	var unmatched int64
	serr := inner.QueryRowContext(tx.ctx, `SELECT id, public_id, change_id, intent_id,
head_oid, path, line, body, author_user_id, remapped_from, unmatched, created_at
FROM change_comments WHERE id = ?`, id).Scan(
		&row.ID, &row.PublicID, &row.ChangeID, &row.IntentID, &row.HeadOid,
		&row.Path, &row.Line, &row.Body, &row.AuthorUserID, &row.RemappedFrom,
		&unmatched, &row.CreatedAt)
	if errors.Is(serr, sql.ErrNoRows) {
		return CommentRow{}, fmt.Errorf("GetComment: %w: comment %d", ErrNotFound, id)
	}
	row.Unmatched = unmatched == 1
	return row, serr
}

// ListCommentsAtHead lists live comments anchored to one head, oldest first.
func (tx *ReadTx) ListCommentsAtHead(changeID int64, headOid string) ([]CommentRow, error) {
	return tx.listCommentsAtHead(changeID, headOid, false)
}

// ListCommentsAtHeadIncludingUnmatched lists every comment anchored to one head,
// including fallback-bucket rows, oldest first.
func (tx *ReadTx) ListCommentsAtHeadIncludingUnmatched(changeID int64, headOid string) ([]CommentRow, error) {
	return tx.listCommentsAtHead(changeID, headOid, true)
}

func (tx *ReadTx) listCommentsAtHead(changeID int64, headOid string, includeUnmatched bool) ([]CommentRow, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	query := `SELECT id, public_id, change_id, intent_id, head_oid, path, line, body,
author_user_id, remapped_from, unmatched, created_at
FROM change_comments WHERE change_id = ? AND head_oid = ?`
	if !includeUnmatched {
		query += ` AND unmatched = 0`
	}
	query += ` ORDER BY id ASC`
	rows, qerr := inner.QueryContext(tx.ctx, query, changeID, headOid)
	if qerr != nil {
		return nil, qerr
	}
	defer func() { _ = rows.Close() }()
	var out []CommentRow
	for rows.Next() {
		var row CommentRow
		var unmatched int64
		if serr := rows.Scan(&row.ID, &row.PublicID, &row.ChangeID, &row.IntentID,
			&row.HeadOid, &row.Path, &row.Line, &row.Body, &row.AuthorUserID,
			&row.RemappedFrom, &unmatched, &row.CreatedAt); serr != nil {
			return nil, serr
		}
		row.Unmatched = unmatched == 1
		out = append(out, row)
	}
	return out, rows.Err()
}

// RemapComment moves a comment to a new head after an exact line remap.
func (tx *WriteTx) RemapComment(changeID int64, commentID int64, oldHead string, newHead string, line sql.NullInt64, unmatched bool) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	moved, merr := execAffected(tx.ctx, inner, `UPDATE change_comments
SET head_oid = ?, line = ?, remapped_from = ?, unmatched = ?
WHERE id = ? AND change_id = ? AND head_oid = ?`,
		newHead, nullInt(line), oldHead, boolInt(unmatched), commentID, changeID, oldHead)
	if merr != nil {
		return merr
	}
	if moved == 0 {
		return fmt.Errorf("RemapComment: %w: comment %d at %s", ErrNotFound, commentID, oldHead)
	}
	return nil
}

// InsertLandIntent opens a land plan in the awaiting state.
func (tx *WriteTx) InsertLandIntent(now time.Time, row LandIntentRow) (LandIntentRow, error) {
	inner, err := tx.live()
	if err != nil {
		return LandIntentRow{}, err
	}
	if row.PublicID == "" {
		row.PublicID = string(NewID(now))
	}
	ms := msOf(now)
	res, ierr := inner.ExecContext(tx.ctx, `INSERT INTO land_intents
(public_id, repo_id, target_ref, change_id, candidate_oid, base_oid, policy_generation, state, reprepares, gap, created_by, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, 'awaiting', 0, ?, ?, ?, ?)`,
		row.PublicID, row.RepoID, row.TargetRef, nullInt(row.ChangeID), row.CandidateOid,
		row.BaseOid, row.PolicyGeneration, row.Gap, row.CreatedBy, ms, ms)
	if ierr != nil {
		return LandIntentRow{}, ierr
	}
	id, ierr := res.LastInsertId()
	if ierr != nil {
		return LandIntentRow{}, ierr
	}
	return tx.GetLandIntent(id)
}

// GetLandIntent loads one land intent inside a write transaction.
func (tx *WriteTx) GetLandIntent(id int64) (LandIntentRow, error) {
	inner, err := tx.live()
	if err != nil {
		return LandIntentRow{}, err
	}
	var row LandIntentRow
	serr := inner.QueryRowContext(tx.ctx, `SELECT id, public_id, repo_id, target_ref, change_id,
candidate_oid, base_oid, policy_generation, state, reprepares, gap, created_by, created_at, updated_at
FROM land_intents WHERE id = ?`, id).Scan(
		&row.ID, &row.PublicID, &row.RepoID, &row.TargetRef, &row.ChangeID,
		&row.CandidateOid, &row.BaseOid, &row.PolicyGeneration, &row.State,
		&row.Reprepares, &row.Gap, &row.CreatedBy, &row.CreatedAt, &row.UpdatedAt)
	if errors.Is(serr, sql.ErrNoRows) {
		return LandIntentRow{}, fmt.Errorf("GetLandIntent: %w: intent %d", ErrNotFound, id)
	}
	return row, serr
}

// MaxLandReprepares caps failed-to-repreparing cycles of one land intent.
const MaxLandReprepares = 3

// SetLandIntentState moves a land intent along the awaiting, executing,
// landed, failed, repreparing machine. Terminal landed rows never move.
// Failed rows move only to repreparing while the reprepare budget holds.
func (tx *WriteTx) SetLandIntentState(now time.Time, id int64, state string) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	current, gerr := tx.GetLandIntent(id)
	if gerr != nil {
		return gerr
	}
	reprepares := current.Reprepares
	switch {
	case current.State == "landed":
		return fmt.Errorf("SetLandIntentState: landed intent %d never moves", id)
	case state == current.State:
		return fmt.Errorf("SetLandIntentState: intent %d already %s", id, state)
	case current.State == "awaiting" && (state == "executing" || state == "failed"):
	case current.State == "executing" && (state == "landed" || state == "failed"):
	case current.State == "repreparing" && (state == "awaiting" || state == "failed"):
	case current.State == "failed" && state == "repreparing" && current.Reprepares < MaxLandReprepares:
		reprepares = current.Reprepares + 1
	default:
		return fmt.Errorf("SetLandIntentState: illegal %s to %s", current.State, state)
	}
	moved, merr := execAffected(tx.ctx, inner, `UPDATE land_intents
SET state = ?, reprepares = ?, updated_at = ? WHERE id = ?`,
		state, reprepares, msOf(now), id)
	if merr != nil {
		return merr
	}
	if moved == 0 {
		return fmt.Errorf("SetLandIntentState: %w: intent %d", ErrNotFound, id)
	}
	return nil
}

// ListLandIntents lists plans for one repository target, newest first.
func (tx *ReadTx) ListLandIntents(repoID int64, target string, limit int) ([]LandIntentRow, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	rows, qerr := inner.QueryContext(tx.ctx, `SELECT id, public_id, repo_id, target_ref, change_id,
candidate_oid, base_oid, policy_generation, state, reprepares, gap, created_by, created_at, updated_at
FROM land_intents WHERE repo_id = ? AND target_ref = ? ORDER BY id DESC LIMIT ?`,
		repoID, target, limit)
	if qerr != nil {
		return nil, qerr
	}
	defer func() { _ = rows.Close() }()
	var out []LandIntentRow
	for rows.Next() {
		var row LandIntentRow
		if serr := rows.Scan(&row.ID, &row.PublicID, &row.RepoID, &row.TargetRef,
			&row.ChangeID, &row.CandidateOid, &row.BaseOid, &row.PolicyGeneration,
			&row.State, &row.Reprepares, &row.Gap, &row.CreatedBy, &row.CreatedAt,
			&row.UpdatedAt); serr != nil {
			return nil, serr
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// InsertLandRun records one execution attempt of a land intent.
func (tx *WriteTx) InsertLandRun(now time.Time, row LandRunRow) (LandRunRow, error) {
	inner, err := tx.live()
	if err != nil {
		return LandRunRow{}, err
	}
	if row.PublicID == "" {
		row.PublicID = string(NewID(now))
	}
	if row.State == "" {
		row.State = "awaiting"
	}
	res, ierr := inner.ExecContext(tx.ctx, `INSERT INTO land_runs (public_id, plan_id, state, detail, created_at)
VALUES (?, ?, ?, ?, ?)`, row.PublicID, row.PlanID, row.State, row.Detail, msOf(now))
	if ierr != nil {
		return LandRunRow{}, ierr
	}
	id, ierr := res.LastInsertId()
	if ierr != nil {
		return LandRunRow{}, ierr
	}
	row.ID = id
	row.CreatedAt = msOf(now)
	return row, nil
}

// InsertIntent creates an intent with the next per-repo number.
func (tx *WriteTx) InsertIntent(now time.Time, row IntentRow) (IntentRow, error) {
	inner, err := tx.live()
	if err != nil {
		return IntentRow{}, err
	}
	if row.PublicID == "" {
		row.PublicID = string(NewID(now))
	}
	if row.Contract == "" {
		row.Contract = "{}"
	}
	var maxNum sql.NullInt64
	if qerr := inner.QueryRowContext(tx.ctx,
		`SELECT max(number) FROM intents WHERE repo_id = ?`, row.RepoID).Scan(&maxNum); qerr != nil {
		return IntentRow{}, qerr
	}
	number := int64(1)
	if maxNum.Valid {
		number = maxNum.Int64 + 1
	}
	ms := msOf(now)
	res, ierr := inner.ExecContext(tx.ctx, `INSERT INTO intents
(public_id, repo_id, number, title, body, state, contract, assignee_user_id, created_by, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, 'open', ?, ?, ?, ?, ?)`,
		row.PublicID, row.RepoID, number, row.Title, row.Body, row.Contract,
		nullInt(row.AssigneeUserID), row.CreatedBy, ms, ms)
	if ierr != nil {
		return IntentRow{}, ierr
	}
	id, ierr := res.LastInsertId()
	if ierr != nil {
		return IntentRow{}, ierr
	}
	row.ID = id
	row.Number = number
	row.State = "open"
	row.CreatedAt = ms
	row.UpdatedAt = ms
	return row, nil
}

// GetIntent loads one intent inside a write transaction.
func (tx *WriteTx) GetIntent(id int64) (IntentRow, error) {
	inner, err := tx.live()
	if err != nil {
		return IntentRow{}, err
	}
	var row IntentRow
	serr := inner.QueryRowContext(tx.ctx, `SELECT id, public_id, repo_id, number, title, body,
state, contract, assignee_user_id, created_by, created_at, updated_at
FROM intents WHERE id = ?`, id).Scan(
		&row.ID, &row.PublicID, &row.RepoID, &row.Number, &row.Title, &row.Body,
		&row.State, &row.Contract, &row.AssigneeUserID, &row.CreatedBy,
		&row.CreatedAt, &row.UpdatedAt)
	if errors.Is(serr, sql.ErrNoRows) {
		return IntentRow{}, fmt.Errorf("GetIntent: %w: intent %d", ErrNotFound, id)
	}
	return row, serr
}

// SetIntentState moves an intent between open, blocked, and closed.
func (tx *WriteTx) SetIntentState(now time.Time, id int64, state string) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	switch state {
	case "open", "blocked", "closed":
	default:
		return fmt.Errorf("SetIntentState: unknown state %q", state)
	}
	moved, merr := execAffected(tx.ctx, inner,
		`UPDATE intents SET state = ?, updated_at = ? WHERE id = ?`, state, msOf(now), id)
	if merr != nil {
		return merr
	}
	if moved == 0 {
		return fmt.Errorf("SetIntentState: %w: intent %d", ErrNotFound, id)
	}
	return nil
}

// LinkIntentChange attaches a change to an intent.
func (tx *WriteTx) LinkIntentChange(intentID, changeID int64, headOid string) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	_, ierr := inner.ExecContext(tx.ctx, `INSERT INTO intent_changes (intent_id, change_id, head_oid)
VALUES (?, ?, ?) ON CONFLICT (intent_id, change_id) DO UPDATE SET head_oid = excluded.head_oid`,
		intentID, changeID, headOid)
	return ierr
}

// UnlinkIntentChange detaches a change from an intent.
func (tx *WriteTx) UnlinkIntentChange(intentID, changeID int64) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	moved, merr := execAffected(tx.ctx, inner,
		`DELETE FROM intent_changes WHERE intent_id = ? AND change_id = ?`, intentID, changeID)
	if merr != nil {
		return merr
	}
	if moved == 0 {
		return fmt.Errorf("UnlinkIntentChange: %w: intent %d change %d", ErrNotFound, intentID, changeID)
	}
	return nil
}

// InsertAsk records a structured question on an intent.
func (tx *WriteTx) InsertAsk(now time.Time, row AskRow) (AskRow, error) {
	inner, err := tx.live()
	if err != nil {
		return AskRow{}, err
	}
	if row.PublicID == "" {
		row.PublicID = string(NewID(now))
	}
	res, ierr := inner.ExecContext(tx.ctx, `INSERT INTO asks
(public_id, intent_id, question, options, blocks_claim, state, answer, answered_by, expires_at, created_by, created_at)
VALUES (?, ?, ?, ?, ?, 'asked', '', NULL, ?, ?, ?)`,
		row.PublicID, row.IntentID, row.Question, row.Options, boolInt(row.BlocksClaim),
		nullInt(row.ExpiresAt), row.CreatedBy, msOf(now))
	if ierr != nil {
		return AskRow{}, ierr
	}
	id, ierr := res.LastInsertId()
	if ierr != nil {
		return AskRow{}, ierr
	}
	row.ID = id
	row.State = "asked"
	row.CreatedAt = msOf(now)
	return row, nil
}

// AnswerAsk records a human answer and unblocks the lease in the same row.
func (tx *WriteTx) AnswerAsk(intentID, askID, answeredBy int64, answer string) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	moved, merr := execAffected(tx.ctx, inner, `UPDATE asks
SET state = 'answered', answer = ?, answered_by = ? WHERE id = ? AND intent_id = ? AND state = 'asked'`,
		answer, answeredBy, askID, intentID)
	if merr != nil {
		return merr
	}
	if moved == 0 {
		return fmt.Errorf("AnswerAsk: %w: ask %d", ErrNotFound, askID)
	}
	return nil
}

// ExpireAsk marks an open ask expired without answering it.
func (tx *WriteTx) ExpireAsk(askID int64) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	moved, merr := execAffected(tx.ctx, inner, `UPDATE asks
SET state = 'expired' WHERE id = ? AND state = 'asked'`, askID)
	if merr != nil {
		return merr
	}
	if moved == 0 {
		return fmt.Errorf("ExpireAsk: %w: ask %d", ErrNotFound, askID)
	}
	return nil
}

// CountOpenBlockingAsks counts asked blocking asks on an intent.
func (tx *ReadTx) CountOpenBlockingAsks(intentID int64) (int64, error) {
	inner, err := tx.live()
	if err != nil {
		return 0, err
	}
	var count int64
	if qerr := inner.QueryRowContext(tx.ctx, `SELECT count(*) FROM asks
WHERE intent_id = ? AND state = 'asked' AND blocks_claim = 1`, intentID).Scan(&count); qerr != nil {
		return 0, qerr
	}
	return count, nil
}

// InsertView creates a named saved query with its first version.
func (tx *WriteTx) InsertView(now time.Time, row ViewRow) (ViewRow, error) {
	inner, err := tx.live()
	if err != nil {
		return ViewRow{}, err
	}
	if row.PublicID == "" {
		row.PublicID = string(NewID(now))
	}
	if row.Predicates == "" {
		row.Predicates = "{}"
	}
	ms := msOf(now)
	res, ierr := inner.ExecContext(tx.ctx, `INSERT INTO views
(public_id, repo_id, name, owner_user_id, shared, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		row.PublicID, row.RepoID, row.Name, nullInt(row.OwnerUserID), boolInt(row.Shared), ms, ms)
	if ierr != nil {
		return ViewRow{}, ierr
	}
	id, ierr := res.LastInsertId()
	if ierr != nil {
		return ViewRow{}, ierr
	}
	if _, ierr := inner.ExecContext(tx.ctx, `INSERT INTO view_versions (view_id, version, revset, predicates, created_at)
VALUES (?, 1, ?, ?, ?)`, id, row.Revset, row.Predicates, ms); ierr != nil {
		return ViewRow{}, ierr
	}
	row.ID = id
	row.Version = 1
	return row, nil
}

// AppendViewVersion pins one more immutable version of a view.
func (tx *WriteTx) AppendViewVersion(now time.Time, viewID int64, revset, predicates string) (int64, error) {
	inner, err := tx.live()
	if err != nil {
		return 0, err
	}
	var maxVersion sql.NullInt64
	if qerr := inner.QueryRowContext(tx.ctx,
		`SELECT max(version) FROM view_versions WHERE view_id = ?`, viewID).Scan(&maxVersion); qerr != nil {
		return 0, qerr
	}
	next := int64(1)
	if maxVersion.Valid {
		next = maxVersion.Int64 + 1
	}
	if _, ierr := inner.ExecContext(tx.ctx, `INSERT INTO view_versions (view_id, version, revset, predicates, created_at)
VALUES (?, ?, ?, ?, ?)`, viewID, next, revset, predicates, msOf(now)); ierr != nil {
		return 0, ierr
	}
	if _, uerr := inner.ExecContext(tx.ctx, `UPDATE views SET updated_at = ? WHERE id = ?`,
		msOf(now), viewID); uerr != nil {
		return 0, uerr
	}
	return next, nil
}

func nullInt(v sql.NullInt64) any {
	if v.Valid {
		return v.Int64
	}
	return nil
}

func nullStr(v sql.NullString) any {
	if v.Valid {
		return v.String
	}
	return nil
}

func boolInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

// ReadLandIntentByPublicID loads one land intent by its public id for reads.
func (tx *ReadTx) ReadLandIntentByPublicID(repoID int64, publicID string) (LandIntentRow, error) {
	inner, err := tx.live()
	if err != nil {
		return LandIntentRow{}, err
	}
	var row LandIntentRow
	serr := inner.QueryRowContext(tx.ctx, `SELECT id, public_id, repo_id, target_ref, change_id,
candidate_oid, base_oid, policy_generation, state, reprepares, gap, created_by, created_at, updated_at
FROM land_intents WHERE repo_id = ? AND public_id = ?`, repoID, publicID).Scan(
		&row.ID, &row.PublicID, &row.RepoID, &row.TargetRef, &row.ChangeID,
		&row.CandidateOid, &row.BaseOid, &row.PolicyGeneration, &row.State,
		&row.Reprepares, &row.Gap, &row.CreatedBy, &row.CreatedAt, &row.UpdatedAt)
	if errors.Is(serr, sql.ErrNoRows) {
		return LandIntentRow{}, fmt.Errorf("ReadLandIntentByPublicID: %w: %s", ErrNotFound, publicID)
	}
	return row, serr
}

// ListLandRunsByPlanID lists every recorded attempt of one land intent.
func (tx *ReadTx) ListLandRunsByPlanID(planID int64) ([]LandRunRow, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	rows, qerr := inner.QueryContext(tx.ctx, `SELECT id, public_id, plan_id, state, detail, created_at
FROM land_runs WHERE plan_id = ? ORDER BY id ASC`, planID)
	if qerr != nil {
		return nil, qerr
	}
	defer func() { _ = rows.Close() }()
	var out []LandRunRow
	for rows.Next() {
		var row LandRunRow
		if serr := rows.Scan(&row.ID, &row.PublicID, &row.PlanID, &row.State, &row.Detail, &row.CreatedAt); serr != nil {
			return nil, serr
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ReadViewByPublicID loads one view by its public id for reads.
func (tx *ReadTx) ReadViewByPublicID(repoID int64, publicID string) (ViewRow, error) {
	inner, err := tx.live()
	if err != nil {
		return ViewRow{}, err
	}
	var row ViewRow
	var version sql.NullInt64
	serr := inner.QueryRowContext(tx.ctx, `SELECT v.id, v.public_id, v.repo_id, v.name,
v.owner_user_id, v.shared, max(vv.version), vv.revset, vv.predicates
FROM views v LEFT JOIN view_versions vv ON vv.view_id = v.id
WHERE v.repo_id = ? AND v.public_id = ? GROUP BY v.id`, repoID, publicID).Scan(
		&row.ID, &row.PublicID, &row.RepoID, &row.Name, &row.OwnerUserID,
		&row.Shared, &version, &row.Revset, &row.Predicates)
	if errors.Is(serr, sql.ErrNoRows) {
		return ViewRow{}, fmt.Errorf("ReadViewByPublicID: %w: %s", ErrNotFound, publicID)
	}
	if version.Valid {
		row.Version = version.Int64
	}
	return row, serr
}

// ListViewsByRepo lists the saved queries of one repository.
func (tx *ReadTx) ListViewsByRepo(repoID int64) ([]ViewRow, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	rows, qerr := inner.QueryContext(tx.ctx, `SELECT v.id, v.public_id, v.repo_id, v.name,
v.owner_user_id, v.shared, max(vv.version)
FROM views v LEFT JOIN view_versions vv ON vv.view_id = v.id
WHERE v.repo_id = ? GROUP BY v.id ORDER BY v.name ASC`, repoID)
	if qerr != nil {
		return nil, qerr
	}
	defer func() { _ = rows.Close() }()
	var out []ViewRow
	for rows.Next() {
		var row ViewRow
		var version sql.NullInt64
		if serr := rows.Scan(&row.ID, &row.PublicID, &row.RepoID, &row.Name,
			&row.OwnerUserID, &row.Shared, &version); serr != nil {
			return nil, serr
		}
		if version.Valid {
			row.Version = version.Int64
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// DeleteView removes one saved query and its versions.
func (tx *WriteTx) DeleteView(id int64) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	moved, merr := execAffected(tx.ctx, inner, `DELETE FROM views WHERE id = ?`, id)
	if merr != nil {
		return merr
	}
	if moved == 0 {
		return fmt.Errorf("DeleteView: %w: view %d", ErrNotFound, id)
	}
	return nil
}

// SetViewShared flips the sharing of one saved query.
func (tx *WriteTx) SetViewShared(now time.Time, id int64, shared bool) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	moved, merr := execAffected(tx.ctx, inner, `UPDATE views SET shared = ?, updated_at = ? WHERE id = ?`,
		boolInt(shared), msOf(now), id)
	if merr != nil {
		return merr
	}
	if moved == 0 {
		return fmt.Errorf("SetViewShared: %w: view %d", ErrNotFound, id)
	}
	return nil
}

// ReadLandIntentByPublic loads one land intent by public id across repos.
func (tx *ReadTx) ReadLandIntentByPublic(publicID string) (LandIntentRow, error) {
	inner, err := tx.live()
	if err != nil {
		return LandIntentRow{}, err
	}
	var row LandIntentRow
	serr := inner.QueryRowContext(tx.ctx, `SELECT id, public_id, repo_id, target_ref, change_id,
candidate_oid, base_oid, policy_generation, state, reprepares, gap, created_by, created_at, updated_at
FROM land_intents WHERE public_id = ?`, publicID).Scan(
		&row.ID, &row.PublicID, &row.RepoID, &row.TargetRef, &row.ChangeID,
		&row.CandidateOid, &row.BaseOid, &row.PolicyGeneration, &row.State,
		&row.Reprepares, &row.Gap, &row.CreatedBy, &row.CreatedAt, &row.UpdatedAt)
	if errors.Is(serr, sql.ErrNoRows) {
		return LandIntentRow{}, fmt.Errorf("ReadLandIntentByPublic: %w: %s", ErrNotFound, publicID)
	}
	return row, serr
}

// WholeChangeComments lists path-less review records at a change head.
func (tx *WriteTx) WholeChangeComments(changeID int64, headOid string) ([]CommentRow, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	rows, qerr := inner.QueryContext(tx.ctx, `SELECT id, public_id, change_id, intent_id, head_oid,
path, line, body, author_user_id, remapped_from, unmatched, created_at
FROM change_comments WHERE change_id = ? AND head_oid = ? AND path IS NULL ORDER BY id ASC`,
		changeID, headOid)
	if qerr != nil {
		return nil, qerr
	}
	defer func() { _ = rows.Close() }()
	var out []CommentRow
	for rows.Next() {
		var row CommentRow
		var unmatched int64
		if serr := rows.Scan(&row.ID, &row.PublicID, &row.ChangeID, &row.IntentID,
			&row.HeadOid, &row.Path, &row.Line, &row.Body, &row.AuthorUserID,
			&row.RemappedFrom, &unmatched, &row.CreatedAt); serr != nil {
			return nil, serr
		}
		row.Unmatched = unmatched == 1
		out = append(out, row)
	}
	return out, rows.Err()
}

// UpdateBookmarkRule replaces the mode and requirements of one bookmark rule
// and bumps its version, returning the new policy generation.
func (tx *WriteTx) UpdateBookmarkRule(repoID int64, pattern, mode, required string) (int64, error) {
	inner, err := tx.live()
	if err != nil {
		return 0, err
	}
	res, uerr := inner.ExecContext(tx.ctx, `UPDATE bookmark_rules
SET mode = ?, required = ?, version = version + 1 WHERE repo_id = ? AND pattern = ?`,
		mode, required, repoID, pattern)
	if uerr != nil {
		return 0, uerr
	}
	affected, aerr := res.RowsAffected()
	if aerr != nil {
		return 0, aerr
	}
	if affected == 0 {
		return 0, fmt.Errorf("UpdateBookmarkRule: %w: %s", ErrNotFound, pattern)
	}
	var version int64
	if qerr := inner.QueryRowContext(tx.ctx, `SELECT version FROM bookmark_rules
WHERE repo_id = ? AND pattern = ?`, repoID, pattern).Scan(&version); qerr != nil {
		return 0, qerr
	}
	return version, nil
}

// GetLandIntentByPublicID loads one land intent by its public id inside a write transaction.
func (tx *WriteTx) GetLandIntentByPublicID(repoID int64, publicID string) (LandIntentRow, error) {
	inner, err := tx.live()
	if err != nil {
		return LandIntentRow{}, err
	}
	var row LandIntentRow
	serr := inner.QueryRowContext(tx.ctx, `SELECT id, public_id, repo_id, target_ref, change_id,
candidate_oid, base_oid, policy_generation, state, reprepares, gap, created_by, created_at, updated_at
FROM land_intents WHERE repo_id = ? AND public_id = ?`, repoID, publicID).Scan(
		&row.ID, &row.PublicID, &row.RepoID, &row.TargetRef, &row.ChangeID,
		&row.CandidateOid, &row.BaseOid, &row.PolicyGeneration, &row.State,
		&row.Reprepares, &row.Gap, &row.CreatedBy, &row.CreatedAt, &row.UpdatedAt)
	if errors.Is(serr, sql.ErrNoRows) {
		return LandIntentRow{}, fmt.Errorf("GetLandIntentByPublicID: %w: %s", ErrNotFound, publicID)
	}
	return row, serr
}

// GetViewByPublicID loads one view by its public id inside a write transaction.
func (tx *WriteTx) GetViewByPublicID(repoID int64, publicID string) (ViewRow, error) {
	inner, err := tx.live()
	if err != nil {
		return ViewRow{}, err
	}
	var row ViewRow
	var version sql.NullInt64
	serr := inner.QueryRowContext(tx.ctx, `SELECT v.id, v.public_id, v.repo_id, v.name,
v.owner_user_id, v.shared, max(vv.version), vv.revset, vv.predicates
FROM views v LEFT JOIN view_versions vv ON vv.view_id = v.id
WHERE v.repo_id = ? AND v.public_id = ? GROUP BY v.id`, repoID, publicID).Scan(
		&row.ID, &row.PublicID, &row.RepoID, &row.Name, &row.OwnerUserID,
		&row.Shared, &version, &row.Revset, &row.Predicates)
	if errors.Is(serr, sql.ErrNoRows) {
		return ViewRow{}, fmt.Errorf("GetViewByPublicID: %w: %s", ErrNotFound, publicID)
	}
	if version.Valid {
		row.Version = version.Int64
	}
	return row, serr
}

// ListCommentsAtHeadIncludingUnmatched lists every comment at a head inside a write transaction.
func (tx *WriteTx) ListCommentsAtHeadIncludingUnmatched(changeID int64, headOid string) ([]CommentRow, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	rows, qerr := inner.QueryContext(tx.ctx, `SELECT id, public_id, change_id, intent_id, head_oid, path, line, body,
author_user_id, remapped_from, unmatched, created_at
FROM change_comments WHERE change_id = ? AND head_oid = ? ORDER BY id ASC`, changeID, headOid)
	if qerr != nil {
		return nil, qerr
	}
	defer func() { _ = rows.Close() }()
	var out []CommentRow
	for rows.Next() {
		var row CommentRow
		var unmatched int64
		if serr := rows.Scan(&row.ID, &row.PublicID, &row.ChangeID, &row.IntentID,
			&row.HeadOid, &row.Path, &row.Line, &row.Body, &row.AuthorUserID,
			&row.RemappedFrom, &unmatched, &row.CreatedAt); serr != nil {
			return nil, serr
		}
		row.Unmatched = unmatched == 1
		out = append(out, row)
	}
	return out, rows.Err()
}

// SetLandRunState moves one land run along the awaiting, executing, landed,
// failed machine.
func (tx *WriteTx) SetLandRunState(now time.Time, id int64, state string) error {
	_ = now
	inner, err := tx.live()
	if err != nil {
		return err
	}
	switch state {
	case "awaiting", "executing", "landed", "failed":
	default:
		return fmt.Errorf("SetLandRunState: unknown state %q", state)
	}
	moved, merr := execAffected(tx.ctx, inner, `UPDATE land_runs
SET state = ? WHERE id = ?`, state, id)
	if merr != nil {
		return merr
	}
	if moved == 0 {
		return fmt.Errorf("SetLandRunState: %w: run %d", ErrNotFound, id)
	}
	return nil
}
