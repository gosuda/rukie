package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type OIDCProviderUpsert struct {
	ID          string
	Issuer      string
	ClientID    string
	Scopes      string
	AllowSignup bool
}

func (s *Store) UpsertOIDCProvider(ctx context.Context, in OIDCProviderUpsert) error {
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		_, ierr := inner.ExecContext(tx.ctx, `INSERT INTO oidc_providers (id, issuer, client_id, scopes, allow_signup)
			VALUES (?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET issuer = ?, client_id = ?, scopes = ?, allow_signup = ?`,
			in.ID, in.Issuer, in.ClientID, in.Scopes, boolToInt64(in.AllowSignup),
			in.Issuer, in.ClientID, in.Scopes, boolToInt64(in.AllowSignup))
		if ierr != nil {
			return fmt.Errorf("UpsertOIDCProvider: %w", mapErr(ierr))
		}
		return nil
	})
}

type OIDCStateCreate struct {
	StateHash   string
	ProviderID  string
	Nonce       string
	Verifier    string
	ReturnTo    string
	BrowserHash string
	LinkUserID  int64
	HasLink     bool
	Now         int64
	ExpiresAt   int64
}

func (s *Store) CreateOIDCState(ctx context.Context, in OIDCStateCreate) error {
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if _, derr := inner.ExecContext(tx.ctx,
			`DELETE FROM oidc_states WHERE expires_at <= ?`, in.Now); derr != nil {
			return fmt.Errorf("CreateOIDCState: %w", derr)
		}
		var linkArg any
		if in.HasLink {
			linkArg = in.LinkUserID
		}
		_, ierr := inner.ExecContext(tx.ctx, `INSERT INTO oidc_states
			(state_sha256, provider_id, nonce, verifier, link_user_id, return_to, browser_sha256, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			in.StateHash, in.ProviderID, in.Nonce, in.Verifier, linkArg, in.ReturnTo, in.BrowserHash, in.Now, in.ExpiresAt)
		if ierr != nil {
			return fmt.Errorf("CreateOIDCState: %w", mapErr(ierr))
		}
		return nil
	})
}

type OIDCState struct {
	Nonce       string
	Verifier    string
	ReturnTo    string
	BrowserHash string
	LinkUserID  int64
	HasLink     bool
}

func (s *Store) ConsumeOIDCState(ctx context.Context, stateHash, providerID string, now int64) (OIDCState, error) {
	var out OIDCState
	err := s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		var linkUserID sql.NullInt64
		cerr := inner.QueryRowContext(tx.ctx, `DELETE FROM oidc_states
			WHERE state_sha256 = ? AND provider_id = ? AND expires_at > ?
			RETURNING nonce, verifier, link_user_id, return_to, browser_sha256`,
			stateHash, providerID, now).Scan(&out.Nonce, &out.Verifier, &linkUserID, &out.ReturnTo, &out.BrowserHash)
		if cerr != nil {
			if errors.Is(cerr, sql.ErrNoRows) {
				return fmt.Errorf("ConsumeOIDCState: %w", ErrNotFound)
			}
			return fmt.Errorf("ConsumeOIDCState: %w", cerr)
		}
		if linkUserID.Valid {
			out.LinkUserID = linkUserID.Int64
			out.HasLink = true
		}
		return nil
	})
	if err != nil {
		return OIDCState{}, err
	}
	return out, nil
}

func (s *Store) LinkOIDCIdentity(ctx context.Context, issuer, subject string, userID, now int64) (int64, error) {
	var owner int64
	err := s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		_, ierr := inner.ExecContext(tx.ctx, `INSERT INTO oidc_links (issuer, subject, user_id, linked_at)
			VALUES (?, ?, ?, ?)`, issuer, subject, userID, now)
		if ierr == nil {
			owner = userID
			return nil
		}
		if !errors.Is(mapErr(ierr), ErrConflict) {
			return fmt.Errorf("LinkOIDCIdentity: %w", mapErr(ierr))
		}
		if lerr := inner.QueryRowContext(tx.ctx, `SELECT user_id FROM oidc_links WHERE issuer = ? AND subject = ?`,
			issuer, subject).Scan(&owner); lerr != nil {
			return fmt.Errorf("LinkOIDCIdentity: %w", lerr)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return owner, nil
}

func (s *Store) LookupOIDCLink(ctx context.Context, issuer, subject string) (int64, error) {
	var userID int64
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if serr := inner.QueryRowContext(tx.ctx, `SELECT user_id FROM oidc_links WHERE issuer = ? AND subject = ?`,
			issuer, subject).Scan(&userID); serr != nil {
			if errors.Is(serr, sql.ErrNoRows) {
				return fmt.Errorf("LookupOIDCLink: %w", ErrNotFound)
			}
			return fmt.Errorf("LookupOIDCLink: %w", serr)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return userID, nil
}

type OIDCUserCreate struct {
	Name     string
	Lower    string
	Email    string
	FullName string
	Hash     string
	Algo     string
	Issuer   string
	Subject  string
	Now      int64
}

func (s *Store) CreateOIDCUser(ctx context.Context, in OIDCUserCreate) (AuthUser, error) {
	var out AuthUser
	err := s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		res, ierr := inner.ExecContext(tx.ctx, `INSERT INTO users
			(name, lower_name, email, full_name, password_hash, password_algo, password_changed_at, oidc_only, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			in.Name, in.Lower, in.Email, in.FullName, in.Hash, in.Algo, in.Now, in.Now, in.Now)
		if ierr != nil {
			return fmt.Errorf("CreateOIDCUser: %w", mapErr(ierr))
		}
		id, derr := res.LastInsertId()
		if derr != nil {
			return fmt.Errorf("CreateOIDCUser: %w", derr)
		}
		if _, lerr := inner.ExecContext(tx.ctx, `INSERT INTO oidc_links (issuer, subject, user_id, linked_at)
			VALUES (?, ?, ?, ?)`, in.Issuer, in.Subject, id, in.Now); lerr != nil {
			return fmt.Errorf("CreateOIDCUser: %w", mapErr(lerr))
		}
		out = AuthUser{ID: id, Name: in.Name, Lower: in.Lower, Email: in.Email,
			FullName: in.FullName, PasswordAlgo: in.Algo, PasswordHash: in.Hash,
			OIDCOnly: true, IsActive: true,
			PasswordChangedAt: in.Now, CreatedAt: in.Now, UpdatedAt: in.Now}
		return nil
	})
	if err != nil {
		return AuthUser{}, err
	}
	return out, nil
}

