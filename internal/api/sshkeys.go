package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
)

type SSHKeyAddBody struct {
	Key string `json:"key" minLength:"1" maxLength:"16384"`
}

type SSHKeyAddIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           SSHKeyAddBody
}

type SSHKeyAddOut struct {
	Fingerprint string `json:"fingerprint"`
	KeyType     string `json:"key_type"`
	Comment     string `json:"comment"`
	ID          int64  `json:"id"`
}

type SSHKeyItem struct {
	Fingerprint string `json:"fingerprint"`
	KeyType     string `json:"key_type"`
	Comment     string `json:"comment"`
	ID          int64  `json:"id"`
	CreatedAt   int64  `json:"created_at"`
	LastUsedAt  int64  `json:"last_used_at"`
}

type SSHKeyListOut struct {
	Keys []SSHKeyItem `json:"keys"`
}

type SSHKeyRemoveIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	ID             string `path:"id"`
}

type SSHKeyRemoveOut struct{}

func sshKeyItemOf(key auth.SSHKey) SSHKeyItem {
	return SSHKeyItem{
		Fingerprint: key.Fingerprint,
		KeyType:     key.KeyType,
		Comment:     key.Comment,
		ID:          key.ID,
		CreatedAt:   key.CreatedAt,
		LastUsedAt:  key.LastUsedAt,
	}
}

func mapSSHKeyError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "already registered") {
		return op.Errorf(op.CodeConflict, "%s", msg)
	}
	if strings.HasPrefix(msg, "ssh key") {
		return op.Errorf(op.CodeValidation, "%s", msg)
	}
	return mapAuthError(err)
}

