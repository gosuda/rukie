package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
)

const intentColumns = `id, public_id, repo_id, number, title, body, state, contract, assignee_id, reporter_id, close_reason, lease_mode, claim_paths, version, closed_at, created_at, updated_at`
const askColumns = `id, public_id, repo_id, intent_id, question, options, blocks_claim, state, COALESCE(answer,''), answer_by, deadline_ms, created_by, created_at, answered_at, version`

func scanIntent(scan func(...any) error) (IntentRow, error) {
	var r IntentRow
	err := scan(&r.ID, &r.PublicID, &r.RepoID, &r.Number, &r.Title, &r.Body, &r.State, &r.Contract, &r.AssigneeUserID, &r.CreatedBy, &r.CloseReason, &r.LeaseMode, &r.ClaimPaths, &r.Version, &r.ClosedAt, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("scanIntent: %w", ErrNotFound)
	}
	return r, err
}

func scanAsk(scan func(...any) error) (AskRow, error) {
	var r AskRow
	err := scan(&r.ID, &r.PublicID, &r.RepoID, &r.IntentID, &r.Question, &r.Options, &r.BlocksClaim, &r.State, &r.Answer, &r.AnsweredBy, &r.DeadlineMS, &r.CreatedBy, &r.CreatedAt, &r.AnsweredAt, &r.Version)
	r.ExpiresAt = sql.NullInt64{Int64: r.DeadlineMS, Valid: true}
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("scanAsk: %w", ErrNotFound)
	}
	return r, err
}

func (tx *WriteTx) intentEvent(now time.Time, kind string, r IntentRow) error {
	body, err := jsonv2.Marshal(map[string]any{"repo_id": r.RepoID, "intent_id": r.PublicID, "task_number": r.Number})
	if err != nil {
		return fmt.Errorf("intentEvent: %w", err)
	}
	_, err = tx.Outbox().Emit(Event{OccurredAt: now, Kind: kind, Body: jsontext.Value(body)})
	return err
}

func EmitTaskEvent(tx *WriteTx, now time.Time, kind string, r IntentRow) error {
	return tx.intentEvent(now, kind, r)
}

func intentNumber(tx *WriteTx, db *sql.Tx, repoID int64) (int64, error) {
	var maxNum sql.NullInt64
	if err := db.QueryRowContext(tx.ctx, `SELECT max(number) FROM intents WHERE repo_id = ?`, repoID).Scan(&maxNum); err != nil {
		return 0, err
	}
	if maxNum.Valid {
		return maxNum.Int64 + 1, nil
	}
	return 1, nil
}

func validLeaseMode(mode string) bool {
	return mode == "enforce" || mode == "advise"
}

func validCloseReason(reason string) bool {
	switch reason {
	case "completed", "duplicate", "wontfix", "abandoned":
		return true
	}
	return false
}

func seedFacets(ctx context.Context, now time.Time, db *sql.Tx, repoID int64) error {
	seeds := []struct{ key, valueType, allowed string }{
		{"risk", "enum", `["low","medium","high"]`},
		{"area", "enum", `["backend","frontend","docs","infra"]`},
		{"size", "enum", `["xs","s","m","l"]`},
	}
	for _, s := range seeds {
		if _, err := db.ExecContext(ctx, `INSERT INTO facets(public_id,repo_id,key,value_type,allowed,required,created_at) VALUES(?,?,?,?,?,0,?) ON CONFLICT(repo_id,key) DO NOTHING`,
			string(NewID(now)), repoID, s.key, s.valueType, s.allowed, msOf(now)); err != nil {
			return fmt.Errorf("seedFacets: %w", err)
		}
	}
	return nil
}

