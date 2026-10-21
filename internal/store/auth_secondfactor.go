package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type TOTPCredentials struct {
	Secret      string
	Pending     string
	LastCounter int64
	VerifiedAt  int64
	HasPending  bool
	HasVerified bool
}

func (s *Store) GetTOTPCredentials(ctx context.Context, userID int64) (TOTPCredentials, error) {
	var out TOTPCredentials
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		var pending sql.NullString
		var verified sql.NullInt64
		if serr := inner.QueryRowContext(tx.ctx, `SELECT secret_sealed, pending_secret_sealed, last_counter, verified_at
			FROM user_totp WHERE user_id = ?`, userID).Scan(
			&out.Secret, &pending, &out.LastCounter, &verified); serr != nil {
			if errors.Is(serr, sql.ErrNoRows) {
				return fmt.Errorf("GetTOTPCredentials: %w: %d", ErrNotFound, userID)
			}
			return fmt.Errorf("GetTOTPCredentials: %w", serr)
		}
		if pending.Valid && pending.String != "" {
			out.Pending = pending.String
			out.HasPending = true
		}
		if verified.Valid {
			out.VerifiedAt = verified.Int64
			out.HasVerified = true
		}
		return nil
	})
	if err != nil {
		return TOTPCredentials{}, err
	}
	return out, nil
}

func (s *Store) StageTOTPSecret(ctx context.Context, userID int64, sealed string, now int64) error {
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		var verified sql.NullInt64
		verr := inner.QueryRowContext(tx.ctx,
			`SELECT verified_at FROM user_totp WHERE user_id = ?`, userID).Scan(&verified)
		if verr != nil {
			if !errors.Is(verr, sql.ErrNoRows) {
				return fmt.Errorf("StageTOTPSecret: %w", verr)
			}
			_, verr = inner.ExecContext(tx.ctx, `INSERT INTO user_totp
				(user_id, secret_sealed, last_counter, created_at, verified_at)
				VALUES (?, ?, -1, ?, NULL)`, userID, sealed, now)
			if verr != nil {
				return fmt.Errorf("StageTOTPSecret: %w", mapErr(verr))
			}
			return nil
		}
		if !verified.Valid {
			_, verr = inner.ExecContext(tx.ctx, `UPDATE user_totp SET secret_sealed = ?, last_counter = -1,
				created_at = ? WHERE user_id = ?`, sealed, now, userID)
		} else {
			_, verr = inner.ExecContext(tx.ctx, `UPDATE user_totp SET pending_secret_sealed = ? WHERE user_id = ?`, sealed, userID)
		}
		if verr != nil {
			return fmt.Errorf("StageTOTPSecret: %w", mapErr(verr))
		}
		return nil
	})
}

func (s *Store) insertRecoveryCodes(txCtx context.Context, inner *sql.Tx, userID, now int64, digests []string) error {
	for _, digest := range digests {
		_, err := inner.ExecContext(txCtx, `INSERT INTO recovery_codes (user_id, code_sha256, created_at)
			VALUES (?, ?, ?)`, userID, digest, now)
		if err != nil {
			return fmt.Errorf("recovery codes: %w", mapErr(err))
		}
	}
	return nil
}

func (s *Store) insertTOTPAttempt(txCtx context.Context, inner *sql.Tx, lower, ip string, now int64) error {
	_, err := inner.ExecContext(txCtx, `INSERT INTO login_attempts (username_lower, ip, at, success, reason)
		VALUES (?, ?, ?, 1, 'totp')`, lower, ip, now)
	if err != nil {
		return fmt.Errorf("totp attempt: %w", mapErr(err))
	}
	return nil
}

func (s *Store) VerifyRecoveryCode(ctx context.Context, userID int64, digest string, now int64, lower, ip string) (bool, error) {
	applied := false
	err := s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		res, rerr := inner.ExecContext(tx.ctx, `UPDATE recovery_codes SET used_at = ?
			WHERE user_id = ? AND code_sha256 = ? AND used_at IS NULL`,
			now, userID, digest)
		if rerr != nil {
			return fmt.Errorf("VerifyRecoveryCode: %w", mapErr(rerr))
		}
		affected, aerr := res.RowsAffected()
		if aerr != nil {
			return fmt.Errorf("VerifyRecoveryCode: %w", aerr)
		}
		if affected != 1 {
			return nil
		}
		applied = true
		return s.insertTOTPAttempt(tx.ctx, inner, lower, ip, now)
	})
	if err != nil {
		return false, err
	}
	return applied, nil
}

func (s *Store) PromoteTOTPSecret(ctx context.Context, userID int64, counter, now int64, lower, ip string, digests []string) (bool, error) {
	applied := false
	err := s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		var pending sql.NullString
		if qerr := inner.QueryRowContext(tx.ctx, `SELECT pending_secret_sealed FROM user_totp WHERE user_id = ?`,
			userID).Scan(&pending); qerr != nil {
			if errors.Is(qerr, sql.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("PromoteTOTPSecret: %w", qerr)
		} else if !pending.Valid || pending.String == "" {
			return nil
		}
		pres, perr := inner.ExecContext(tx.ctx, `UPDATE user_totp SET secret_sealed = ?, pending_secret_sealed = NULL,
			last_counter = ?, verified_at = ? WHERE user_id = ? AND last_counter < ?`,
			pending.String, counter, now, userID, counter)
		if perr != nil {
			return fmt.Errorf("PromoteTOTPSecret: %w", mapErr(perr))
		}
		affected, aerr := pres.RowsAffected()
		if aerr != nil {
			return fmt.Errorf("PromoteTOTPSecret: %w", aerr)
		}
		if affected != 1 {
			return nil
		}
		if _, uerr := inner.ExecContext(tx.ctx, `UPDATE users SET totp_enabled = 1, updated_at = ? WHERE id = ?`,
			now, userID); uerr != nil {
			return fmt.Errorf("PromoteTOTPSecret: %w", mapErr(uerr))
		}
		if cerr := s.insertRecoveryCodes(tx.ctx, inner, userID, now, digests); cerr != nil {
			return fmt.Errorf("PromoteTOTPSecret: %w", cerr)
		}
		applied = true
		return s.insertTOTPAttempt(tx.ctx, inner, lower, ip, now)
	})
	if err != nil {
		return false, err
	}
	return applied, nil
}

