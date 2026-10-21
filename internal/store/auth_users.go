package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type AuthUser struct {
	Name              string
	Lower             string
	Email             string
	FullName          string
	PasswordAlgo      string
	PasswordHash      string
	LockedUntil       int64
	FailedLoginCount  int64
	ID                int64
	PasswordChangedAt int64
	CreatedAt         int64
	UpdatedAt         int64
	MustChange        bool
	TOTPEnabled       bool
	OIDCOnly          bool
	IsAdmin           bool
	IsActive          bool
}

const sqlAuthUserColumns = `id, name, lower_name, email, full_name, password_algo,
	must_change_password, totp_enabled, oidc_only, is_admin, is_active,
	failed_login_count, locked_until, password_changed_at, created_at, updated_at`

func scanAuthUser(row interface{ Scan(dest ...any) error }, u *AuthUser, hash *string) error {
	var mustChange, totp, oidc, admin, active int64
	var locked sql.NullInt64
	var columns []any
	columns = append(columns, &u.ID, &u.Name, &u.Lower, &u.Email, &u.FullName,
		&u.PasswordAlgo, &mustChange, &totp, &oidc, &admin, &active,
		&u.FailedLoginCount, &locked, &u.PasswordChangedAt, &u.CreatedAt, &u.UpdatedAt)
	if hash != nil {
		columns = append(columns, hash)
	}
	if err := row.Scan(columns...); err != nil {
		return err
	}
	u.MustChange = mustChange != 0
	u.TOTPEnabled = totp != 0
	u.OIDCOnly = oidc != 0
	u.IsAdmin = admin != 0
	u.IsActive = active != 0
	if locked.Valid {
		u.LockedUntil = locked.Int64
	}
	return nil
}

func (s *Store) GetAuthUserByName(ctx context.Context, lower string) (AuthUser, error) {
	var out AuthUser
	var hash string
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		serr := scanAuthUser(inner.QueryRowContext(tx.ctx,
			`SELECT `+sqlAuthUserColumns+`, password_hash FROM users WHERE lower_name = ?`, lower), &out, &hash)
		if errors.Is(serr, sql.ErrNoRows) {
			return fmt.Errorf("GetAuthUserByName: %w: %s", ErrNotFound, lower)
		}
		return serr
	})
	if err != nil {
		return AuthUser{}, err
	}
	out.PasswordHash = hash
	return out, nil
}

func (s *Store) GetAuthUserByID(ctx context.Context, id int64) (AuthUser, error) {
	var out AuthUser
	var hash string
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		serr := scanAuthUser(inner.QueryRowContext(tx.ctx,
			`SELECT `+sqlAuthUserColumns+`, password_hash FROM users WHERE id = ?`, id), &out, &hash)
		if errors.Is(serr, sql.ErrNoRows) {
			return fmt.Errorf("GetAuthUserByID: %w: %d", ErrNotFound, id)
		}
		return serr
	})
	if err != nil {
		return AuthUser{}, err
	}
	out.PasswordHash = hash
	return out, nil
}

func (s *Store) GetAuthUserByEmail(ctx context.Context, email string) (AuthUser, error) {
	var out AuthUser
	var hash string
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		serr := scanAuthUser(inner.QueryRowContext(tx.ctx,
			`SELECT `+sqlAuthUserColumns+`, password_hash FROM users WHERE email = ? LIMIT 1`, email), &out, &hash)
		if errors.Is(serr, sql.ErrNoRows) {
			return fmt.Errorf("GetAuthUserByEmail: %w", ErrNotFound)
		}
		return serr
	})
	if err != nil {
		return AuthUser{}, err
	}
	out.PasswordHash = hash
	return out, nil
}

type AuthUserCreate struct {
	Name       string
	Lower      string
	Email      string
	FullName   string
	Hash       string
	Algo       string
	Now        int64
	MustChange bool
	Admin      bool
}

