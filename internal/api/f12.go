package api

import (
	"cmp"
	"context"
	"errors"
	"html"
	"io"
	"net/http"
	"strings"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
)

type TOTPVerifyBody struct {
	Code string `json:"code" minLength:"6" maxLength:"24"`
}

type TOTPVerifyIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           TOTPVerifyBody
}

type TOTPVerifyOut struct {
	Username      string   `json:"username"`
	Reveal        string   `json:"-"`
	RecoveryCodes []string `json:"recovery_codes,omitempty"`
	RecoveryUsed  bool     `json:"recovery_used,omitzero"`
}

type TOTPChallengePageOut struct {
	Username string `json:"username"`
}

type TOTPEnrollIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
}

type TOTPEnrollOut struct {
	URI    string `json:"uri,omitempty"`
	Secret string `json:"secret,omitempty"`
	Reveal string `json:"-"`
}

type TOTPEmptyIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
}

type TOTPEmptyOut struct{}

type TOTPRegenOut struct {
	Reveal string   `json:"-"`
	Codes  []string `json:"codes,omitempty"`
}

type TOTPPageIn struct {
	Reveal string `query:"reveal"`
}

type TOTPPageOut struct {
	RevealKind string   `json:"reveal_kind,omitempty"`
	URI        string   `json:"uri,omitempty"`
	Secret     string   `json:"secret,omitempty"`
	Providers  []string `json:"providers"`
	Linked     []string `json:"linked"`
	Codes      []string `json:"codes,omitempty"`
	Enabled    bool     `json:"enabled"`
	HasPending bool     `json:"has_pending"`
}

type OIDCStartIn struct {
	Provider string `path:"provider"`
	Next     string `query:"next"`
}

type OIDCStartOut struct {
	Provider string `json:"provider"`
	AuthURL  string `json:"auth_url"`
}

type OIDCCallbackIn struct {
	Provider string `path:"provider"`
	Code     string `query:"code"`
	State    string `query:"state"`
}

type OIDCCallbackOut struct {
	Username string `json:"username"`
	ReturnTo string `json:"return_to"`
	IsNew    bool   `json:"is_new,omitzero"`
	NeedTOTP bool   `json:"need_totp,omitzero"`
	Linked   bool   `json:"linked,omitzero"`
}

type OIDCLinkBody struct {
	Provider string `json:"provider" minLength:"1" maxLength:"64"`
}

type OIDCLinkIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           OIDCLinkBody
}

type OIDCLinkOut struct {
	AuthURL string `json:"auth_url"`
}

type TOTPResetBody struct {
	RevokeSessions bool `json:"revoke_sessions"`
}

type TOTPResetIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	User           string `path:"user"`
	Body           TOTPResetBody
}

type TOTPResetOut struct{}

func mapF12Error(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, auth.ErrTOTPNotEnrolled):
		return op.Errorf(op.CodeValidation, "totp is not enrolled")
	case errors.Is(err, auth.ErrOIDCUnknownProvider):
		return op.Errorf(op.CodeNotFound, "unknown oidc provider")
	case errors.Is(err, auth.ErrOIDCBadState):
		return op.Errorf(op.CodeValidation, "invalid or expired oidc state")
	case errors.Is(err, auth.ErrOIDCIPBlocked):
		return op.Errorf(op.CodeForbidden, "oidc logins from this address are temporarily blocked")
	case errors.Is(err, auth.ErrOIDCBadToken):
		return op.Errorf(op.CodeUnauthorized, "invalid identity token")
	case errors.Is(err, auth.ErrOIDCEmailLink):
		return op.Errorf(op.CodeConflict, "an account with this email exists: sign in, then link")
	case errors.Is(err, auth.ErrOIDCSignupDisabled):
		return op.Errorf(op.CodeForbidden, "signup is disabled: ask an admin to link your account")
	case errors.Is(err, auth.ErrOIDCLinkConflict):
		return op.Errorf(op.CodeConflict, "this identity is linked to a different account")
	default:
		return mapAuthError(err)
	}
}