type OIDCBadStateRecord struct {
	IP           string
	Now          int64
	WindowStart  int64
	PruneBefore  int64
	Limit        int
	BlockedUntil int64
}

func (s *Store) RecordOIDCBadState(ctx context.Context, r OIDCBadStateRecord) error {
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if _, ierr := inner.ExecContext(tx.ctx, `INSERT INTO login_attempts (username_lower, ip, at, success, reason)
			VALUES ('-', ?, ?, 0, 'oidc-bad-state')`, r.IP, r.Now); ierr != nil {
			return fmt.Errorf("RecordOIDCBadState: %w", mapErr(ierr))
		}
		if _, derr := inner.ExecContext(tx.ctx, `DELETE FROM login_attempts
			WHERE reason = 'oidc-bad-state' AND at < ?`, r.PruneBefore); derr != nil {
			return fmt.Errorf("RecordOIDCBadState: %w", derr)
		}
		var recent int
		if cerr := inner.QueryRowContext(tx.ctx, `SELECT COUNT(*) FROM login_attempts
			WHERE ip = ? AND reason = 'oidc-bad-state' AND success = 0 AND at >= ?`,
			r.IP, r.WindowStart).Scan(&recent); cerr != nil {
			return fmt.Errorf("RecordOIDCBadState: %w", cerr)
		}
		if recent >= r.Limit {
			_, berr := inner.ExecContext(tx.ctx, `INSERT INTO oidc_ip_blocks (ip, blocked_until, created_at)
				VALUES (?, ?, ?) ON CONFLICT(ip) DO UPDATE SET blocked_until = ?, created_at = ?`,
				r.IP, r.BlockedUntil, r.Now, r.BlockedUntil, r.Now)
			if berr != nil {
				return fmt.Errorf("RecordOIDCBadState: %w", mapErr(berr))
			}
		}
		return nil
	})
}

func (s *Store) GetOIDCIPBlock(ctx context.Context, ip string) (int64, error) {
	var until int64
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if serr := inner.QueryRowContext(tx.ctx,
			`SELECT blocked_until FROM oidc_ip_blocks WHERE ip = ?`, ip).Scan(&until); serr != nil {
			if errors.Is(serr, sql.ErrNoRows) {
				return fmt.Errorf("GetOIDCIPBlock: %w", ErrNotFound)
			}
			return fmt.Errorf("GetOIDCIPBlock: %w", serr)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return until, nil
}

func (s *Store) ClearOIDCIPBlock(ctx context.Context, ip string) error {
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if _, derr := inner.ExecContext(tx.ctx, `DELETE FROM oidc_ip_blocks WHERE ip = ?`, ip); derr != nil {
			return fmt.Errorf("ClearOIDCIPBlock: %w", derr)
		}
		return nil
	})
}

func (s *Store) ListOIDCIssuers(ctx context.Context, userID int64) ([]string, error) {
	var out []string
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		rows, qerr := inner.QueryContext(tx.ctx,
			`SELECT DISTINCT issuer FROM oidc_links WHERE user_id = ?`, userID)
		if qerr != nil {
			return fmt.Errorf("ListOIDCIssuers: %w", qerr)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var issuer string
			if serr := rows.Scan(&issuer); serr != nil {
				return fmt.Errorf("ListOIDCIssuers: %w", serr)
			}
			out = append(out, issuer)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