func (s *Store) InsertAuthUser(ctx context.Context, in AuthUserCreate) (AuthUser, error) {
	var out AuthUser
	err := s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		var id int64
		serr := inner.QueryRowContext(tx.ctx, `INSERT INTO users
			(name, lower_name, email, full_name, password_hash, password_algo, password_changed_at, must_change_password, is_admin, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			RETURNING id`,
			in.Name, in.Lower, in.Email, in.FullName, in.Hash, in.Algo, in.Now,
			boolToInt64(in.MustChange), boolToInt64(in.Admin), in.Now, in.Now).Scan(&id)
		if serr != nil {
			return fmt.Errorf("InsertAuthUser: %w", mapErr(serr))
		}
		out = AuthUser{ID: id, Name: in.Name, Lower: in.Lower, Email: in.Email,
			FullName: in.FullName, PasswordAlgo: in.Algo, PasswordHash: in.Hash,
			MustChange: in.MustChange, IsAdmin: in.Admin, IsActive: true,
			PasswordChangedAt: in.Now, CreatedAt: in.Now, UpdatedAt: in.Now}
		return nil
	})
	if err != nil {
		return AuthUser{}, err
	}
	return out, nil
}

func (s *Store) BootstrapAuthAdmin(ctx context.Context, name, lower, hash, algo string, now int64) (AuthUser, error) {
	var out AuthUser
	err := s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		res, ierr := inner.ExecContext(tx.ctx, `INSERT INTO users
			(name, lower_name, email, full_name, password_hash, password_algo, password_changed_at, is_admin, created_at, updated_at)
			SELECT ?, ?, '', '', ?, ?, ?, 1, ?, ?
			WHERE NOT EXISTS (SELECT 1 FROM users WHERE is_admin != 0)`,
			name, lower, hash, algo, now, now, now)
		if ierr != nil {
			return fmt.Errorf("BootstrapAuthAdmin: %w", mapErr(ierr))
		}
		affected, aerr := res.RowsAffected()
		if aerr != nil {
			return fmt.Errorf("BootstrapAuthAdmin: %w", aerr)
		}
		if affected == 0 {
			var dup int64
			if qerr := inner.QueryRowContext(tx.ctx,
				`SELECT id FROM users WHERE lower_name = ?`, lower).Scan(&dup); qerr == nil {
				return fmt.Errorf("BootstrapAuthAdmin: %w: %s", ErrConflict, lower)
			}
			return fmt.Errorf("BootstrapAuthAdmin: %w", ErrAdminExists)
		}
		id, ierr := res.LastInsertId()
		if ierr != nil {
			return fmt.Errorf("BootstrapAuthAdmin: %w", ierr)
		}
		out = AuthUser{ID: id, Name: name, Lower: lower, PasswordAlgo: algo,
			PasswordHash: hash, IsAdmin: true, IsActive: true,
			PasswordChangedAt: now, CreatedAt: now, UpdatedAt: now}
		return nil
	})
	if err != nil {
		return AuthUser{}, err
	}
	return out, nil
}

func (s *Store) RecordAuthLoginSuccess(ctx context.Context, userID int64, lower, ip string, now int64, newHash string) error {
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if _, uerr := inner.ExecContext(tx.ctx, `UPDATE users SET failed_login_count = 0, locked_until = NULL,
			updated_at = ? WHERE id = ?`, now, userID); uerr != nil {
			return fmt.Errorf("RecordAuthLoginSuccess: %w", mapErr(uerr))
		}
		if _, derr := inner.ExecContext(tx.ctx,
			`DELETE FROM login_attempts WHERE username_lower = ? AND success = 0`, lower); derr != nil {
			return fmt.Errorf("RecordAuthLoginSuccess: %w", derr)
		}
		if _, ierr := inner.ExecContext(tx.ctx, `INSERT INTO login_attempts (username_lower, ip, at, success, reason)
			VALUES (?, ?, ?, 1, 'password')`, lower, ip, now); ierr != nil {
			return fmt.Errorf("RecordAuthLoginSuccess: %w", mapErr(ierr))
		}
		if newHash != "" {
			if _, herr := inner.ExecContext(tx.ctx, `UPDATE users SET password_hash = ?, password_changed_at = ? WHERE id = ?`,
				newHash, now, userID); herr != nil {
				return fmt.Errorf("RecordAuthLoginSuccess: %w", mapErr(herr))
			}
		}
		return nil
	})
}

