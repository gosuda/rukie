package api

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
)

type LoginBody struct {
	Username string `json:"username" minLength:"1" maxLength:"39"`
	Password string `json:"password" format:"password" minLength:"1" maxLength:"512"`
}

type LoginIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           LoginBody
}

type LoginOut struct {
	Username           string `json:"username"`
	UserID             int64  `json:"user_id"`
	MustChangePassword bool   `json:"must_change_password"`
	TOTPRequired       bool   `json:"totp_required,omitzero"`
}

type LoginPageOut struct {
	Username string `json:"username"`
}

type LogoutIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
}

type LogoutOut struct{}

type PasswordChangeBody struct {
	Current string `json:"current" format:"password" minLength:"1" maxLength:"512"`
	Next    string `json:"next" format:"password" minLength:"12" maxLength:"512"`
}

type PasswordChangeIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           PasswordChangeBody
}

type PasswordChangeOut struct{}

type TokenIn struct {
	Scopes string  `json:"scopes"`
	Name   string  `json:"name,omitempty" maxLength:"64"`
	TTL    string  `json:"ttl,omitempty"`
	Repos  []int64 `json:"repos,omitempty"`
}

type TokenCreateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           TokenIn
}

type TokenOut struct {
	Prefix    string   `json:"prefix"`
	Secret    string   `json:"secret,omitempty"`
	Scopes    []string `json:"scopes"`
	ExpiresAt int64    `json:"expires_at"`
}

type TokenListIn struct {
	Reveal string `query:"reveal"`
}

type TokenListItem struct {
	Prefix     string   `json:"prefix"`
	Name       string   `json:"name"`
	Scopes     []string `json:"scopes"`
	ExpiresAt  int64    `json:"expires_at"`
	LastUsedAt int64    `json:"last_used_at"`
	CreatedAt  int64    `json:"created_at"`
}

type TokenListOut struct {
	Reveal string          `json:"reveal,omitempty"`
	Secret string          `json:"secret,omitempty"`
	Tokens []TokenListItem `json:"tokens"`
}

type TokenRevokeIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	ID             string `path:"id"`
}

type TokenRevokeOut struct{}

type SessionListOut struct {
	Sessions      []SessionItem `json:"sessions"`
	OIDCProviders []string      `json:"oidc_providers,omitempty"`
	OIDCLinked    []string      `json:"oidc_linked,omitempty"`
	TOTPEnabled   bool          `json:"totp_enabled,omitzero"`
}

type SessionItem struct {
	Prefix     string `json:"prefix"`
	CreatedAt  int64  `json:"created_at"`
	LastSeenAt int64  `json:"last_seen_at"`
	Current    bool   `json:"current"`
}

type SessionRevokeIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	ID             string `path:"id"`
}

type SessionRevokeOut struct{}

func tokenOutOf(t auth.Token, secret string) TokenOut {
	return TokenOut{Prefix: t.PublicPrefix, Secret: secret, Scopes: auth.ScopeNames(t.Scopes), ExpiresAt: t.ExpiresAt}
}

func loginRateLimited(ctx context.Context, deps Deps, username string) *op.Error {
	if deps.Limiter == nil {
		return nil
	}
	d := deps.Limiter.Admit(ctx, op.RateKey{Kind: op.RateIPLogin, ID: "login:" + strings.ToLower(username)})
	if !d.Allow {
		return &op.Error{Code: op.CodeRateLimited, Detail: "too many login attempts", RetryAfter: d.RetryAfter}
	}
	return nil
}

