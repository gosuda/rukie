package api

import (
	"fmt"
	"net/http"

	"github.com/gosuda/rukie/internal/op"
)

// RegisterInbox registers the F8 inbox operations. All eight scopes are
// public-read at the transport layer; every handler gates on a human
// principal with an active account and addresses the caller only.
func RegisterInbox(t *op.Table, deps Deps) error {
	if err := op.Register(t, op.Op[InboxListIn, InboxListOut]{
		Name: "inbox.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/inbox",
		Scope: op.ScopePublicRead, Summary: "List inbox threads",
		Description: "The caller's threads, defaulting to unread, with the unread badge count. Cursor pages over bumped_at. Poll no faster than the documented rate; exceeding it answers 429.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeInvalid, op.CodeValidation, op.CodeForbidden, op.CodeNotFound, op.CodeRateLimited},
		ETag: func(out InboxListOut) string {
			return fmt.Sprintf("\"inbox-%d-%s\"", out.Unread, out.Next)
		},
		Pagination: &op.Pagination[InboxListOut]{Default: 50, Max: 100, Next: func(out InboxListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: inboxListHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[InboxGetIn, InboxGetOut]{
		Name: "inbox.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/inbox/thread",
		Scope: op.ScopePublicRead, Summary: "Show an inbox thread",
		Description: "One thread with its item history. API reads never auto-mark; call inbox.mark to change state. The HTML thread page marks read on view.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeInvalid, op.CodeNotFound},
		ETag: func(out InboxGetOut) string {
			return fmt.Sprintf("\"inbox-thread-%s-%d\"", out.Thread.ID, out.Thread.BumpedAt)
		},
		Handler: inboxGetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[InboxMarkIn, InboxMarkOut]{
		Name: "inbox.mark", Kind: op.KindMutation, Method: http.MethodPost, Path: "/inbox",
		Scope: op.ScopePublicRead, Summary: "Mark inbox threads",
		Description: "Apply one state op to up to 100 threads atomically per thread; partial success reports per id. Snooze needs snooze_until. Idempotent per thread.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeInvalid, op.CodeValidation, op.CodeRateLimited},
		Handler:     inboxMarkHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[InboxSnoozeIn, InboxSnoozeOut]{
		Name: "inbox.snooze", Kind: op.KindMutation, Method: http.MethodPost, Path: "/inbox/snooze",
		Scope: op.ScopePublicRead, Summary: "Snooze inbox threads",
		Description: "Snooze up to 100 threads until a time in (now+1m, now+90d]. Waking threads return to unread.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeInvalid, op.CodeValidation, op.CodeRateLimited},
		Handler:     inboxSnoozeHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[InboxCountIn, InboxCountOut]{
		Name: "inbox.count", Kind: op.KindRead, Method: http.MethodGet, Path: "/inbox/count",
		Scope: op.ScopePublicRead, Summary: "Count inbox threads",
		Description: "Cheap badge gate: unread threads plus snoozed threads whose wake time passed. Call count before list.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeRateLimited},
		ETag: func(out InboxCountOut) string {
			return fmt.Sprintf("\"inbox-count-%d-%d\"", out.Unread, out.SnoozedAwakePending)
		},
		Handler: inboxCountHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[InboxWatchSetIn, InboxWatchSetOut]{
		Name: "inbox.watch.set", Kind: op.KindMutation, Method: http.MethodPost, Path: "/inbox/watch",
		Scope: op.ScopePublicRead, Summary: "Set a repository watch",
		Description: "Set the caller's watch level for one repository. Ignore needs explicit confirmation; custom needs a non-empty subset of changes, tasks, releases, ci.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeInvalid, op.CodeValidation, op.CodeNotFound},
		Handler:     inboxWatchSetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[InboxWatchListIn, InboxWatchListOut]{
		Name: "inbox.watch.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/inbox/watches",
		Scope: op.ScopePublicRead, Summary: "List repository watches",
		Description: "Every explicit repo watch of the caller with the global default level for new repositories.",
		Problems:    []op.Code{op.CodeUnauthorized},
		ETag: func(out InboxWatchListOut) string {
			return fmt.Sprintf("\"inbox-watches-%d-%s\"", len(out.Watches), out.DefaultLevel)
		},
		Handler: inboxWatchListHandler(deps),
	}); err != nil {
		return err
	}
	return op.Register(t, op.Op[InboxSettingsIn, InboxSettingsOut]{
		Name: "inbox.settings", Kind: op.KindMutation, Method: http.MethodPost, Path: "/inbox/settings",
		Scope: op.ScopePublicRead, Summary: "Update inbox settings",
		Description: "Set the default watch level for new repositories (custom is forbidden here) and the claim warning window in seconds (300..86400). Absent fields keep their values.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeInvalid, op.CodeValidation},
		Handler:     inboxSettingsHandler(deps),
	})
}
