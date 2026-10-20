package store

import (
	"fmt"
	"time"

	"encoding/json/jsontext"
)

const (
	feedMaxIDLen     = 128
	feedMaxRefName   = 512
	feedMaxTargetLen = 256
)

const (
	sqlPushInsert = `INSERT INTO pending_pushes (invocation_id, repo_id, actor_id, landed, attempted_at)
VALUES (?1, ?2, ?3, ?4, ?5) ON CONFLICT (invocation_id) DO NOTHING`

	sqlPushGet = `SELECT invocation_id, repo_id, actor_id, landed, attempted_at, enqueued FROM pending_pushes WHERE invocation_id = ?1`

	sqlPushLanded = `UPDATE pending_pushes SET landed = ?1, attempted_at = ?2 WHERE invocation_id = ?3`

	sqlPushEnqueued = `UPDATE pending_pushes SET enqueued = 1 WHERE invocation_id = ?1 AND enqueued = 0`

	sqlTipPut = `INSERT INTO ref_tips (repo_id, name, target, updated_at) VALUES (?1, ?2, ?3, ?4)
ON CONFLICT (repo_id, name) DO UPDATE SET target = excluded.target, updated_at = excluded.updated_at`

	sqlTipDelete = `DELETE FROM ref_tips WHERE repo_id = ?1 AND name = ?2`

	sqlTipsClear = `DELETE FROM ref_tips WHERE repo_id = ?1`

	sqlTipsList = `SELECT name, target FROM ref_tips WHERE repo_id = ?1`

	sqlSeenInsert = `INSERT INTO follower_seen (repo_id, seq, name, new, seen_at) VALUES (?1, ?2, ?3, ?4, ?5)
ON CONFLICT (repo_id, seq) DO NOTHING`

	sqlSeenSweep = `DELETE FROM follower_seen WHERE repo_id = ?1 AND seq <= ?2`
)

type PendingPush struct {
	Landed       string
	InvocationID string
	RepoID       int64
	ActorID      int64
	AttemptedAt  int64
	Enqueued     int64
}

func (tx *WriteTx) PutPendingPush(push PendingPush) (bool, error) {
	if len(push.InvocationID) < 1 || len(push.InvocationID) > feedMaxIDLen {
		return false, fmt.Errorf("PutPendingPush: %w: invocation id must be 1 to 128 bytes", ErrInvalid)
	}
	if push.RepoID <= 0 {
		return false, fmt.Errorf("PutPendingPush: %w: repo id must be positive", ErrInvalid)
	}
	if push.ActorID < 0 {
		return false, fmt.Errorf("PutPendingPush: %w: actor id must not be negative", ErrInvalid)
	}
	if len(push.Landed) == 0 || !jsontext.Value(push.Landed).IsValid() {
		return false, fmt.Errorf("PutPendingPush: %w: landed must be valid JSON", ErrInvalid)
	}
	if push.AttemptedAt <= 0 {
		return false, fmt.Errorf("PutPendingPush: %w: attempted_at must be after the Unix epoch", ErrInvalid)
	}
	inner, err := tx.live()
	if err != nil {
		return false, err
	}
	moved, err := execAffected(tx.ctx, inner, sqlPushInsert, push.InvocationID, push.RepoID, push.ActorID, push.Landed, push.AttemptedAt)
	if err != nil {
		return false, fmt.Errorf("PutPendingPush: %w", mapErr(err))
	}
	return moved == 1, nil
}

func (tx *WriteTx) GetPendingPush(id string) (PendingPush, error) {
	inner, err := tx.live()
	if err != nil {
		return PendingPush{}, err
	}
	var push PendingPush
	var landed string
	err = inner.QueryRowContext(tx.ctx, sqlPushGet, id).Scan(&push.InvocationID, &push.RepoID, &push.ActorID, &landed, &push.AttemptedAt, &push.Enqueued)
	if err != nil {
		return PendingPush{}, fmt.Errorf("GetPendingPush: %w", err)
	}
	push.Landed = landed
	return push, nil
}

func (tx *WriteTx) SetPushLanded(id, landed string, attemptedAt int64) error {
	if len(landed) == 0 || !jsontext.Value(landed).IsValid() {
		return fmt.Errorf("SetPushLanded: %w: landed must be valid JSON", ErrInvalid)
	}
	if attemptedAt <= 0 {
		return fmt.Errorf("SetPushLanded: %w: attempted_at must be after the Unix epoch", ErrInvalid)
	}
	inner, err := tx.live()
	if err != nil {
		return err
	}
	moved, err := execAffected(tx.ctx, inner, sqlPushLanded, landed, attemptedAt, id)
	if err != nil {
		return fmt.Errorf("SetPushLanded: %w", mapErr(err))
	}
	if moved == 0 {
		return fmt.Errorf("SetPushLanded: %w: no pending push", ErrNotFound)
	}
	return nil
}

func (tx *WriteTx) MarkPushEnqueued(id string) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	moved, err := execAffected(tx.ctx, inner, sqlPushEnqueued, id)
	if err != nil {
		return fmt.Errorf("MarkPushEnqueued: %w", mapErr(err))
	}
	if moved == 0 {
		return fmt.Errorf("MarkPushEnqueued: %w: push is missing or already enqueued", ErrStale)
	}
	return nil
}

