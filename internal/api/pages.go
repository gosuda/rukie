package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"html"
	"strings"
	"time"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/store"
)

type Deps struct {
	Service       *auth.Service
	Limiter       *access.Limiter
	Reveals       *auth.RevealCache
	Store         *store.Store
	Ruk           *ruk.Client
	Queue         *queue.Queue
	OnRepoCreated func(repoID int64)
	OnRepoDeleted func(repoID int64)
	Clock         func() time.Time
	RepoRoot      string
	RukBin        string
	DataDir       string
	SealKey       []byte
}

func (d Deps) now() time.Time {
	if d.Clock != nil {
		return d.Clock()
	}
	return time.Now()
}

const fontLink = `<link rel="stylesheet" href="https://fonts.bunny.net/css?family=space-grotesk:500,600,700%7Csource-sans-3:400,600%7Cibm-plex-mono:400,500,600&display=swap" referrerpolicy="no-referrer">`

func pageShell(title, body string) string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n<title>")
	b.WriteString(html.EscapeString(title))
	b.WriteString(" — rukie</title>\n")
	b.WriteString(fontLink)
	b.WriteString("\n</head>\n<body>\n<main>\n")
	b.WriteString(body)
	b.WriteString("\n</main>\n</body>\n</html>\n")
	return b.String()
}

func newIdem() string {
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	return base64.RawURLEncoding.EncodeToString(raw[:])
}

func formOpen(action, csrf string) string {
	return "<form method=\"post\" action=\"" + html.EscapeString(action) + "\">\n" +
		"<input type=\"hidden\" name=\"_csrf\" value=\"" + html.EscapeString(csrf) + "\">\n" +
		"<input type=\"hidden\" name=\"_idem\" value=\"" + newIdem() + "\">\n"
}

func textField(name, label, value string, password bool) string {
	kind := "text"
	if password {
		kind = "password"
	}
	return "<label>" + html.EscapeString(label) + "\n" +
		"<input type=\"" + kind + "\" name=\"" + html.EscapeString(name) + "\" value=\"" + html.EscapeString(value) + "\"" + passwordComplete(name, password) + ">\n</label>\n"
}

func passwordComplete(name string, password bool) string {
	if !password {
		return ""
	}
	if name == "next" {
		return " autocomplete=\"new-password\""
	}
	return " autocomplete=\"current-password\""
}

func hiddenField(name, value string) string {
	return "<input type=\"hidden\" name=\"" + html.EscapeString(name) + "\" value=\"" + html.EscapeString(value) + "\">\n"
}

func checkField(name, label string, checked bool) string {
	attr := ""
	if checked {
		attr = " checked"
	}
	return "<label><input type=\"checkbox\" name=\"" + html.EscapeString(name) + "\" value=\"1\"" + attr + "> " + html.EscapeString(label) + "</label>\n"
}

func submitField(label string) string {
	return "<button type=\"submit\">" + html.EscapeString(label) + "</button>\n</form>\n"
}

func heading(text string) string {
	return "<h1>" + html.EscapeString(text) + "</h1>\n"
}

func para(text string) string {
	return "<p>" + html.EscapeString(text) + "</p>\n"
}

func errorList(errs []op.FieldError) string {
	if len(errs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<ul>\n")
	for _, e := range errs {
		b.WriteString("<li>" + html.EscapeString(e.Location+": "+e.Message) + "</li>\n")
	}
	b.WriteString("</ul>\n")
	return b.String()
}

func mapAuthError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case msg == "Invalid username or password.":
		return op.Errorf(op.CodeUnauthorized, "Invalid username or password.")
	case msg == "unknown user":
		return op.Errorf(op.CodeNotFound, "unknown user")
	case msg == "username is taken":
		return op.Errorf(op.CodeConflict, "username is taken")
	case msg == "username is reserved" || strings.HasPrefix(msg, "username must match"):
		return op.Errorf(op.CodeValidation, "%s", msg)
	case msg == "an admin already exists":
		return op.Errorf(op.CodeConflict, "an admin already exists")
	case strings.HasPrefix(msg, "cannot delete the last") || strings.HasPrefix(msg, "cannot deactivate the last"):
		return op.Errorf(op.CodeConflict, "%s", msg)
	case msg == "session authentication required":
		return op.Errorf(op.CodeForbidden, "session authentication required")
	case msg == "authentication required" || msg == "fresh authentication required":
		return op.Errorf(op.CodeUnauthorized, "%s", msg)
	case msg == "representation changed":
		return op.Errorf(op.CodeStale, "representation changed")
	case msg == "if-match is required":
		return op.Errorf(op.CodePreconditionReq, "if-match is required")
	case msg == "session prefix is ambiguous" || msg == "token prefix is ambiguous":
		return op.Errorf(op.CodeValidation, "%s", msg)
	case msg == "invalid session" || msg == "invalid token":
		return op.Errorf(op.CodeUnauthorized, "invalid credential")
	case strings.HasPrefix(msg, "password ") || strings.HasPrefix(msg, "password must") || strings.HasPrefix(msg, "password is") || strings.HasPrefix(msg, "password exceeds"):
		return op.Errorf(op.CodeValidation, "%s", msg)
	case strings.HasPrefix(msg, "token lifetime") || strings.HasPrefix(msg, "token name") || strings.HasPrefix(msg, "at least one scope") || strings.HasPrefix(msg, "unknown scope") || strings.HasPrefix(msg, "duplicate scope") || strings.HasPrefix(msg, "public:read"):
		return op.Errorf(op.CodeValidation, "%s", msg)
	case strings.HasPrefix(msg, "full name") || strings.HasPrefix(msg, "email ") || strings.HasPrefix(msg, "email must"):
		return op.Errorf(op.CodeValidation, "%s", msg)
	case strings.HasPrefix(msg, "username "):
		return op.Errorf(op.CodeValidation, "%s", msg)
	default:
		return op.Wrap(op.CodeInternal, err, "internal error")
	}
}

func requireAdmin(ctx context.Context, svc *auth.Service, p op.Principal) (auth.User, error) {
	u, err := svc.GetUserByID(ctx, p.UserID)
	if err != nil || !u.IsAdmin || !u.IsActive {
		return auth.User{}, op.Errorf(op.CodeForbidden, "site admin required")
	}
	return u, nil
}
