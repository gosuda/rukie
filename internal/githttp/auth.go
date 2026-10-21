package githttp

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
)

type identity struct {
	repos  map[int64]bool
	user   auth.User
	token  auth.Token
	caller access.Caller
	authed bool
}

func (s *Server) authenticate(r *http.Request) (identity, bool, bool) {
	var zero identity
	header := r.Header.Get("Authorization")
	if header == "" {
		return identity{caller: access.Caller{Active: true}}, true, false
	}
	scheme, value, _ := strings.Cut(header, " ")
	secret := value
	username := ""
	pwHint := false
	if strings.EqualFold(scheme, "Basic") {
		raw, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			raw, err = base64.RawStdEncoding.DecodeString(value)
			if err != nil {
				return zero, false, false
			}
		}
		username, secret, _ = strings.Cut(string(raw), ":")
		if username == "" || secret == "" {
			return zero, false, false
		}
		pwHint = !auth.ValidTokenShape(secret)
	} else if !strings.EqualFold(scheme, "Bearer") || secret == "" {
		return zero, false, false
	}
	tok, user, err := s.auth.LookupToken(r.Context(), secret, clientIP(r))
	if err != nil {
		return zero, false, pwHint
	}
	if username != "" && !strings.EqualFold(username, user.Name) {
		return zero, false, pwHint
	}
	repos, err := s.auth.TokenRepos(r.Context(), tok.Hash)
	if err != nil {
		return zero, false, pwHint
	}
	set := make(map[int64]bool, len(repos))
	for _, id := range repos {
		set[id] = true
	}
	return identity{
		caller: access.Caller{
			UserID:     user.ID,
			Scopes:     tok.Scopes,
			Active:     true,
			Authed:     true,
			PublicOnly: tok.PublicOnly,
			IsToken:    true,
			IsAdmin:    user.IsAdmin,
		},
		user:   user,
		token:  tok,
		repos:  set,
		authed: true,
	}, true, false
}

func (s *Server) countGitFailure(w http.ResponseWriter, r *http.Request) bool {
	decision := s.limiter.Admit(r.Context(), op.RateKey{Kind: op.RateIPGit, ID: clientIP(r)})
	if !decision.Allow {
		writeRateLimited(w, decision.RetryAfter)
		return false
	}
	return true
}

func (s *Server) failAuth(w http.ResponseWriter, r *http.Request) {
	if !s.countGitFailure(w, r) {
		return
	}
	writeUnauthorized(w)
}

func (s *Server) failAuthPasswordHint(w http.ResponseWriter, r *http.Request) {
	if !s.countGitFailure(w, r) {
		return
	}
	writeUnauthorizedHint(w)
}

func (s *Server) denyAnonymous(w http.ResponseWriter, r *http.Request, id identity) bool {
	if id.authed {
		return false
	}
	s.failAuth(w, r)
	return true
}

func (s *Server) checkEntryRate(w http.ResponseWriter, r *http.Request, id identity) bool {
	key := op.RateKey{Kind: op.RateIPAnon, ID: clientIP(r)}
	if id.authed {
		key = op.RateKey{Kind: op.RateToken, ID: "token:" + id.token.Hash}
	}
	decision := s.limiter.Admit(r.Context(), key)
	if !decision.Allow {
		writeRateLimited(w, decision.RetryAfter)
		return false
	}
	return true
}

func (s *Server) lookupRepo(ctx context.Context, owner, repo string) (int64, bool) {
	meta, merr := s.store.LookupUserByName(ctx, strings.ToLower(owner))
	if merr != nil {
		return 0, false
	}
	var repoID int64
	rerr := s.store.Read(ctx, func(tx *store.ReadTx) error {
		row, ferr := tx.FindRepo(meta.ID, 0, repo)
		if ferr != nil {
			return ferr
		}
		repoID = row.ID
		return nil
	})
	if rerr != nil {
		return 0, false
	}
	return repoID, true
}

func (s *Server) authorize(id identity, repoID int64, action, ref string) access.Decision {
	return access.Check(s.resolver, id.caller, action, access.Resource{
		Kind:   access.KindRepo,
		RepoID: repoID,
		Ref:    ref,
	})
}

func (s *Server) tokenReposAllow(id identity, repoID int64) bool {
	if !id.authed || len(id.repos) == 0 {
		return true
	}
	return id.repos[repoID]
}