func (tx *WriteTx) InsertIntent(now time.Time, r IntentRow) (IntentRow, error) {
	db, err := tx.live()
	if err != nil {
		return r, fmt.Errorf("InsertIntent: %w", err)
	}
	if len(r.Body) > 65536 {
		return r, fmt.Errorf("InsertIntent: %w: body", ErrInvalid)
	}
	r.PublicID = string(NewID(now))
	r.Contract = defaultString(r.Contract, `{"requirements":[]}`)
	if !jsonv1.Valid([]byte(r.Contract)) {
		return r, fmt.Errorf("InsertIntent: %w: contract", ErrInvalid)
	}
	r.LeaseMode = defaultString(r.LeaseMode, "advise")
	if !validLeaseMode(r.LeaseMode) {
		return r, fmt.Errorf("InsertIntent: %w: lease mode", ErrInvalid)
	}
	r.ClaimPaths = defaultString(r.ClaimPaths, "[]")
	if !jsonv1.Valid([]byte(r.ClaimPaths)) {
		return r, fmt.Errorf("InsertIntent: %w: claim paths", ErrInvalid)
	}
	if serr := seedFacets(tx.ctx, now, db, r.RepoID); serr != nil {
		return r, serr
	}
	number, err := intentNumber(tx, db, r.RepoID)
	if err != nil {
		return r, fmt.Errorf("InsertIntent: %w", err)
	}
	res, err := db.ExecContext(tx.ctx, `INSERT INTO intents
(public_id, repo_id, number, title, body, state, contract, assignee_id, reporter_id, lease_mode, claim_paths, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, 'open', ?, ?, ?, ?, ?, ?, ?)`,
		r.PublicID, r.RepoID, number, r.Title, r.Body, r.Contract,
		nullInt(r.AssigneeUserID), r.CreatedBy, r.LeaseMode, r.ClaimPaths, msOf(now), msOf(now))
	if err != nil {
		return r, fmt.Errorf("InsertIntent: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return r, fmt.Errorf("InsertIntent: %w", err)
	}
	r, err = tx.GetIntent(id)
	if err != nil {
		return r, err
	}
	if err = tx.intentEvent(now, "task.created", r); err != nil {
		return r, fmt.Errorf("InsertIntent: %w", err)
	}
	return r, nil
}

func (tx *WriteTx) GetIntent(id int64) (IntentRow, error) {
	db, err := tx.live()
	if err != nil {
		return IntentRow{}, err
	}
	return scanIntent(db.QueryRowContext(tx.ctx, `SELECT `+intentColumns+` FROM intents WHERE id = ?`, id).Scan)
}

func (tx *ReadTx) ReadIntentByPublicID(repoID int64, publicID string) (IntentRow, error) {
	db, err := tx.live()
	if err != nil {
		return IntentRow{}, err
	}
	return scanIntent(db.QueryRowContext(tx.ctx, `SELECT `+intentColumns+` FROM intents WHERE repo_id = ? AND public_id = ?`, repoID, publicID).Scan)
}

func (tx *ReadTx) ReadIntentByNumber(repoID, number int64) (IntentRow, error) {
	db, err := tx.live()
	if err != nil {
		return IntentRow{}, err
	}
	return scanIntent(db.QueryRowContext(tx.ctx, `SELECT `+intentColumns+` FROM intents WHERE repo_id = ? AND number = ?`, repoID, number).Scan)
}

func (tx *ReadTx) ListIntents(repoID int64, state string, limit int) ([]IntentRow, error) {
	db, err := tx.live()
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("ListIntents: %w: limit", ErrInvalid)
	}
	rows, err := db.QueryContext(tx.ctx, `SELECT `+intentColumns+` FROM intents
WHERE (? = 0 OR repo_id = ?) AND (? = '' OR state = ?) ORDER BY updated_at DESC, id DESC LIMIT ?`,
		repoID, repoID, state, state, limit)
	if err != nil {
		return nil, fmt.Errorf("ListIntents: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []IntentRow{}
	for rows.Next() {
		r, serr := scanIntent(rows.Scan)
		if serr != nil {
			return nil, serr
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (tx *WriteTx) UpdateIntent(now time.Time, r IntentRow, expectedVersion int64) (IntentRow, error) {
	db, err := tx.live()
	if err != nil {
		return r, err
	}
	var current int64
	if serr := db.QueryRowContext(tx.ctx, `SELECT version FROM intents WHERE id = ? AND repo_id = ?`, r.ID, r.RepoID).Scan(&current); serr != nil {
		if errors.Is(serr, sql.ErrNoRows) {
			return r, fmt.Errorf("UpdateIntent: %w", ErrNotFound)
		}
		return r, fmt.Errorf("UpdateIntent: %w", serr)
	}
	if current != expectedVersion {
		return r, fmt.Errorf("UpdateIntent: %w", ErrStale)
	}
	if len(r.Body) > 65536 {
		return r, fmt.Errorf("UpdateIntent: %w: body", ErrInvalid)
	}
	if !jsonv1.Valid([]byte(r.Contract)) {
		return r, fmt.Errorf("UpdateIntent: %w: contract", ErrInvalid)
	}
	if !validLeaseMode(r.LeaseMode) {
		return r, fmt.Errorf("UpdateIntent: %w: lease mode", ErrInvalid)
	}
	if !jsonv1.Valid([]byte(r.ClaimPaths)) {
		return r, fmt.Errorf("UpdateIntent: %w: claim paths", ErrInvalid)
	}
	n, err := execAffected(tx.ctx, db, `UPDATE intents
SET title = ?, body = ?, contract = ?, assignee_id = ?, lease_mode = ?, claim_paths = ?, version = version + 1, updated_at = ?
WHERE id = ? AND repo_id = ? AND version = ?`,
		r.Title, r.Body, r.Contract, nullInt(r.AssigneeUserID), r.LeaseMode, r.ClaimPaths, msOf(now), r.ID, r.RepoID, expectedVersion)
	if err != nil {
		return r, fmt.Errorf("UpdateIntent: %w", err)
	}
	if n != 1 {
		return r, fmt.Errorf("UpdateIntent: %w", ErrStale)
	}
	return tx.GetIntent(r.ID)
}

func (tx *WriteTx) SetIntentStateVersion(now time.Time, id, version int64, state, reason string) (IntentRow, error) {
	db, err := tx.live()
	if err != nil {
		return IntentRow{}, err
	}
	r, err := tx.GetIntent(id)
	if err != nil {
		return r, err
	}
	switch state {
	case "open", "blocked":
		if reason != "" {
			return r, fmt.Errorf("SetIntentStateVersion: %w: close reason", ErrInvalid)
		}
	case "closed":
		if !validCloseReason(defaultString(reason, "completed")) {
			return r, fmt.Errorf("SetIntentStateVersion: %w: close reason", ErrInvalid)
		}
	default:
		return r, fmt.Errorf("SetIntentStateVersion: %w: state", ErrInvalid)
	}
	var closed any
	var why any
	if state == "closed" {
		closed = msOf(now)
		why = defaultString(reason, "completed")
	}
	n, err := execAffected(tx.ctx, db, `UPDATE intents
SET state = ?, close_reason = ?, closed_at = ?, updated_at = ?, version = version + 1 WHERE id = ? AND version = ?`,
		state, why, closed, msOf(now), id, version)
	if err != nil {
		return r, fmt.Errorf("SetIntentStateVersion: %w", err)
	}
	if n != 1 {
		return r, fmt.Errorf("SetIntentStateVersion: %w", ErrStale)
	}
	r, err = tx.GetIntent(id)
	if err != nil {
		return r, err
	}
	if state == "closed" {
		err = tx.intentEvent(now, "task.closed", r)
	}
	return r, err
}

func (tx *WriteTx) SetIntentState(now time.Time, id int64, state string) error {
	r, err := tx.GetIntent(id)
	if err != nil {
		return err
	}
	_, err = tx.SetIntentStateVersion(now, id, r.Version, state, "")
	return err
}

func (tx *WriteTx) GetAsk(id int64) (AskRow, error) {
	db, err := tx.live()
	if err != nil {
		return AskRow{}, err
	}
	return scanAsk(db.QueryRowContext(tx.ctx, `SELECT `+askColumns+` FROM asks WHERE id = ?`, id).Scan)
}

func (tx *ReadTx) ReadAskByPublicID(repoID int64, publicID string) (AskRow, error) {
	db, err := tx.live()
	if err != nil {
		return AskRow{}, err
	}
	return scanAsk(db.QueryRowContext(tx.ctx, `SELECT `+askColumns+` FROM asks WHERE repo_id = ? AND public_id = ?`, repoID, publicID).Scan)
}

func (tx *ReadTx) ListAsks(repoID, intentID int64, state string, limit int) ([]AskRow, error) {
	db, err := tx.live()
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("ListAsks: %w: limit", ErrInvalid)
	}
	rows, err := db.QueryContext(tx.ctx, `SELECT `+askColumns+` FROM asks
WHERE repo_id = ? AND (? = 0 OR intent_id = ?) AND (? = '' OR state = ?) ORDER BY id DESC LIMIT ?`,
		repoID, intentID, intentID, state, state, limit)
	if err != nil {
		return nil, fmt.Errorf("ListAsks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []AskRow{}
	for rows.Next() {
		r, serr := scanAsk(rows.Scan)
		if serr != nil {
			return nil, serr
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (tx *WriteTx) InsertAsk(now time.Time, r AskRow) (AskRow, error) {
	db, err := tx.live()
	if err != nil {
		return r, err
	}
	intent, err := tx.GetIntent(r.IntentID)
	if err != nil {
		return r, err
	}
	if r.RepoID == 0 {
		r.RepoID = intent.RepoID
	}
	if r.RepoID != intent.RepoID {
		return r, fmt.Errorf("InsertAsk: %w: repository mismatch", ErrInvalid)
	}
	if r.DeadlineMS == 0 && r.ExpiresAt.Valid {
		r.DeadlineMS = r.ExpiresAt.Int64
	}
	if r.DeadlineMS == 0 {
		r.DeadlineMS = msOf(now.Add(72 * time.Hour))
	}
	if r.DeadlineMS <= msOf(now) || r.DeadlineMS > msOf(now.Add(30*24*time.Hour)) {
		return r, fmt.Errorf("InsertAsk: %w: deadline", ErrInvalid)
	}
	if !jsonv1.Valid([]byte(defaultString(r.Options, "[]"))) {
		return r, fmt.Errorf("InsertAsk: %w: options", ErrInvalid)
	}
	r.PublicID = string(NewID(now))
	r.Options = defaultString(r.Options, "[]")
	res, err := db.ExecContext(tx.ctx, `INSERT INTO asks
(public_id, repo_id, intent_id, question, options, blocks_claim, deadline_ms, created_by, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.PublicID, r.RepoID, r.IntentID, r.Question, r.Options, r.BlocksClaim, r.DeadlineMS, r.CreatedBy, msOf(now))
	if err != nil {
		return r, fmt.Errorf("InsertAsk: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return r, err
	}
	if r.BlocksClaim && intent.State != "closed" {
		_, err = db.ExecContext(tx.ctx, `UPDATE intents SET state = 'blocked', version = version + 1, updated_at = ? WHERE id = ?`,
			msOf(now), r.IntentID)
		if err != nil {
			return r, err
		}
	}
	return tx.GetAsk(id)
}

func (tx *WriteTx) unblockIntent(now time.Time, intentID int64) error {
	db, err := tx.live()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(tx.ctx, `UPDATE intents
SET state = 'open', version = version + 1, updated_at = ?
WHERE id = ? AND state = 'blocked'
AND NOT EXISTS (SELECT 1 FROM asks WHERE intent_id = ? AND state = 'asked' AND blocks_claim = 1 AND deadline_ms > ?)`,
		msOf(now), intentID, intentID, msOf(now))
	return err
}

func (tx *WriteTx) AnswerAskAt(now time.Time, intentID, askID, answeredBy int64, answer string) error {
	db, err := tx.live()
	if err != nil {
		return err
	}
	if answer == "" {
		return fmt.Errorf("AnswerAskAt: %w: empty answer", ErrInvalid)
	}
	n, err := execAffected(tx.ctx, db, `UPDATE asks
SET state = 'answered', answer = ?, answer_by = ?, answered_at = ?, version = version + 1
WHERE id = ? AND intent_id = ? AND state = 'asked' AND deadline_ms > ?`,
		answer, answeredBy, msOf(now), askID, intentID, msOf(now))
	if err != nil {
		return fmt.Errorf("AnswerAskAt: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("AnswerAskAt: %w: expired or terminal ask", ErrStale)
	}
	return tx.unblockIntent(now, intentID)
}

func (tx *WriteTx) AnswerAsk(intentID, askID, answeredBy int64, answer string) error {
	return tx.AnswerAskAt(time.Now(), intentID, askID, answeredBy, answer)
}

func (tx *WriteTx) CancelAsk(now time.Time, askID, version int64) error {
	db, err := tx.live()
	if err != nil {
		return err
	}
	r, err := tx.GetAsk(askID)
	if err != nil {
		return err
	}
	n, err := execAffected(tx.ctx, db, `UPDATE asks
SET state = 'expired', version = version + 1 WHERE id = ? AND version = ? AND state = 'asked'`,
		askID, version)
	if err != nil {
		return fmt.Errorf("CancelAsk: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("CancelAsk: %w", ErrStale)
	}
	return tx.unblockIntent(now, r.IntentID)
}

func (tx *WriteTx) ExpireAsk(askID int64) error {
	r, err := tx.GetAsk(askID)
	if err != nil {
		return err
	}
	now := time.Now()
	if r.DeadlineMS > msOf(now) {
		return fmt.Errorf("ExpireAsk: %w: deadline not reached", ErrInvalid)
	}
	return tx.CancelAsk(now, askID, r.Version)
}

func (tx *WriteTx) ExpireIntentAsks(now time.Time, intentID int64) error {
	db, err := tx.live()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(tx.ctx, `UPDATE asks
SET state = 'expired', version = version + 1 WHERE intent_id = ? AND state = 'asked' AND deadline_ms <= ?`,
		intentID, msOf(now))
	if err != nil {
		return fmt.Errorf("ExpireIntentAsks: %w", err)
	}
	return tx.unblockIntent(now, intentID)
}

type IntentChangeRow struct {
	PublicID  string
	ChangeID  string
	Relation  string
	HeadOID   sql.NullString
	IntentID  int64
	RepoID    int64
	AddedBy   int64
	CreatedAt int64
	ID        int64
}

type FacetRow struct {
	PublicID  string
	Key       string
	ValueType string
	Allowed   string
	Required  bool
	RepoID    int64
	ID        int64
	CreatedAt int64
}

func scanIntentChange(scan func(...any) error) (IntentChangeRow, error) {
	var r IntentChangeRow
	err := scan(&r.ID, &r.PublicID, &r.IntentID, &r.RepoID, &r.ChangeID, &r.HeadOID, &r.Relation, &r.AddedBy, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("scanIntentChange: %w", ErrNotFound)
	}
	return r, err
}

func validRelation(relation string) bool {
	switch relation {
	case "implements", "fixes", "relates":
		return true
	}
	return false
}

func (tx *WriteTx) InsertIntentChange(now time.Time, r IntentChangeRow) (IntentChangeRow, error) {
	db, err := tx.live()
	if err != nil {
		return r, err
	}
	intent, err := tx.GetIntent(r.IntentID)
	if err != nil {
		return r, err
	}
	if r.RepoID == 0 {
		r.RepoID = intent.RepoID
	}
	if r.RepoID != intent.RepoID {
		return r, fmt.Errorf("InsertIntentChange: %w: repository mismatch", ErrInvalid)
	}
	if r.ChangeID == "" || len(r.ChangeID) > 128 {
		return r, fmt.Errorf("InsertIntentChange: %w: change id", ErrInvalid)
	}
	r.Relation = defaultString(r.Relation, "implements")
	if !validRelation(r.Relation) {
		return r, fmt.Errorf("InsertIntentChange: %w: relation", ErrInvalid)
	}
	r.PublicID = string(NewID(now))
	_, err = db.ExecContext(tx.ctx, `INSERT INTO intent_changes
(public_id, intent_id, repo_id, change_id, head_oid, relation, added_by, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (intent_id, change_id) DO UPDATE SET head_oid = excluded.head_oid, relation = excluded.relation`,
		r.PublicID, r.IntentID, r.RepoID, r.ChangeID, nullStr(r.HeadOID), r.Relation, intent.CreatedBy, msOf(now))
	if err != nil {
		return r, fmt.Errorf("InsertIntentChange: %w", err)
	}
	return tx.readIntentChange(r.IntentID, r.ChangeID)
}

func (tx *ReadTx) ReadIntentChange(intentID int64, changeID string) (IntentChangeRow, error) {
	db, err := tx.live()
	if err != nil {
		return IntentChangeRow{}, err
	}
	return scanIntentChange(db.QueryRowContext(tx.ctx, `SELECT id, public_id, intent_id, repo_id, change_id, head_oid, relation, added_by, created_at
FROM intent_changes WHERE intent_id = ? AND change_id = ?`, intentID, changeID).Scan)
}

func (tx *ReadTx) ListIntentChanges(intentID int64) ([]IntentChangeRow, error) {
	db, err := tx.live()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(tx.ctx, `SELECT id, public_id, intent_id, repo_id, change_id, head_oid, relation, added_by, created_at
FROM intent_changes WHERE intent_id = ? ORDER BY id ASC`, intentID)
	if err != nil {
		return nil, fmt.Errorf("ListIntentChanges: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []IntentChangeRow{}
	for rows.Next() {
		r, serr := scanIntentChange(rows.Scan)
		if serr != nil {
			return nil, serr
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (tx *WriteTx) DeleteIntentChange(now time.Time, intentID int64, changeID string) error {
	db, err := tx.live()
	if err != nil {
		return err
	}
	n, err := execAffected(tx.ctx, db, `DELETE FROM intent_changes WHERE intent_id = ? AND change_id = ?`, intentID, changeID)
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("DeleteIntentChange: %w", ErrNotFound)
	}
	_, err = db.ExecContext(tx.ctx, `UPDATE intents SET updated_at = ?, version = version + 1 WHERE id = ?`, msOf(now), intentID)
	return err
}

func (tx *WriteTx) LinkIntentChange(intentID, changeID int64, headOid string) error {
	db, err := tx.live()
	if err != nil {
		return err
	}
	var repoID int64
	if err = db.QueryRowContext(tx.ctx, `SELECT repo_id FROM intents WHERE id = ?`, intentID).Scan(&repoID); err != nil {
		return err
	}
	var key string
	if err = db.QueryRowContext(tx.ctx, `SELECT change_key FROM changes WHERE id = ? AND repo_id = ?`, changeID, repoID).Scan(&key); err != nil {
		return err
	}
	_, err = tx.InsertIntentChange(time.Now(), IntentChangeRow{
		IntentID: intentID,
		ChangeID: key,
		HeadOID:  sql.NullString{String: headOid, Valid: headOid != ""},
	})
	return err
}

func (tx *WriteTx) UnlinkIntentChange(intentID, changeID int64) error {
	db, err := tx.live()
	if err != nil {
		return err
	}
	var repoID int64
	if err = db.QueryRowContext(tx.ctx, `SELECT repo_id FROM intents WHERE id = ?`, intentID).Scan(&repoID); err != nil {
		return err
	}
	var key string
	if err = db.QueryRowContext(tx.ctx, `SELECT change_key FROM changes WHERE id = ? AND repo_id = ?`, changeID, repoID).Scan(&key); err != nil {
		return err
	}
	return tx.DeleteIntentChange(time.Now(), intentID, key)
}

func (tx *WriteTx) readIntentChange(intentID int64, changeID string) (IntentChangeRow, error) {
	db, err := tx.live()
	if err != nil {
		return IntentChangeRow{}, err
	}
	return scanIntentChange(db.QueryRowContext(tx.ctx, `SELECT id, public_id, intent_id, repo_id, change_id, head_oid, relation, added_by, created_at
FROM intent_changes WHERE intent_id = ? AND change_id = ?`, intentID, changeID).Scan)
}

func validFacetKey(k string) bool {
	if len(k) < 1 || len(k) > 64 {
		return false
	}
	if k[0] < 'a' || k[0] > 'z' {
		return false
	}
	for _, c := range k {
		if c != '_' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func (tx *WriteTx) readFacet(repoID int64, key string) (FacetRow, error) {
	db, err := tx.live()
	if err != nil {
		return FacetRow{}, err
	}
	var r FacetRow
	var req int64
	err = db.QueryRowContext(tx.ctx, `SELECT id, public_id, repo_id, key, value_type, allowed, required, created_at
FROM facets WHERE repo_id = ? AND key = ?`, repoID, key).Scan(
		&r.ID, &r.PublicID, &r.RepoID, &r.Key, &r.ValueType, &r.Allowed, &req, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("readFacet: %w", ErrNotFound)
	}
	r.Required = req == 1
	return r, err
}

func (tx *WriteTx) UpsertFacet(now time.Time, repoID int64, key, valueType, allowed string, required bool) (FacetRow, error) {
	db, err := tx.live()
	if err != nil {
		return FacetRow{}, err
	}
	if !validFacetKey(key) || (valueType != "enum" && valueType != "bool" && valueType != "int" && valueType != "text") {
		return FacetRow{}, fmt.Errorf("UpsertFacet: %w: invalid facet", ErrInvalid)
	}
	if !jsonv1.Valid([]byte(defaultString(allowed, "[]"))) {
		return FacetRow{}, fmt.Errorf("UpsertFacet: %w: allowed", ErrInvalid)
	}
	r := FacetRow{Key: key, ValueType: valueType, Allowed: defaultString(allowed, "[]"), Required: required, RepoID: repoID}
	r.PublicID = string(NewID(now))
	_, err = db.ExecContext(tx.ctx, `INSERT INTO facets
(public_id, repo_id, key, value_type, allowed, required, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (repo_id, key) DO UPDATE SET value_type = excluded.value_type, allowed = excluded.allowed, required = excluded.required`,
		r.PublicID, repoID, key, valueType, r.Allowed, boolInt(required), msOf(now))
	if err != nil {
		return r, fmt.Errorf("UpsertFacet: %w", err)
	}
	return tx.readFacet(repoID, key)
}

func (tx *ReadTx) ReadFacet(repoID int64, key string) (FacetRow, error) {
	db, err := tx.live()
	if err != nil {
		return FacetRow{}, err
	}
	var r FacetRow
	var req int64
	err = db.QueryRowContext(tx.ctx, `SELECT id, public_id, repo_id, key, value_type, allowed, required, created_at
FROM facets WHERE repo_id = ? AND key = ?`, repoID, key).Scan(
		&r.ID, &r.PublicID, &r.RepoID, &r.Key, &r.ValueType, &r.Allowed, &req, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("ReadFacet: %w", ErrNotFound)
	}
	r.Required = req == 1
	return r, err
}

func (tx *WriteTx) SetIntentFacets(now time.Time, intentID int64, values map[string]string) error {
	db, err := tx.live()
	if err != nil {
		return err
	}
	intent, err := tx.GetIntent(intentID)
	if err != nil {
		return err
	}
	for k, v := range values {
		if !jsonv1.Valid([]byte(v)) {
			return fmt.Errorf("SetIntentFacets: %w: facet %s", ErrInvalid, k)
		}
		f, ferr := tx.readFacet(intent.RepoID, k)
		if ferr != nil {
			return ferr
		}
		_, ferr = db.ExecContext(tx.ctx, `INSERT INTO intent_facets
(intent_id, facet_id, value, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT (intent_id, facet_id) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			intentID, f.ID, v, msOf(now))
		if ferr != nil {
			return fmt.Errorf("SetIntentFacets: %w", ferr)
		}
	}
	_, err = db.ExecContext(tx.ctx, `UPDATE intents SET updated_at = ?, version = version + 1 WHERE id = ?`, msOf(now), intentID)
	return err
}

func (tx *ReadTx) ListIntentFacets(intentID int64) (map[string]string, error) {
	db, err := tx.live()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(tx.ctx, `SELECT f.key, intent_facets.value
FROM intent_facets JOIN facets f ON f.id = intent_facets.facet_id WHERE intent_facets.intent_id = ?`, intentID)
	if err != nil {
		return nil, fmt.Errorf("ListIntentFacets: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err = rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func (tx *ReadTx) CountOpenBlockingAsks(intentID int64) (int64, error) {
	return tx.CountOpenBlockingAsksAt(time.Now(), intentID)
}

func (tx *ReadTx) CountOpenBlockingAsksAt(now time.Time, intentID int64) (int64, error) {
	db, err := tx.live()
	if err != nil {
		return 0, err
	}
	var n int64
	err = db.QueryRowContext(tx.ctx, `SELECT count(*) FROM asks
WHERE intent_id = ? AND state = 'asked' AND blocks_claim = 1 AND deadline_ms > ?`, intentID, msOf(now)).Scan(&n)
	return n, err
}
