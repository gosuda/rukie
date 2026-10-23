package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

const keysExportPath = "/{owner}.keys"

type KeysExportIn struct {
	Owner string `path:"owner"`
}

type KeysExportOut struct {
	Keys []string `json:"keys"`
}

func KeysExportOp(d Deps) op.Op[KeysExportIn, KeysExportOut] {
	return op.Op[KeysExportIn, KeysExportOut]{
		Name:        "auth.sshkey.keys",
		Kind:        op.KindRead,
		Method:      http.MethodGet,
		Path:        keysExportPath,
		Scope:       op.ScopePublicRead,
		Summary:     "List one user's public SSH keys",
		Description: "Public export of the authorized_keys lines attached to a username.",
		Problems:    []op.Code{op.CodeNotFound},
		ETag: func(out KeysExportOut) string {
			sum := sha256.Sum256([]byte(strings.Join(out.Keys, "\n")))
			return hex.EncodeToString(sum[:])
		},
		Handler: func(ctx context.Context, _ op.Principal, in KeysExportIn) (KeysExportOut, error) {
			lines, err := d.exportKeys(ctx, in.Owner)
			if err != nil {
				return KeysExportOut{}, err
			}
			return KeysExportOut{Keys: lines}, nil
		},
	}
}

func (d Deps) exportKeys(ctx context.Context, owner string) ([]string, error) {
	user, err := d.Service.GetUser(ctx, owner)
	if err != nil || !user.IsActive {
		return nil, op.Errorf(op.CodeNotFound, "unknown user")
	}
	keys, err := d.Service.ListSSHKeys(ctx, user.ID)
	if err != nil {
		return nil, op.Wrap(op.CodeInternal, err, "list ssh keys failed")
	}
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key.PublicKey)
	}
	return lines, nil
}

func matchKeysTemplate(path string) (string, bool) {
	params, ok := matchRouteTemplate(keysExportPath, "/"+strings.Trim(path, "/"))
	if !ok {
		return "", false
	}
	owner := params["owner"]
	if owner == "" {
		return "", false
	}
	return owner, true
}

func matchRouteTemplate(tmpl, path string) (map[string]string, bool) {
	tsegs := strings.Split(tmpl, "/")
	psegs := strings.Split(path, "/")
	if len(tsegs) != len(psegs) {
		return nil, false
	}
	out := map[string]string{}
	for i, ts := range tsegs {
		ps := psegs[i]
		if !strings.Contains(ts, "{") {
			if ts != ps {
				return nil, false
			}
			continue
		}
		name, prefix, suffix, ok := splitRouteSegment(ts)
		if !ok {
			return nil, false
		}
		if !strings.HasPrefix(ps, prefix) || !strings.HasSuffix(ps, suffix) {
			return nil, false
		}
		out[name] = ps[len(prefix) : len(ps)-len(suffix)]
	}
	return out, true
}

func splitRouteSegment(seg string) (string, string, string, bool) {
	prefix, after, ok := strings.Cut(seg, "{")
	if !ok {
		return "", "", "", false
	}
	name, suffix, ok := strings.Cut(after, "}")
	if !ok || strings.Contains(name, "{") || strings.Contains(suffix, "{") {
		return "", "", "", false
	}
	return name, prefix, suffix, true
}

func OwnerKeysPath(path string) (string, bool) {
	owner, ok := matchKeysTemplate(path)
	if !ok {
		return "", false
	}
	if SiteSegment(owner) {
		return "", false
	}
	return owner, true
}

func ServeOwnerKeys(w http.ResponseWriter, r *http.Request, deps Deps, owner string) {
	lines, err := deps.exportKeys(r.Context(), owner)
	if err != nil {
		oerr, isNotFound := errors.AsType[*op.Error](err)
		status := http.StatusInternalServerError
		body := "internal error\n"
		if isNotFound && oerr.Code == op.CodeNotFound {
			status = http.StatusNotFound
			body = "unknown user\n"
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	for _, line := range lines {
		_, _ = io.WriteString(w, line+"\n")
	}
}
