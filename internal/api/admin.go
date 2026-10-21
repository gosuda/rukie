package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
)

type AdminCreateBody struct {
	Name     string `json:"name" minLength:"1" maxLength:"39"`
	Email    string `json:"email,omitempty" maxLength:"255"`
	FullName string `json:"full_name,omitempty" maxLength:"255"`
	Password string `json:"password,omitempty" format:"password" maxLength:"512"`
	Random   bool   `json:"random,omitempty"`
	IsAdmin  bool   `json:"is_admin,omitempty"`
}

type AdminCreateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           AdminCreateBody
}

type AdminCreateOut struct {
	Name         string `json:"name"`
	TempPassword string `json:"temp_password,omitempty"`
}

type AdminResetBody struct {
	Password   string `json:"password,omitempty" format:"password" maxLength:"512"`
	Random     bool   `json:"random,omitempty"`
	KeepTokens bool   `json:"keep_tokens,omitempty"`
}

type AdminResetIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	User           string `path:"user"`
	Body           AdminResetBody
}

type AdminResetOut struct {
	TempPassword string `json:"temp_password,omitempty"`
}

type AdminUnlockIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	User           string `path:"user"`
}

type AdminUnlockOut struct{}

type AdminUpdateBody struct {
	FullName *string `json:"full_name,omitempty" maxLength:"255"`
	Email    *string `json:"email,omitempty" maxLength:"255"`
	IsActive *bool   `json:"is_active,omitempty"`
}

type AdminUpdateIn struct {
	Body           AdminUpdateBody
	IdempotencyKey string `header:"Idempotency-Key"`
	IfMatch        string `header:"If-Match"`
	User           string `path:"user"`
}

type AdminUpdateOut struct {
	Name     string `json:"name"`
	IsActive bool   `json:"is_active"`
}

type AdminListIn struct {
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

type AdminListItem struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	IsAdmin  bool   `json:"is_admin"`
	IsActive bool   `json:"is_active"`
}

type AdminListOut struct {
	Next  string          `json:"next,omitempty"`
	Users []AdminListItem `json:"users"`
}

func adminETag(out AdminListOut) string {
	h := 0
	for _, u := range out.Users {
		h = h*31 + len(u.Name)
	}
	return fmt.Sprintf("admins-%d-%d", len(out.Users), h)
}

func listUsers(ctx context.Context, deps Deps, cursor string, limit int) (AdminListOut, error) {
	users, next, err := deps.Service.ListUsers(ctx, cursor, limit)
	if err != nil {
		return AdminListOut{}, mapAuthError(err)
	}
	out := AdminListOut{Next: next}
	for _, u := range users {
		out.Users = append(out.Users, AdminListItem{Name: u.Name, FullName: u.FullName, IsAdmin: u.IsAdmin, IsActive: u.IsActive})
	}
	return out, nil
}

