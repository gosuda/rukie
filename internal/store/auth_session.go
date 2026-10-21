package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type SessionRecord struct {
	IDHash            string
	AuthLevel         string
	UserID            int64
	AuthedAt          int64
	CreatedAt         int64
	LastSeenAt        int64
	AbsoluteExpiresAt int64
	RowID             int64
	TOTPOk            bool
}

const sqlSessionColumns = `rowid, id_hash, user_id, auth_level, authed_at, totp_ok,
	created_at, last_seen_at, absolute_expires_at`

func scanSessionRecord(row interface{ Scan(dest ...any) error }, out *SessionRecord) error {
	var totp int64
	if err := row.Scan(&out.RowID, &out.IDHash, &out.UserID, &out.AuthLevel, &out.AuthedAt, &totp,
		&out.CreatedAt, &out.LastSeenAt, &out.AbsoluteExpiresAt); err != nil {
		return err
	}
	out.TOTPOk = totp != 0
	return nil
}

type SessionCreate struct {
	IDHash            string
	AuthLevel         string
	CSRF              string
	IP                string
	UserAgent         string
	UserID            int64
	AuthedAt          int64
	CreatedAt         int64
	LastSeenAt        int64
	AbsoluteExpiresAt int64
	TOTPOk            bool
}

func (s *Store) CreateSession(ctx context.Context, in SessionCreate) error {
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if _, derr := inner.ExecContext(tx.ctx,
			`DELETE FROM sessions WHERE user_id = ? AND user_agent = ?`, in.UserID, in.UserAgent); derr != nil {
			return fmt.Errorf("CreateSession: %w", derr)
		}
		totp := boolToInt64(in.TOTPOk)
		_, ierr := inner.ExecContext(tx.ctx, `INSERT INTO sessions
			(id_hash, user_id, csrf_token, auth_level, authed_at, totp_ok, created_at, last_seen_at, absolute_expires_at, created_ip, user_agent)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			in.IDHash, in.UserID, in.CSRF, in.AuthLevel, in.AuthedAt, totp,
			in.CreatedAt, in.LastSeenAt, in.AbsoluteExpiresAt, in.IP, in.UserAgent)
		if ierr != nil {
			return fmt.Errorf("CreateSession: %w", mapErr(ierr))
		}
		return nil
	})
}

func (s *Store) GetSessionByHash(ctx context.Context, hash string) (SessionRecord, error) {
	var out SessionRecord
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if serr := scanSessionRecord(inner.QueryRowContext(tx.ctx,
			`SELECT `+sqlSessionColumns+` FROM sessions WHERE id_hash = ?`, hash), &out); serr != nil {
			if errors.Is(serr, sql.ErrNoRows) {
				return fmt.Errorf("GetSessionByHash: %w", ErrNotFound)
			}
			return fmt.Errorf("GetSessionByHash: %w", serr)
		}
		return nil
	})
	if err != nil {
		return SessionRecord{}, err
	}
	return out, nil
}

func (s *Store) GetSessionByID(ctx context.Context, rowid int64) (SessionRecord, error) {
	var out SessionRecord
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if serr := scanSessionRecord(inner.QueryRowContext(tx.ctx,
			`SELECT `+sqlSessionColumns+` FROM sessions WHERE rowid = ?`, rowid), &out); serr != nil {
			if errors.Is(serr, sql.ErrNoRows) {
				return fmt.Errorf("GetSessionByID: %w: %d", ErrNotFound, rowid)
			}
			return fmt.Errorf("GetSessionByID: %w", serr)
		}
		return nil
	})
	if err != nil {
		return SessionRecord{}, err
	}
	return out, nil
}

func (s *Store) DeleteSessionByHash(ctx context.Context, hash string) (int64, error) {
	var userID int64
	err := s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if serr := inner.QueryRowContext(tx.ctx,
			`SELECT user_id FROM sessions WHERE id_hash = ?`, hash).Scan(&userID); serr != nil {
			if !errors.Is(serr, sql.ErrNoRows) {
				return fmt.Errorf("DeleteSessionByHash: %w", serr)
			}
		}
		if _, derr := inner.ExecContext(tx.ctx, `DELETE FROM sessions WHERE id_hash = ?`, hash); derr != nil {
			return fmt.Errorf("DeleteSessionByHash: %w", derr)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return userID, nil
}

func (s *Store) DeleteSessionByRowID(ctx context.Context, userID, rowid int64) (string, error) {
	var hash string
	err := s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if serr := inner.QueryRowContext(tx.ctx,
			`SELECT id_hash FROM sessions WHERE rowid = ? AND user_id = ?`, rowid, userID).Scan(&hash); serr != nil {
			if errors.Is(serr, sql.ErrNoRows) {
				return fmt.Errorf("DeleteSessionByRowID: %w", ErrNotFound)
			}
			return fmt.Errorf("DeleteSessionByRowID: %w", serr)
		}
		if _, derr := inner.ExecContext(tx.ctx, `DELETE FROM sessions WHERE rowid = ?`, rowid); derr != nil {
			return fmt.Errorf("DeleteSessionByRowID: %w", derr)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hash, nil
}

func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if _, derr := inner.ExecContext(tx.ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); derr != nil {
			return fmt.Errorf("DeleteUserSessions: %w", derr)
		}
		return nil
	})
}

func (s *Store) ListSessionsByUser(ctx context.Context, userID int64) ([]SessionRecord, error) {
	var out []SessionRecord
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		rows, qerr := inner.QueryContext(tx.ctx, `SELECT `+sqlSessionColumns+` FROM sessions
			WHERE user_id = ? ORDER BY last_seen_at DESC`, userID)
		if qerr != nil {
			return fmt.Errorf("ListSessionsByUser: %w", qerr)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var rec SessionRecord
			if serr := scanSessionRecord(rows, &rec); serr != nil {
				return fmt.Errorf("ListSessionsByUser: %w", serr)
			}
			out = append(out, rec)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) ListSessionHashesByUser(ctx context.Context, userID int64) ([]string, error) {
	var out []string
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		rows, qerr := inner.QueryContext(tx.ctx, `SELECT id_hash FROM sessions WHERE user_id = ?`, userID)
		if qerr != nil {
			return fmt.Errorf("ListSessionHashesByUser: %w", qerr)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var h string
			if serr := rows.Scan(&h); serr != nil {
				return fmt.Errorf("ListSessionHashesByUser: %w", serr)
			}
			out = append(out, h)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) TouchSession(ctx context.Context, hash string, now int64) error {
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if _, uerr := inner.ExecContext(tx.ctx,
			`UPDATE sessions SET last_seen_at = ? WHERE id_hash = ?`, now, hash); uerr != nil {
			return fmt.Errorf("TouchSession: %w", mapErr(uerr))
		}
		return nil
	})
}

func (s *Store) RotateSession(ctx context.Context, oldHash string, next SessionCreate) error {
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if _, derr := inner.ExecContext(tx.ctx, `DELETE FROM sessions WHERE id_hash = ?`, oldHash); derr != nil {
			return fmt.Errorf("RotateSession: %w", derr)
		}
		totp := boolToInt64(next.TOTPOk)
		_, ierr := inner.ExecContext(tx.ctx, `INSERT INTO sessions
			(id_hash, user_id, csrf_token, auth_level, authed_at, totp_ok, created_at, last_seen_at, absolute_expires_at, created_ip, user_agent)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			next.IDHash, next.UserID, next.CSRF, next.AuthLevel, next.AuthedAt, totp,
			next.CreatedAt, next.LastSeenAt, next.AbsoluteExpiresAt, next.IP, next.UserAgent)
		if ierr != nil {
			return fmt.Errorf("RotateSession: %w", mapErr(ierr))
		}
		return nil
	})
}