func RegisterAuth(t *op.Table, deps Deps) error {
	if err := op.Register(t, op.Op[op.NoBody, LoginPageOut]{
		Name: "auth.login.page", Kind: op.KindRead, Method: http.MethodGet, Path: "/login",
		Scope: op.ScopePublicRead, Summary: "Show the login form", Hidden: true,
		Description: "Page-only. Renders the zero-JS login form.",
		Handler: func(_ context.Context, _ op.Principal, _ op.NoBody) (LoginPageOut, error) {
			return LoginPageOut{}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[LoginIn, LoginOut]{
		Name: "auth.login", Kind: op.KindMutation, Method: http.MethodPost, Path: "/login",
		Scope: op.ScopePublicRead, Summary: "Sign in with a password",
		Description: "Verifies an argon2id password, rotates the session, and sets the session cookie.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeRateLimited},
		Location: func(out LoginOut) string {
			if out.TOTPRequired {
				return "/login/totp"
			}
			return "/settings"
		},
		Handler: func(ctx context.Context, _ op.Principal, in LoginIn) (LoginOut, error) {
			res, err := deps.Service.Login(ctx, auth.LoginInput{Username: in.Body.Username, Password: in.Body.Password, IP: op.ClientIP(ctx), UserAgent: op.UserAgent(ctx)})
			if err != nil {
				if rl := loginRateLimited(ctx, deps, in.Body.Username); rl != nil {
					return LoginOut{}, rl
				}
				return LoginOut{}, mapAuthError(err)
			}
			op.SetCookie(ctx, auth.SessionCookie(res.Secret, time.UnixMilli(res.Session.AbsoluteExpiresAt), deps.now()))
			return LoginOut{Username: res.User.Name, UserID: res.User.ID, MustChangePassword: res.User.MustChangePassword, TOTPRequired: res.TOTPRequired}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[LogoutOut, LogoutOut]{
		Name: "auth.logout.page", Kind: op.KindRead, Method: http.MethodGet, Path: "/logout",
		Scope: op.ScopePublicRead, Summary: "Confirm sign out", Hidden: true,
		Description: "Page-only. Renders the zero-JS sign-out confirmation.",
		Handler: func(_ context.Context, _ op.Principal, _ LogoutOut) (LogoutOut, error) {
			return LogoutOut{}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[LogoutIn, LogoutOut]{
		Name: "auth.logout", Kind: op.KindMutation, Method: http.MethodPost, Path: "/logout",
		Scope: op.ScopePublicRead, Summary: "Sign out",
		Description: "Revokes the current session and clears the session cookie.",
		Location:    func(LogoutOut) string { return "/login" },
		Handler: func(ctx context.Context, p op.Principal, _ LogoutIn) (LogoutOut, error) {
			if userID, rowid, err := auth.RequireSession(p); err == nil {
				if rerr := deps.Service.RevokeSessionByID(ctx, rowid, userID, "", ""); rerr != nil {
					return LogoutOut{}, mapAuthError(rerr)
				}
			}
			op.SetCookie(ctx, auth.ClearSessionCookie())
			return LogoutOut{}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[PasswordChangeIn, PasswordChangeOut]{
		Name: "auth.password.change", Kind: op.KindMutation, Method: http.MethodPost, Path: "/settings/security/password",
		Scope: op.ScopeRepoRead, Summary: "Change the account password",
		Description: "Requires a fresh session. Revokes every session including the current one.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeValidation},
		Location:    func(PasswordChangeOut) string { return "/login" },
		Handler: func(ctx context.Context, p op.Principal, in PasswordChangeIn) (PasswordChangeOut, error) {
			userID, err := auth.RequireFresh(ctx, deps.Service, p, deps.now())
			if err != nil {
				return PasswordChangeOut{}, mapAuthError(err)
			}
			if err := deps.Service.ChangePassword(ctx, userID, in.Body.Current, in.Body.Next, ""); err != nil {
				return PasswordChangeOut{}, mapAuthError(err)
			}
			op.SetCookie(ctx, auth.ClearSessionCookie())
			return PasswordChangeOut{}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[TokenListIn, TokenListOut]{
		Name: "auth.token.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/settings/tokens",
		Scope: op.ScopeRepoRead, Summary: "List scoped agent tokens",
		Description: "Requires a fresh session. Shows token prefixes, scopes, and expiry.",
		Problems:    []op.Code{op.CodeUnauthorized},
		ETag: func(out TokenListOut) string {
			h := 0
			for _, tok := range out.Tokens {
				h = h*31 + len(tok.Prefix) + int(tok.ExpiresAt%1000003)
			}
			return fmt.Sprintf("tokens-%d-%d", len(out.Tokens), h)
		},
		Handler: func(ctx context.Context, p op.Principal, in TokenListIn) (TokenListOut, error) {
			userID, err := auth.RequireFresh(ctx, deps.Service, p, deps.now())
			if err != nil {
				return TokenListOut{}, mapAuthError(err)
			}
			tokens, err := deps.Service.ListTokens(ctx, userID)
			if err != nil {
				return TokenListOut{}, mapAuthError(err)
			}
			out := TokenListOut{}
			for _, tok := range tokens {
				out.Tokens = append(out.Tokens, TokenListItem{
					Prefix: tok.PublicPrefix, Name: tok.Name, Scopes: auth.ScopeNames(tok.Scopes),
					ExpiresAt: tok.ExpiresAt, LastUsedAt: tok.LastUsedAt, CreatedAt: tok.CreatedAt,
				})
			}
			if in.Reveal != "" {
				if secret, ok := deps.Reveals.Take(in.Reveal); ok {
					out.Reveal = in.Reveal
					out.Secret = secret
				}
			}
			return out, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[TokenCreateIn, TokenOut]{
		Name: "auth.token.create", Kind: op.KindMutation, Method: http.MethodPost, Path: "/settings/tokens",
		Scope: op.ScopeRepoRead, Summary: "Mint a scoped agent token",
		Description: "Session-only with fresh auth. The secret is shown once. A repo selector narrows the token to listed repositories.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeValidation, op.CodeNotFound},
		Location:    func(out TokenOut) string { return "/settings/tokens?reveal=" + out.Prefix },
		Handler: func(ctx context.Context, p op.Principal, in TokenCreateIn) (TokenOut, error) {
			userID, err := auth.RequireFresh(ctx, deps.Service, p, deps.now())
			if err != nil {
				return TokenOut{}, mapAuthError(err)
			}
			scopes, err := auth.ParseScopeSet(strings.Fields(in.Body.Scopes))
			if err != nil {
				return TokenOut{}, mapAuthError(err)
			}
			var ttl time.Duration
			if in.Body.TTL != "" {
				ttl, err = time.ParseDuration(in.Body.TTL)
				if err != nil {
					return TokenOut{}, op.Errorf(op.CodeValidation, "unparseable ttl")
				}
			}
			repos, verr := tokenRepoSelector(ctx, deps, p, in.Body.Repos)
			if verr != nil {
				return TokenOut{}, verr
			}
			minted, err := deps.Service.CreateToken(ctx, auth.CreateTokenInput{
				UserID: userID, Name: in.Body.Name, Scopes: scopes, TTL: ttl, Repos: repos,
			})
			if err != nil {
				return TokenOut{}, mapAuthError(err)
			}
			deps.Reveals.Store(minted.Token.PublicPrefix, minted.Secret)
			return tokenOutOf(minted.Token, minted.Secret), nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[TokenRevokeIn, TokenRevokeOut]{
		Name: "auth.token.revoke", Kind: op.KindMutation, Method: http.MethodPost, Path: "/settings/tokens/{id}/revoke",
		Scope: op.ScopeRepoRead, Summary: "Revoke a scoped agent token",
		Description: "Session with fresh auth revokes any own token by prefix. A token revokes only itself.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeNotFound},
		Location:    func(TokenRevokeOut) string { return "/settings/tokens" },
		Handler: func(ctx context.Context, p op.Principal, in TokenRevokeIn) (TokenRevokeOut, error) {
			if userID, _, err := auth.RequireSession(p); err == nil {
				if _, ferr := auth.RequireFresh(ctx, deps.Service, p, deps.now()); ferr != nil {
					return TokenRevokeOut{}, mapAuthError(ferr)
				}
				ok, rerr := deps.Service.RevokeTokenByPrefix(ctx, userID, in.ID, "")
				if rerr != nil {
					return TokenRevokeOut{}, mapAuthError(rerr)
				}
				if !ok {
					return TokenRevokeOut{}, op.Errorf(op.CodeNotFound, "unknown token")
				}
				return TokenRevokeOut{}, nil
			}
			if p.TokenID > 0 {
				if rerr := deps.Service.RevokeOwnToken(ctx, p.TokenID, in.ID, ""); rerr != nil {
					return TokenRevokeOut{}, mapAuthError(rerr)
				}
				return TokenRevokeOut{}, nil
			}
			return TokenRevokeOut{}, op.Errorf(op.CodeUnauthorized, "authentication required")
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[op.NoBody, SessionListOut]{
		Name: "auth.session.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/settings/security",
		Scope: op.ScopeRepoRead, Summary: "List active sessions",
		Description: "Session-only. Shows session age and marks the current one.",
		Problems:    []op.Code{op.CodeUnauthorized},
		ETag: func(out SessionListOut) string {
			return fmt.Sprintf("sessions-%d", len(out.Sessions))
		},
		Handler: func(ctx context.Context, p op.Principal, _ op.NoBody) (SessionListOut, error) {
			userID, rowid, err := auth.RequireSession(p)
			if err != nil {
				return SessionListOut{}, mapAuthError(err)
			}
			sessions, err := deps.Service.ListSessions(ctx, userID)
			if err != nil {
				return SessionListOut{}, mapAuthError(err)
			}
			out := SessionListOut{}
			if enabled, _, serr := deps.Service.TOTPStatus(ctx, userID); serr == nil {
				out.TOTPEnabled = enabled
			}
			out.OIDCProviders = deps.Service.OIDCProviders()
			if linked, lerr := deps.Service.OIDCLinkedProviders(ctx, userID); lerr == nil {
				out.OIDCLinked = linked
			}
			for _, s := range sessions {
				prefix := s.IDHash
				if len(prefix) > 12 {
					prefix = prefix[:12]
				}
				out.Sessions = append(out.Sessions, SessionItem{
					Prefix: prefix, CreatedAt: s.CreatedAt, LastSeenAt: s.LastSeenAt, Current: s.RowID == rowid,
				})
			}
			return out, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[SessionRevokeIn, SessionRevokeOut]{
		Name: "auth.session.revoke", Kind: op.KindMutation, Method: http.MethodPost, Path: "/settings/security/sessions/{id}/revoke",
		Scope: op.ScopeRepoRead, Summary: "Revoke a session",
		Description: "Session-only. Revoking the current session also clears its cookie.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeNotFound},
		Location:    func(SessionRevokeOut) string { return "/settings/security" },
		Handler: func(ctx context.Context, p op.Principal, in SessionRevokeIn) (SessionRevokeOut, error) {
			userID, rowid, err := auth.RequireSession(p)
			if err != nil {
				return SessionRevokeOut{}, mapAuthError(err)
			}
			ok, rerr := deps.Service.RevokeSessionByPrefix(ctx, userID, in.ID)
			if rerr != nil {
				return SessionRevokeOut{}, mapAuthError(rerr)
			}
			if !ok {
				return SessionRevokeOut{}, op.Errorf(op.CodeNotFound, "unknown session")
			}
			if _, terr := deps.Service.GetSessionByID(ctx, rowid); terr != nil {
				op.SetCookie(ctx, auth.ClearSessionCookie())
			}
			return SessionRevokeOut{}, nil
		},
	}); err != nil {
		return err
	}
	return nil
}

func RegisterAuthViews(v *op.Views) {
	op.PageView(v, "auth.login.page", func(w io.Writer, c op.ViewCtx, _ LoginPageOut) error {
		body := heading("Sign in") + errorList(c.Errors) +
			formOpen("/login", c.CSRF) +
			textField("username", "Username", c.Input.Get("username"), false) +
			textField("password", "Password", "", true) +
			submitField("Sign in") +
			para("Locked out? Ask your admin: rukie admin user reset-password --user <name>")
		_, err := io.WriteString(w, pageShell("Sign in", body))
		return err
	})
	v.Form("auth.login", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Sign in") + errorList(c.Errors) +
			formOpen("/login", c.CSRF) +
			textField("username", "Username", c.Input.Get("username"), false) +
			textField("password", "Password", "", true) +
			submitField("Sign in")
		_, err := io.WriteString(w, pageShell("Sign in", body))
		return err
	})
	op.PageView(v, "auth.logout.page", func(w io.Writer, c op.ViewCtx, _ LogoutOut) error {
		body := heading("Sign out") + para("Sign out of this session?") +
			formOpen("/logout", c.CSRF) + submitField("Sign out")
		_, err := io.WriteString(w, pageShell("Sign out", body))
		return err
	})
	v.Form("auth.logout", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Sign out") + errorList(c.Errors) +
			formOpen("/logout", c.CSRF) + submitField("Sign out")
		_, err := io.WriteString(w, pageShell("Sign out", body))
		return err
	})
	op.PageView(v, "auth.session.list", func(w io.Writer, c op.ViewCtx, out SessionListOut) error {
		var b strings.Builder
		b.WriteString(heading("Security"))
		b.WriteString(errorList(c.Errors))
		b.WriteString("<h2>Change password</h2>\n")
		b.WriteString(formOpen("/settings/security/password", c.CSRF) +
			textField("current", "Current password", "", true) +
			textField("next", "New password (12+ characters)", "", true) +
			submitField("Change password"))
		b.WriteString("<h2>Second factor</h2>\n")
		if out.TOTPEnabled {
			b.WriteString(para("A second factor is enabled on this account."))
		} else {
			b.WriteString(para("No second factor is enrolled."))
		}
		b.WriteString("<p><a href=\"/settings/security/totp\">Manage second factor and recovery codes</a></p>\n")
		b.WriteString(formOpen("/settings/security/totp", c.CSRF) + submitField("Enroll a second factor"))
		if out.TOTPEnabled {
			b.WriteString(formOpen("/settings/security/totp/disable", c.CSRF) + submitField("Disable second factor"))
			b.WriteString(formOpen("/settings/security/recovery", c.CSRF) + submitField("Regenerate recovery codes"))
		}
		b.WriteString("<h2>Linked identities</h2>\n<ul>\n")
		for _, issuer := range out.OIDCLinked {
			b.WriteString("<li>" + html.EscapeString(issuer) + "</li>\n")
		}
		b.WriteString("</ul>\n")
		for _, provider := range out.OIDCProviders {
			b.WriteString("<p><a href=\"/login/oidc/" + html.EscapeString(provider) + "\">Log in via " + html.EscapeString(provider) + "</a></p>\n")
			b.WriteString(formOpen("/settings/security/oidc/link", c.CSRF) +
				hiddenField("provider", provider) +
				submitField("Link "+provider))
		}
		b.WriteString("<h2>Sessions</h2>\n<ul>\n")
		for _, s := range out.Sessions {
			mark := ""
			if s.Current {
				mark = " (current)"
			}
			b.WriteString("<li>" + s.Prefix + mark +
				" <form method=\"post\" action=\"/settings/security/sessions/" + s.Prefix + "/revoke\">\n" +
				"<input type=\"hidden\" name=\"_csrf\" value=\"" + c.CSRF + "\">\n" +
				"<input type=\"hidden\" name=\"_idem\" value=\"" + newIdem() + "\">\n" +
				"<button type=\"submit\">Revoke</button>\n</form></li>\n")
		}
		b.WriteString("</ul>\n")
		_, err := io.WriteString(w, pageShell("Security", b.String()))
		return err
	})
	v.Form("auth.password.change", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Change password") + errorList(c.Errors) +
			formOpen("/settings/security/password", c.CSRF) +
			textField("current", "Current password", "", true) +
			textField("next", "New password (12+ characters)", "", true) +
			submitField("Change password")
		_, err := io.WriteString(w, pageShell("Change password", body))
		return err
	})
	op.PageView(v, "auth.token.list", func(w io.Writer, c op.ViewCtx, out TokenListOut) error {
		var b strings.Builder
		b.WriteString(heading("Agent tokens"))
		b.WriteString(errorList(c.Errors))
		if out.Secret != "" {
			b.WriteString("<p>New token <code>" + out.Reveal + "</code>, shown once:</p>\n")
			b.WriteString("<p><code>" + out.Secret + "</code></p>\n")
		}
		b.WriteString("<h2>Mint a token</h2>\n")
		b.WriteString(formOpen("/settings/tokens", c.CSRF) +
			textField("name", "Name", "", false) +
			textField("scopes", "Scopes (space-separated)", "repo:read repo:write", false) +
			submitField("Mint token"))
		b.WriteString("<h2>Tokens</h2>\n<ul>\n")
		for _, tok := range out.Tokens {
			b.WriteString("<li>" + tok.Prefix + " " + tok.Name +
				" <form method=\"post\" action=\"/settings/tokens/" + tok.Prefix + "/revoke\">\n" +
				"<input type=\"hidden\" name=\"_csrf\" value=\"" + c.CSRF + "\">\n" +
				"<input type=\"hidden\" name=\"_idem\" value=\"" + newIdem() + "\">\n" +
				"<button type=\"submit\">Revoke</button>\n</form></li>\n")
		}
		b.WriteString("</ul>\n")
		_, err := io.WriteString(w, pageShell("Agent tokens", b.String()))
		return err
	})
	v.Form("auth.token.create", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Mint a token") + errorList(c.Errors) +
			formOpen("/settings/tokens", c.CSRF) +
			textField("name", "Name", "", false) +
			textField("scopes", "Scopes (space-separated)", "repo:read repo:write", false) +
			submitField("Mint token")
		_, err := io.WriteString(w, pageShell("Mint a token", body))
		return err
	})
}