type AuthLockoutPolicy struct {
	Threshold    int
	WindowMillis int64
	BaseMillis   int64
	MaxMillis    int64
	RetainMillis int64
}

type AuthFailureRecord struct {
	Lower       string
	IP          string
	Reason      string
	Policy      AuthLockoutPolicy
	UserID      int64
	Now         int64
	LockedUntil int64
}

func (s *Store) RecordAuthLoginFailure(ctx context.Context, r AuthFailureRecord) (bool, error) {
	escalated := false
	err := s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if _, ierr := inner.ExecContext(tx.ctx, `INSERT INTO login_attempts (username_lower, ip, at, success, reason)
			VALUES (?, ?, ?, 0, ?)`, r.Lower, r.IP, r.Now, r.Reason); ierr != nil {
			return fmt.Errorf("RecordAuthLoginFailure: %w", mapErr(ierr))
		}
		if _, derr := inner.ExecContext(tx.ctx, `DELETE FROM login_attempts WHERE at < ?`,
			r.Now-r.Policy.RetainMillis); derr != nil {
			return fmt.Errorf("RecordAuthLoginFailure: %w", derr)
		}
		var recent int
		if cerr := inner.QueryRowContext(tx.ctx, `SELECT COUNT(*) FROM login_attempts
			WHERE username_lower = ? AND at >= ? AND success = 0`, r.Lower, r.Now-r.Policy.WindowMillis).Scan(&recent); cerr != nil {
			return fmt.Errorf("RecordAuthLoginFailure: %w", cerr)
		}
		if r.LockedUntil > r.Now {
			remaining := r.LockedUntil - r.Now
			doubled := remaining * 2
			if doubled > r.Policy.MaxMillis {
				doubled = r.Policy.MaxMillis
			}
			if _, uerr := inner.ExecContext(tx.ctx, `UPDATE users SET locked_until = ?, updated_at = ? WHERE id = ?`,
				r.Now+doubled, r.Now, r.UserID); uerr != nil {
				return fmt.Errorf("RecordAuthLoginFailure: %w", mapErr(uerr))
			}
			return killPendingAuthSessions(tx.ctx, inner, r.UserID)
		}
		if recent >= r.Policy.Threshold {
			if _, uerr := inner.ExecContext(tx.ctx, `UPDATE users SET failed_login_count = ?, locked_until = ?, updated_at = ? WHERE id = ?`,
				recent, r.Now+r.Policy.BaseMillis, r.Now, r.UserID); uerr != nil {
				return fmt.Errorf("RecordAuthLoginFailure: %w", mapErr(uerr))
			}
			if kerr := killPendingAuthSessions(tx.ctx, inner, r.UserID); kerr != nil {
				return kerr
			}
			escalated = true
			return nil
		}
		_, uerr := inner.ExecContext(tx.ctx, `UPDATE users SET failed_login_count = ?, updated_at = ? WHERE id = ?`, recent, r.Now, r.UserID)
		if uerr != nil {
			return fmt.Errorf("RecordAuthLoginFailure: %w", mapErr(uerr))
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return escalated, nil
}

func killPendingAuthSessions(ctx context.Context, tx *sql.Tx, userID int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?
		AND auth_level IN ('password-pending', 'oidc-pending')`, userID)
	if err != nil {
		return fmt.Errorf("RecordAuthLoginFailure: %w", err)
	}
	return nil
}

func (s *Store) ApplyPasswordChange(ctx context.Context, userID int64, newHash string, now int64) error {
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if _, uerr := inner.ExecContext(tx.ctx, `UPDATE users SET password_hash = ?, password_changed_at = ?,
			must_change_password = 0, failed_login_count = 0, locked_until = NULL, updated_at = ? WHERE id = ?`,
			newHash, now, now, userID); uerr != nil {
			return fmt.Errorf("ApplyPasswordChange: %w", mapErr(uerr))
		}
		_, derr := inner.ExecContext(tx.ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
		if derr != nil {
			return fmt.Errorf("ApplyPasswordChange: %w", derr)
		}
		return nil
	})
}

func (s *Store) ApplyPasswordReset(ctx context.Context, userID int64, newHash string, mustChange bool, now int64) error {
	return s.applyPasswordReset(ctx, userID, newHash, mustChange, now, false)
}

func (s *Store) ApplyPasswordResetRevokingTokens(ctx context.Context, userID int64, newHash string, mustChange bool, now int64) error {
	return s.applyPasswordReset(ctx, userID, newHash, mustChange, now, true)
}

func (s *Store) applyPasswordReset(ctx context.Context, userID int64, newHash string, mustChange bool, now int64, revokeTokens bool) error {
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if _, uerr := inner.ExecContext(tx.ctx, `UPDATE users SET password_hash = ?, password_changed_at = ?,
			must_change_password = ?, failed_login_count = 0, locked_until = NULL, updated_at = ? WHERE id = ?`,
			newHash, now, boolToInt64(mustChange), now, userID); uerr != nil {
			return fmt.Errorf("ApplyPasswordReset: %w", mapErr(uerr))
		}
		if _, derr := inner.ExecContext(tx.ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); derr != nil {
			return fmt.Errorf("ApplyPasswordReset: %w", derr)
		}
		if revokeTokens {
			_, terr := inner.ExecContext(tx.ctx, `UPDATE scoped_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, now, userID)
			if terr != nil {
				return fmt.Errorf("ApplyPasswordReset: %w", mapErr(terr))
			}
		}
		return nil
	})
}

