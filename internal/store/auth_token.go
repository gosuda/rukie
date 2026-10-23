package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type TokenRecord struct {
	Hash         string
	PublicPrefix string
	Name         string
	Scopes       string
	LastUsedIP   string
	UserID       int64
	ExpiresAt    int64
	CreatedAt    int64
	LastUsedAt   int64
	RevokedAt    int64
	RowID        int64
	PublicOnly   bool
}

const tokenColumnList = `rowid, token_hash, public_prefix, user_id, name, scopes, expires_at, created_at, last_used_at, last_used_ip, revoked_at, public_only`

func scanTokenRecord(row interface{ Scan(dest ...any) error }, out *TokenRecord) error {
	var lastUsed, revoked sql.NullInt64
	var lastIP sql.NullString
	var publicOnly int64
	if err := row.Scan(&out.RowID, &out.Hash, &out.PublicPrefix, &out.UserID, &out.Name, &out.Scopes,
		&out.ExpiresAt, &out.CreatedAt, &lastUsed, &lastIP, &revoked, &publicOnly); err != nil {
		return err
	}
	if lastUsed.Valid {
		out.LastUsedAt = lastUsed.Int64
	}
	if lastIP.Valid {
		out.LastUsedIP = lastIP.String
	}
	if revoked.Valid {
		out.RevokedAt = revoked.Int64
	}
	out.PublicOnly = publicOnly != 0
	return nil
}

type TokenCreate struct {
	Hash       string
	Prefix     string
	Name       string
	Scopes     string
	IP         string
	RepoIDs    []int64
	UserID     int64
	ExpiresAt  int64
	CreatedAt  int64
	PublicOnly bool
}

func (s *Store) CreateToken(ctx context.Context, in TokenCreate) error {
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if _, ierr := inner.ExecContext(tx.ctx, `INSERT INTO scoped_tokens
			(token_hash, public_prefix, user_id, name, scopes, expires_at, created_at, created_ip, public_only)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			in.Hash, in.Prefix, in.UserID, in.Name, in.Scopes,
			in.ExpiresAt, in.CreatedAt, in.IP, boolToInt64(in.PublicOnly)); ierr != nil {
			return fmt.Errorf("CreateToken: %w", mapErr(ierr))
		}
		for _, repoID := range in.RepoIDs {
			if _, rerr := inner.ExecContext(tx.ctx,
				`INSERT INTO token_repos (token_hash, repo_id) VALUES (?, ?)`,
				in.Hash, repoID); rerr != nil {
				return fmt.Errorf("CreateToken: %w", mapErr(rerr))
			}
		}
		return nil
	})
}

func (s *Store) GetTokenByHash(ctx context.Context, hash string) (TokenRecord, error) {
	var out TokenRecord
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if serr := scanTokenRecord(inner.QueryRowContext(tx.ctx,
			`SELECT `+tokenColumnList+` FROM scoped_tokens WHERE token_hash = ?`, hash), &out); serr != nil {
			if errors.Is(serr, sql.ErrNoRows) {
				return fmt.Errorf("GetTokenByHash: %w", ErrNotFound)
			}
			return fmt.Errorf("GetTokenByHash: %w", serr)
		}
		return nil
	})
	if err != nil {
		return TokenRecord{}, err
	}
	return out, nil
}

func (s *Store) GetTokenByRowID(ctx context.Context, rowid int64) (TokenRecord, error) {
	var out TokenRecord
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if serr := scanTokenRecord(inner.QueryRowContext(tx.ctx,
			`SELECT `+tokenColumnList+` FROM scoped_tokens WHERE rowid = ?`, rowid), &out); serr != nil {
			if errors.Is(serr, sql.ErrNoRows) {
				return fmt.Errorf("GetTokenByRowID: %w: %d", ErrNotFound, rowid)
			}
			return fmt.Errorf("GetTokenByRowID: %w", serr)
		}
		return nil
	})
	if err != nil {
		return TokenRecord{}, err
	}
	return out, nil
}

func (s *Store) TouchToken(ctx context.Context, hash string, now int64, ip string) error {
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if _, uerr := inner.ExecContext(tx.ctx,
			`UPDATE scoped_tokens SET last_used_at = ?, last_used_ip = ? WHERE token_hash = ?`, now, ip, hash); uerr != nil {
			return fmt.Errorf("TouchToken: %w", mapErr(uerr))
		}
		return nil
	})
}

func (s *Store) ListTokensByUser(ctx context.Context, userID int64) ([]TokenRecord, error) {
	var out []TokenRecord
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		rows, qerr := inner.QueryContext(tx.ctx, `SELECT `+tokenColumnList+`
			FROM scoped_tokens WHERE user_id = ? AND revoked_at IS NULL ORDER BY created_at DESC`, userID)
		if qerr != nil {
			return fmt.Errorf("ListTokensByUser: %w", qerr)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var rec TokenRecord
			if serr := scanTokenRecord(rows, &rec); serr != nil {
				return fmt.Errorf("ListTokensByUser: %w", serr)
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

func (s *Store) RevokeTokenByHash(ctx context.Context, hash string, now int64) error {
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		_, uerr := inner.ExecContext(tx.ctx,
			`UPDATE scoped_tokens SET revoked_at = ? WHERE token_hash = ? AND revoked_at IS NULL`, now, hash)
		if uerr != nil {
			return fmt.Errorf("RevokeTokenByHash: %w", mapErr(uerr))
		}
		return nil
	})
}

func (s *Store) RevokeUserTokens(ctx context.Context, userID int64, now int64) (int64, error) {
	var n int64
	err := s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		res, uerr := inner.ExecContext(tx.ctx,
			`UPDATE scoped_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, now, userID)
		if uerr != nil {
			return fmt.Errorf("RevokeUserTokens: %w", mapErr(uerr))
		}
		affected, aerr := res.RowsAffected()
		if aerr != nil {
			return fmt.Errorf("RevokeUserTokens: %w", aerr)
		}
		n = affected
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}
