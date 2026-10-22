package api

import (
	"fmt"
	"net/http"

	"github.com/gosuda/rukie/internal/op"
)

// RegisterMirror registers the F3 push mirror operations.
func RegisterMirror(t *op.Table, deps Deps) error {
	if err := op.Register(t, op.Op[MirrorListIn, MirrorListOut]{
		Name: "mirror.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/settings/mirrors",
		Scope: op.ScopeMirrorsRead, Summary: "List push mirrors",
		Description: "Push mirrors of one repository, oldest first. Credentials carry metadata only.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out MirrorListOut) string {
			return fmt.Sprintf("\"mirrors-%d-%s\"", len(out.Mirrors), out.Next)
		},
		Pagination: &op.Pagination[MirrorListOut]{Default: 30, Max: 100, Next: func(out MirrorListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: mirrorListHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[MirrorCreateIn, MirrorCreateOut]{
		Name: "mirror.create", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/settings/mirrors",
		Scope: op.ScopeMirrorsTrigger, Summary: "Create a push mirror",
		Description: "Create a push mirror and queue the first sync run. The token is sealed at rest and never returned.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden, op.CodeConflict},
		Handler:     mirrorCreateHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[MirrorGetIn, MirrorGetOut]{
		Name: "mirror.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/settings/mirrors/{id}",
		Scope: op.ScopeMirrorsRead, Summary: "Show a push mirror",
		Description: "One push mirror by public id with an optional ref state page.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out MirrorGetOut) string {
			return fmt.Sprintf("\"mirror-%s-%d-%d-%s\"", out.Mirror.ID, out.Mirror.NextRunAt, len(out.Refs), out.Next)
		},
		Pagination: &op.Pagination[MirrorGetOut]{Default: 30, Max: 100, Next: func(out MirrorGetOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: mirrorGetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[MirrorUpdateIn, MirrorUpdateOut]{
		Name: "mirror.update", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/settings/mirrors/{id}",
		Scope: op.ScopeMirrorsTrigger, Summary: "Update a push mirror",
		Description: "Change mirror settings. A new token rotates the seal. A settings change queues a reconcile run.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden, op.CodeConflict},
		Handler:     mirrorUpdateHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[MirrorDeleteIn, MirrorDeleteOut]{
		Name: "mirror.delete", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/settings/mirrors/{id}/delete",
		Scope: op.ScopeMirrorsTrigger, Summary: "Delete a push mirror",
		Description: "Delete a push mirror with its runs and ref state. A run already in flight finishes on its own.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		Handler:     mirrorDeleteHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[MirrorSyncIn, MirrorSyncOut]{
		Name: "mirror.sync", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/settings/mirrors/{id}/sync",
		Scope: op.ScopeMirrorsTrigger, Summary: "Sync a push mirror",
		Description: "Preview or start a manual sync. A dry run returns the plan only. A live run answers 202.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden, op.CodeConflict},
		Status:      202,
		Handler:     mirrorSyncHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[MirrorRunListIn, MirrorRunListOut]{
		Name: "mirror.run.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/settings/mirrors/{id}/runs",
		Scope: op.ScopeMirrorsRead, Summary: "List mirror sync runs",
		Description: "Sync runs of one mirror, oldest first, with an optional state filter.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out MirrorRunListOut) string {
			return fmt.Sprintf("\"mirror-runs-%d-%s\"", len(out.Runs), out.Next)
		},
		Pagination: &op.Pagination[MirrorRunListOut]{Default: 30, Max: 100, Next: func(out MirrorRunListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: mirrorRunListHandler(deps),
	}); err != nil {
		return err
	}
	return op.Register(t, op.Op[MirrorRunGetIn, MirrorRunGetOut]{
		Name: "mirror.run.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/settings/mirrors/{id}/runs/{run}",
		Scope: op.ScopeMirrorsRead, Summary: "Show a mirror sync run",
		Description: "One sync run of one mirror by public id.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out MirrorRunGetOut) string {
			return fmt.Sprintf("\"mirror-run-%s-%d\"", out.Run.ID, out.Run.FinishedAt)
		},
		Handler: mirrorRunGetHandler(deps),
	})
}