func (s *Store) ClearAuthLockout(ctx context.Context, userID int64, now int64) error {
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		_, uerr := inner.ExecContext(tx.ctx, `UPDATE users SET failed_login_count = 0, locked_until = NULL, updated_at = ? WHERE id = ?`, now, userID)
		if uerr != nil {
			return fmt.Errorf("ClearAuthLockout: %w", mapErr(uerr))
		}
		return nil
	})
}

func (s *Store) UpdateAuthUserAdmin(ctx context.Context, userID int64, fullName, email string, active bool, now int64) error {
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		_, uerr := inner.ExecContext(tx.ctx, `UPDATE users SET full_name = ?, email = ?, is_active = ?, updated_at = ? WHERE id = ?`,
			fullName, email, boolToInt64(active), now, userID)
		if uerr != nil {
			return fmt.Errorf("UpdateAuthUserAdmin: %w", mapErr(uerr))
		}
		return nil
	})
}

func (s *Store) UpdateAuthUserSelf(ctx context.Context, userID int64, fullName, email string, now int64) error {
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		_, uerr := inner.ExecContext(tx.ctx, `UPDATE users SET full_name = ?, email = ?, updated_at = ? WHERE id = ?`,
			fullName, email, now, userID)
		if uerr != nil {
			return fmt.Errorf("UpdateAuthUserSelf: %w", mapErr(uerr))
		}
		return nil
	})
}

func (s *Store) DeleteAuthUser(ctx context.Context, userID int64) error {
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		var sole int64
		if qerr := inner.QueryRowContext(tx.ctx, `SELECT COUNT(*) FROM team_members m
			JOIN teams t ON t.id = m.team_id
			WHERE t.lower_name = 'owners' AND m.user_id = ?
			AND (SELECT COUNT(*) FROM team_members m2 WHERE m2.team_id = t.id) <= 1`, userID).Scan(&sole); qerr != nil {
			return fmt.Errorf("DeleteAuthUser: %w", qerr)
		}
		if sole > 0 {
			return fmt.Errorf("DeleteAuthUser: %w", ErrSoleOwner)
		}
		if _, derr := inner.ExecContext(tx.ctx, `DELETE FROM users WHERE id = ?`, userID); derr != nil {
			return fmt.Errorf("DeleteAuthUser: %w", mapErr(derr))
		}
		return nil
	})
}

