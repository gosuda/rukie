package store

import (
	"context"
	"database/sql"
	"errors"
)

type ScopedTokenMeta struct {
	Hash       string
	UserID     int64
	PublicOnly bool
}

func (s *Store) LookupScopedToken(ctx context.Context, rowID int64) (ScopedTokenMeta, error) {
	var out ScopedTokenMeta
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		var publicOnly int64
		serr := inner.QueryRowContext(tx.ctx,
			`SELECT token_hash, public_only, user_id FROM scoped_tokens WHERE rowid = ?`, rowID).Scan(
			&out.Hash, &publicOnly, &out.UserID)
		if errors.Is(serr, sql.ErrNoRows) {
			return errors.Join(ErrNotFound, serr)
		}
		out.PublicOnly = publicOnly == 1
		return serr
	})
	if err != nil {
		return ScopedTokenMeta{}, err
	}
	return out, nil
}

type UserMeta struct {
	Name     string
	Email    string
	FullName string
	ID       int64
	IsActive bool
	IsAdmin  bool
}

func (s *Store) LookupScopedTokenByHash(ctx context.Context, hash string) (ScopedTokenMeta, error) {
	var out ScopedTokenMeta
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		var publicOnly int64
		serr := inner.QueryRowContext(tx.ctx,
			`SELECT token_hash, public_only, user_id FROM scoped_tokens WHERE token_hash = ?`, hash).Scan(
			&out.Hash, &publicOnly, &out.UserID)
		if errors.Is(serr, sql.ErrNoRows) {
			return errors.Join(ErrNotFound, serr)
		}
		out.PublicOnly = publicOnly == 1
		return serr
	})
	if err != nil {
		return ScopedTokenMeta{}, err
	}
	return out, nil
}

func (s *Store) LookupUserByID(ctx context.Context, userID int64) (UserMeta, error) {
	var out UserMeta
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		var active, admin int64
		serr := inner.QueryRowContext(tx.ctx,
			`SELECT id, name, email, full_name, is_active, is_admin FROM users WHERE id = ?`, userID).Scan(
			&out.ID, &out.Name, &out.Email, &out.FullName, &active, &admin)
		if errors.Is(serr, sql.ErrNoRows) {
			return errors.Join(ErrNotFound, serr)
		}
		out.IsActive = active != 0
		out.IsAdmin = admin != 0
		return serr
	})
	if err != nil {
		return UserMeta{}, err
	}
	return out, nil
}

func (s *Store) LookupUserByName(ctx context.Context, lower string) (UserMeta, error) {
	var out UserMeta
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		var active, admin int64
		serr := inner.QueryRowContext(tx.ctx,
			`SELECT id, name, email, full_name, is_active, is_admin FROM users WHERE lower_name = ?`, lower).Scan(
			&out.ID, &out.Name, &out.Email, &out.FullName, &active, &admin)
		if errors.Is(serr, sql.ErrNoRows) {
			return errors.Join(ErrNotFound, serr)
		}
		out.IsActive = active != 0
		out.IsAdmin = admin != 0
		return serr
	})
	if err != nil {
		return UserMeta{}, err
	}
	return out, nil
}

func (s *Store) ListTokenRepos(ctx context.Context, hash string) ([]int64, error) {
	var out []int64
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		rows, qerr := inner.QueryContext(tx.ctx,
			`SELECT repo_id FROM token_repos WHERE token_hash = ?`, hash)
		if qerr != nil {
			return qerr
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id int64
			if serr := rows.Scan(&id); serr != nil {
				return serr
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