func oidcSessionUser(ctx context.Context, svc *auth.Service, p op.Principal) int64 {
	if userID, _, err := auth.RequireSession(p); err == nil {
		if u, uerr := svc.GetUserByID(ctx, userID); uerr == nil && u.IsActive {
			return userID
		}
	}
	return 0
}

func stashReveal(deps Deps, kind string, payload map[string]any) string {
	raw, _ := jsonv2.Marshal(payload, jsonv1.DefaultOptionsV1())
	key := newIdem()
	deps.Reveals.Store("f12-"+key, kind+"\n"+string(raw))
	return key
}

func takeReveal(deps Deps, key string) (string, map[string]any, bool) {
	secret, ok := deps.Reveals.Take("f12-" + key)
	if !ok {
		return "", nil, false
	}
	kind, rest, _ := strings.Cut(secret, "\n")
	var payload map[string]any
	if err := jsonv2.Unmarshal([]byte(rest), &payload, jsonv1.DefaultOptionsV1()); err != nil {
		return "", nil, false
	}
	return kind, payload, true
}

// RegisterF12 wires the TOTP, recovery, OIDC, and admin reset operations.
func RegisterF12(t *op.Table, deps Deps) error {
	if err := op.Register(t, op.Op[TOTPVerifyIn, TOTPVerifyOut]{
		Name: "auth.totp.verify", Kind: op.KindMutation, Method: http.MethodPost, Path: "/login/totp",
		Scope: op.ScopePublicRead, Summary: "Verify a second-factor code",
		Description: "Accepts a TOTP code or a single-use recovery code on a pending session, then rotates to a full session.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeValidation},
		Location:    totpVerifyLocation,
		Handler: func(ctx context.Context, p op.Principal, in TOTPVerifyIn) (TOTPVerifyOut, error) {
			userID, rowid, err := auth.RequireSession(p)
			if err != nil {
				return TOTPVerifyOut{}, mapAuthError(err)
			}
			res, verr := deps.Service.VerifyTOTPByRowID(ctx, userID, rowid, in.Body.Code, op.ClientIP(ctx), "")
			if verr != nil {
				return TOTPVerifyOut{}, mapF12Error(verr)
			}
			if res.Secret != "" {
				op.SetCookie(ctx, auth.SessionCookie(res.Secret, time.UnixMilli(res.Session.AbsoluteExpiresAt), deps.now()))
			}
			u, _ := deps.Service.GetUserByID(ctx, userID)
			out := TOTPVerifyOut{Username: u.Name, RecoveryUsed: res.RecoveryUsed, RecoveryCodes: res.RecoveryCodes}
			if len(res.RecoveryCodes) > 0 {
				out.Reveal = stashReveal(deps, "codes", map[string]any{"codes": res.RecoveryCodes})
			}
			return out, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[op.NoBody, TOTPChallengePageOut]{
		Name: "auth.totpverify.page", Kind: op.KindRead, Method: http.MethodGet, Path: "/login/totp",
		Scope: op.ScopePublicRead, Summary: "Show the second-factor form", Hidden: true,
		Description: "Page-only. Renders the zero-JS second-factor form.",
		Handler: func(_ context.Context, _ op.Principal, _ op.NoBody) (TOTPChallengePageOut, error) {
			return TOTPChallengePageOut{}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[TOTPEnrollIn, TOTPEnrollOut]{
		Name: "auth.totp.enroll", Kind: op.KindMutation, Method: http.MethodPost, Path: "/settings/security/totp",
		Scope: op.ScopeRepoRead, Summary: "Stage a TOTP secret",
		Description: "Session with fresh auth. Returns the otpauth URI and manual key once; verifying a code enables it.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeValidation},
		Location:    func(out TOTPEnrollOut) string { return "/settings/security/totp?reveal=" + out.Reveal },
		Handler: func(ctx context.Context, p op.Principal, _ TOTPEnrollIn) (TOTPEnrollOut, error) {
			userID, err := auth.RequireFresh(ctx, deps.Service, p, deps.now())
			if err != nil {
				return TOTPEnrollOut{}, mapAuthError(err)
			}
			enr, eerr := deps.Service.EnrollTOTP(ctx, userID, op.ClientIP(ctx))
			if eerr != nil {
				return TOTPEnrollOut{}, mapF12Error(eerr)
			}
			out := TOTPEnrollOut{URI: enr.URI, Secret: enr.SecretBase32}
			out.Reveal = stashReveal(deps, "enroll", map[string]any{"uri": enr.URI, "secret": enr.SecretBase32})
			return out, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[TOTPEmptyIn, TOTPEmptyOut]{
		Name: "auth.totp.disable", Kind: op.KindMutation, Method: http.MethodPost, Path: "/settings/security/totp/disable",
		Scope: op.ScopeRepoRead, Summary: "Disable the second factor",
		Description: "Session with fresh auth. Clears the secret and burns recovery codes.",
		Problems:    []op.Code{op.CodeUnauthorized},
		Location:    func(TOTPEmptyOut) string { return "/settings/security" },
		Handler: func(ctx context.Context, p op.Principal, _ TOTPEmptyIn) (TOTPEmptyOut, error) {
			userID, err := auth.RequireFresh(ctx, deps.Service, p, deps.now())
			if err != nil {
				return TOTPEmptyOut{}, mapAuthError(err)
			}
			if derr := deps.Service.DisableTOTP(ctx, userID, op.ClientIP(ctx)); derr != nil {
				return TOTPEmptyOut{}, mapF12Error(derr)
			}
			return TOTPEmptyOut{}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[TOTPEmptyIn, TOTPRegenOut]{
		Name: "auth.recovery.regenerate", Kind: op.KindMutation, Method: http.MethodPost, Path: "/settings/security/recovery",
		Scope: op.ScopeRepoRead, Summary: "Regenerate recovery codes",
		Description: "Session with fresh auth. Invalidates every code and issues ten fresh ones, shown once.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeValidation},
		Location:    func(out TOTPRegenOut) string { return "/settings/security/totp?reveal=" + out.Reveal },
		Handler: func(ctx context.Context, p op.Principal, _ TOTPEmptyIn) (TOTPRegenOut, error) {
			userID, err := auth.RequireFresh(ctx, deps.Service, p, deps.now())
			if err != nil {
				return TOTPRegenOut{}, mapAuthError(err)
			}
			codes, rerr := deps.Service.RegenerateRecoveryCodes(ctx, userID, op.ClientIP(ctx))
			if rerr != nil {
				return TOTPRegenOut{}, mapF12Error(rerr)
			}
			out := TOTPRegenOut{Codes: codes}
			out.Reveal = stashReveal(deps, "codes", map[string]any{"codes": codes})
			return out, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[TOTPPageIn, TOTPPageOut]{
		Name: "auth.totp.page", Kind: op.KindRead, Method: http.MethodGet, Path: "/settings/security/totp",
		Scope: op.ScopeRepoRead, Summary: "Show second-factor settings", Hidden: true,
		Description: "Page-only. Shows enrollment state, the once-displayed secret, and linked providers.",
		Handler: func(ctx context.Context, p op.Principal, in TOTPPageIn) (TOTPPageOut, error) {
			userID, _, err := auth.RequireSession(p)
			if err != nil {
				return TOTPPageOut{}, mapAuthError(err)
			}
			enabled, pending, serr := deps.Service.TOTPStatus(ctx, userID)
			if serr != nil {
				return TOTPPageOut{}, mapF12Error(serr)
			}
			linked, _ := deps.Service.OIDCLinkedProviders(ctx, userID)
			out := TOTPPageOut{Enabled: enabled, HasPending: pending, Providers: deps.Service.OIDCProviders(), Linked: linked}
			if in.Reveal != "" {
				if kind, payload, ok := takeReveal(deps, in.Reveal); ok {
					out.RevealKind = kind
					if s, ok := payload["secret"].(string); ok {
						out.Secret = s
					}
					if u, ok := payload["uri"].(string); ok {
						out.URI = u
					}
					if c, ok := payload["codes"].([]any); ok {
						for _, item := range c {
							if s, ok := item.(string); ok {
								out.Codes = append(out.Codes, s)
							}
						}
					}
				}
			}
			return out, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[OIDCStartIn, OIDCStartOut]{
		Name: "auth.oidc.start", Kind: op.KindRead, Method: http.MethodGet, Path: "/login/oidc/{provider}",
		Scope: op.ScopePublicRead, Summary: "Begin an OIDC login",
		Description: "Mints single-use state with PKCE S256 and nonce. The page links on to the provider.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeForbidden},
		Handler: func(ctx context.Context, _ op.Principal, in OIDCStartIn) (OIDCStartOut, error) {
			redirect, browser, serr := deps.Service.StartOIDCLogin(ctx, auth.StartOIDCLoginInput{
				ProviderID: in.Provider, ReturnTo: cmp.Or(in.Next, "/"), IP: op.ClientIP(ctx),
			})
			if serr != nil {
				return OIDCStartOut{}, mapF12Error(serr)
			}
			op.SetCookie(ctx, auth.OIDCBrowserCookie(browser))
			return OIDCStartOut{Provider: in.Provider, AuthURL: redirect}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[OIDCCallbackIn, OIDCCallbackOut]{
		Name: "auth.oidc.callback", Kind: op.KindRead, Method: http.MethodGet, Path: "/login/oidc/{provider}/callback",
		Scope: op.ScopePublicRead, Summary: "Finish an OIDC login",
		Description: "Consumes single-use state, exchanges the code with PKCE, verifies nonce, and signs the user in.",
		Problems:    []op.Code{op.CodeValidation, op.CodeUnauthorized, op.CodeForbidden, op.CodeConflict, op.CodeNotFound},
		Handler: func(ctx context.Context, p op.Principal, in OIDCCallbackIn) (OIDCCallbackOut, error) {
			if in.Code == "" || in.State == "" {
				return OIDCCallbackOut{}, op.Errorf(op.CodeValidation, "code and state are required")
			}
			userID := oidcSessionUser(ctx, deps.Service, p)
			fresh := false
			if userID != 0 {
				if _, ferr := auth.RequireFresh(ctx, deps.Service, p, deps.now()); ferr == nil {
					fresh = true
				}
			}
			res, cerr := deps.Service.HandleOIDCCallback(ctx, auth.OIDCCallbackInput{
				ProviderID: in.Provider, Code: in.Code, State: in.State,
				IP: op.ClientIP(ctx), UserAgent: op.UserAgent(ctx),
				BrowserSecret: op.Cookie(ctx, auth.OIDCBrowserCookieName()),
				SessionUserID: userID, SessionFresh: fresh,
			})
			if cerr != nil {
				return OIDCCallbackOut{}, mapF12Error(cerr)
			}
			op.SetCookie(ctx, auth.ClearOIDCBrowserCookie())
			if res.Secret != "" {
				op.SetCookie(ctx, auth.SessionCookie(res.Secret, time.UnixMilli(res.Session.AbsoluteExpiresAt), deps.now()))
			}
			return OIDCCallbackOut{Username: res.User.Name, ReturnTo: cmp.Or(res.ReturnTo, "/"), IsNew: res.IsNew, NeedTOTP: res.NeedTOTP, Linked: res.Linked}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[OIDCLinkIn, OIDCLinkOut]{
		Name: "auth.oidc.link", Kind: op.KindMutation, Method: http.MethodPost, Path: "/settings/security/oidc/link",
		Scope: op.ScopeRepoRead, Summary: "Link an OIDC identity",
		Description: "Session with fresh auth. Starts a link-mode flow; posting the form redirects to the provider.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeNotFound},
		Location:    func(out OIDCLinkOut) string { return out.AuthURL },
		Handler: func(ctx context.Context, p op.Principal, in OIDCLinkIn) (OIDCLinkOut, error) {
			userID, err := auth.RequireFresh(ctx, deps.Service, p, deps.now())
			if err != nil {
				return OIDCLinkOut{}, mapAuthError(err)
			}
			redirect, browser, serr := deps.Service.StartOIDCLogin(ctx, auth.StartOIDCLoginInput{
				ProviderID: in.Body.Provider, ReturnTo: "/settings/security", IP: op.ClientIP(ctx), LinkUserID: userID,
			})
			if serr != nil {
				return OIDCLinkOut{}, mapF12Error(serr)
			}
			op.SetCookie(ctx, auth.OIDCBrowserCookie(browser))
			return OIDCLinkOut{AuthURL: redirect}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[TOTPResetIn, TOTPResetOut]{
		Name: "admin.user.totp-reset", Kind: op.KindMutation, Method: http.MethodPost, Path: "/admin/users/{user}/totp-reset",
		Scope: op.ScopeAdminWrite, Summary: "Reset a user second factor",
		Description: "Site admins only. Clears the TOTP secret, burns recovery codes, and optionally revokes sessions.",
		Problems:    []op.Code{op.CodeForbidden, op.CodeNotFound},
		Location:    func(TOTPResetOut) string { return "/admin/users" },
		Handler: func(ctx context.Context, p op.Principal, in TOTPResetIn) (TOTPResetOut, error) {
			admin, err := requireAdmin(ctx, deps.Service, p)
			if err != nil {
				return TOTPResetOut{}, err
			}
			if rerr := deps.Service.TOTPReset(ctx, admin.ID, in.User, in.Body.RevokeSessions, op.ClientIP(ctx)); rerr != nil {
				return TOTPResetOut{}, mapF12Error(rerr)
			}
			return TOTPResetOut{}, nil
		},
	}); err != nil {
		return err
	}
	return nil
}

func totpVerifyLocation(out TOTPVerifyOut) string {
	if out.Reveal != "" {
		return "/settings/security/totp?reveal=" + out.Reveal
	}
	return "/"
}

// RegisterF12Views renders the TOTP and OIDC forms and pages.
func RegisterF12Views(v *op.Views) {
	op.PageView(v, "auth.totpverify.page", func(w io.Writer, c op.ViewCtx, _ TOTPChallengePageOut) error {
		body := heading("Second factor") + errorList(c.Errors) +
			para("Enter the 6-digit code from your authenticator, or one unused recovery code.") +
			formOpen("/login/totp", c.CSRF) +
			textField("code", "Code", "", false) +
			submitField("Verify")
		_, err := io.WriteString(w, pageShell("Second factor", body))
		return err
	})
	v.Form("auth.totp.verify", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Second factor") + errorList(c.Errors) +
			formOpen("/login/totp", c.CSRF) +
			textField("code", "Code", "", false) +
			submitField("Verify")
		_, err := io.WriteString(w, pageShell("Second factor", body))
		return err
	})
	op.PageView(v, "auth.totp.page", func(w io.Writer, c op.ViewCtx, out TOTPPageOut) error {
		var b strings.Builder
		b.WriteString(heading("Second factor"))
		b.WriteString(errorList(c.Errors))
		switch out.RevealKind {
		case "enroll":
			b.WriteString("<h2>Enrollment secret</h2>\n")
			b.WriteString(para("Add this secret to your authenticator, then verify a code below. It is shown once."))
			b.WriteString("<p>Manual key: <code>" + html.EscapeString(out.Secret) + "</code></p>\n")
			b.WriteString("<p>URI: <code>" + html.EscapeString(out.URI) + "</code></p>\n")
			b.WriteString(formOpen("/login/totp", c.CSRF) +
				textField("code", "Code", "", false) +
				submitField("Verify and enable"))
		case "codes":
			b.WriteString("<h2>Recovery codes</h2>\n")
			b.WriteString(para("Each code works once. They are shown once."))
			b.WriteString("<ul>\n")
			for _, code := range out.Codes {
				b.WriteString("<li><code>" + html.EscapeString(code) + "</code></li>\n")
			}
			b.WriteString("</ul>\n")
		}
		switch {
		case out.Enabled:
			b.WriteString(para("A second factor is enabled on this account."))
		case !out.HasPending:
			b.WriteString(formOpen("/settings/security/totp", c.CSRF) + submitField("Enroll a second factor"))
		default:
			b.WriteString(para("An enrollment is pending: verify a code to enable it."))
			b.WriteString(formOpen("/login/totp", c.CSRF) +
				textField("code", "Code", "", false) +
				submitField("Verify and enable"))
		}
		if out.Enabled {
			b.WriteString("<h2>Disable</h2>\n")
			b.WriteString(formOpen("/settings/security/totp/disable", c.CSRF) + submitField("Disable second factor"))
			b.WriteString("<h2>Recovery codes</h2>\n")
			b.WriteString(formOpen("/settings/security/recovery", c.CSRF) + submitField("Regenerate recovery codes"))
		}
		b.WriteString("<h2>Linked identities</h2>\n<ul>\n")
		for _, issuer := range out.Linked {
			b.WriteString("<li>" + html.EscapeString(issuer) + "</li>\n")
		}
		b.WriteString("</ul>\n<h2>Link an identity</h2>\n")
		for _, provider := range out.Providers {
			b.WriteString(formOpen("/settings/security/oidc/link", c.CSRF) +
				hiddenField("provider", provider) +
				submitField("Link "+provider))
		}
		_, err := io.WriteString(w, pageShell("Second factor", b.String()))
		return err
	})
	v.Form("auth.totp.enroll", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Enroll a second factor") + errorList(c.Errors) +
			formOpen("/settings/security/totp", c.CSRF) +
			submitField("Enroll a second factor")
		_, err := io.WriteString(w, pageShell("Enroll a second factor", body))
		return err
	})
	v.Form("auth.totp.disable", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Disable second factor") + errorList(c.Errors) +
			formOpen("/settings/security/totp/disable", c.CSRF) +
			submitField("Disable second factor")
		_, err := io.WriteString(w, pageShell("Disable second factor", body))
		return err
	})
	v.Form("auth.recovery.regenerate", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Regenerate recovery codes") + errorList(c.Errors) +
			formOpen("/settings/security/recovery", c.CSRF) +
			submitField("Regenerate recovery codes")
		_, err := io.WriteString(w, pageShell("Regenerate recovery codes", body))
		return err
	})
	op.PageView(v, "auth.oidc.start", func(w io.Writer, c op.ViewCtx, out OIDCStartOut) error {
		body := heading("Log in via "+out.Provider) + errorList(c.Errors) +
			para("Continue to the identity provider to sign in.") +
			"<p><a href=\"" + html.EscapeString(out.AuthURL) + "\">Continue to " + html.EscapeString(out.Provider) + "</a></p>\n"
		_, err := io.WriteString(w, pageShell("Log in", body))
		return err
	})
	op.PageView(v, "auth.oidc.callback", func(w io.Writer, c op.ViewCtx, out OIDCCallbackOut) error {
		var b strings.Builder
		b.WriteString(heading("Signed in"))
		b.WriteString(errorList(c.Errors))
		continueTo := out.ReturnTo
		if continueTo == "" || continueTo == "/" {
			continueTo = "/settings/security"
			if !out.Linked {
				continueTo = "/"
			}
		}
		switch {
		case out.Linked:
			b.WriteString(para("The identity is now linked to " + out.Username + "."))
		case out.NeedTOTP:
			b.WriteString(para("Signed in as " + out.Username + ". Enter a second-factor code."))
			b.WriteString("<p><a href=\"/login/totp\">Continue to second factor</a></p>\n")
		default:
			b.WriteString(para("Signed in as " + out.Username + "."))
		}
		b.WriteString("<p><a href=\"" + html.EscapeString(continueTo) + "\">Continue</a></p>\n")
		_, err := io.WriteString(w, pageShell("Signed in", b.String()))
		return err
	})
	v.Form("auth.oidc.link", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Link an identity") + errorList(c.Errors) +
			formOpen("/settings/security/oidc/link", c.CSRF) +
			textField("provider", "Provider", "", false) +
			submitField("Link identity")
		_, err := io.WriteString(w, pageShell("Link an identity", body))
		return err
	})
	v.Form("admin.user.totp-reset", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Reset second factor") + errorList(c.Errors) +
			formOpen(c.Path, c.CSRF) +
			checkField("revoke_sessions", "Revoke sessions", false) +
			submitField("Reset second factor")
		_, err := io.WriteString(w, pageShell("Reset second factor", body))
		return err
	})
}
