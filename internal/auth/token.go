package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/gosuda/rukie/internal/op"
)

const tokenPrefix = "rk_"

var tokenShape = regexp.MustCompile(`^rk_[A-Za-z0-9_-]{43}$`)

func MintTokenString() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return tokenPrefix + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func ValidTokenShape(secret string) bool {
	return tokenShape.MatchString(secret)
}

func TokenPublicPrefix(secret string) string {
	if len(secret) < 12 {
		return secret
	}
	return secret[:12]
}

func ExpandScopes(scopes op.Scopes) op.Scopes {
	out := scopes.Add(op.ScopePublicRead)
	if scopes.Has(op.ScopeRepoWrite) {
		out = out.Add(op.ScopeRepoRead)
	}
	if scopes.Has(op.ScopeIntentsWrite) {
		out = out.Add(op.ScopeIntentsRead)
	}
	if scopes.Has(op.ScopeCITrigger) {
		out = out.Add(op.ScopeCIRead)
	}
	if scopes.Has(op.ScopeWebhooksWrite) {
		out = out.Add(op.ScopeWebhooksRead)
	}
	if scopes.Has(op.ScopeReleasesWrite) {
		out = out.Add(op.ScopeReleasesRead)
	}
	if scopes.Has(op.ScopeMirrorsTrigger) {
		out = out.Add(op.ScopeMirrorsRead)
	}
	if scopes.Has(op.ScopeAdminRead) {
		for _, s := range op.AllScopes() {
			if strings.HasSuffix(s.String(), ":read") || s == op.ScopePublicRead {
				out = out.Add(s)
			}
		}
	}
	if scopes.Has(op.ScopeAdminWrite) {
		out = ScopesAll()
	}
	return out
}

func ScopesAll() op.Scopes {
	var out op.Scopes
	for _, s := range op.AllScopes() {
		out = out.Add(s)
	}
	return out
}

func ParseScopeSet(names []string) (op.Scopes, error) {
	var out op.Scopes
	seen := map[op.Scope]bool{}
	for _, n := range names {
		s, ok := op.ParseScope(strings.TrimSpace(n))
		if !ok {
			return op.Scopes(0), errors.New("unknown scope " + n)
		}
		if s == op.ScopePublicRead {
			return op.Scopes(0), errors.New("public:read is not mintable")
		}
		if seen[s] {
			return op.Scopes(0), errors.New("duplicate scope " + n)
		}
		seen[s] = true
		out = out.Add(s)
	}
	if len(seen) == 0 {
		return op.Scopes(0), errors.New("at least one scope is required")
	}
	return out, nil
}

func ScopeNames(s op.Scopes) []string {
	var out []string
	for _, scope := range op.AllScopes() {
		if s.Has(scope) {
			out = append(out, scope.String())
		}
	}
	return out
}

type Token struct {
	Hash         string
	PublicPrefix string
	Name         string
	UserID       int64
	Scopes       op.Scopes
	ExpiresAt    int64
	CreatedAt    int64
	LastUsedAt   int64
	RevokedAt    int64
	PublicOnly   bool
}

type TokenSecret struct {
	Secret string
	Token  Token
}

type CreateTokenInput struct {
	Name       string
	IP         string
	Scopes     op.Scopes
	TTL        time.Duration
	UserID     int64
	PublicOnly bool
}

func (s *Service) CreateToken(ctx context.Context, in CreateTokenInput) (TokenSecret, error) {
	ttl := in.TTL
	if ttl <= 0 {
		ttl = tokenDefaultTTL
	}
	if ttl > tokenMaxTTL {
		return TokenSecret{}, errors.New("token lifetime exceeds 366 days")
	}
	if len(in.Name) > 64 {
		return TokenSecret{}, errors.New("token name exceeds 64 characters")
	}
	secret, err := MintTokenString()
	if err != nil {
		return TokenSecret{}, err
	}
	now := millis(s.clock())
	tok := Token{
		Hash:         hashBearer(secret),
		PublicPrefix: TokenPublicPrefix(secret),
		UserID:       in.UserID,
		Name:         in.Name,
		Scopes:       ExpandScopes(in.Scopes),
		PublicOnly:   in.PublicOnly,
		ExpiresAt:    now + ttl.Milliseconds(),
		CreatedAt:    now,
	}
	publicOnlyInt := 0
	if in.PublicOnly {
		publicOnlyInt = 1
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO scoped_tokens
		(token_hash, public_prefix, user_id, name, scopes, expires_at, created_at, created_ip, public_only)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tok.Hash, tok.PublicPrefix, in.UserID, in.Name, strings.Join(ScopeNames(tok.Scopes), " "),
		tok.ExpiresAt, now, in.IP, publicOnlyInt)
	if err != nil {
		return TokenSecret{}, err
	}
	s.recordAudit(ctx, AuditEvent{Actor: in.UserID, Action: "token.create", Target: "token:" + tok.PublicPrefix, IP: in.IP, Result: "ok"})
	return TokenSecret{Token: tok, Secret: secret}, nil
}

var ErrTokenInvalid = errors.New("invalid token")

