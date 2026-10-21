package api

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
)

type UserGetIn struct {
	Owner string `path:"owner"`
}

type UserGetOut struct {
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	Website     string `json:"website,omitempty"`
	Visibility  string `json:"visibility,omitempty"`
	Name        string `json:"name"`
	FullName    string `json:"full_name"`
	Kind        string `json:"kind,omitempty"`
	Role        string `json:"role,omitempty"`
	UpdatedAt   int64  `json:"updated_at,omitzero"`
}

type SelfUpdateBody struct {
	FullName *string `json:"full_name,omitempty" maxLength:"255"`
	Email    *string `json:"email,omitempty" maxLength:"255"`
}

type SelfUpdateIn struct {
	Body           SelfUpdateBody
	IdempotencyKey string `header:"Idempotency-Key"`
	IfMatch        string `header:"If-Match"`
}

type SelfUpdateOut struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Email    string `json:"email"`
}

type SelfDeleteIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	IfMatch        string `header:"If-Match"`
}

type SelfDeleteOut struct{}

type SettingsPageOut struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Email    string `json:"email"`
	ETag     string `json:"etag"`
}

func RegisterUser(t *op.Table, deps Deps) error {
	if err := op.Register(t, op.Op[op.NoBody, SettingsPageOut]{
		Name: "settings.page", Kind: op.KindRead, Method: http.MethodGet, Path: "/settings",
		Scope: op.ScopePublicRead, Summary: "Show profile settings", Hidden: true,
		Description: "Page-only. Renders the zero-JS profile form.",
		ETag: func(out SettingsPageOut) string {
			return out.ETag
		},
		Handler: func(ctx context.Context, p op.Principal, _ op.NoBody) (SettingsPageOut, error) {
			userID, _, err := auth.RequireSession(p)
			if err != nil {
				return SettingsPageOut{}, mapAuthError(err)
			}
			u, err := deps.Service.GetUserByID(ctx, userID)
			if err != nil {
				return SettingsPageOut{}, mapAuthError(err)
			}
			tag, err := deps.Service.ETagFor(ctx, userID)
			if err != nil {
				return SettingsPageOut{}, mapAuthError(err)
			}
			return SettingsPageOut{Name: u.Name, FullName: u.FullName, Email: u.Email, ETag: tag}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[UserGetIn, UserGetOut]{
		Name: "user.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}",
		Scope: op.ScopePublicRead, Summary: "Show a user profile",
		Description: "Public directory entry. Unknown or inactive users return 404.",
		Problems:    []op.Code{op.CodeNotFound},
		ETag: func(out UserGetOut) string {
			if out.Kind == "org" {
				return fmt.Sprintf("\"org-%s-%d\"", out.Name, out.UpdatedAt)
			}
			return "\"user-" + out.Name + "\""
		},
		Handler: func(ctx context.Context, p op.Principal, in UserGetIn) (UserGetOut, error) {
			u, err := deps.Service.GetUser(ctx, in.Owner)
			if err == nil {
				if !u.IsActive {
					return UserGetOut{}, op.Errorf(op.CodeNotFound, "unknown user")
				}
				return UserGetOut{Name: u.Name, FullName: u.FullName}, nil
			}
			if deps.Store == nil {
				return UserGetOut{}, op.Errorf(op.CodeNotFound, "unknown user")
			}
			return orgProfileForOwner(ctx, deps, p, in.Owner)
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[SelfUpdateIn, SelfUpdateOut]{
		Name: "user.self.update", Kind: op.KindMutation, Method: http.MethodPost, Path: "/settings",
		Scope: op.ScopeRepoRead, Summary: "Update your profile",
		Description: "Session-only. Requires a current If-Match entity tag.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeValidation, op.CodeStale, op.CodePreconditionReq},
		Concurrency: op.ConcurrencyIfMatchRequired,
		ETag: func(out SelfUpdateOut) string {
			return "\"user-" + out.Name + "\""
		},
		Location: func(SelfUpdateOut) string { return "/settings" },
		Handler: func(ctx context.Context, p op.Principal, in SelfUpdateIn) (SelfUpdateOut, error) {
			userID, _, err := auth.RequireSession(p)
			if err != nil {
				return SelfUpdateOut{}, mapAuthError(err)
			}
			u, err := deps.Service.UpdateSelf(ctx, userID, auth.UpdateSelfInput{
				FullName: in.Body.FullName, Email: in.Body.Email, IfMatch: in.IfMatch,
			})
			if err != nil {
				return SelfUpdateOut{}, mapAuthError(err)
			}
			return SelfUpdateOut{Name: u.Name, FullName: u.FullName, Email: u.Email}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[SelfDeleteIn, SelfDeleteOut]{
		Name: "user.self.delete", Kind: op.KindMutation, Method: http.MethodPost, Path: "/settings/delete",
		Scope: op.ScopeRepoRead, Summary: "Delete your account",
		Description: "Session-only with fresh auth and a current If-Match entity tag. Refuses the last active admin.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeConflict, op.CodeStale, op.CodePreconditionReq},
		Location:    func(SelfDeleteOut) string { return "/login" },
		Handler: func(ctx context.Context, p op.Principal, in SelfDeleteIn) (SelfDeleteOut, error) {
			userID, err := auth.RequireFresh(ctx, deps.Service, p, deps.now())
			if err != nil {
				return SelfDeleteOut{}, mapAuthError(err)
			}
			if err := deps.Service.DeleteSelf(ctx, userID, in.IfMatch, ""); err != nil {
				return SelfDeleteOut{}, mapAuthError(err)
			}
			op.SetCookie(ctx, auth.ClearSessionCookie())
			return SelfDeleteOut{}, nil
		},
	}); err != nil {
		return err
	}
	return nil
}

func RegisterUserViews(v *op.Views) {
	op.PageView(v, "settings.page", func(w io.Writer, c op.ViewCtx, out SettingsPageOut) error {
		body := heading("Profile settings") + errorList(c.Errors) +
			formOpen("/settings", c.CSRF) +
			hiddenField("_etag", out.ETag) +
			textField("full_name", "Full name", out.FullName, false) +
			textField("email", "Email", out.Email, false) +
			submitField("Save profile") +
			"<h2>Delete account</h2>\n" +
			formOpen("/settings/delete", c.CSRF) +
			hiddenField("_etag", out.ETag) +
			submitField("Delete my account")
		_, err := io.WriteString(w, pageShell("Profile settings", body))
		return err
	})
	op.PageView(v, "user.get", func(w io.Writer, c op.ViewCtx, out UserGetOut) error {
		if out.Kind == "org" {
			return writeOrgProfile(w, c, OrgGetOut{Org: OrgJSON{
				Name: out.Name, DisplayName: out.DisplayName, Description: out.Description,
				Website: out.Website, Visibility: out.Visibility, UpdatedAt: out.UpdatedAt,
				Role: out.Role,
			}})
		}
		body := heading(out.Name) + para(out.FullName)
		_, err := io.WriteString(w, pageShell(out.Name, body))
		return err
	})
	v.Form("user.self.update", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Profile settings") + errorList(c.Errors) +
			formOpen("/settings", c.CSRF) +
			hiddenField("_etag", c.Input.Get("_etag")) +
			textField("full_name", "Full name", c.Input.Get("full_name"), false) +
			textField("email", "Email", c.Input.Get("email"), false) +
			submitField("Save profile")
		_, err := io.WriteString(w, pageShell("Profile settings", body))
		return err
	})
	v.Form("user.self.delete", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Delete account") + errorList(c.Errors) +
			formOpen("/settings/delete", c.CSRF) +
			hiddenField("_etag", c.Input.Get("_etag")) +
			submitField("Delete my account")
		_, err := io.WriteString(w, pageShell("Delete account", body))
		return err
	})
}
