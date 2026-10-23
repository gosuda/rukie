package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
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
	out := op.Scopes(0)
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
	Repos      []int64
	Scopes     op.Scopes
	TTL        time.Duration
	UserID     int64
	PublicOnly bool
}

func tokenFromStore(rec store.TokenRecord) Token {
	tok := Token{
		Hash:         rec.Hash,
		PublicPrefix: rec.PublicPrefix,
		UserID:       rec.UserID,
		Name:         rec.Name,
		ExpiresAt:    rec.ExpiresAt,
		CreatedAt:    rec.CreatedAt,
		LastUsedAt:   rec.LastUsedAt,
		RevokedAt:    rec.RevokedAt,
		PublicOnly:   rec.PublicOnly,
	}
	for n := range strings.FieldsSeq(rec.Scopes) {
		if scope, ok := op.ParseScope(n); ok {
			tok.Scopes = tok.Scopes.Add(scope)
		}
	}
	return tok
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
	scopesRaw := strings.Join(ScopeNames(tok.Scopes), " ")
	serr := s.store.CreateToken(ctx, store.TokenCreate{
		Hash:       tok.Hash,
		Prefix:     tok.PublicPrefix,
		Name:       in.Name,
		Scopes:     scopesRaw,
		IP:         in.IP,
		UserID:     in.UserID,
		ExpiresAt:  tok.ExpiresAt,
		CreatedAt:  now,
		PublicOnly: in.PublicOnly,
		RepoIDs:    in.Repos,
	})
	if serr != nil {
		return TokenSecret{}, serr
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
	rec, err := s.store.GetTokenByHash(ctx, hash)
	if err != nil {
		return Token{}, User{}, ErrTokenInvalid
	}
	tok := tokenFromStore(rec)
	now := millis(s.clock())
	if tok.RevokedAt != 0 {
		return Token{}, User{}, ErrTokenInvalid
	}
	if now >= tok.ExpiresAt {
		s.recordAudit(ctx, AuditEvent{Actor: tok.UserID, Action: "token.expired", Target: "token:" + tok.PublicPrefix, IP: ip, Result: "deny"})
		return Token{}, User{}, ErrTokenInvalid
	}
	au, uerr := s.store.GetAuthUserByID(ctx, tok.UserID)
	if uerr != nil {
		return Token{}, User{}, ErrTokenInvalid
	}
	u, _ := authUserFromStore(au)
	if !u.IsActive {
		return Token{}, User{}, ErrTokenInvalid
	}
	if rec.LastUsedAt == 0 || now-rec.LastUsedAt >= tokenUseTrackGranularity.Milliseconds() {
		_ = s.store.TouchToken(ctx, hash, now, ip)
		tok.LastUsedAt = now
	}
	return tok, u, nil
}

func (s *Service) ListTokens(ctx context.Context, userID int64) ([]Token, error) {
	recs, err := s.store.ListTokensByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Token, 0, len(recs))
	for _, rec := range recs {
		out = append(out, tokenFromStore(rec))
	}
	return out, nil
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
	if rerr := s.store.RevokeTokenByHash(ctx, match, now); rerr != nil {
		return false, rerr
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "token.revoke", Target: "token:" + match[:12], IP: ip, Result: "ok"})
	return true, nil
}

func (s *Service) RevokeOwnToken(ctx context.Context, rowid int64, prefix, ip string) error {
	rec, err := s.store.GetTokenByRowID(ctx, rowid)
	if err != nil {
		return ErrTokenInvalid
	}
	if prefix != "" && rec.PublicPrefix != prefix {
		if len(prefix) > len(rec.Hash) || rec.Hash[:len(prefix)] != prefix {
			return ErrTokenInvalid
		}
	}
	now := millis(s.clock())
	if rerr := s.store.RevokeTokenByHash(ctx, rec.Hash, now); rerr != nil {
		return rerr
	}
	s.recordAudit(ctx, AuditEvent{Actor: rec.UserID, Action: "token.revoke", Target: "token:" + rec.PublicPrefix, IP: ip, Result: "ok"})
	return nil
}

func (s *Service) RevokeUserTokens(ctx context.Context, userID int64, reason string) (int64, error) {
	now := millis(s.clock())
	n, err := s.store.RevokeUserTokens(ctx, userID, now)
	if err != nil {
		return 0, err
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: reason, Result: "ok"})
	return n, nil
}

func (s *Service) TokenRepos(ctx context.Context, hash string) ([]int64, error) {
	return s.store.ListTokenRepos(ctx, hash)
}