func (s *Store) ListAuthUsers(ctx context.Context, after string, limit int) ([]AuthUser, error) {
	var out []AuthUser
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		rows, qerr := inner.QueryContext(tx.ctx,
			`SELECT `+sqlAuthUserColumns+` FROM users WHERE name > ? ORDER BY name LIMIT ?`, after, limit)
		if qerr != nil {
			return fmt.Errorf("ListAuthUsers: %w", qerr)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var u AuthUser
			if serr := scanAuthUser(rows, &u, nil); serr != nil {
				return fmt.Errorf("ListAuthUsers: %w", serr)
			}
			out = append(out, u)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) CountActiveAuthAdmins(ctx context.Context) (int64, error) {
	var n int64
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if serr := inner.QueryRowContext(tx.ctx,
			`SELECT COUNT(*) FROM users WHERE is_admin != 0 AND is_active != 0`).Scan(&n); serr != nil {
			return fmt.Errorf("CountActiveAuthAdmins: %w", serr)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Store) AuthUserUpdatedAt(ctx context.Context, userID int64) (int64, error) {
	var updated int64
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if serr := inner.QueryRowContext(tx.ctx,
			`SELECT updated_at FROM users WHERE id = ?`, userID).Scan(&updated); serr != nil {
			if errors.Is(serr, sql.ErrNoRows) {
				return fmt.Errorf("AuthUserUpdatedAt: %w: %d", ErrNotFound, userID)
			}
			return fmt.Errorf("AuthUserUpdatedAt: %w", serr)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return updated, nil
}

type AuditRecord struct {
	Action     string
	TargetType string
	TargetID   string
	IP         string
	UserAgent  string
	Prefix     string
	Result     string
	At         int64
	Actor      int64
}

func (s *Store) RecordAudit(ctx context.Context, r AuditRecord) error {
	var actorArg any
	if r.Actor != 0 {
		actorArg = r.Actor
	}
	var prefixArg any
	if len(r.Prefix) >= 12 {
		prefixArg = r.Prefix[:12]
	}
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		_, ierr := inner.ExecContext(tx.ctx, `INSERT INTO audit_events
			(at, actor_user_id, action, target_type, target_id, ip, user_agent, session_hash_prefix, result, details)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.At, actorArg, r.Action, r.TargetType, r.TargetID, r.IP, r.UserAgent, prefixArg, r.Result, "{}")
		if ierr != nil {
			return fmt.Errorf("RecordAudit: %w", mapErr(ierr))
		}
		return nil
	})
}

func (s *Store) CountAuditEvents(ctx context.Context, action, targetType, targetID string) (int64, error) {
	var n int64
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if serr := inner.QueryRowContext(tx.ctx, `SELECT COUNT(*) FROM audit_events
			WHERE action = ? AND target_type = ? AND target_id = ?`, action, targetType, targetID).Scan(&n); serr != nil {
			return fmt.Errorf("CountAuditEvents: %w", serr)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Store) RecordLoginAttempt(ctx context.Context, lower, ip string, at int64, success bool, reason string) error {
	flag := 0
	if success {
		flag = 1
	}
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		_, ierr := inner.ExecContext(tx.ctx, `INSERT INTO login_attempts (username_lower, ip, at, success, reason)
			VALUES (?, ?, ?, ?, ?)`, lower, ip, at, flag, reason)
		if ierr != nil {
			return fmt.Errorf("RecordLoginAttempt: %w", mapErr(ierr))
		}
		return nil
	})
}

func boolToInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