func (s *Service) LookupToken(ctx context.Context, secret, ip string) (Token, User, error) {
	if !ValidTokenShape(secret) {
		return Token{}, User{}, ErrTokenInvalid
	}
	hash := hashBearer(secret)
	var tok Token
	var scopesRaw string
	var lastUsed, revoked sql.NullInt64
	var lastIP sql.NullString
	var publicOnly int64
	now := millis(s.clock())
	err := s.db.QueryRowContext(ctx, `SELECT token_hash, public_prefix, user_id, name, scopes,
		expires_at, created_at, last_used_at, last_used_ip, revoked_at, public_only
		FROM scoped_tokens WHERE token_hash = ?`, hash).Scan(
		&tok.Hash, &tok.PublicPrefix, &tok.UserID, &tok.Name, &scopesRaw,
		&tok.ExpiresAt, &tok.CreatedAt, &lastUsed, &lastIP, &revoked, &publicOnly)
	if err != nil {
		return Token{}, User{}, ErrTokenInvalid
	}
	if revoked.Valid {
		return Token{}, User{}, ErrTokenInvalid
	}
	if now >= tok.ExpiresAt {
		s.recordAudit(ctx, AuditEvent{Actor: tok.UserID, Action: "token.expired", Target: "token:" + tok.PublicPrefix, IP: ip, Result: "deny"})
		return Token{}, User{}, ErrTokenInvalid
	}
	for n := range strings.FieldsSeq(scopesRaw) {
		if scope, ok := op.ParseScope(n); ok {
			tok.Scopes = tok.Scopes.Add(scope)
		}
	}
	tok.PublicOnly = publicOnly != 0
	if lastUsed.Valid {
		tok.LastUsedAt = lastUsed.Int64
	}
	if revoked.Valid {
		tok.RevokedAt = revoked.Int64
	}
	var u User
	err = s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, tok.UserID).Scan(
		&u.ID, &u.Name, &u.LowerName, &u.Email, &u.FullName,
		&u.PasswordAlgo, boolInt(&u.MustChangePassword), boolInt(&u.TOTPEnabled), boolInt(&u.OIDCOnly),
		boolInt(&u.IsAdmin), boolInt(&u.IsActive),
		&u.FailedLoginCount, nullInt(&u.LockedUntil), &u.PasswordChangedAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil || !u.IsActive {
		return Token{}, User{}, ErrTokenInvalid
	}
	if !lastUsed.Valid || now-lastUsed.Int64 >= tokenUseTrackGranularity.Milliseconds() {
		_, _ = s.db.ExecContext(ctx, `UPDATE scoped_tokens SET last_used_at = ?, last_used_ip = ? WHERE token_hash = ?`, now, ip, hash)
		tok.LastUsedAt = now
	}
	return tok, u, nil
}

func (s *Service) ListTokens(ctx context.Context, userID int64) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT token_hash, public_prefix, user_id, name, scopes,
		expires_at, created_at, last_used_at, revoked_at, public_only
		FROM scoped_tokens WHERE user_id = ? AND revoked_at IS NULL ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Token
	for rows.Next() {
		var tok Token
		var scopesRaw string
		var lastUsed, revoked sql.NullInt64
		var publicOnly int64
		if err := rows.Scan(&tok.Hash, &tok.PublicPrefix, &tok.UserID, &tok.Name, &scopesRaw,
			&tok.ExpiresAt, &tok.CreatedAt, &lastUsed, &revoked, &publicOnly); err != nil {
			return nil, err
		}
		for n := range strings.FieldsSeq(scopesRaw) {
			if scope, ok := op.ParseScope(n); ok {
				tok.Scopes = tok.Scopes.Add(scope)
			}
		}
		if lastUsed.Valid {
			tok.LastUsedAt = lastUsed.Int64
		}
		if revoked.Valid {
			tok.RevokedAt = revoked.Int64
		}
		tok.PublicOnly = publicOnly != 0
		out = append(out, tok)
	}
	return out, rows.Err()
}

func (s *Service) RevokeTokenByPrefix(ctx context.Context, userID int64, prefix, ip string) (bool, error) {
	tokens, err := s.ListTokens(ctx, userID)
	if err != nil {
		return false, err
	}
	var match string
	for _, t := range tokens {
		if strings.HasPrefix(t.PublicPrefix, prefix) || strings.HasPrefix(t.Hash, prefix) {
			if match != "" {
				return false, errors.New("token prefix is ambiguous")
			}
			match = t.Hash
		}
	}
	if match == "" {
		return false, nil
	}
	now := millis(s.clock())
	_, err = s.db.ExecContext(ctx, `UPDATE scoped_tokens SET revoked_at = ? WHERE token_hash = ? AND revoked_at IS NULL`, now, match)
	if err != nil {
		return false, err
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "token.revoke", Target: "token:" + match[:12], IP: ip, Result: "ok"})
	return true, nil
}

func (s *Service) RevokeOwnToken(ctx context.Context, rowid int64, prefix, ip string) error {
	var hash, storedPrefix string
	var userID int64
	err := s.db.QueryRowContext(ctx, `SELECT token_hash, public_prefix, user_id FROM scoped_tokens WHERE rowid = ?`, rowid).Scan(&hash, &storedPrefix, &userID)
	if err != nil {
		return ErrTokenInvalid
	}
	if prefix != "" && storedPrefix != prefix {
		if len(prefix) > len(hash) || hash[:len(prefix)] != prefix {
			return ErrTokenInvalid
		}
	}
	now := millis(s.clock())
	_, err = s.db.ExecContext(ctx, `UPDATE scoped_tokens SET revoked_at = ? WHERE token_hash = ? AND revoked_at IS NULL`, now, hash)
	if err != nil {
		return err
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "token.revoke", Target: "token:" + storedPrefix, IP: ip, Result: "ok"})
	return nil
}

func (s *Service) RevokeUserTokens(ctx context.Context, userID int64, reason string) (int64, error) {
	now := millis(s.clock())
	res, err := s.db.ExecContext(ctx, `UPDATE scoped_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, now, userID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: reason, Result: "ok"})
	return n, nil
}

func (s *Service) TokenRepos(ctx context.Context, hash string) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id FROM token_repos WHERE token_hash = ?`, hash)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
