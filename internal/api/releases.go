package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
	"github.com/gosuda/rukie/internal/web/markdown"
)

const releaseMaxAssetBytes = 2 << 30

type ReleaseAssetJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	SHA256      string `json:"sha256"`
	URL         string `json:"url"`
	Size        int64  `json:"size"`
	UploadedBy  int64  `json:"uploaded_by"`
	CreatedAt   int64  `json:"created_at"`
}

type ReleaseJSON struct {
	Commit      string             `json:"commit,omitempty"`
	Title       string             `json:"title"`
	Notes       string             `json:"notes"`
	NotesHTML   string             `json:"notes_html"`
	Tag         string             `json:"tag"`
	ETag        string             `json:"etag"`
	Assets      []ReleaseAssetJSON `json:"assets"`
	Author      int64              `json:"author"`
	CreatedAt   int64              `json:"created_at"`
	PublishedAt int64              `json:"published_at,omitempty"`
	Draft       bool               `json:"draft"`
	Prerelease  bool               `json:"prerelease"`
	Annotated   bool               `json:"annotated,omitempty"`
}

type ReleaseListIn struct {
	Owner         string `path:"owner"`
	Repo          string `path:"repo"`
	Cursor        string `query:"cursor"`
	Limit         int    `query:"limit"`
	IncludeDrafts bool   `query:"include_drafts"`
}

type ReleaseListOut struct {
	Next     string        `json:"-"`
	Releases []ReleaseJSON `json:"releases"`
}

type ReleaseGetIn struct {
	Owner string `path:"owner"`
	Repo  string `path:"repo"`
	Tag   string `path:"tag"`
}

type ReleaseGetOut struct {
	Release ReleaseJSON `json:"release"`
}

type ReleaseCreateBody struct {
	Tag        string `json:"tag" minLength:"1" maxLength:"255"`
	Title      string `json:"title" minLength:"1" maxLength:"200"`
	Notes      string `json:"notes,omitempty" maxLength:"65536"`
	Draft      bool   `json:"draft,omitempty"`
	Prerelease bool   `json:"prerelease,omitempty"`
}

type ReleaseCreateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Body           ReleaseCreateBody
}

type ReleaseCreateOut struct {
	Release ReleaseJSON `json:"release"`
}

type ReleaseUpdateBody struct {
	Title      *string `json:"title,omitempty" maxLength:"200"`
	Notes      *string `json:"notes,omitempty" maxLength:"65536"`
	Prerelease *bool   `json:"prerelease,omitempty"`
	Draft      *bool   `json:"draft,omitempty"`
}

type ReleaseUpdateIn struct {
	Body           ReleaseUpdateBody
	IdempotencyKey string `header:"Idempotency-Key"`
	IfMatch        string `header:"If-Match"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Tag            string `path:"tag"`
}

type ReleaseUpdateOut struct {
	Release ReleaseJSON `json:"release"`
}

type ReleaseDeleteIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	IfMatch        string `header:"If-Match"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Tag            string `path:"tag"`
}

type ReleaseDeleteOut struct {
	RepairDetail string `json:"repair_detail,omitempty"`
	Deleted      bool   `json:"deleted"`
	RepairNeeded bool   `json:"repair_needed,omitempty"`
}

type ReleaseAssetListIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	Tag    string `path:"tag"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

type ReleaseAssetListOut struct {
	Next   string             `json:"-"`
	Assets []ReleaseAssetJSON `json:"assets"`
}

type ReleaseAssetCreateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	IfMatch        string `header:"If-Match"`
	IfNoneMatch    string `header:"If-None-Match"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Tag            string `path:"tag"`
	Name           string `path:"name"`
}

type ReleaseAssetCreateOut struct {
	Asset ReleaseAssetJSON `json:"asset"`
}

type ReleaseAssetGetIn struct {
	Owner string `path:"owner"`
	Repo  string `path:"repo"`
	Tag   string `path:"tag"`
	Name  string `path:"name"`
}

type ReleaseAssetGetOut struct {
	Asset ReleaseAssetJSON `json:"asset"`
}

type ReleaseAssetDeleteIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	IfMatch        string `header:"If-Match"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Tag            string `path:"tag"`
	Name           string `path:"name"`
}

type ReleaseAssetDeleteOut struct{}

type ArchiveGetIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	Rev    string `path:"rev"`
	Format string `query:"format"`
}

type ArchiveGetOut struct {
	Commit      string `json:"commit"`
	Format      string `json:"format"`
	ContentType string `json:"content_type"`
	ETag        string `json:"etag"`
}

func releaseBase(owner, repo string) string {
	return "/" + owner + "/" + repo + "/releases"
}

func releaseTagURL(owner, repo, tag string) string {
	return releaseBase(owner, repo) + "/tag/" + tag
}

func releaseETag(row store.ReleaseRow) string {
	return "\"rel-" + row.PublicID + "-" + strconv.FormatInt(row.Revision, 10) + "\""
}

func assetETag(sum string) string {
	return "\"" + sum + "\""
}

func (d Deps) releaseRowJSON(ctx context.Context, rc repoContext, row store.ReleaseRow) (ReleaseJSON, error) {
	out := ReleaseJSON{
		Tag:        row.TagName,
		Title:      row.Title,
		Notes:      row.NotesMarkdown,
		Draft:      row.State == "draft",
		Prerelease: row.Prerelease,
		Commit:     row.TargetOID,
		Annotated:  row.TargetKind == "annotated",
		Author:     row.AuthorID,
		CreatedAt:  row.CreatedAt.Unix(),
		ETag:       releaseETag(row),
		Assets:     []ReleaseAssetJSON{},
	}
	if !row.PublishedAt.IsZero() {
		out.PublishedAt = row.PublishedAt.Unix()
	}
	if row.NotesHTML != "" {
		out.NotesHTML = row.NotesHTML
	} else if row.NotesMarkdown != "" {
		rendered, rerr := markdown.Render([]byte(row.NotesMarkdown))
		if rerr != nil {
			return ReleaseJSON{}, op.Wrap(op.CodeInternal, rerr, "release notes could not be rendered")
		}
		out.NotesHTML = string(rendered)
	}
	assets, aerr := d.Store.ReleaseAssets(ctx, row.ID)
	if aerr != nil {
		return ReleaseJSON{}, op.Wrap(op.CodeInternal, aerr, "release assets could not be loaded")
	}
	for _, asset := range assets {
		out.Assets = append(out.Assets, releaseAssetJSON(rc, row.TagName, asset))
	}
	return out, nil
}

func releaseAssetJSON(rc repoContext, tag string, asset store.AssetRow) ReleaseAssetJSON {
	return ReleaseAssetJSON{
		ID:          asset.PublicID,
		Name:        asset.Name,
		Size:        asset.Size,
		ContentType: asset.ContentType,
		SHA256:      asset.SHA256,
		UploadedBy:  asset.UploaderID,
		CreatedAt:   asset.CreatedAt.Unix(),
		URL:         "/" + rc.owner + "/" + rc.name + "/releases/assets/" + tag + "/" + asset.Name,
	}
}

func (d Deps) loadReleaseForRead(ctx context.Context, rq requester, rc repoContext, tag string) (store.ReleaseRow, bool, error) {
	row, rerr := d.Store.ReleaseByTag(ctx, rc.row.ID, tag)
	if rerr != nil {
		if errors.Is(rerr, store.ErrNotFound) {
			return store.ReleaseRow{}, false, op.Errorf(op.CodeNotFound, "release was not found")
		}
		return store.ReleaseRow{}, false, op.Wrap(op.CodeInternal, rerr, "release lookup failed")
	}
	canWrite := d.checkRepoAccess(rq, rc.row, "repo.push", "") == nil
	if row.State == "draft" && !canWrite {
		return store.ReleaseRow{}, false, op.Errorf(op.CodeNotFound, "release was not found")
	}
	if row.State != "draft" {
		if derr := d.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
			return store.ReleaseRow{}, false, derr
		}
	}
	return row, canWrite, nil
}

func (d Deps) loadReleaseForWrite(ctx context.Context, rq requester, owner, repo, tag string) (repoContext, store.ReleaseRow, error) {
	rc, cerr := d.resolveRepo(ctx, rq, owner, repo, false)
	if cerr != nil {
		return repoContext{}, store.ReleaseRow{}, cerr
	}
	if derr := d.checkRepoAccess(rq, rc.row, "release.publish", ""); derr != nil {
		return repoContext{}, store.ReleaseRow{}, derr
	}
	if rc.row.Archived {
		return repoContext{}, store.ReleaseRow{}, op.Errorf(op.CodeArchived, "repository is archived")
	}
	row, rerr := d.Store.ReleaseByTag(ctx, rc.row.ID, tag)
	if rerr != nil {
		if errors.Is(rerr, store.ErrNotFound) {
			return repoContext{}, store.ReleaseRow{}, op.Errorf(op.CodeNotFound, "release was not found")
		}
		return repoContext{}, store.ReleaseRow{}, op.Wrap(op.CodeInternal, rerr, "release lookup failed")
	}
	return rc, row, nil
}

func parseReleaseCursor(raw string) (int64, int64, error) {
	if raw == "" {
		return 0, 0, nil
	}
	before, after, ok := strings.Cut(raw, ":")
	if !ok {
		return 0, 0, op.Errorf(op.CodeInvalid, "cursor is not valid")
	}
	created, cerr := strconv.ParseInt(before, 10, 64)
	if cerr != nil || created <= 0 {
		return 0, 0, op.Errorf(op.CodeInvalid, "cursor is not valid")
	}
	id, ierr := strconv.ParseInt(after, 10, 64)
	if ierr != nil || id <= 0 {
		return 0, 0, op.Errorf(op.CodeInvalid, "cursor is not valid")
	}
	return created, id, nil
}

func releaseListHandler(d Deps) func(context.Context, op.Principal, ReleaseListIn) (ReleaseListOut, error) {
	return func(ctx context.Context, p op.Principal, in ReleaseListIn) (ReleaseListOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ReleaseListOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return ReleaseListOut{}, cerr
		}
		canWrite := d.checkRepoAccess(rq, rc.row, "repo.push", "") == nil
		if derr := d.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
			return ReleaseListOut{}, derr
		}
		created, id, perr := parseReleaseCursor(in.Cursor)
		if perr != nil {
			return ReleaseListOut{}, perr
		}
		limit := clampLimit(in.Limit, 50, 200)
		var rows []store.ReleaseRow
		rerr = d.Store.Read(ctx, func(tx *store.ReadTx) error {
			var lerr error
			page := store.ReleasePage{Limit: limit + 1, AfterCreated: created, AfterID: id}
			if in.IncludeDrafts && canWrite {
				rows, lerr = tx.ListReleasesIncludingDrafts(rc.row.ID, page)
			} else {
				rows, lerr = tx.ListPublishedReleases(rc.row.ID, page)
			}
			return lerr
		})
		if rerr != nil {
			return ReleaseListOut{}, op.Wrap(op.CodeInternal, rerr, "release list failed")
		}
		out := ReleaseListOut{Releases: []ReleaseJSON{}}
		var last store.ReleaseRow
		truncated := false
		for _, row := range rows {
			if len(out.Releases) == limit {
				truncated = true
				break
			}
			item, jerr := d.releaseRowJSON(ctx, rc, row)
			if jerr != nil {
				return ReleaseListOut{}, jerr
			}
			out.Releases = append(out.Releases, item)
			last = row
		}
		if truncated {
			out.Next = fmt.Sprintf("%d:%d", last.CreatedAt.Unix(), last.ID)
		}
		return out, nil
	}
}

func releaseGetHandler(d Deps) func(context.Context, op.Principal, ReleaseGetIn) (ReleaseGetOut, error) {
	return func(ctx context.Context, p op.Principal, in ReleaseGetIn) (ReleaseGetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ReleaseGetOut{}, rerr
		}
		if terr := checkTagName(in.Tag); terr != nil {
			return ReleaseGetOut{}, terr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return ReleaseGetOut{}, cerr
		}
		row, _, lerr := d.loadReleaseForRead(ctx, rq, rc, in.Tag)
		if lerr != nil {
			return ReleaseGetOut{}, lerr
		}
		item, jerr := d.releaseRowJSON(ctx, rc, row)
		if jerr != nil {
			return ReleaseGetOut{}, jerr
		}
		return ReleaseGetOut{Release: item}, nil
	}
}

func checkReleaseTitle(title string) error {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return op.Errorf(op.CodeValidation, "title must not be empty")
	}
	if len([]rune(trimmed)) > 200 {
		return op.Errorf(op.CodeValidation, "title must be at most 200 characters")
	}
	return nil
}

func checkReleaseNotes(notes string) error {
	if len(notes) > 65536 {
		return op.Errorf(op.CodeValidation, "notes must be at most 65536 bytes")
	}
	return nil
}

func releaseCreateHandler(d Deps) func(context.Context, op.Principal, ReleaseCreateIn) (ReleaseCreateOut, error) {
	return func(ctx context.Context, p op.Principal, in ReleaseCreateIn) (ReleaseCreateOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ReleaseCreateOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return ReleaseCreateOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "release.publish", ""); derr != nil {
			return ReleaseCreateOut{}, derr
		}
		if rc.row.Archived {
			return ReleaseCreateOut{}, op.Errorf(op.CodeArchived, "repository is archived")
		}
		if terr := checkTagName(in.Body.Tag); terr != nil {
			return ReleaseCreateOut{}, terr
		}
		if terr := checkReleaseTitle(in.Body.Title); terr != nil {
			return ReleaseCreateOut{}, terr
		}
		if nerr := checkReleaseNotes(in.Body.Notes); nerr != nil {
			return ReleaseCreateOut{}, nerr
		}
		now := d.now()
		publicID := string(store.NewID(now))
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			_, ierr := tx.InsertRelease(store.ReleaseInsert{
				RepoID:     rc.row.ID,
				AuthorID:   rq.userID,
				PublicID:   publicID,
				Tag:        in.Body.Tag,
				Title:      strings.TrimSpace(in.Body.Title),
				Notes:      in.Body.Notes,
				Prerelease: in.Body.Prerelease,
				CreatedAt:  now,
			})
			if ierr != nil {
				if errors.Is(ierr, store.ErrConflict) {
					return op.Errorf(op.CodeConflict, "release tag is already taken")
				}
				return ierr
			}
			return nil
		})
		if werr != nil {
			if oerr, ok := errors.AsType[*op.Error](werr); ok {
				return ReleaseCreateOut{}, oerr
			}
			return ReleaseCreateOut{}, op.Wrap(op.CodeInternal, werr, "release create failed")
		}
		row, rerr := d.Store.ReleaseByTag(ctx, rc.row.ID, in.Body.Tag)
		if rerr != nil {
			return ReleaseCreateOut{}, op.Wrap(op.CodeInternal, rerr, "release lookup failed")
		}
		if !in.Body.Draft {
			published, perr := d.publishReleaseRow(ctx, rq, rc, row)
			if perr != nil {
				return ReleaseCreateOut{}, perr
			}
			row = published
		}
		item, jerr := d.releaseRowJSON(ctx, rc, row)
		if jerr != nil {
			return ReleaseCreateOut{}, jerr
		}
		return ReleaseCreateOut{Release: item}, nil
	}
}

func (d Deps) publishReleaseRow(ctx context.Context, _ requester, rc repoContext, row store.ReleaseRow) (store.ReleaseRow, error) {
	if row.State != "draft" {
		return store.ReleaseRow{}, op.Errorf(op.CodeConflict, "release is already published")
	}
	if d.Ruk == nil {
		return store.ReleaseRow{}, op.Errorf(op.CodeUnavailable, "ruk backend is not configured")
	}
	daemon, oerr := d.openDaemonRepo(ctx, rc.row)
	if oerr != nil {
		return store.ReleaseRow{}, oerr
	}
	cc := rc
	cc.daemon = daemon
	target, ok := d.exactRefTarget(ctx, cc, tagsPrefix+row.TagName)
	if !ok {
		return store.ReleaseRow{}, op.Errorf(op.CodeNotFound, "push the tag first")
	}
	commit, tagObject, perr := d.peelTag(ctx, cc, target)
	if perr != nil {
		return store.ReleaseRow{}, perr
	}
	kind := "lightweight"
	if tagObject != "" {
		kind = "annotated"
	}
	rendered, rerr := markdown.Render([]byte(row.NotesMarkdown))
	if rerr != nil {
		return store.ReleaseRow{}, op.Wrap(op.CodeInternal, rerr, "release notes could not be rendered")
	}
	now := d.now()
	frozenAt := now.UTC().Format(time.RFC3339)
	var freezeReply map[string]any
	if ferr := d.callRuk(ctx, daemon, wire.MethodReleaseFreezeSet, map[string]any{
		"name":             tagsPrefix + row.TagName,
		"owner_release_id": strconv.FormatInt(row.ID, 10),
		"target_oid":       commit,
		"frozen_at":        frozenAt,
	}, &freezeReply); ferr != nil {
		return store.ReleaseRow{}, ferr
	}
	werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
		if perr := tx.PublishRelease(row.ID, commit, kind, string(rendered), now); perr != nil {
			return perr
		}
		return tx.PublishAssets(row.ID, now)
	})
	if werr != nil {
		var clearReply map[string]any
		cerr := d.callRuk(ctx, daemon, wire.MethodReleaseFreezeClear, map[string]any{
			"name":             tagsPrefix + row.TagName,
			"owner_release_id": strconv.FormatInt(row.ID, 10),
		}, &clearReply)
		if cerr != nil {
			return store.ReleaseRow{}, op.Wrap(op.CodeInternal, errors.Join(werr, cerr), "release publish failed and the freeze marker needs repair")
		}
		if errors.Is(werr, store.ErrNotFound) {
			return store.ReleaseRow{}, op.Errorf(op.CodeConflict, "release changed under this request")
		}
		return store.ReleaseRow{}, op.Wrap(op.CodeInternal, werr, "release publish failed")
	}
	d.freezeAssetPermissions(ctx, row.ID)
	updated, uerr := d.Store.ReleaseByTag(ctx, rc.row.ID, row.TagName)
	if uerr != nil {
		return store.ReleaseRow{}, op.Wrap(op.CodeInternal, uerr, "release lookup failed")
	}
	return updated, nil
}

func (d Deps) freezeAssetPermissions(ctx context.Context, releaseID int64) {
	assets, aerr := d.Store.ReleaseAssets(ctx, releaseID)
	if aerr != nil {
		return
	}
	for _, asset := range assets {
		if asset.Path == "" {
			continue
		}
		chmodReleaseFile(releaseFilePath(d.releaseRoot(), asset.Path))
	}
}

func releaseUpdateHandler(d Deps) func(context.Context, op.Principal, ReleaseUpdateIn) (ReleaseUpdateOut, error) {
	return func(ctx context.Context, p op.Principal, in ReleaseUpdateIn) (ReleaseUpdateOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ReleaseUpdateOut{}, rerr
		}
		if terr := checkTagName(in.Tag); terr != nil {
			return ReleaseUpdateOut{}, terr
		}
		rc, row, lerr := d.loadReleaseForWrite(ctx, rq, in.Owner, in.Repo, in.Tag)
		if lerr != nil {
			return ReleaseUpdateOut{}, lerr
		}
		if in.IfMatch == "" {
			return ReleaseUpdateOut{}, op.Errorf(op.CodePreconditionReq, "If-Match is required")
		}
		if !etagMatches(in.IfMatch, releaseETag(row)) {
			return ReleaseUpdateOut{}, op.Errorf(op.CodeStale, "release changed under this request")
		}
		if row.State != "draft" {
			if in.Body.Title != nil || in.Body.Notes != nil || in.Body.Prerelease != nil || (in.Body.Draft != nil && !*in.Body.Draft) {
				return ReleaseUpdateOut{}, op.Errorf(op.CodeConflict, "published releases are immutable")
			}
			item, jerr := d.releaseRowJSON(ctx, rc, row)
			if jerr != nil {
				return ReleaseUpdateOut{}, jerr
			}
			return ReleaseUpdateOut{Release: item}, nil
		}
		title := row.Title
		notes := row.NotesMarkdown
		prerelease := row.Prerelease
		if in.Body.Title != nil {
			if terr := checkReleaseTitle(*in.Body.Title); terr != nil {
				return ReleaseUpdateOut{}, terr
			}
			title = strings.TrimSpace(*in.Body.Title)
		}
		if in.Body.Notes != nil {
			if nerr := checkReleaseNotes(*in.Body.Notes); nerr != nil {
				return ReleaseUpdateOut{}, nerr
			}
			notes = *in.Body.Notes
		}
		if in.Body.Prerelease != nil {
			prerelease = *in.Body.Prerelease
		}
		uerr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			return tx.UpdateDraftRelease(row.ID, title, notes, prerelease)
		})
		if uerr != nil {
			if errors.Is(uerr, store.ErrNotFound) {
				return ReleaseUpdateOut{}, op.Errorf(op.CodeConflict, "release changed under this request")
			}
			return ReleaseUpdateOut{}, op.Wrap(op.CodeInternal, uerr, "release update failed")
		}
		updated, gerr := d.Store.ReleaseByTag(ctx, rc.row.ID, row.TagName)
		if gerr != nil {
			return ReleaseUpdateOut{}, op.Wrap(op.CodeInternal, gerr, "release lookup failed")
		}
		if in.Body.Draft != nil && !*in.Body.Draft {
			published, perr := d.publishReleaseRow(ctx, rq, rc, updated)
			if perr != nil {
				return ReleaseUpdateOut{}, perr
			}
			updated = published
		}
		item, jerr := d.releaseRowJSON(ctx, rc, updated)
		if jerr != nil {
			return ReleaseUpdateOut{}, jerr
		}
		return ReleaseUpdateOut{Release: item}, nil
	}
}

func releaseDeleteHandler(d Deps) func(context.Context, op.Principal, ReleaseDeleteIn) (ReleaseDeleteOut, error) {
	return func(ctx context.Context, p op.Principal, in ReleaseDeleteIn) (ReleaseDeleteOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ReleaseDeleteOut{}, rerr
		}
		if terr := checkTagName(in.Tag); terr != nil {
			return ReleaseDeleteOut{}, terr
		}
		rc, row, lerr := d.loadReleaseForWrite(ctx, rq, in.Owner, in.Repo, in.Tag)
		if lerr != nil {
			return ReleaseDeleteOut{}, lerr
		}
		if in.IfMatch == "" {
			return ReleaseDeleteOut{}, op.Errorf(op.CodePreconditionReq, "If-Match is required")
		}
		if !etagMatches(in.IfMatch, releaseETag(row)) {
			return ReleaseDeleteOut{}, op.Errorf(op.CodeStale, "release changed under this request")
		}
		now := d.now()
		if row.State == "draft" {
			derr := d.discardDraftRelease(ctx, rc, row)
			if derr != nil {
				return ReleaseDeleteOut{}, derr
			}
			return ReleaseDeleteOut{Deleted: true}, nil
		}
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			return tx.DeleteRelease(row.ID, now)
		})
		if werr != nil {
			if errors.Is(werr, store.ErrNotFound) {
				return ReleaseDeleteOut{}, op.Errorf(op.CodeConflict, "release changed under this request")
			}
			return ReleaseDeleteOut{}, op.Wrap(op.CodeInternal, werr, "release delete failed")
		}
		if d.Ruk != nil {
			daemon, oerr := d.openDaemonRepo(ctx, rc.row)
			if oerr != nil {
				return ReleaseDeleteOut{Deleted: true, RepairNeeded: true, RepairDetail: "release deleted, tag still frozen — run repair"}, nil
			}
			var clearReply map[string]any
			cerr := d.callRuk(ctx, daemon, wire.MethodReleaseFreezeClear, map[string]any{
				"name":             tagsPrefix + row.TagName,
				"owner_release_id": strconv.FormatInt(row.ID, 10),
			}, &clearReply)
			if cerr != nil {
				return ReleaseDeleteOut{Deleted: true, RepairNeeded: true, RepairDetail: "release deleted, tag still frozen — run repair"}, nil
			}
		}
		return ReleaseDeleteOut{Deleted: true}, nil
	}
}

func (d Deps) discardDraftRelease(ctx context.Context, rc repoContext, row store.ReleaseRow) error {
	if d.Ruk != nil {
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return oerr
		}
		var clearReply map[string]any
		cerr := d.callRuk(ctx, daemon, wire.MethodReleaseFreezeClear, map[string]any{
			"name":             tagsPrefix + row.TagName,
			"owner_release_id": strconv.FormatInt(row.ID, 10),
		}, &clearReply)
		if cerr != nil {
			return cerr
		}
	}
	werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
		if derr := tx.DeleteReleaseAssets(row.ID); derr != nil {
			return derr
		}
		return tx.HardDeleteRelease(row.ID)
	})
	if werr != nil {
		if errors.Is(werr, store.ErrNotFound) {
			return op.Errorf(op.CodeConflict, "release changed under this request")
		}
		return op.Wrap(op.CodeInternal, werr, "release discard failed")
	}
	return nil
}

func releaseAssetListHandler(d Deps) func(context.Context, op.Principal, ReleaseAssetListIn) (ReleaseAssetListOut, error) {
	return func(ctx context.Context, p op.Principal, in ReleaseAssetListIn) (ReleaseAssetListOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ReleaseAssetListOut{}, rerr
		}
		if terr := checkTagName(in.Tag); terr != nil {
			return ReleaseAssetListOut{}, terr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return ReleaseAssetListOut{}, cerr
		}
		row, _, lerr := d.loadReleaseForRead(ctx, rq, rc, in.Tag)
		if lerr != nil {
			return ReleaseAssetListOut{}, lerr
		}
		limit := clampLimit(in.Limit, 50, 200)
		assets, aerr := d.Store.ReleaseAssets(ctx, row.ID)
		if aerr != nil {
			return ReleaseAssetListOut{}, op.Wrap(op.CodeInternal, aerr, "asset list failed")
		}
		out := ReleaseAssetListOut{Assets: []ReleaseAssetJSON{}}
		started := in.Cursor == ""
		truncated := false
		for _, asset := range assets {
			if !started {
				if asset.Name == in.Cursor {
					started = true
				}
				continue
			}
			if len(out.Assets) == limit {
				truncated = true
				break
			}
			out.Assets = append(out.Assets, releaseAssetJSON(rc, row.TagName, asset))
		}
		if truncated {
			out.Next = out.Assets[len(out.Assets)-1].Name
		}
		return out, nil
	}
}

func releaseAssetCreateHandler(d Deps) func(context.Context, op.Principal, ReleaseAssetCreateIn) (ReleaseAssetCreateOut, error) {
	return func(ctx context.Context, p op.Principal, in ReleaseAssetCreateIn) (ReleaseAssetCreateOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ReleaseAssetCreateOut{}, rerr
		}
		if terr := checkTagName(in.Tag); terr != nil {
			return ReleaseAssetCreateOut{}, terr
		}
		if nerr := checkAssetName(in.Name); nerr != nil {
			return ReleaseAssetCreateOut{}, nerr
		}
		_, row, lerr := d.loadReleaseForWrite(ctx, rq, in.Owner, in.Repo, in.Tag)
		if lerr != nil {
			return ReleaseAssetCreateOut{}, lerr
		}
		if row.State != "draft" {
			return ReleaseAssetCreateOut{}, op.Errorf(op.CodeConflict, "published releases are immutable")
		}
		if in.IfMatch == "" && in.IfNoneMatch == "" {
			return ReleaseAssetCreateOut{}, op.Errorf(op.CodePreconditionReq, "If-Match or If-None-Match is required")
		}
		assets, aerr := d.Store.ReleaseAssets(ctx, row.ID)
		if aerr != nil {
			return ReleaseAssetCreateOut{}, op.Wrap(op.CodeInternal, aerr, "asset lookup failed")
		}
		for _, asset := range assets {
			if !strings.EqualFold(asset.Name, in.Name) {
				continue
			}
			if in.IfNoneMatch != "" {
				return ReleaseAssetCreateOut{}, op.Errorf(op.CodeConflict, "asset already exists")
			}
			if !etagMatches(in.IfMatch, assetETag(asset.SHA256)) {
				return ReleaseAssetCreateOut{}, op.Errorf(op.CodeStale, "asset changed under this request")
			}
		}
		return ReleaseAssetCreateOut{}, op.Errorf(op.CodeUnsupportedMedia, "upload asset bytes as application/octet-stream")
	}
}

func releaseAssetGetHandler(d Deps) func(context.Context, op.Principal, ReleaseAssetGetIn) (ReleaseAssetGetOut, error) {
	return func(ctx context.Context, p op.Principal, in ReleaseAssetGetIn) (ReleaseAssetGetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ReleaseAssetGetOut{}, rerr
		}
		if terr := checkTagName(in.Tag); terr != nil {
			return ReleaseAssetGetOut{}, terr
		}
		if nerr := checkAssetName(in.Name); nerr != nil {
			return ReleaseAssetGetOut{}, nerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return ReleaseAssetGetOut{}, cerr
		}
		row, _, lerr := d.loadReleaseForRead(ctx, rq, rc, in.Tag)
		if lerr != nil {
			return ReleaseAssetGetOut{}, lerr
		}
		asset, aerr := d.findReleaseAsset(ctx, row.ID, in.Name)
		if aerr != nil {
			return ReleaseAssetGetOut{}, aerr
		}
		return ReleaseAssetGetOut{Asset: releaseAssetJSON(rc, row.TagName, asset)}, nil
	}
}

func (d Deps) findReleaseAsset(ctx context.Context, releaseID int64, name string) (store.AssetRow, error) {
	assets, aerr := d.Store.ReleaseAssets(ctx, releaseID)
	if aerr != nil {
		return store.AssetRow{}, op.Wrap(op.CodeInternal, aerr, "asset lookup failed")
	}
	for _, asset := range assets {
		if strings.EqualFold(asset.Name, name) {
			return asset, nil
		}
	}
	uuid, uerr := d.Store.AssetByPublicID(ctx, name)
	if uerr != nil {
		if errors.Is(uerr, store.ErrNotFound) {
			return store.AssetRow{}, op.Errorf(op.CodeNotFound, "asset was not found")
		}
		return store.AssetRow{}, op.Wrap(op.CodeInternal, uerr, "asset lookup failed")
	}
	if uuid.ReleaseID != releaseID {
		return store.AssetRow{}, op.Errorf(op.CodeNotFound, "asset was not found")
	}
	return uuid, nil
}

func releaseAssetDeleteHandler(d Deps) func(context.Context, op.Principal, ReleaseAssetDeleteIn) (ReleaseAssetDeleteOut, error) {
	return func(ctx context.Context, p op.Principal, in ReleaseAssetDeleteIn) (ReleaseAssetDeleteOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ReleaseAssetDeleteOut{}, rerr
		}
		if terr := checkTagName(in.Tag); terr != nil {
			return ReleaseAssetDeleteOut{}, terr
		}
		if nerr := checkAssetName(in.Name); nerr != nil {
			return ReleaseAssetDeleteOut{}, nerr
		}
		_, row, lerr := d.loadReleaseForWrite(ctx, rq, in.Owner, in.Repo, in.Tag)
		if lerr != nil {
			return ReleaseAssetDeleteOut{}, lerr
		}
		if row.State != "draft" {
			return ReleaseAssetDeleteOut{}, op.Errorf(op.CodeConflict, "published releases are immutable")
		}
		if in.IfMatch == "" {
			return ReleaseAssetDeleteOut{}, op.Errorf(op.CodePreconditionReq, "If-Match is required")
		}
		asset, aerr := d.findReleaseAsset(ctx, row.ID, in.Name)
		if aerr != nil {
			return ReleaseAssetDeleteOut{}, aerr
		}
		if !etagMatches(in.IfMatch, assetETag(asset.SHA256)) {
			return ReleaseAssetDeleteOut{}, op.Errorf(op.CodeStale, "asset changed under this request")
		}
		derr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			return tx.DeleteAssetByPublicID(asset.PublicID)
		})
		if derr != nil {
			if errors.Is(derr, store.ErrNotFound) {
				return ReleaseAssetDeleteOut{}, op.Errorf(op.CodeConflict, "asset changed under this request")
			}
			return ReleaseAssetDeleteOut{}, op.Wrap(op.CodeInternal, derr, "asset delete failed")
		}
		return ReleaseAssetDeleteOut{}, nil
	}
}

func checkAssetName(name string) error {
	if len(name) < 1 || len(name) > 255 {
		return op.Errorf(op.CodeValidation, "asset name must be 1 to 255 characters")
	}
	if strings.Contains(name, "/") {
		return op.Errorf(op.CodeValidation, "asset name must not contain slashes")
	}
	for _, c := range name {
		if c < 0x20 || c == 0x7f {
			return op.Errorf(op.CodeValidation, "asset name must not contain control characters")
		}
	}
	return nil
}

func archiveGetHandler(d Deps) func(context.Context, op.Principal, ArchiveGetIn) (ArchiveGetOut, error) {
	return func(ctx context.Context, p op.Principal, in ArchiveGetIn) (ArchiveGetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ArchiveGetOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return ArchiveGetOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
			return ArchiveGetOut{}, derr
		}
		format, label, gzipped, ferr := normalizeArchiveFormat(in.Format)
		if ferr != nil {
			return ArchiveGetOut{}, ferr
		}
		if d.Ruk == nil {
			return ArchiveGetOut{}, op.Errorf(op.CodeUnavailable, "ruk backend is not configured")
		}
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return ArchiveGetOut{}, oerr
		}
		cc := rc
		cc.daemon = daemon
		resolved, rverr := d.resolveRev(ctx, cc, in.Rev)
		if rverr != nil {
			return ArchiveGetOut{}, rverr
		}
		return ArchiveGetOut{
			Commit:      resolved.commit,
			Format:      label,
			ContentType: archiveContentType(format, gzipped),
			ETag:        "\"" + resolved.commit + ":" + label + "\"",
		}, nil
	}
}

func normalizeArchiveFormat(raw string) (ruk.ArchiveFormat, string, bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "tar":
		return ruk.ArchiveTar, "tar", false, nil
	case "tar.gz", "tgz":
		return ruk.ArchiveTar, "tar.gz", true, nil
	case "zip":
		return ruk.ArchiveZip, "zip", false, nil
	default:
		return "", "", false, op.Errorf(op.CodeInvalid, "archive format must be tar or zip")
	}
}

func archiveContentType(format ruk.ArchiveFormat, gzipped bool) string {
	if gzipped {
		return "application/gzip"
	}
	if format == ruk.ArchiveZip {
		return "application/zip"
	}
	return "application/x-tar"
}

func RegisterReleases(t *op.Table, deps Deps) error {
	if err := op.Register(t, op.Op[ReleaseListIn, ReleaseListOut]{
		Name: "release.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/releases",
		Scope: op.ScopePublicRead, Summary: "List releases",
		Description: "Keyset pages over published releases, drafts included for writers.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out ReleaseListOut) string {
			return "\"releases-" + strconv.Itoa(len(out.Releases)) + "-" + strconv.FormatUint(etagHash(out.Next), 16) + "\""
		},
		Pagination: &op.Pagination[ReleaseListOut]{Default: 50, Max: 200, Next: func(out ReleaseListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: releaseListHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ReleaseGetIn, ReleaseGetOut]{
		Name: "release.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/releases/tag/{tag...}",
		Scope: op.ScopePublicRead, Summary: "Show a release",
		Description: "One release by tag with its assets. Drafts stay invisible without write access.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeValidation, op.CodeInvalid, op.CodeForbidden},
		ETag:        func(out ReleaseGetOut) string { return out.Release.ETag },
		Handler:     releaseGetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ReleaseCreateIn, ReleaseCreateOut]{
		Name: "release.create", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/releases/new",
		Scope: op.ScopeReleasesWrite, Summary: "Create a release",
		Description: "Create a draft, or publish at once when draft is false. Publishing pins the tag through the released-tag freeze.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeInvalid, op.CodeNotFound, op.CodeConflict, op.CodeArchived, op.CodeUnavailable},
		ETag:        func(out ReleaseCreateOut) string { return out.Release.ETag },
		Location:    func(ReleaseCreateOut) string { return "" },
		Handler:     releaseCreateHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ReleaseUpdateIn, ReleaseUpdateOut]{
		Name: "release.update", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/releases/edit/{tag...}",
		Scope: op.ScopeReleasesWrite, Summary: "Update or publish a draft release",
		Description: "Drafts stay mutable until publish. Publishing pins the tag through the released-tag freeze.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeInvalid, op.CodeNotFound, op.CodeConflict, op.CodeStale, op.CodePreconditionReq, op.CodeArchived, op.CodeUnavailable},
		Concurrency: op.ConcurrencyIfMatchRequired,
		Status:      http.StatusOK,
		ETag:        func(out ReleaseUpdateOut) string { return out.Release.ETag },
		Location:    func(ReleaseUpdateOut) string { return "" },
		Handler:     releaseUpdateHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ReleaseDeleteIn, ReleaseDeleteOut]{
		Name: "release.delete", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/releases/delete/{tag...}",
		Scope: op.ScopeReleasesWrite, Summary: "Delete a release",
		Description: "Published releases soft-delete and clear the freeze marker with an owner check. Drafts discard.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeInvalid, op.CodeNotFound, op.CodeStale, op.CodePreconditionReq, op.CodeArchived, op.CodeUnavailable},
		Concurrency: op.ConcurrencyIfMatchRequired,
		Status:      http.StatusOK,
		ETag:        func(ReleaseDeleteOut) string { return "\"release-deleted\"" },
		Location:    func(ReleaseDeleteOut) string { return "" },
		Handler:     releaseDeleteHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ReleaseAssetListIn, ReleaseAssetListOut]{
		Name: "release.asset.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/releases/assets/{tag}",
		Scope: op.ScopePublicRead, Summary: "List release assets",
		Description: "Keyset pages over one release asset set by name.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeValidation, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out ReleaseAssetListOut) string {
			return "\"assets-" + strconv.Itoa(len(out.Assets)) + "-" + strconv.FormatUint(etagHash(out.Next), 16) + "\""
		},
		Pagination: &op.Pagination[ReleaseAssetListOut]{Default: 50, Max: 200, Next: func(out ReleaseAssetListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: releaseAssetListHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ReleaseAssetCreateIn, ReleaseAssetCreateOut]{
		Name: "release.asset.create", Kind: op.KindMutation, Method: http.MethodPut, Path: "/{owner}/{repo}/releases/assets/{tag}/{name}",
		Scope: op.ScopeReleasesWrite, Summary: "Upload a release asset",
		Description: "Streams asset bytes for a draft release with digest verification. The byte upload adapter serves this route.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeInvalid, op.CodeNotFound, op.CodeConflict, op.CodeStale, op.CodePreconditionReq, op.CodeArchived, op.CodeUnsupportedMedia},
		Concurrency: op.ConcurrencyIfMatchOptional,
		Status:      http.StatusCreated,
		ETag:        func(out ReleaseAssetCreateOut) string { return assetETag(out.Asset.SHA256) },
		Location:    func(out ReleaseAssetCreateOut) string { return out.Asset.URL },
		Handler:     releaseAssetCreateHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ReleaseAssetGetIn, ReleaseAssetGetOut]{
		Name: "release.asset.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/releases/assets/{tag}/{name}",
		Scope: op.ScopePublicRead, Summary: "Describe a release asset",
		Description: "One asset metadata record. The byte download adapter serves this route for byte requests.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeValidation, op.CodeInvalid, op.CodeForbidden},
		ETag:        func(out ReleaseAssetGetOut) string { return assetETag(out.Asset.SHA256) },
		Handler:     releaseAssetGetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ReleaseAssetDeleteIn, ReleaseAssetDeleteOut]{
		Name: "release.asset.delete", Kind: op.KindMutation, Method: http.MethodDelete, Path: "/{owner}/{repo}/releases/assets/{tag}/{name}",
		Scope: op.ScopeReleasesWrite, Summary: "Delete a draft release asset",
		Description: "Draft assets delete. Published assets refuse.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeInvalid, op.CodeNotFound, op.CodeConflict, op.CodeStale, op.CodePreconditionReq, op.CodeArchived},
		Concurrency: op.ConcurrencyIfMatchRequired,
		Status:      http.StatusNoContent,
		ETag:        func(ReleaseAssetDeleteOut) string { return "\"asset-deleted\"" },
		Location:    func(ReleaseAssetDeleteOut) string { return "" },
		Handler:     releaseAssetDeleteHandler(deps),
	}); err != nil {
		return err
	}
	return op.Register(t, op.Op[ArchiveGetIn, ArchiveGetOut]{
		Name: "code.archive.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/archive/{rev}",
		Scope: op.ScopePublicRead, Summary: "Describe a streamed archive",
		Description: "Archive metadata for one revision. The byte download adapter streams tar or zip through the Ruk archive method.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden, op.CodeUnavailable},
		ETag:        func(out ArchiveGetOut) string { return out.ETag },
		Handler:     archiveGetHandler(deps),
	})
}
