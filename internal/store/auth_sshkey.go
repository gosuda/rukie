package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type SSHKeyRecord struct {
	Fingerprint string
	KeyType     string
	PublicKey   string
	Comment     string
	LastUsedAt  int64
	ID          int64
	UserID      int64
	CreatedAt   int64
}

const sqlSSHKeyColumns = `id, user_id, fingerprint, key_type,
	public_key, comment, created_at, last_used_at`

func scanSSHKeyRecord(row interface{ Scan(dest ...any) error }, out *SSHKeyRecord) error {
	var last sql.NullInt64
	if err := row.Scan(&out.ID, &out.UserID, &out.Fingerprint, &out.KeyType,
		&out.PublicKey, &out.Comment, &out.CreatedAt, &last); err != nil {
		return err
	}
	if last.Valid {
		out.LastUsedAt = last.Int64
	}
	return nil
}

func (s *Store) SSHKeyFingerprintTaken(ctx context.Context, fingerprint string) (bool, error) {
	var taken bool
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		var count int64
		if serr := inner.QueryRowContext(tx.ctx,
			`SELECT COUNT(*) FROM ssh_keys WHERE fingerprint = ?`, fingerprint).Scan(&count); serr != nil {
			return fmt.Errorf("SSHKeyFingerprintTaken: %w", serr)
		}
		taken = count > 0
		return nil
	})
	if err != nil {
		return false, err
	}
	return taken, nil
}

func (s *Store) InsertSSHKey(ctx context.Context, in SSHKeyRecord) (int64, error) {
	var id int64
	err := s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		res, ierr := inner.ExecContext(tx.ctx, `INSERT INTO ssh_keys
			(user_id, fingerprint, key_type, public_key, comment, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			in.UserID, in.Fingerprint, in.KeyType, in.PublicKey, in.Comment, in.CreatedAt)
		if ierr != nil {
			return fmt.Errorf("InsertSSHKey: %w", mapErr(ierr))
		}
		inserted, derr := res.LastInsertId()
		if derr != nil {
			return fmt.Errorf("InsertSSHKey: %w", derr)
		}
		id = inserted
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) ListSSHKeysByUser(ctx context.Context, userID int64) ([]SSHKeyRecord, error) {
	var out []SSHKeyRecord
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		rows, qerr := inner.QueryContext(tx.ctx, `SELECT `+sqlSSHKeyColumns+`
			FROM ssh_keys WHERE user_id = ? ORDER BY created_at DESC, id DESC`, userID)
		if qerr != nil {
			return fmt.Errorf("ListSSHKeysByUser: %w", qerr)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var rec SSHKeyRecord
			if serr := scanSSHKeyRecord(rows, &rec); serr != nil {
				return fmt.Errorf("ListSSHKeysByUser: %w", serr)
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

func (s *Store) DeleteSSHKey(ctx context.Context, id, userID int64) (bool, error) {
	deleted := false
	err := s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		res, derr := inner.ExecContext(tx.ctx, `DELETE FROM ssh_keys WHERE id = ? AND user_id = ?`, id, userID)
		if derr != nil {
			return fmt.Errorf("DeleteSSHKey: %w", derr)
		}
		n, aerr := res.RowsAffected()
		if aerr != nil {
			return fmt.Errorf("DeleteSSHKey: %w", aerr)
		}
		deleted = n != 0
		return nil
	})
	if err != nil {
		return false, err
	}
	return deleted, nil
}

func (s *Store) GetSSHKeyByFingerprint(ctx context.Context, fingerprint string) (SSHKeyRecord, error) {
	var out SSHKeyRecord
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if serr := scanSSHKeyRecord(inner.QueryRowContext(tx.ctx, `SELECT `+sqlSSHKeyColumns+`
			FROM ssh_keys WHERE fingerprint = ?`, fingerprint), &out); serr != nil {
			if errors.Is(serr, sql.ErrNoRows) {
				return fmt.Errorf("GetSSHKeyByFingerprint: %w", ErrNotFound)
			}
			return fmt.Errorf("GetSSHKeyByFingerprint: %w", serr)
		}
		return nil
	})
	if err != nil {
		return SSHKeyRecord{}, err
	}
	return out, nil
}

func (s *Store) TouchSSHKey(ctx context.Context, id, nowMillis, thresholdMillis int64) error {
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		_, uerr := inner.ExecContext(tx.ctx, `UPDATE ssh_keys SET last_used_at = ?
			WHERE id = ? AND (last_used_at IS NULL OR last_used_at <= ?)`,
			nowMillis, id, thresholdMillis)
		if uerr != nil {
			return fmt.Errorf("TouchSSHKey: %w", mapErr(uerr))
		}
		return nil
	})
}