func (s *Store) ConfirmTOTPSecret(ctx context.Context, userID int64, counter, now int64, lower, ip string, digests []string) (bool, error) {
	applied := false
	err := s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		cres, cerr := inner.ExecContext(tx.ctx, `UPDATE user_totp SET last_counter = ? WHERE user_id = ? AND last_counter < ?`,
			counter, userID, counter)
		if cerr != nil {
			return fmt.Errorf("ConfirmTOTPSecret: %w", mapErr(cerr))
		}
		affected, aerr := cres.RowsAffected()
		if aerr != nil {
			return fmt.Errorf("ConfirmTOTPSecret: %w", aerr)
		}
		if affected != 1 {
			return nil
		}
		if _, verr := inner.ExecContext(tx.ctx, `UPDATE user_totp SET verified_at = ? WHERE user_id = ?`,
			now, userID); verr != nil {
			return fmt.Errorf("ConfirmTOTPSecret: %w", mapErr(verr))
		}
		if _, uerr := inner.ExecContext(tx.ctx, `UPDATE users SET totp_enabled = 1, updated_at = ? WHERE id = ?`,
			now, userID); uerr != nil {
			return fmt.Errorf("ConfirmTOTPSecret: %w", mapErr(uerr))
		}
		if cerr := s.insertRecoveryCodes(tx.ctx, inner, userID, now, digests); cerr != nil {
			return fmt.Errorf("ConfirmTOTPSecret: %w", cerr)
		}
		applied = true
		return s.insertTOTPAttempt(tx.ctx, inner, lower, ip, now)
	})
	if err != nil {
		return false, err
	}
	return applied, nil
}

func (s *Store) AdvanceTOTPCounter(ctx context.Context, userID int64, counter, now int64, lower, ip string) (bool, error) {
	applied := false
	err := s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		cres, cerr := inner.ExecContext(tx.ctx, `UPDATE user_totp SET last_counter = ? WHERE user_id = ? AND last_counter < ?`,
			counter, userID, counter)
		if cerr != nil {
			return fmt.Errorf("AdvanceTOTPCounter: %w", mapErr(cerr))
		}
		affected, aerr := cres.RowsAffected()
		if aerr != nil {
			return fmt.Errorf("AdvanceTOTPCounter: %w", aerr)
		}
		if affected != 1 {
			return nil
		}
		applied = true
		return s.insertTOTPAttempt(tx.ctx, inner, lower, ip, now)
	})
	if err != nil {
		return false, err
	}
	return applied, nil
}

func (s *Store) ClearTOTP(ctx context.Context, userID int64, now int64) error {
	return s.clearTOTP(ctx, userID, now, false)
}

func (s *Store) ClearTOTPRevokingSessions(ctx context.Context, userID int64, now int64) error {
	return s.clearTOTP(ctx, userID, now, true)
}

func (s *Store) clearTOTP(ctx context.Context, userID int64, now int64, revokeSessions bool) error {
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if _, derr := inner.ExecContext(tx.ctx, `DELETE FROM user_totp WHERE user_id = ?`, userID); derr != nil {
			return fmt.Errorf("ClearTOTP: %w", derr)
		}
		if _, derr := inner.ExecContext(tx.ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID); derr != nil {
			return fmt.Errorf("ClearTOTP: %w", derr)
		}
		if _, uerr := inner.ExecContext(tx.ctx, `UPDATE users SET totp_enabled = 0, updated_at = ? WHERE id = ?`,
			now, userID); uerr != nil {
			return fmt.Errorf("ClearTOTP: %w", mapErr(uerr))
		}
		if revokeSessions {
			_, serr := inner.ExecContext(tx.ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
			if serr != nil {
				return fmt.Errorf("ClearTOTP: %w", serr)
			}
		}
		return nil
	})
}

func (s *Store) ReplaceRecoveryCodes(ctx context.Context, userID int64, now int64, digests []string) error {
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if _, derr := inner.ExecContext(tx.ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID); derr != nil {
			return fmt.Errorf("ReplaceRecoveryCodes: %w", derr)
		}
		if cerr := s.insertRecoveryCodes(tx.ctx, inner, userID, now, digests); cerr != nil {
			return fmt.Errorf("ReplaceRecoveryCodes: %w", cerr)
		}
		return nil
	})
}

func (s *Store) CountRecentTOTPFailures(ctx context.Context, lower string, sinceMillis int64) (int64, error) {
	var n int64
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if serr := inner.QueryRowContext(tx.ctx, `SELECT COUNT(*) FROM login_attempts
			WHERE username_lower = ? AND reason = 'totp' AND success = 0 AND at >= ?`,
			lower, sinceMillis).Scan(&n); serr != nil {
			return fmt.Errorf("CountRecentTOTPFailures: %w", serr)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}