func RegisterAdmin(t *op.Table, deps Deps) error {
	if err := op.Register(t, op.Op[AdminListIn, AdminListOut]{
		Name: "admin.user.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/admin/users",
		Scope: op.ScopeAdminRead, Summary: "List users for administration",
		Description: "Site admins only. Cursor pages by username.",
		Problems:    []op.Code{op.CodeForbidden},
		ETag:        adminETag,
		Pagination: &op.Pagination[AdminListOut]{Default: 25, Max: 100, Next: func(out AdminListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: func(ctx context.Context, p op.Principal, in AdminListIn) (AdminListOut, error) {
			if _, err := requireAdmin(ctx, deps.Service, p); err != nil {
				return AdminListOut{}, err
			}
			return listUsers(ctx, deps, in.Cursor, in.Limit)
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[AdminCreateIn, AdminCreateOut]{
		Name: "admin.user.create", Kind: op.KindMutation, Method: http.MethodPost, Path: "/admin/users",
		Scope: op.ScopeAdminWrite, Summary: "Create a user account",
		Description: "Site admins only. First admin comes from rukie bootstrap instead.",
		Problems:    []op.Code{op.CodeForbidden, op.CodeValidation, op.CodeConflict},
		Location:    func(AdminCreateOut) string { return "/admin/users" },
		Handler: func(ctx context.Context, p op.Principal, in AdminCreateIn) (AdminCreateOut, error) {
			admin, err := requireAdmin(ctx, deps.Service, p)
			if err != nil {
				return AdminCreateOut{}, err
			}
			if in.Body.Password == "" && !in.Body.Random {
				return AdminCreateOut{}, op.Errorf(op.CodeValidation, "password or random is required")
			}
			if derr := rejectTakenOrgName(ctx, deps, in.Body.Name); derr != nil {
				return AdminCreateOut{}, derr
			}
			if in.Body.Random {
				_, temp, cerr := deps.Service.CreateUserRandom(ctx, admin.ID, auth.CreateUserRandomInput{
					Name: in.Body.Name, Email: in.Body.Email, FullName: in.Body.FullName, IsAdmin: in.Body.IsAdmin,
				}, "")
				if cerr != nil {
					return AdminCreateOut{}, mapAuthError(cerr)
				}
				return AdminCreateOut{Name: in.Body.Name, TempPassword: temp}, nil
			}
			if _, cerr := deps.Service.CreateUser(ctx, admin.ID, auth.CreateUserInput{
				Name: in.Body.Name, Email: in.Body.Email, FullName: in.Body.FullName, Password: in.Body.Password, IsAdmin: in.Body.IsAdmin,
			}, ""); cerr != nil {
				return AdminCreateOut{}, mapAuthError(cerr)
			}
			return AdminCreateOut{Name: in.Body.Name}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[AdminResetIn, AdminResetOut]{
		Name: "admin.user.reset-password", Kind: op.KindMutation, Method: http.MethodPost, Path: "/admin/users/{user}/reset-password",
		Scope: op.ScopeAdminWrite, Summary: "Reset a user password",
		Description: "Site admins only. Revokes sessions and, unless kept, scoped tokens.",
		Problems:    []op.Code{op.CodeForbidden, op.CodeValidation, op.CodeNotFound},
		Location:    func(AdminResetOut) string { return "/admin/users" },
		Handler: func(ctx context.Context, p op.Principal, in AdminResetIn) (AdminResetOut, error) {
			admin, err := requireAdmin(ctx, deps.Service, p)
			if err != nil {
				return AdminResetOut{}, err
			}
			if in.Body.Password == "" && !in.Body.Random {
				return AdminResetOut{}, op.Errorf(op.CodeValidation, "password or random is required")
			}
			temp, err := deps.Service.ResetPassword(ctx, admin.ID, auth.ResetPasswordInput{
				Target: in.User, Password: in.Body.Password, Random: in.Body.Random, RevokeTokens: !in.Body.KeepTokens,
			}, "")
			if err != nil {
				return AdminResetOut{}, mapAuthError(err)
			}
			return AdminResetOut{TempPassword: temp}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[AdminUnlockIn, AdminUnlockOut]{
		Name: "admin.user.unlock", Kind: op.KindMutation, Method: http.MethodPost, Path: "/admin/users/{user}/unlock",
		Scope: op.ScopeAdminWrite, Summary: "Clear a user lockout",
		Description: "Site admins only. Clears failed-login count and lockout.",
		Problems:    []op.Code{op.CodeForbidden, op.CodeNotFound},
		Location:    func(AdminUnlockOut) string { return "/admin/users" },
		Handler: func(ctx context.Context, p op.Principal, in AdminUnlockIn) (AdminUnlockOut, error) {
			admin, err := requireAdmin(ctx, deps.Service, p)
			if err != nil {
				return AdminUnlockOut{}, err
			}
			if err := deps.Service.UnlockUser(ctx, admin.ID, in.User, ""); err != nil {
				return AdminUnlockOut{}, mapAuthError(err)
			}
			return AdminUnlockOut{}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[AdminUpdateIn, AdminUpdateOut]{
		Name: "admin.user.update", Kind: op.KindMutation, Method: http.MethodPost, Path: "/admin/users/{user}",
		Scope: op.ScopeAdminWrite, Summary: "Update a user account",
		Description: "Site admins only. Requires a current If-Match entity tag. Refuses the last active admin.",
		Problems:    []op.Code{op.CodeForbidden, op.CodeValidation, op.CodeNotFound, op.CodeConflict, op.CodeStale, op.CodePreconditionReq},
		Concurrency: op.ConcurrencyIfMatchRequired,
		ETag: func(out AdminUpdateOut) string {
			return "\"admin-" + out.Name + "\""
		},
		Location: func(AdminUpdateOut) string { return "/admin/users" },
		Handler: func(ctx context.Context, p op.Principal, in AdminUpdateIn) (AdminUpdateOut, error) {
			admin, err := requireAdmin(ctx, deps.Service, p)
			if err != nil {
				return AdminUpdateOut{}, err
			}
			u, err := deps.Service.UpdateUser(ctx, admin.ID, auth.UpdateUserInput{
				Target: in.User, FullName: in.Body.FullName, Email: in.Body.Email, IsActive: in.Body.IsActive, IfMatch: in.IfMatch,
			}, "")
			if err != nil {
				return AdminUpdateOut{}, mapAuthError(err)
			}
			return AdminUpdateOut{Name: u.Name, IsActive: u.IsActive}, nil
		},
	}); err != nil {
		return err
	}
	return nil
}

func RegisterAdminViews(v *op.Views) {
	op.PageView(v, "admin.user.list", func(w io.Writer, c op.ViewCtx, out AdminListOut) error {
		var b strings.Builder
		b.WriteString(heading("Users"))
		b.WriteString(errorList(c.Errors))
		b.WriteString("<h2>Create user</h2>\n")
		b.WriteString(formOpen("/admin/users", c.CSRF) +
			textField("name", "Username", "", false) +
			textField("password", "Password", "", true) +
			checkField("random", "Random temporary password", false) +
			checkField("is_admin", "Site admin", false) +
			submitField("Create user"))
		b.WriteString("<h2>Users</h2>\n<ul>\n")
		for _, u := range out.Users {
			role := ""
			if u.IsAdmin {
				role = " (admin)"
			}
			state := ""
			if !u.IsActive {
				state = " (inactive)"
			}
			b.WriteString("<li>" + u.Name + role + state +
				" <form method=\"post\" action=\"/admin/users/" + u.Name + "/unlock\">\n" +
				"<input type=\"hidden\" name=\"_csrf\" value=\"" + c.CSRF + "\">\n" +
				"<input type=\"hidden\" name=\"_idem\" value=\"" + newIdem() + "\">\n" +
				"<button type=\"submit\">Unlock</button>\n</form></li>\n")
		}
		b.WriteString("</ul>\n")
		_, err := io.WriteString(w, pageShell("Users", b.String()))
		return err
	})
	v.Form("admin.user.create", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Create user") + errorList(c.Errors) +
			formOpen("/admin/users", c.CSRF) +
			textField("name", "Username", c.Input.Get("name"), false) +
			textField("password", "Password", "", true) +
			checkField("random", "Random temporary password", false) +
			checkField("is_admin", "Site admin", false) +
			submitField("Create user")
		_, err := io.WriteString(w, pageShell("Create user", body))
		return err
	})
}