func checkTip(repoID int64, name, target string, at time.Time) error {
	if repoID <= 0 {
		return fmt.Errorf("tip: %w: repo id must be positive", ErrInvalid)
	}
	if len(name) < 1 || len(name) > feedMaxRefName {
		return fmt.Errorf("tip: %w: name must be 1 to 512 bytes", ErrInvalid)
	}
	if len(target) < 1 || len(target) > feedMaxTargetLen {
		return fmt.Errorf("tip: %w: target must be 1 to 256 bytes", ErrInvalid)
	}
	if msOf(at) <= 0 {
		return fmt.Errorf("tip: %w: time must be after the Unix epoch", ErrInvalid)
	}
	return nil
}

func (tx *WriteTx) PutRefTip(repoID int64, name, target string, at time.Time) error {
	if err := checkTip(repoID, name, target, at); err != nil {
		return err
	}
	inner, err := tx.live()
	if err != nil {
		return err
	}
	if _, err = execAffected(tx.ctx, inner, sqlTipPut, repoID, name, target, msOf(at)); err != nil {
		return fmt.Errorf("PutRefTip: %w", mapErr(err))
	}
	return nil
}

func (tx *WriteTx) DeleteRefTip(repoID int64, name string) error {
	if repoID <= 0 || len(name) == 0 {
		return fmt.Errorf("DeleteRefTip: %w: repo id must be positive and name must not be empty", ErrInvalid)
	}
	inner, err := tx.live()
	if err != nil {
		return err
	}
	if _, err = execAffected(tx.ctx, inner, sqlTipDelete, repoID, name); err != nil {
		return fmt.Errorf("DeleteRefTip: %w", mapErr(err))
	}
	return nil
}

func (tx *WriteTx) ReplaceRefTips(repoID int64, tips map[string]string, at time.Time) error {
	if repoID <= 0 {
		return fmt.Errorf("ReplaceRefTips: %w: repo id must be positive", ErrInvalid)
	}
	if msOf(at) <= 0 {
		return fmt.Errorf("ReplaceRefTips: %w: time must be after the Unix epoch", ErrInvalid)
	}
	inner, err := tx.live()
	if err != nil {
		return err
	}
	if _, err = execAffected(tx.ctx, inner, sqlTipsClear, repoID); err != nil {
		return fmt.Errorf("ReplaceRefTips: %w", mapErr(err))
	}
	stamp := msOf(at)
	for name, target := range tips {
		if err = checkTip(repoID, name, target, at); err != nil {
			return err
		}
		if _, err = execAffected(tx.ctx, inner, sqlTipPut, repoID, name, target, stamp); err != nil {
			return fmt.Errorf("ReplaceRefTips: %w", mapErr(err))
		}
	}
	return nil
}

func (tx *ReadTx) RefTips(repoID int64) (map[string]string, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	return queryTips(tx.ctx, inner, repoID)
}

func (tx *WriteTx) WriteTips(repoID int64) (map[string]string, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	return queryTips(tx.ctx, inner, repoID)
}

func (tx *WriteTx) InsertSeen(repoID int64, seq uint64, name, news string, at time.Time) (bool, error) {
	if repoID <= 0 {
		return false, fmt.Errorf("InsertSeen: %w: repo id must be positive", ErrInvalid)
	}
	if seq > 9223372036854775807 {
		return false, fmt.Errorf("InsertSeen: %w: seq overflows int64", ErrInvalid)
	}
	if len(name) < 1 || len(name) > feedMaxRefName {
		return false, fmt.Errorf("InsertSeen: %w: name must be 1 to 512 bytes", ErrInvalid)
	}
	if len(news) > feedMaxTargetLen {
		return false, fmt.Errorf("InsertSeen: %w: new must be at most 256 bytes", ErrInvalid)
	}
	if msOf(at) <= 0 {
		return false, fmt.Errorf("InsertSeen: %w: time must be after the Unix epoch", ErrInvalid)
	}
	inner, err := tx.live()
	if err != nil {
		return false, err
	}
	moved, err := execAffected(tx.ctx, inner, sqlSeenInsert, repoID, int64(seq), name, news, msOf(at))
	if err != nil {
		return false, fmt.Errorf("InsertSeen: %w", mapErr(err))
	}
	return moved == 1, nil
}

func (tx *WriteTx) SweepSeen(repoID int64, through uint64) error {
	if repoID <= 0 {
		return fmt.Errorf("SweepSeen: %w: repo id must be positive", ErrInvalid)
	}
	if through > 9223372036854775807 {
		return fmt.Errorf("SweepSeen: %w: seq overflows int64", ErrInvalid)
	}
	inner, err := tx.live()
	if err != nil {
		return err
	}
	if _, err = execAffected(tx.ctx, inner, sqlSeenSweep, repoID, int64(through)); err != nil {
		return fmt.Errorf("SweepSeen: %w", mapErr(err))
	}
	return nil
}