// RegisterSSHKeys wires the SSH key ops over the shared auth service.
func RegisterSSHKeys(t *op.Table, deps Deps) error {
	if err := op.Register(t, op.Op[op.NoBody, SSHKeyListOut]{
		Name: "auth.sshkey.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/settings/keys",
		Scope: op.ScopeRepoRead, Summary: "List SSH public keys",
		Description: "Session-only with fresh auth. Shows fingerprints, types, and use times.",
		Problems:    []op.Code{op.CodeUnauthorized},
		ETag: func(out SSHKeyListOut) string {
			h := 0
			for _, key := range out.Keys {
				h = h*31 + int(key.ID)
			}
			return fmt.Sprintf("sshkeys-%d-%d", len(out.Keys), h)
		},
		Handler: func(ctx context.Context, p op.Principal, _ op.NoBody) (SSHKeyListOut, error) {
			userID, err := auth.RequireFresh(ctx, deps.Service, p, deps.now())
			if err != nil {
				return SSHKeyListOut{}, mapAuthError(err)
			}
			keys, err := deps.Service.ListSSHKeys(ctx, userID)
			if err != nil {
				return SSHKeyListOut{}, mapSSHKeyError(err)
			}
			out := SSHKeyListOut{}
			for _, key := range keys {
				out.Keys = append(out.Keys, sshKeyItemOf(key))
			}
			return out, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[SSHKeyAddIn, SSHKeyAddOut]{
		Name: "auth.sshkey.add", Kind: op.KindMutation, Method: http.MethodPost, Path: "/settings/keys",
		Scope: op.ScopeRepoRead, Summary: "Add an SSH public key",
		Description: "Session-only with fresh auth. Pastes one authorized_keys line; shows the fingerprint for confirmation.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeValidation, op.CodeConflict},
		Location:    func(SSHKeyAddOut) string { return "/settings/keys" },
		Handler: func(ctx context.Context, p op.Principal, in SSHKeyAddIn) (SSHKeyAddOut, error) {
			userID, err := auth.RequireFresh(ctx, deps.Service, p, deps.now())
			if err != nil {
				return SSHKeyAddOut{}, mapAuthError(err)
			}
			key, err := deps.Service.AddSSHKey(ctx, userID, in.Body.Key, "")
			if err != nil {
				return SSHKeyAddOut{}, mapSSHKeyError(err)
			}
			return SSHKeyAddOut{
				Fingerprint: key.Fingerprint,
				KeyType:     key.KeyType,
				Comment:     key.Comment,
				ID:          key.ID,
			}, nil
		},
	}); err != nil {
		return err
	}
	return op.Register(t, op.Op[SSHKeyRemoveIn, SSHKeyRemoveOut]{
		Name: "auth.sshkey.remove", Kind: op.KindMutation, Method: http.MethodPost, Path: "/settings/keys/{id}/delete",
		Scope: op.ScopeRepoRead, Summary: "Delete an SSH public key",
		Description: "Session-only with fresh auth. Deletes one own key by row id.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeValidation, op.CodeNotFound},
		Location:    func(SSHKeyRemoveOut) string { return "/settings/keys" },
		Handler: func(ctx context.Context, p op.Principal, in SSHKeyRemoveIn) (SSHKeyRemoveOut, error) {
			userID, err := auth.RequireFresh(ctx, deps.Service, p, deps.now())
			if err != nil {
				return SSHKeyRemoveOut{}, mapAuthError(err)
			}
			id, err := strconv.ParseInt(in.ID, 10, 64)
			if err != nil || id <= 0 {
				return SSHKeyRemoveOut{}, op.Errorf(op.CodeValidation, "ssh key id is not valid")
			}
			ok, err := deps.Service.RemoveSSHKey(ctx, userID, id, "")
			if err != nil {
				return SSHKeyRemoveOut{}, mapSSHKeyError(err)
			}
			if !ok {
				return SSHKeyRemoveOut{}, op.Errorf(op.CodeNotFound, "unknown ssh key")
			}
			return SSHKeyRemoveOut{}, nil
		},
	})
}

// RegisterSSHKeysViews binds the key pages beside the derived forms.
func RegisterSSHKeysViews(v *op.Views) {
	op.PageView(v, "auth.sshkey.list", func(w io.Writer, c op.ViewCtx, out SSHKeyListOut) error {
		var b strings.Builder
		b.WriteString(heading("SSH keys"))
		b.WriteString(errorList(c.Errors))
		b.WriteString("<h2>Add a key</h2>\n")
		b.WriteString(formOpen("/settings/keys", c.CSRF) +
			textField("key", "Authorized_keys line", "", false) +
			submitField("Add key"))
		b.WriteString("<h2>Keys</h2>\n<ul>\n")
		for _, key := range out.Keys {
			b.WriteString("<li>" + key.KeyType + " " + key.Fingerprint +
				" <form method=\"post\" action=\"/settings/keys/" + strconv.FormatInt(key.ID, 10) + "/delete\">\n" +
				"<input type=\"hidden\" name=\"_csrf\" value=\"" + c.CSRF + "\">\n" +
				"<input type=\"hidden\" name=\"_idem\" value=\"" + newIdem() + "\">\n" +
				"<button type=\"submit\">Delete</button>\n</form></li>\n")
		}
		b.WriteString("</ul>\n")
		_, err := io.WriteString(w, pageShell("SSH keys", b.String()))
		return err
	})
	v.Form("auth.sshkey.add", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Add an SSH key") + errorList(c.Errors) +
			formOpen("/settings/keys", c.CSRF) +
			textField("key", "Authorized_keys line", "", false) +
			submitField("Add key")
		_, err := io.WriteString(w, pageShell("Add an SSH key", body))
		return err
	})
}

// OwnerKeysPath matches single-segment *.keys paths for the owner handler.
// The stripped owner must route owner-side under the same first-segment
// dispatch rule as the git catch-all: names inside the reserved registry
// (see internal/reserved, api.SiteSegment) fall through to the site mux
// and never serve owner keys.
func OwnerKeysPath(path string) (string, bool) {
	name := strings.Trim(path, "/")
	if name == "" || strings.Contains(name, "/") {
		return "", false
	}
	owner, ok := strings.CutSuffix(name, ".keys")
	if !ok || owner == "" {
		return "", false
	}
	if SiteSegment(owner) {
		return "", false
	}
	return owner, true
}

// ServeOwnerKeys serves public key lines as text for the owner handler.
func ServeOwnerKeys(w http.ResponseWriter, r *http.Request, deps Deps, owner string) {
	user, err := deps.Service.GetUser(r.Context(), owner)
	if err != nil || !user.IsActive {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "unknown user\n")
		return
	}
	keys, err := deps.Service.ListSSHKeys(r.Context(), user.ID)
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "internal error\n")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	for _, key := range keys {
		_, _ = io.WriteString(w, key.PublicKey+"\n")
	}
}
