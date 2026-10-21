package api

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/rukproc"
	"github.com/gosuda/rukie/internal/store"
)

type RepoJSON struct {
	PurgeAfter    *int64   `json:"purge_after,omitempty"`
	Owner         string   `json:"owner"`
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	Website       string   `json:"website,omitempty"`
	Visibility    string   `json:"visibility"`
	DefaultBranch string   `json:"default_branch"`
	State         string   `json:"state"`
	Topics        []string `json:"topics,omitempty"`
	CreatedAt     int64    `json:"created_at"`
	UpdatedAt     int64    `json:"updated_at"`
	Archived      bool     `json:"archived"`
}

func repoJSON(owner string, row store.RepoRow) RepoJSON {
	out := RepoJSON{
		Owner:         owner,
		Name:          row.Name,
		Description:   row.Description,
		Website:       row.Website,
		Visibility:    row.Visibility,
		DefaultBranch: row.DefaultBranch,
		State:         "active",
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
		Archived:      row.Archived,
	}
	if row.Topics != "" && row.Topics != "[]" {
		var topics []string
		if unmarshalJSON([]byte(row.Topics), &topics) == nil {
			out.Topics = topics
		}
	}
	if row.DeletedAt.Valid {
		out.State = "deleted"
		if row.PurgeAfter.Valid {
			purge := row.PurgeAfter.Int64
			out.PurgeAfter = &purge
		}
	}
	return out
}

func repoOwnerName(ctx context.Context, st *store.Store, row store.RepoRow) string {
	if row.OwnerUserID.Valid && st != nil {
		if user, uerr := st.LookupUserByID(ctx, row.OwnerUserID.Int64); uerr == nil {
			return user.Name
		}
	}
	return ""
}

type RepoCreateBody struct {
	Owner         string   `json:"owner" minLength:"1" maxLength:"39"`
	Name          string   `json:"name" minLength:"1" maxLength:"100"`
	Description   string   `json:"description,omitempty" maxLength:"2048"`
	Website       string   `json:"website,omitempty" maxLength:"2048"`
	Visibility    string   `json:"visibility,omitempty"`
	DefaultBranch string   `json:"default_branch,omitempty" maxLength:"255"`
	Topics        []string `json:"topics,omitempty"`
	InitReadme    bool     `json:"init_readme,omitempty"`
}

type RepoCreateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           RepoCreateBody
}

type RepoCreateOut struct {
	Repo RepoJSON `json:"repo"`
}

type RepoGetIn struct {
	Owner string `path:"owner"`
	Repo  string `path:"repo"`
}

type RepoReadmeJSON struct {
	Path      string `json:"path"`
	Content   string `json:"content,omitempty"`
	Size      uint64 `json:"size"`
	Truncated bool   `json:"truncated"`
}

type RepoGetOut struct {
	Readme *RepoReadmeJSON `json:"readme,omitempty"`
	Commit string          `json:"commit,omitempty"`
	Next   string          `json:"-"`
	Tree   []TreeEntryJSON `json:"tree,omitempty"`
	Repo   RepoJSON        `json:"repo"`
	Empty  bool            `json:"empty"`
}

type RepoListIn struct {
	Owner      string `query:"owner"`
	Visibility string `query:"visibility"`
	Sort       string `query:"sort"`
	Cursor     string `query:"cursor"`
	Limit      int    `query:"limit"`
}

type RepoListOut struct {
	Next  string     `json:"-"`
	Repos []RepoJSON `json:"repos"`
}

type RepoUpdateBody struct {
	Description   *string  `json:"description,omitempty" maxLength:"2048"`
	Website       *string  `json:"website,omitempty" maxLength:"2048"`
	DefaultBranch *string  `json:"default_branch,omitempty" maxLength:"255"`
	Archived      *bool    `json:"archived,omitempty"`
	Topics        []string `json:"topics,omitempty"`
}

type RepoUpdateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	IfMatch        string `header:"If-Match"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Body           RepoUpdateBody
}

type RepoUpdateOut struct {
	Repo RepoJSON `json:"repo"`
}

type RepoVisibilityBody struct {
	Visibility string `json:"visibility"`
	Confirm    string `json:"confirm" maxLength:"140"`
}

type RepoVisibilityIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	IfMatch        string `header:"If-Match"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Body           RepoVisibilityBody
}

type RepoVisibilityOut struct {
	Repo RepoJSON `json:"repo"`
}

type RepoRenameBody struct {
	Name     string `json:"name" minLength:"1" maxLength:"100"`
	NewOwner string `json:"new_owner,omitempty" maxLength:"39"`
}

type RepoRenameIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	IfMatch        string `header:"If-Match"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Body           RepoRenameBody
}

type RepoRenameOut struct {
	Repo RepoJSON `json:"repo"`
}

type RepoDeleteBody struct {
	Confirm string `json:"confirm" maxLength:"140"`
}

type RepoDeleteIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	IfMatch        string `header:"If-Match"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Body           RepoDeleteBody
}

type RepoDeleteOut struct {
	State      string `json:"state"`
	PurgeAfter int64  `json:"purge_after"`
}

type RepoRestoreIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	IfMatch        string `header:"If-Match"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
}

type RepoRestoreOut struct {
	Repo RepoJSON `json:"repo"`
}

type CollaboratorListIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

type CollaboratorItem struct {
	User       string `json:"user"`
	Permission string `json:"permission"`
	Via        string `json:"via"`
}

type TeamGrantJSON struct {
	Team       string `json:"team"`
	Permission string `json:"permission"`
}

type CollaboratorListOut struct {
	Next          string             `json:"-"`
	Collaborators []CollaboratorItem `json:"collaborators"`
	Teams         []TeamGrantJSON    `json:"teams,omitempty"`
}

type CollaboratorAddBody struct {
	Permission string `json:"permission"`
}

type CollaboratorAddIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	User           string `path:"user"`
	Body           CollaboratorAddBody
}

type CollaboratorAddOut struct{}

type CollaboratorRemoveIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	User           string `path:"user"`
}

type CollaboratorRemoveOut struct{}

func fullName(owner, name string) string {
	return owner + "/" + name
}

func requireStore(d Deps) (*store.Store, error) {
	if d.Store == nil {
		return nil, op.Errorf(op.CodeUnavailable, "repository store is not configured")
	}
	return d.Store, nil
}

func createRepoHandler(d Deps) func(context.Context, op.Principal, RepoCreateIn) (RepoCreateOut, error) {
	return func(ctx context.Context, p op.Principal, in RepoCreateIn) (RepoCreateOut, error) {
		st, serr := requireStore(d)
		if serr != nil {
			return RepoCreateOut{}, serr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return RepoCreateOut{}, rerr
		}
		if !rq.authed {
			return RepoCreateOut{}, op.Errorf(op.CodeUnauthorized, "authentication is required")
		}
		body := in.Body
		owner := strings.ToLower(strings.TrimSpace(body.Owner))
		if err := checkOwnerName(owner); err != nil {
			return RepoCreateOut{}, err
		}
		if err := checkRepoName(body.Name); err != nil {
			return RepoCreateOut{}, err
		}
		visibility := cmp.Or(body.Visibility, "private")
		if err := checkVisibility(visibility); err != nil {
			return RepoCreateOut{}, err
		}
		defaultBranch := cmp.Or(body.DefaultBranch, "main")
		if err := checkBranchName(defaultBranch); err != nil {
			return RepoCreateOut{}, err
		}
		if body.Website != "" && !isHTTPURL(body.Website) {
			return RepoCreateOut{}, op.Errorf(op.CodeValidation, "website must be an http or https URL")
		}
		topicsJSON, terr := checkTopics(body.Topics)
		if terr != nil {
			return RepoCreateOut{}, terr
		}
		if rq.isToken {
			if !rq.caller.Scopes.Has(op.ScopeRepoCreate) {
				return RepoCreateOut{}, op.Errorf(op.CodeForbidden, "token needs the repo:create scope")
			}
			if owner != strings.ToLower(rq.user.Name) {
				return RepoCreateOut{}, op.Errorf(op.CodeForbidden, "tokens create only in the token owner's namespace")
			}
		} else if owner != strings.ToLower(rq.user.Name) {
			return RepoCreateOut{}, op.Errorf(op.CodeForbidden, "organization repositories are not served yet")
		}
		destMeta, uerr := d.Store.LookupUserByName(ctx, owner)
		if uerr != nil {
			return RepoCreateOut{}, op.Errorf(op.CodeValidation, "destination owner does not exist")
		}
		destUser := auth.User{ID: destMeta.ID, Name: destMeta.Name, Email: destMeta.Email, FullName: destMeta.FullName, IsActive: destMeta.IsActive, IsAdmin: destMeta.IsAdmin}
		daemonPath := owner + "/" + body.Name + ".git"
		if d.RepoRoot == "" {
			return RepoCreateOut{}, op.Errorf(op.CodeUnavailable, "repository root is not configured")
		}
		if d.Ruk == nil {
			return RepoCreateOut{}, op.Errorf(op.CodeUnavailable, "ruk backend is not configured")
		}
		initArgs := map[string]any{
			"root":           d.RepoRoot,
			"path":           daemonPath,
			"default_branch": "refs/heads/" + defaultBranch,
		}
		var initReply repoInitReply
		ierr := d.callRuk(ctx, ruk.Repo{}, wire.MethodRepoInit, initArgs, &initReply)
		if ierr != nil {
			if oerr, ok := errors.AsType[*op.Error](ierr); ok && oerr.Code == op.CodeConflict {
				return RepoCreateOut{}, op.Errorf(op.CodeConflict, "repository name is already taken")
			}
			return RepoCreateOut{}, ierr
		}
		if body.InitReadme {
			if rerr := d.commitInitialReadme(ctx, daemonPath, defaultBranch, destUser, initReply.ObjectFormat); rerr != nil {
				return RepoCreateOut{}, rerr
			}
		}
		now := d.now()
		var row store.RepoRow
		werr := st.Write(ctx, func(tx *store.WriteTx) error {
			id, ierr := tx.InsertRepo(now, store.RepoInsert{
				OwnerUserID:   sql.NullInt64{Int64: destUser.ID, Valid: true},
				Name:          body.Name,
				Visibility:    visibility,
				Description:   body.Description,
				Website:       body.Website,
				Topics:        topicsJSON,
				DefaultBranch: defaultBranch,
				RukPath:       daemonPath,
			})
			if ierr != nil {
				return ierr
			}
			if rerr := tx.InsertDefaultRule(id, now, defaultBranch); rerr != nil {
				return rerr
			}
			createdBody, _ := marshalJSON(map[string]any{
				"owner": owner, "name": body.Name, "repo_id": id, "visibility": visibility,
			})
			if _, rerr := tx.Outbox().Emit(store.Event{
				Kind:       "repo.created",
				Body:       createdBody,
				OccurredAt: now,
			}); rerr != nil {
				return rerr
			}
			var gerr error
			row, gerr = tx.GetRepoForUpdate(id, false)
			return gerr
		})
		if werr != nil {
			if isUniqueViolation(werr) {
				return RepoCreateOut{}, op.Errorf(op.CodeConflict, "repository name is already taken")
			}
			return RepoCreateOut{}, op.Wrap(op.CodeInternal, werr, "repository record failed")
		}
		if d.OnRepoCreated != nil {
			d.OnRepoCreated(row.ID)
		}
		out := RepoCreateOut{Repo: repoJSON(destUser.Name, row)}
		return out, nil
	}
}

type repoInitReply struct {
	DefaultBranch *string `json:"default_branch"`
	Path          string  `json:"path"`
	StoreID       string  `json:"store_id"`
	ObjectFormat  string  `json:"object_format"`
}

func isHTTPURL(raw string) bool {
	if len(raw) > 2048 {
		return false
	}
	lower := strings.ToLower(raw)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "constraint")
}

func (d Deps) commitInitialReadme(ctx context.Context, daemonPath, defaultBranch string, owner auth.User, objectFormat string) error {
	bin := d.RukBin
	if bin == "" {
		resolved, rerr := rukproc.ResolveBinary("")
		if rerr != nil {
			return op.Errorf(op.CodeUnavailable, "ruk binary is not available for the readme commit")
		}
		bin = resolved
	}
	abs := daemonPath
	if d.RepoRoot != "" && !isAbsPath(abs) {
		abs = joinPath(d.RepoRoot, abs)
	}
	content := "# " + filepath.Base(strings.TrimSuffix(abs, ".git")) + "\n"
	format := strings.ToLower(objectFormat)
	blob, berr := stageBlob(ctx, bin, abs, format, content)
	if berr != nil {
		return berr
	}
	tree, terr := stageTree(ctx, bin, abs, format, blob)
	if terr != nil {
		return terr
	}
	now := d.now()
	email := owner.Email
	if email == "" {
		email = strings.ToLower(owner.Name) + "@rukie.local"
	}
	name := owner.FullName
	if name == "" {
		name = owner.Name
	}
	sig := map[string]any{
		"name": name, "email": email,
		"timestamp": now.Unix(), "offset": "+0000",
	}
	repo, oerr := d.Ruk.OpenRepo(ctx, filepath.Join(abs, ".ruk"))
	if oerr != nil {
		return mapRukError(oerr)
	}
	args := map[string]any{
		"tree": tree, "parents": []any{},
		"author": sig, "committer": sig,
		"message": "Initial commit\n",
		"ref_update": map[string]any{
			"name": "refs/heads/" + defaultBranch, "expected": "absent",
		},
	}
	var reply map[string]any
	cerr := d.callRuk(ctx, repo, wire.MethodGitCommit, args, &reply)
	if cerr != nil {
		if oerr, ok := errors.AsType[*op.Error](cerr); ok && oerr.Code == op.CodeConflict {
			return nil
		}
		return cerr
	}
	return nil
}

func stageEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"RUK_COORDINATOR=off",
		"LANG=C",
	}
}

func stageBlob(ctx context.Context, bin, repoPath, format, content string) (string, error) {
	staged, serr := writeStageFile(content)
	if serr != nil {
		return "", serr
	}
	defer func() { _ = os.Remove(staged) }()
	out, rerr := rukproc.Run(ctx, bin,
		[]string{"git", "--git-dir=" + repoPath, "hash-object", "-w", staged}, stageEnv())
	if rerr != nil {
		return "", op.Wrap(op.CodeUnavailable, rerr, "readme blob could not be staged")
	}
	return parseStagedOid(strings.TrimSpace(string(out)), format)
}

func stageTree(ctx context.Context, bin, repoPath, format, blob string) (string, error) {
	entry, eerr := treeEntryBytes(format, blob)
	if eerr != nil {
		return "", eerr
	}
	staged, serr := writeStageFileBytes(entry)
	if serr != nil {
		return "", serr
	}
	defer func() { _ = os.Remove(staged) }()
	out, rerr := rukproc.Run(ctx, bin,
		[]string{"git", "--git-dir=" + repoPath, "hash-object", "-w", "-t", "tree", staged}, stageEnv())
	if rerr != nil {
		return "", op.Wrap(op.CodeUnavailable, rerr, "readme tree could not be staged")
	}
	return parseStagedOid(strings.TrimSpace(string(out)), format)
}

func writeStageFile(content string) (string, error) {
	return writeStageFileBytes([]byte(content))
}

func writeStageFileBytes(content []byte) (string, error) {
	staged, serr := os.CreateTemp("", "rukie-readme-*")
	if serr != nil {
		return "", op.Wrap(op.CodeInternal, serr, "readme staging file failed")
	}
	if _, werr := staged.Write(content); werr != nil {
		_ = staged.Close()
		_ = os.Remove(staged.Name())
		return "", op.Wrap(op.CodeInternal, werr, "readme staging file failed")
	}
	if cerr := staged.Close(); cerr != nil {
		_ = os.Remove(staged.Name())
		return "", op.Wrap(op.CodeInternal, cerr, "readme staging file failed")
	}
	return staged.Name(), nil
}

func treeEntryBytes(format, blob string) ([]byte, error) {
	raw, herr := parseStagedOid(blob, format)
	if herr != nil {
		return nil, herr
	}
	oid, _ := hexDecode(raw)
	entry := []byte("100644 README.md\x00")
	return append(entry, oid...), nil
}

func parseStagedOid(out, format string) (string, error) {
	lower := strings.ToLower(strings.TrimSpace(out))
	want := 40
	if format == "sha256" {
		want = 64
	}
	if len(lower) != want || !isFullOid(lower) {
		return "", op.Errorf(op.CodeUnavailable, "ruk staged an unreadable object id")
	}
	return lower, nil
}

func hexDecode(s string) ([]byte, error) {
	raw := make([]byte, len(s)/2)
	for i := range raw {
		hi := hexValue(s[2*i])
		lo := hexValue(s[2*i+1])
		if hi > 15 || lo > 15 {
			return nil, fmt.Errorf("not hexadecimal")
		}
		raw[i] = hi<<4 | lo
	}
	return raw, nil
}

func hexValue(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	default:
		return 255
	}
}

func getRepoHandler(d Deps) func(context.Context, op.Principal, RepoGetIn) (RepoGetOut, error) {
	return func(ctx context.Context, p op.Principal, in RepoGetIn) (RepoGetOut, error) {
		if _, serr := requireStore(d); serr != nil {
			return RepoGetOut{}, serr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return RepoGetOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, true)
		if cerr != nil {
			return RepoGetOut{}, cerr
		}
		if rc.row.DeletedAt.Valid {
			if !canSeeDeleted(rq, rc.row) {
				return RepoGetOut{}, op.Errorf(op.CodeNotFound, "repository was not found")
			}
			return RepoGetOut{Repo: repoJSON(rc.owner, rc.row)}, nil
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
			return RepoGetOut{}, derr
		}
		out := RepoGetOut{Repo: repoJSON(rc.owner, rc.row)}
		if derr := d.fillRepoHome(ctx, rc, &out); derr != nil {
			return RepoGetOut{}, derr
		}
		return out, nil
	}
}

func (d Deps) fillRepoHome(ctx context.Context, rc repoContext, out *RepoGetOut) error {
	if d.Ruk == nil {
		return nil
	}
	daemon, oerr := d.openDaemonRepo(ctx, rc.row)
	if oerr != nil {
		return oerr
	}
	rc.daemon = daemon
	resolved, rerr := d.resolveRev(ctx, rc, rc.row.DefaultBranch)
	if rerr != nil {
		if oerr, ok := errors.AsType[*op.Error](rerr); ok && oerr.Code == op.CodeNotFound {
			out.Empty = true
			return nil
		}
		return rerr
	}
	out.Commit = resolved.commit
	var reply treeReply
	if terr := d.callRuk(ctx, daemon, wire.MethodTree, map[string]any{
		"rev": map[string]any{"commit": resolved.commit}, "limit": 100,
	}, &reply); terr != nil {
		return terr
	}
	for _, entry := range reply.Entries.Items {
		out.Tree = append(out.Tree, TreeEntryJSON{
			Name: entry.Name.String(), Oid: entry.Oid, Kind: entry.Kind,
		})
	}
	if reply.Entries.NextAfter != nil {
		out.Next = reply.Entries.NextAfter.String()
	}
	if readme := pickReadme(reply.Entries.Items); readme != "" {
		body, berr := d.readFileBody(ctx, daemon, resolved.commit, readme)
		if berr == nil && body.Blob != nil {
			content, cerr := byteSourceBytes(body.Blob.Content)
			if cerr == nil && !isBinaryContent(content) {
				const readmeCap = 64 << 10
				out.Readme = &RepoReadmeJSON{Path: readme, Size: body.Blob.Size}
				if len(content) > readmeCap {
					out.Readme.Content = string(content[:readmeCap])
					out.Readme.Truncated = true
				} else {
					out.Readme.Content = string(content)
				}
			}
		}
	}
	return nil
}

func pickReadme(entries []TreeEntry) string {
	for _, entry := range entries {
		if entry.Name.String() == "README.md" {
			return "README.md"
		}
	}
	for _, entry := range entries {
		name := entry.Name.String()
		if name == "README" {
			return name
		}
		if len(name) > 7 {
			lower := strings.ToLower(name[:7])
			if lower == "readme." {
				return name
			}
		}
	}
	return ""
}

func canSeeDeleted(rq requester, row store.RepoRow) bool {
	if !rq.authed {
		return false
	}
	if rq.caller.IsAdmin {
		return true
	}
	if row.OwnerUserID.Valid && row.OwnerUserID.Int64 == rq.userID {
		return true
	}
	return false
}

func listReposHandler(d Deps) func(context.Context, op.Principal, RepoListIn) (RepoListOut, error) {
	return func(ctx context.Context, p op.Principal, in RepoListIn) (RepoListOut, error) {
		st, serr := requireStore(d)
		if serr != nil {
			return RepoListOut{}, serr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return RepoListOut{}, rerr
		}
		filter := store.RepoListFilter{Limit: clampLimit(in.Limit, 30, 100), Cursor: in.Cursor}
		switch in.Sort {
		case "", "updated", "created", "name":
			if in.Sort == "" {
				filter.Sort = "updated"
			} else {
				filter.Sort = in.Sort
			}
		default:
			return RepoListOut{}, op.Errorf(op.CodeValidation, "sort must be updated, created, or name")
		}
		if in.Visibility != "" {
			if verr := checkVisibility(in.Visibility); verr != nil {
				return RepoListOut{}, verr
			}
			filter.Visibility = in.Visibility
		}
		if in.Owner != "" {
			if oerr := checkOwnerName(strings.ToLower(in.Owner)); oerr != nil {
				return RepoListOut{}, oerr
			}
			umeta, uerr := d.Store.LookupUserByName(ctx, strings.ToLower(in.Owner))
			if uerr != nil {
				return RepoListOut{Repos: []RepoJSON{}}, nil
			}
			filter.OwnerUserID = umeta.ID
		}
		var rows []store.RepoRow
		var next string
		rderr := st.Read(ctx, func(tx *store.ReadTx) error {
			var lerr error
			rows, next, lerr = tx.ListRepos(filter)
			return lerr
		})
		if rderr != nil {
			return RepoListOut{}, op.Wrap(op.CodeInternal, rderr, "repository listing failed")
		}
		out := RepoListOut{Next: next, Repos: []RepoJSON{}}
		for _, row := range rows {
			owner := repoOwnerName(ctx, st, row)
			if owner == "" {
				continue
			}
			if derr := d.checkRepoAccess(rq, row, "repo.view", ""); derr != nil {
				continue
			}
			out.Repos = append(out.Repos, repoJSON(owner, row))
		}
		return out, nil
	}
}

func updateRepoHandler(d Deps) func(context.Context, op.Principal, RepoUpdateIn) (RepoUpdateOut, error) {
	return func(ctx context.Context, p op.Principal, in RepoUpdateIn) (RepoUpdateOut, error) {
		st, serr := requireStore(d)
		if serr != nil {
			return RepoUpdateOut{}, serr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return RepoUpdateOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return RepoUpdateOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.settings", ""); derr != nil {
			return RepoUpdateOut{}, derr
		}
		body := in.Body
		if body.Website != nil && *body.Website != "" && !isHTTPURL(*body.Website) {
			return RepoUpdateOut{}, op.Errorf(op.CodeValidation, "website must be an http or https URL")
		}
		var topicsJSON *string
		if body.Topics != nil {
			encoded, terr := checkTopics(body.Topics)
			if terr != nil {
				return RepoUpdateOut{}, terr
			}
			topicsJSON = &encoded
		}
		if body.DefaultBranch != nil {
			if berr := checkBranchName(*body.DefaultBranch); berr != nil {
				return RepoUpdateOut{}, berr
			}
		}
		now := d.now()
		var updated store.RepoRow
		werr := st.Write(ctx, func(tx *store.WriteTx) error {
			current, gerr := tx.GetRepoForUpdate(rc.row.ID, false)
			if gerr != nil {
				if errors.Is(gerr, store.ErrNotFound) {
					return op.Errorf(op.CodeNotFound, "repository was not found")
				}
				return gerr
			}
			if merr := matchRepoETag(in.IfMatch, rc.owner, current); merr != nil {
				return merr
			}
			up := store.RepoUpdate{
				Description:   body.Description,
				Website:       body.Website,
				Topics:        topicsJSON,
				DefaultBranch: body.DefaultBranch,
				Archived:      body.Archived,
			}
			if uerr := tx.UpdateRepo(rc.row.ID, now, up); uerr != nil {
				return uerr
			}
			var ferr error
			updated, ferr = tx.GetRepoForUpdate(rc.row.ID, false)
			return ferr
		})
		if werr != nil {
			if _, ok := errors.AsType[*op.Error](werr); ok {
				return RepoUpdateOut{}, werr
			}
			return RepoUpdateOut{}, op.Wrap(op.CodeInternal, werr, "repository update failed")
		}
		return RepoUpdateOut{Repo: repoJSON(rc.owner, updated)}, nil
	}
}

func visibilityRepoHandler(d Deps) func(context.Context, op.Principal, RepoVisibilityIn) (RepoVisibilityOut, error) {
	return func(ctx context.Context, p op.Principal, in RepoVisibilityIn) (RepoVisibilityOut, error) {
		st, serr := requireStore(d)
		if serr != nil {
			return RepoVisibilityOut{}, serr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return RepoVisibilityOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return RepoVisibilityOut{}, cerr
		}
		if verr := checkVisibility(in.Body.Visibility); verr != nil {
			return RepoVisibilityOut{}, verr
		}
		if in.Body.Confirm != fullName(rc.owner, rc.row.Name) {
			return RepoVisibilityOut{}, op.Errorf(op.CodeValidation, "confirmation must repeat the repository full name")
		}
		action := "repo.visibility"
		if widensVisibility(rc.row.Visibility, in.Body.Visibility) {
			action = "repo.visibility_widen"
		}
		if derr := d.checkRepoAccess(rq, rc.row, action, ""); derr != nil {
			return RepoVisibilityOut{}, derr
		}
		now := d.now()
		visibility := in.Body.Visibility
		var updated store.RepoRow
		werr := st.Write(ctx, func(tx *store.WriteTx) error {
			current, gerr := tx.GetRepoForUpdate(rc.row.ID, false)
			if gerr != nil {
				if errors.Is(gerr, store.ErrNotFound) {
					return op.Errorf(op.CodeNotFound, "repository was not found")
				}
				return gerr
			}
			if merr := matchRepoETag(in.IfMatch, rc.owner, current); merr != nil {
				return merr
			}
			if uerr := tx.UpdateRepo(rc.row.ID, now, store.RepoUpdate{Visibility: &visibility}); uerr != nil {
				return uerr
			}
			var ferr error
			updated, ferr = tx.GetRepoForUpdate(rc.row.ID, false)
			return ferr
		})
		if werr != nil {
			if _, ok := errors.AsType[*op.Error](werr); ok {
				return RepoVisibilityOut{}, werr
			}
			return RepoVisibilityOut{}, op.Wrap(op.CodeInternal, werr, "visibility change failed")
		}
		return RepoVisibilityOut{Repo: repoJSON(rc.owner, updated)}, nil
	}
}

func widensVisibility(from, to string) bool {
	rank := map[string]int{"private": 0, "internal": 1, "public": 2}
	return rank[to] > rank[from]
}

func renameRepoHandler(d Deps) func(context.Context, op.Principal, RepoRenameIn) (RepoRenameOut, error) {
	return func(ctx context.Context, p op.Principal, in RepoRenameIn) (RepoRenameOut, error) {
		st, serr := requireStore(d)
		if serr != nil {
			return RepoRenameOut{}, serr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return RepoRenameOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return RepoRenameOut{}, cerr
		}
		if merr := matchRepoETag(in.IfMatch, rc.owner, rc.row); merr != nil {
			return RepoRenameOut{}, merr
		}
		if berr := checkRepoName(in.Body.Name); berr != nil {
			return RepoRenameOut{}, berr
		}
		newOwnerName := rc.owner
		if in.Body.NewOwner != "" {
			newOwnerName = strings.ToLower(in.Body.NewOwner)
			if oerr := checkOwnerName(newOwnerName); oerr != nil {
				return RepoRenameOut{}, oerr
			}
		}
		newUser, uerr := d.Store.LookupUserByName(ctx, newOwnerName)
		if uerr != nil {
			return RepoRenameOut{}, op.Errorf(op.CodeValidation, "destination owner does not exist")
		}
		moving := newUser.ID != rc.row.OwnerUserID.Int64
		if moving {
			if newOwnerName != strings.ToLower(rq.user.Name) {
				return RepoRenameOut{}, op.Errorf(op.CodeForbidden, "renames stay in the caller's namespace")
			}
			if rq.isToken && !rq.caller.Scopes.Has(op.ScopeRepoCreate) {
				return RepoRenameOut{}, op.Errorf(op.CodeForbidden, "token needs the repo:create scope for the destination")
			}
			if derr := d.checkRepoAccessWithDest(rq, rc.row, "repo.transfer", "", newUser.ID, 0); derr != nil {
				return RepoRenameOut{}, derr
			}
		} else if derr := d.checkRepoAccess(rq, rc.row, "repo.settings", ""); derr != nil {
			return RepoRenameOut{}, derr
		}
		if d.Ruk == nil {
			return RepoRenameOut{}, op.Errorf(op.CodeUnavailable, "ruk backend is not configured")
		}
		oldPath := rc.row.RukPath
		newPath := newOwnerName + "/" + in.Body.Name + ".git"
		if oldPath != "" && oldPath != newPath {
			rerr := d.callRuk(ctx, ruk.Repo{}, wire.MethodRepoRename,
				map[string]any{"repo": oldPath, "new_path": newPath}, &map[string]any{})
			if rerr != nil {
				return RepoRenameOut{}, rerr
			}
		}
		now := d.now()
		var updated store.RepoRow
		werr := st.Write(ctx, func(tx *store.WriteTx) error {
			current, gerr := tx.GetRepoForUpdate(rc.row.ID, false)
			if gerr != nil {
				if errors.Is(gerr, store.ErrNotFound) {
					return op.Errorf(op.CodeNotFound, "repository was not found")
				}
				return gerr
			}
			if merr := matchRepoETag(in.IfMatch, rc.owner, current); merr != nil {
				return merr
			}
			if rerr := tx.RenameRepo(rc.row.ID, newUser.ID, in.Body.Name, newPath, now); rerr != nil {
				if isUniqueViolation(rerr) {
					return op.Errorf(op.CodeConflict, "repository name is already taken")
				}
				return rerr
			}
			if current.OwnerUserID.Valid {
				if perr := tx.PutRedirect(current.OwnerUserID.Int64, current.Name, rc.row.ID, now); perr != nil {
					return perr
				}
			}
			var ferr error
			updated, ferr = tx.GetRepoForUpdate(rc.row.ID, false)
			return ferr
		})
		if werr != nil {
			if _, ok := errors.AsType[*op.Error](werr); ok {
				return RepoRenameOut{}, werr
			}
			if isUniqueViolation(werr) {
				return RepoRenameOut{}, op.Errorf(op.CodeConflict, "repository name is already taken")
			}
			return RepoRenameOut{}, op.Wrap(op.CodeInternal, werr, "repository rename failed")
		}
		return RepoRenameOut{Repo: repoJSON(newUser.Name, updated)}, nil
	}
}

func deleteRepoHandler(d Deps) func(context.Context, op.Principal, RepoDeleteIn) (RepoDeleteOut, error) {
	return func(ctx context.Context, p op.Principal, in RepoDeleteIn) (RepoDeleteOut, error) {
		st, serr := requireStore(d)
		if serr != nil {
			return RepoDeleteOut{}, serr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return RepoDeleteOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return RepoDeleteOut{}, cerr
		}
		if in.Body.Confirm != fullName(rc.owner, rc.row.Name) {
			return RepoDeleteOut{}, op.Errorf(op.CodeValidation, "confirmation must repeat the repository full name")
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.delete", ""); derr != nil {
			return RepoDeleteOut{}, derr
		}
		now := d.now()
		purgeAfter := now.Add(repoPurgeDelay())
		werr := st.Write(ctx, func(tx *store.WriteTx) error {
			current, gerr := tx.GetRepoForUpdate(rc.row.ID, false)
			if gerr != nil {
				if errors.Is(gerr, store.ErrNotFound) {
					return op.Errorf(op.CodeNotFound, "repository was not found")
				}
				return gerr
			}
			if merr := matchRepoETag(in.IfMatch, rc.owner, current); merr != nil {
				return merr
			}
			if serr := tx.SoftDeleteRepo(rc.row.ID, now, purgeAfter); serr != nil {
				return serr
			}
			tombstone, _ := marshalJSON(map[string]any{
				"owner": rc.owner, "name": rc.row.Name, "repo_id": rc.row.ID,
				"purge_after": purgeAfter.UnixMilli(),
			})
			if _, rerr := tx.Outbox().Emit(store.Event{
				Kind:       "repo.deleted",
				Body:       tombstone,
				OccurredAt: now,
			}); rerr != nil {
				return rerr
			}
			payload, _ := marshalJSON(map[string]any{
				"repo_id":    rc.row.ID,
				"path":       rc.row.RukPath,
				"not_before": purgeAfter.UnixMilli(),
			})
			_, jerr := tx.EnqueueJob(store.JobSpec{
				ID:      fmt.Sprintf("repo-purge:%d", rc.row.ID),
				Kind:    "repo_purge",
				RepoID:  rc.row.ID,
				Payload: string(payload),
				At:      now,
			})
			return jerr
		})
		if werr != nil {
			if _, ok := errors.AsType[*op.Error](werr); ok {
				return RepoDeleteOut{}, werr
			}
			return RepoDeleteOut{}, op.Wrap(op.CodeInternal, werr, "repository delete failed")
		}
		if d.OnRepoDeleted != nil {
			d.OnRepoDeleted(rc.row.ID)
		}
		return RepoDeleteOut{State: "deleted", PurgeAfter: purgeAfter.UnixMilli()}, nil
	}
}

func restoreRepoHandler(d Deps) func(context.Context, op.Principal, RepoRestoreIn) (RepoRestoreOut, error) {
	return func(ctx context.Context, p op.Principal, in RepoRestoreIn) (RepoRestoreOut, error) {
		st, serr := requireStore(d)
		if serr != nil {
			return RepoRestoreOut{}, serr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return RepoRestoreOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, true)
		if cerr != nil {
			return RepoRestoreOut{}, cerr
		}
		if !rc.row.DeletedAt.Valid {
			return RepoRestoreOut{}, op.Errorf(op.CodeConflict, "repository is not deleted")
		}
		if !canSeeDeleted(rq, rc.row) {
			return RepoRestoreOut{}, op.Errorf(op.CodeNotFound, "repository was not found")
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.delete", ""); derr != nil {
			return RepoRestoreOut{}, derr
		}
		now := d.now()
		var updated store.RepoRow
		werr := st.Write(ctx, func(tx *store.WriteTx) error {
			current, gerr := tx.GetRepoForUpdate(rc.row.ID, true)
			if gerr != nil {
				if errors.Is(gerr, store.ErrNotFound) {
					return op.Errorf(op.CodeNotFound, "repository was not found")
				}
				return gerr
			}
			if !current.DeletedAt.Valid {
				return op.Errorf(op.CodeConflict, "repository is not deleted")
			}
			if merr := matchRepoETag(in.IfMatch, rc.owner, current); merr != nil {
				return merr
			}
			if rerr := tx.RestoreRepo(rc.row.ID, now); rerr != nil {
				return rerr
			}
			var ferr error
			updated, ferr = tx.GetRepoForUpdate(rc.row.ID, false)
			return ferr
		})
		if werr != nil {
			if _, ok := errors.AsType[*op.Error](werr); ok {
				return RepoRestoreOut{}, werr
			}
			return RepoRestoreOut{}, op.Wrap(op.CodeInternal, werr, "repository restore failed")
		}
		if d.OnRepoCreated != nil {
			d.OnRepoCreated(updated.ID)
		}
		return RepoRestoreOut{Repo: repoJSON(rc.owner, updated)}, nil
	}
}

func listCollaboratorsHandler(d Deps) func(context.Context, op.Principal, CollaboratorListIn) (CollaboratorListOut, error) {
	return func(ctx context.Context, p op.Principal, in CollaboratorListIn) (CollaboratorListOut, error) {
		st, serr := requireStore(d)
		if serr != nil {
			return CollaboratorListOut{}, serr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return CollaboratorListOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return CollaboratorListOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
			return CollaboratorListOut{}, derr
		}
		if rc.row.OrgID != 0 {
			teams, terr := d.teamGrantsForRepo(ctx, rc.row)
			if terr != nil {
				return CollaboratorListOut{}, terr
			}
			return CollaboratorListOut{Collaborators: []CollaboratorItem{}, Teams: teams}, nil
		}
		limit := clampLimit(in.Limit, 30, 100)
		var grants []store.GrantRow
		rderr := st.Read(ctx, func(tx *store.ReadTx) error {
			var lerr error
			grants, lerr = tx.ListGrants(rc.row.ID)
			return lerr
		})
		if rderr != nil {
			return CollaboratorListOut{}, op.Wrap(op.CodeInternal, rderr, "collaborator listing failed")
		}
		out := CollaboratorListOut{Collaborators: []CollaboratorItem{}}
		started := in.Cursor == ""
		for _, grant := range grants {
			if !started {
				if grant.Username == in.Cursor {
					started = true
				}
				continue
			}
			if len(out.Collaborators) >= limit {
				out.Next = grant.Username
				break
			}
			out.Collaborators = append(out.Collaborators, CollaboratorItem{
				User: grant.Username, Permission: grant.Role, Via: "direct",
			})
		}
		return out, nil
	}
}

func addCollaboratorHandler(d Deps) func(context.Context, op.Principal, CollaboratorAddIn) (CollaboratorAddOut, error) {
	return func(ctx context.Context, p op.Principal, in CollaboratorAddIn) (CollaboratorAddOut, error) {
		st, serr := requireStore(d)
		if serr != nil {
			return CollaboratorAddOut{}, serr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return CollaboratorAddOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return CollaboratorAddOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.grant", ""); derr != nil {
			return CollaboratorAddOut{}, derr
		}
		role := strings.ToLower(strings.TrimSpace(in.Body.Permission))
		switch role {
		case "read", "triage", "write", "maintain", "admin":
		default:
			return CollaboratorAddOut{}, op.Errorf(op.CodeValidation, "permission must be read, triage, write, maintain, or admin")
		}
		targetName := strings.ToLower(strings.TrimSpace(in.User))
		if uerr := checkOwnerName(targetName); uerr != nil {
			return CollaboratorAddOut{}, uerr
		}
		target, uerr := d.Store.LookupUserByName(ctx, targetName)
		if uerr != nil {
			return CollaboratorAddOut{}, op.Errorf(op.CodeNotFound, "user was not found")
		}
		if rc.row.OwnerUserID.Valid && rc.row.OwnerUserID.Int64 == target.ID {
			return CollaboratorAddOut{}, op.Errorf(op.CodeConflict, "the owner already holds every permission")
		}
		if rc.row.OrgID != 0 {
			return CollaboratorAddOut{}, op.Errorf(op.CodeConflict, "organization repositories grant through teams")
		}
		werr := st.Write(ctx, func(tx *store.WriteTx) error {
			return tx.PutGrant(rc.row.ID, target.ID, role)
		})
		if werr != nil {
			return CollaboratorAddOut{}, op.Wrap(op.CodeInternal, werr, "collaborator grant failed")
		}
		return CollaboratorAddOut{}, nil
	}
}

func removeCollaboratorHandler(d Deps) func(context.Context, op.Principal, CollaboratorRemoveIn) (CollaboratorRemoveOut, error) {
	return func(ctx context.Context, p op.Principal, in CollaboratorRemoveIn) (CollaboratorRemoveOut, error) {
		st, serr := requireStore(d)
		if serr != nil {
			return CollaboratorRemoveOut{}, serr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return CollaboratorRemoveOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return CollaboratorRemoveOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.grant", ""); derr != nil {
			return CollaboratorRemoveOut{}, derr
		}
		targetName := strings.ToLower(strings.TrimSpace(in.User))
		target, uerr := d.Store.LookupUserByName(ctx, targetName)
		if uerr != nil {
			return CollaboratorRemoveOut{}, op.Errorf(op.CodeNotFound, "user was not found")
		}
		werr := st.Write(ctx, func(tx *store.WriteTx) error {
			return tx.DeleteGrant(rc.row.ID, target.ID)
		})
		if werr != nil {
			if errors.Is(werr, store.ErrNotFound) {
				return CollaboratorRemoveOut{}, op.Errorf(op.CodeNotFound, "collaborator was not found")
			}
			return CollaboratorRemoveOut{}, op.Wrap(op.CodeInternal, werr, "collaborator removal failed")
		}
		return CollaboratorRemoveOut{}, nil
	}
}

type RepoNewPageOut struct{}

type RepoSettingsPageOut struct {
	ETag string   `json:"etag"`
	Repo RepoJSON `json:"repo"`
}

func RegisterRepos(t *op.Table, deps Deps) error {
	if err := op.Register(t, op.Op[op.NoBody, RepoNewPageOut]{
		Name: "repo.create.page", Kind: op.KindRead, Method: http.MethodGet, Path: "/new/repo",
		Scope: op.ScopePublicRead, Summary: "Show the new repository form",
		Description: "Page-only. Renders the zero-JS creation form.",
		ETag:        func(RepoNewPageOut) string { return "\"repo-new\"" },
		Hidden:      true,
		Handler: func(context.Context, op.Principal, op.NoBody) (RepoNewPageOut, error) {
			return RepoNewPageOut{}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[RepoGetIn, RepoSettingsPageOut]{
		Name: "repo.settings.page", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/settings",
		Scope: op.ScopePublicRead, Summary: "Show repository settings",
		Description: "Page-only. Renders the zero-JS settings forms with a current entity tag.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeValidation, op.CodeForbidden},
		ETag:        func(out RepoSettingsPageOut) string { return out.ETag },
		Hidden:      true,
		Handler:     settingsPageHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[RepoCreateIn, RepoCreateOut]{
		Name: "repo.create", Kind: op.KindMutation, Method: http.MethodPost, Path: "/new/repo",
		Scope: op.ScopeRepoCreate, Summary: "Create a repository",
		Description: "Ruk first: repo.init, then the repo row, the default bookmark rule, and the " +
			"repo.created outbox row. Tokens need repo:create and stay in the owner's namespace.",
		Problems: []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeInvalid, op.CodeConflict, op.CodeUnavailable},
		Location: func(out RepoCreateOut) string { return "/" + out.Repo.Owner + "/" + out.Repo.Name },
		ETag:     func(out RepoCreateOut) string { return repoOutETag(out.Repo) },
		Handler:  createRepoHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[RepoGetIn, RepoGetOut]{
		Name: "repo.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}",
		Scope: op.ScopePublicRead, Summary: "Show a repository",
		Description: "Repo home. Renders the default-branch tree with the README. Deleted repos read 404 " +
			"except for their admins.",
		Problems: []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeValidation, op.CodeForbidden},
		ETag: func(out RepoGetOut) string {
			if out.Commit == "" {
				return repoOutETag(out.Repo)
			}
			return fmt.Sprintf("\"repo-%s-%s-%d-%s\"", out.Repo.Owner, out.Repo.Name, out.Repo.UpdatedAt, out.Commit)
		},
		Handler: getRepoHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[RepoListIn, RepoListOut]{
		Name: "repo.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/repos",
		Scope: op.ScopePublicRead, Summary: "List repositories",
		Description: "Keyset pages over updated, created, or name. Private rows stay invisible.",
		Problems:    []op.Code{op.CodeValidation, op.CodeInvalid},
		ETag: func(out RepoListOut) string {
			if len(out.Repos) == 0 {
				return "\"repos-empty\""
			}
			first, last := out.Repos[0], out.Repos[len(out.Repos)-1]
			return fmt.Sprintf("\"repos-%d-%s-%s-%s\"", len(out.Repos), first.Name, last.Name, out.Next)
		},
		Pagination: &op.Pagination[RepoListOut]{Default: 30, Max: 100, Next: func(out RepoListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: listReposHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[RepoUpdateIn, RepoUpdateOut]{
		Name: "repo.update", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/settings/general",
		Scope: op.ScopeRepoWrite, Summary: "Update repository settings",
		Description: "Role maintain. Requires a current If-Match entity tag compared in the store transaction.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeInvalid, op.CodeNotFound, op.CodeStale, op.CodePreconditionReq},
		Concurrency: op.ConcurrencyIfMatchRequired,
		Status:      http.StatusOK,
		Location:    func(RepoUpdateOut) string { return "" },
		ETag:        func(out RepoUpdateOut) string { return repoOutETag(out.Repo) },
		Handler:     updateRepoHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[RepoVisibilityIn, RepoVisibilityOut]{
		Name: "repo.visibility.set", Kind: op.KindMutation, Method: http.MethodPost,
		Path:  "/{owner}/{repo}/settings/visibility",
		Scope: op.ScopeRepoWrite, Summary: "Change repository visibility",
		Description: "Widening needs admin. Confirmation repeats the full name. " +
			"Requires a current If-Match entity tag.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeInvalid, op.CodeNotFound, op.CodeStale, op.CodePreconditionReq},
		Concurrency: op.ConcurrencyIfMatchRequired,
		Status:      http.StatusOK,
		ETag:        func(out RepoVisibilityOut) string { return repoOutETag(out.Repo) },
		Location:    func(RepoVisibilityOut) string { return "" },
		Handler:     visibilityRepoHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[RepoRenameIn, RepoRenameOut]{
		Name: "repo.rename", Kind: op.KindMutation, Method: http.MethodPost,
		Path:  "/{owner}/{repo}/settings/rename",
		Scope: op.ScopeRepoWrite, Summary: "Rename or transfer a repository",
		Description: "Ruk repo.rename first, then the row and a redirect from the old path. " +
			"Renames stay in the caller's namespace. Requires a current If-Match entity tag.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeInvalid, op.CodeNotFound, op.CodeConflict, op.CodeStale, op.CodePreconditionReq},
		Concurrency: op.ConcurrencyIfMatchRequired,
		Status:      http.StatusOK,
		Location:    func(out RepoRenameOut) string { return "/" + out.Repo.Owner + "/" + out.Repo.Name },
		ETag:        func(out RepoRenameOut) string { return repoOutETag(out.Repo) },
		Handler:     renameRepoHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[RepoDeleteIn, RepoDeleteOut]{
		Name: "repo.delete", Kind: op.KindMutation, Method: http.MethodPost,
		Path:  "/{owner}/{repo}/settings/delete",
		Scope: op.ScopeAdminWrite, Summary: "Soft-delete a repository",
		Description: "Role admin. Soft-deletes with a purge horizon, emits repo.deleted, and enqueues " +
			"the repo_purge job. Requires a current If-Match entity tag.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeInvalid, op.CodeNotFound, op.CodeStale, op.CodePreconditionReq},
		Concurrency: op.ConcurrencyIfMatchRequired,
		Status:      http.StatusAccepted,
		Location:    func(RepoDeleteOut) string { return "/repos" },
		ETag:        func(out RepoDeleteOut) string { return fmt.Sprintf("\"repo-deleted-%d\"", out.PurgeAfter) },
		Handler:     deleteRepoHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[RepoRestoreIn, RepoRestoreOut]{
		Name: "repo.restore", Kind: op.KindMutation, Method: http.MethodPost,
		Path:  "/{owner}/{repo}/settings/restore",
		Scope: op.ScopeAdminWrite, Summary: "Restore a soft-deleted repository",
		Description: "Role admin. Clears the tombstone before the purge horizon. " +
			"Requires a current If-Match entity tag.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeInvalid, op.CodeConflict, op.CodeNotFound, op.CodeStale, op.CodePreconditionReq},
		Concurrency: op.ConcurrencyIfMatchRequired,
		Status:      http.StatusOK,
		ETag:        func(out RepoRestoreOut) string { return repoOutETag(out.Repo) },
		Location:    func(RepoRestoreOut) string { return "" },
		Handler:     restoreRepoHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[CollaboratorListIn, CollaboratorListOut]{
		Name: "repo.collaborator.list", Kind: op.KindRead, Method: http.MethodGet,
		Path:  "/{owner}/{repo}/settings/collaborators",
		Scope: op.ScopePublicRead, Summary: "List repository collaborators",
		Description: "Direct grants. Team grants arrive with organizations.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeValidation, op.CodeForbidden},
		ETag: func(out CollaboratorListOut) string {
			return fmt.Sprintf("\"collabs-%d-%s\"", len(out.Collaborators), out.Next)
		},
		Pagination: &op.Pagination[CollaboratorListOut]{Default: 30, Max: 100, Next: func(out CollaboratorListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: listCollaboratorsHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[CollaboratorAddIn, CollaboratorAddOut]{
		Name: "repo.collaborator.add", Kind: op.KindMutation, Method: http.MethodPost,
		Path:  "/{owner}/{repo}/settings/collaborators/{user}",
		Scope: op.ScopeAdminWrite, Summary: "Grant a collaborator role",
		Description: "Role admin. Idempotent over the grant row.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeInvalid, op.CodeNotFound, op.CodeConflict},
		Status:      http.StatusNoContent,
		Location:    func(CollaboratorAddOut) string { return "" },
		Handler:     addCollaboratorHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[CollaboratorRemoveIn, CollaboratorRemoveOut]{
		Name: "repo.collaborator.remove", Kind: op.KindMutation, Method: http.MethodPost,
		Path:  "/{owner}/{repo}/settings/collaborators/{user}/remove",
		Scope: op.ScopeAdminWrite, Summary: "Remove a collaborator grant",
		Description: "Role admin.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeInvalid, op.CodeNotFound},
		Status:      http.StatusNoContent,
		Location:    func(CollaboratorRemoveOut) string { return "" },
		Handler:     removeCollaboratorHandler(deps),
	}); err != nil {
		return err
	}
	return nil
}

func settingsPageHandler(d Deps) func(context.Context, op.Principal, RepoGetIn) (RepoSettingsPageOut, error) {
	return func(ctx context.Context, p op.Principal, in RepoGetIn) (RepoSettingsPageOut, error) {
		st, serr := requireStore(d)
		if serr != nil {
			return RepoSettingsPageOut{}, serr
		}
		_ = st
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return RepoSettingsPageOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, true)
		if cerr != nil {
			return RepoSettingsPageOut{}, cerr
		}
		if rc.row.DeletedAt.Valid {
			if !canSeeDeleted(rq, rc.row) {
				return RepoSettingsPageOut{}, op.Errorf(op.CodeNotFound, "repository was not found")
			}
			return RepoSettingsPageOut{Repo: repoJSON(rc.owner, rc.row), ETag: repoETag(rc.row)}, nil
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
			return RepoSettingsPageOut{}, derr
		}
		return RepoSettingsPageOut{Repo: repoJSON(rc.owner, rc.row), ETag: repoETag(rc.row)}, nil
	}
}

func repoOutETag(repo RepoJSON) string {
	return fmt.Sprintf("\"repo-%s-%s-%d\"", repo.Owner, repo.Name, repo.UpdatedAt)
}

func RegisterReposViews(v *op.Views) {
	op.PageView(v, "repo.create.page", writeRepoNewPage)
	v.Form("repo.create", func(w io.Writer, c op.ViewCtx) error {
		return writeRepoNewPage(w, c, RepoNewPageOut{})
	})
	op.PageView(v, "repo.get", writeRepoHome)
	op.PageView(v, "repo.list", writeRepoList)
	op.PageView(v, "repo.settings.page", writeRepoSettings)
	v.Form("repo.update", func(w io.Writer, c op.ViewCtx) error {
		return writeRepoSettings(w, c, RepoSettingsPageOut{})
	})
	v.Form("repo.visibility.set", func(w io.Writer, c op.ViewCtx) error {
		return writeRepoSettings(w, c, RepoSettingsPageOut{})
	})
	v.Form("repo.rename", func(w io.Writer, c op.ViewCtx) error {
		return writeRepoSettings(w, c, RepoSettingsPageOut{})
	})
	v.Form("repo.delete", func(w io.Writer, c op.ViewCtx) error {
		return writeRepoSettings(w, c, RepoSettingsPageOut{})
	})
	v.Form("repo.restore", func(w io.Writer, c op.ViewCtx) error {
		return writeRepoSettings(w, c, RepoSettingsPageOut{})
	})
	op.PageView(v, "repo.collaborator.list", writeCollaborators)
	v.Form("repo.collaborator.add", func(w io.Writer, c op.ViewCtx) error {
		return writeCollaborators(w, c, CollaboratorListOut{})
	})
	v.Form("repo.collaborator.remove", func(w io.Writer, c op.ViewCtx) error {
		return writeCollaborators(w, c, CollaboratorListOut{})
	})
}
