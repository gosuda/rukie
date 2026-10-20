package api

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	jsonv1 "encoding/json"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

type requester struct {
	tokenRepos map[int64]bool
	tokenHash  string
	user       auth.User
	caller     access.Caller
	userID     int64
	authed     bool
	isToken    bool
}

func (d Deps) requester(ctx context.Context, p op.Principal) (requester, error) {
	var r requester
	if p.Kind == op.PrincipalAnonymous || p.UserID == 0 {
		r.caller = access.Caller{Active: true}
		return r, nil
	}
	r.authed = true
	r.userID = p.UserID
	if p.TokenID < 0 {
		user, uerr := d.Service.GetUserByID(ctx, p.UserID)
		if uerr != nil {
			return requester{}, mapAuthError(uerr)
		}
		r.user = user
		r.caller = access.Caller{
			UserID:  p.UserID,
			Scopes:  p.Grants,
			Active:  user.IsActive,
			Authed:  true,
			IsAdmin: user.IsAdmin,
		}
		return r, nil
	}
	var hash string
	var publicOnly int64
	var tokenUser int64
	derr := d.Service.DB().QueryRowContext(ctx,
		`SELECT token_hash, public_only, user_id FROM scoped_tokens WHERE rowid = ?`, p.TokenID).Scan(
		&hash, &publicOnly, &tokenUser)
	if derr != nil {
		return requester{}, op.Errorf(op.CodeUnauthorized, "token is not valid")
	}
	user, uerr := d.Service.GetUserByID(ctx, p.UserID)
	if uerr != nil {
		return requester{}, mapAuthError(uerr)
	}
	repos, rerr := d.Service.TokenRepos(ctx, hash)
	if rerr != nil {
		return requester{}, op.Wrap(op.CodeInternal, rerr, "token repository selector is unavailable")
	}
	set := make(map[int64]bool, len(repos))
	for _, id := range repos {
		set[id] = true
	}
	r.isToken = true
	r.user = user
	r.tokenHash = hash
	r.tokenRepos = set
	r.caller = access.Caller{
		UserID:     p.UserID,
		Scopes:     p.Grants,
		Active:     user.IsActive,
		Authed:     true,
		PublicOnly: publicOnly == 1,
		IsToken:    true,
		IsAdmin:    user.IsAdmin,
	}
	return r, nil
}

func (r requester) tokenAllows(repoID int64) bool {
	if !r.isToken || len(r.tokenRepos) == 0 {
		return true
	}
	return r.tokenRepos[repoID]
}

func mapRukError(err error) error {
	if err == nil {
		return nil
	}
	rerr, ok := errors.AsType[*ruk.Error](err)
	if !ok {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return op.Wrap(op.CodeUnavailable, err, "ruk call did not complete")
		}
		return op.Wrap(op.CodeInternal, err, "ruk call failed")
	}
	code := string(rerr.Code)
	if mapped, ok := op.FromRuk(code); ok {
		detail := rerr.Message
		if detail == "" {
			detail = "ruk " + code
		}
		return &op.Error{Code: mapped, Detail: detail}
	}
	switch rerr.Code {
	case wire.CodeNotFound:
		return op.Errorf(op.CodeNotFound, "ruk object was not found")
	case wire.CodeInvalid:
		return op.Errorf(op.CodeInvalid, "%s", firstLine(rerr.Message))
	case wire.CodeHosted:
		return op.Errorf(op.CodeUnavailable, "ruk host refused the call")
	default:
		return op.Wrap(op.CodeInternal, err, "ruk call failed")
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if s == "" {
		return "ruk request was invalid"
	}
	return s
}

type ByteString string

func (b *ByteString) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := unmarshalString(data, &s); err != nil {
			return err
		}
		*b = ByteString(s)
		return nil
	}
	var obj struct {
		Hex string `json:"hex"`
	}
	if err := unmarshalObject(data, &obj); err != nil {
		return err
	}
	raw, herr := hex.DecodeString(obj.Hex)
	if herr != nil {
		return herr
	}
	*b = ByteString(raw)
	return nil
}

func (b ByteString) String() string {
	return string(b)
}

func (b ByteString) MarshalJSON() ([]byte, error) {
	if utf8.ValidString(string(b)) {
		return marshalJSON(string(b))
	}
	return marshalJSON(map[string]string{"hex": hex.EncodeToString([]byte(b))})
}

type CommitRev struct {
	Ref    string `json:"ref,omitempty"`
	Commit string `json:"commit,omitempty"`
}

type RevisionRef struct {
	Head *CommitRev `json:"head,omitempty"`
}

type RukPage struct {
	Cursor *string `json:"cursor,omitempty"`
	Limit  *int    `json:"limit,omitempty"`
}

type KeysetPage struct {
	NextAfter *ByteString `json:"next_after"`
	Items     []TreeEntry `json:"items"`
}

type TreeEntry struct {
	Name ByteString `json:"name"`
	Oid  string     `json:"oid"`
	Kind string     `json:"kind"`
}

type RefsKeysetPage struct {
	NextAfter *ByteString    `json:"next_after"`
	Items     []ruk.RefEntry `json:"items"`
}

type PageItems[T any] struct {
	Next  *map[string]any `json:"next"`
	Items []T             `json:"items"`
}

type CommitSummary struct {
	Head       map[string]any   `json:"head"`
	Change     *string          `json:"change,omitempty"`
	Author     RukSignature     `json:"author"`
	Committer  RukSignature     `json:"committer"`
	Subject    ByteString       `json:"subject"`
	Parents    []map[string]any `json:"parents"`
	Conflicted bool             `json:"conflicted"`
	Divergent  bool             `json:"divergent"`
}

type RukSignature struct {
	Name      ByteString `json:"name"`
	Email     ByteString `json:"email"`
	Offset    string     `json:"offset"`
	Timestamp int64      `json:"timestamp"`
}

type BlameHunk struct {
	Head         map[string]any `json:"head"`
	Change       *string        `json:"change,omitempty"`
	OriginalPath ByteString     `json:"original_path"`
	Author       RukSignature   `json:"author"`
	StartLine    uint32         `json:"start_line"`
	LineCount    uint32         `json:"line_count"`
}

type FileDiff struct {
	OldPath    *ByteString     `json:"old_path,omitempty"`
	Body       map[string]any  `json:"body"`
	Old        *map[string]any `json:"old,omitempty"`
	New        *map[string]any `json:"new,omitempty"`
	Path       ByteString      `json:"path"`
	Kind       string          `json:"kind"`
	Conflicted bool            `json:"conflicted"`
}

type QueryMatch struct {
	Change string           `json:"change"`
	Heads  []map[string]any `json:"heads"`
}

type FileBlob struct {
	Content    map[string]any `json:"content"`
	Oid        string         `json:"oid"`
	Size       uint64         `json:"size"`
	Executable bool           `json:"executable"`
}

type FileBody struct {
	Blob    *FileBlob `json:"blob,omitempty"`
	Symlink *struct {
		Target ByteString `json:"target"`
	} `json:"symlink,omitempty"`
	Gitlink *struct {
		Commit string `json:"commit"`
	} `json:"gitlink,omitempty"`
	Directory *struct {
		Entries PageItems[TreeEntry] `json:"entries"`
	} `json:"directory,omitempty"`
}

type ShowCommit struct {
	Head   map[string]any `json:"head"`
	Change *string        `json:"change,omitempty"`
	Record ShowRecord     `json:"record"`
}

type ShowRecord struct {
	Encoded     map[string]any  `json:"encoded"`
	Predecessor *map[string]any `json:"predecessor,omitempty"`
	Kind        string          `json:"kind"`
	ChangeID    string          `json:"change_id"`
}

type ShowBody struct {
	Commit *ShowCommit `json:"commit,omitempty"`
	Object *struct {
		Content map[string]any `json:"content"`
		Oid     string         `json:"oid"`
		Kind    string         `json:"kind"`
		Size    uint64         `json:"size"`
	} `json:"object,omitempty"`
}

type AttestationRecord struct {
	Supersedes    *string `json:"supersedes,omitempty"`
	Subject       string  `json:"subject"`
	Kind          string  `json:"kind"`
	Result        string  `json:"result"`
	Signer        string  `json:"signer"`
	PayloadDigest string  `json:"payload_digest"`
	SignedAt      string  `json:"signed_at"`
	Status        string  `json:"status"`
}

func byteSourceBytes(src map[string]any) ([]byte, error) {
	rawInline, ok := src["inline"]
	if !ok {
		if _, streamed := src["streamed"]; streamed {
			return nil, op.Errorf(op.CodeUnavailable, "ruk streamed this body; bulk streaming is not served")
		}
		return nil, op.Errorf(op.CodeInternal, "ruk body has no readable source")
	}
	holder, ok := rawInline.(map[string]any)
	if !ok {
		return nil, op.Errorf(op.CodeInternal, "ruk inline body is malformed")
	}
	data, ok := holder["data"]
	if !ok {
		return nil, op.Errorf(op.CodeInternal, "ruk inline body has no data")
	}
	switch typed := data.(type) {
	case string:
		return []byte(typed), nil
	case map[string]any:
		hexRaw, ok := typed["hex"].(string)
		if !ok {
			return nil, op.Errorf(op.CodeInternal, "ruk hex body is malformed")
		}
		raw, herr := hex.DecodeString(hexRaw)
		if herr != nil {
			return nil, op.Errorf(op.CodeInvalid, "ruk hex body is not hexadecimal")
		}
		return raw, nil
	default:
		return nil, op.Errorf(op.CodeInternal, "ruk body data is malformed")
	}
}

type repoContext struct {
	resolver *access.SQLResolver
	daemon   ruk.Repo
	owner    string
	name     string
	row      store.RepoRow
	request  requester
}

func (d Deps) resolveRepo(ctx context.Context, r requester, owner, name string, includeDeleted bool) (repoContext, error) {
	var out repoContext
	if err := checkOwnerName(owner); err != nil {
		return out, err
	}
	if err := checkRepoName(name); err != nil {
		return out, err
	}
	user, uerr := d.Service.GetUser(ctx, owner)
	if uerr != nil {
		return out, op.Errorf(op.CodeNotFound, "repository was not found")
	}
	var row store.RepoRow
	serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
		var ferr error
		if includeDeleted {
			row, ferr = tx.FindRepoWithDeleted(user.ID, 0, name)
		} else {
			row, ferr = tx.FindRepo(user.ID, 0, name)
		}
		return ferr
	})
	if serr != nil {
		if errors.Is(serr, store.ErrNotFound) {
			if target, ok := d.followRedirect(ctx, user.ID, name); ok {
				row = target
			} else {
				return out, op.Errorf(op.CodeNotFound, "repository was not found")
			}
		} else {
			return out, op.Wrap(op.CodeInternal, serr, "repository lookup failed")
		}
	}
	out.row = row
	out.owner = user.Name
	out.name = row.Name
	out.request = r
	out.resolver = access.NewSQLResolver(d.Service.DB(), nil)
	return out, nil
}

func (d Deps) openDaemonRepo(ctx context.Context, row store.RepoRow) (ruk.Repo, error) {
	if d.Ruk == nil {
		return ruk.Repo{}, op.Errorf(op.CodeUnavailable, "ruk backend is not configured")
	}
	if row.RukPath == "" {
		return ruk.Repo{}, op.Errorf(op.CodeInternal, "repository has no daemon path")
	}
	abs := row.RukPath
	if d.RepoRoot != "" && !isAbsPath(abs) {
		abs = joinPath(d.RepoRoot, abs)
	}
	repo, oerr := d.Ruk.OpenRepo(ctx, abs)
	if oerr != nil {
		return ruk.Repo{}, mapRukError(oerr)
	}
	return repo, nil
}

func (d Deps) checkRepoAccess(r requester, row store.RepoRow, action string, ref string) error {
	resolver := access.NewSQLResolver(d.Service.DB(), nil)
	resource := access.Resource{Kind: access.KindRepo, RepoID: row.ID, Ref: ref}
	decision := access.Check(resolver, r.caller, action, resource)
	if !decision.Allow {
		if decision.NotFound {
			return op.Errorf(op.CodeNotFound, "repository was not found")
		}
		return op.Errorf(op.CodeForbidden, "repository access was refused")
	}
	if !r.tokenAllows(row.ID) {
		return op.Errorf(op.CodeNotFound, "repository was not found")
	}
	return nil
}

func isAbsPath(p string) bool {
	return len(p) > 0 && p[0] == '/'
}

func joinPath(root, rel string) string {
	return strings.TrimSuffix(root, "/") + "/" + strings.TrimPrefix(rel, "/")
}

type resolvedRev struct {
	commit    string
	tagObject string
	ref       string
	kind      string
}

func isFullOid(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := range s {
		if !isHexByte(s[i]) {
			return false
		}
	}
	return true
}

func isHexByte(c byte) bool {
	switch {
	case c >= '0' && c <= '9':
		return true
	case c >= 'a' && c <= 'f':
		return true
	case c >= 'A' && c <= 'F':
		return true
	default:
		return false
	}
}

func (d Deps) resolveRev(ctx context.Context, rc repoContext, rev string) (resolvedRev, error) {
	trimmed := strings.TrimSpace(rev)
	if trimmed == "" {
		return resolvedRev{}, op.Errorf(op.CodeInvalid, "revision must not be empty")
	}
	if len(trimmed) > 512 {
		return resolvedRev{}, op.Errorf(op.CodeInvalid, "revision is too long")
	}
	if isFullOid(trimmed) {
		lower := strings.ToLower(trimmed)
		kind, commit, tagObject, rerr := d.classifyOid(ctx, rc, lower)
		if rerr != nil {
			return resolvedRev{}, rerr
		}
		return resolvedRev{commit: commit, tagObject: tagObject, kind: kind}, nil
	}
	branchRef := "refs/heads/" + trimmed
	if !strings.HasPrefix(trimmed, "refs/") {
		if target, ok := d.exactRefTarget(ctx, rc, branchRef); ok {
			commit, cerr := d.peelToCommit(ctx, rc, target)
			if cerr != nil {
				return resolvedRev{}, cerr
			}
			return resolvedRev{commit: commit, ref: branchRef, kind: "branch"}, nil
		}
		tagRef := "refs/tags/" + trimmed
		if target, ok := d.exactRefTarget(ctx, rc, tagRef); ok {
			commit, tagObject, cerr := d.peelTag(ctx, rc, target)
			if cerr != nil {
				return resolvedRev{}, cerr
			}
			return resolvedRev{commit: commit, tagObject: tagObject, ref: tagRef, kind: "tag"}, nil
		}
		if isChangeID(trimmed) {
			commit, cerr := d.queryRevCommit(ctx, rc, "change("+trimmed+")")
			if cerr != nil {
				return resolvedRev{}, cerr
			}
			return resolvedRev{commit: commit, kind: "change"}, nil
		}
		commit, qerr := d.queryRevCommit(ctx, rc, trimmed)
		if qerr != nil {
			return resolvedRev{}, qerr
		}
		return resolvedRev{commit: commit, kind: "commit"}, nil
	}
	target, ok := d.exactRefTarget(ctx, rc, trimmed)
	if !ok {
		return resolvedRev{}, op.Errorf(op.CodeNotFound, "revision was not found")
	}
	kind := "branch"
	if strings.HasPrefix(trimmed, "refs/tags/") {
		kind = "tag"
	}
	commit, cerr := d.peelToCommit(ctx, rc, target)
	if cerr != nil {
		return resolvedRev{}, cerr
	}
	return resolvedRev{commit: commit, ref: trimmed, kind: kind}, nil
}

func isChangeID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 'k' || s[i] > 'z' {
			return false
		}
	}
	return true
}

func (d Deps) exactRefTarget(ctx context.Context, rc repoContext, ref string) (string, bool) {
	if d.Ruk == nil {
		return "", false
	}
	reply, rerr := ruk.Call[map[string]any, refsListReply](ctx, d.Ruk, rc.daemon, wire.MethodRefsList,
		map[string]any{"prefix": ref, "limit": 2})
	if rerr != nil {
		return "", false
	}
	for _, entry := range reply.Refs.Items {
		if entry.Name != ref {
			continue
		}
		if entry.Target.Direct != nil {
			return *entry.Target.Direct, true
		}
		return "", false
	}
	return "", false
}

type refsListReply struct {
	Refs RefsKeysetPage `json:"refs"`
}

func (d Deps) classifyOid(ctx context.Context, rc repoContext, oid string) (string, string, string, error) {
	target, serr := d.showTarget(ctx, rc, map[string]any{"object": map[string]any{"oid": oid}})
	if serr != nil {
		return "", "", "", serr
	}
	if target.Commit != nil {
		headOid, herr := gitHeadOid(target.Commit.Head)
		if herr != nil {
			return "", "", "", herr
		}
		return "commit", headOid, "", nil
	}
	if target.Object != nil {
		switch target.Object.Kind {
		case "commit":
			return "commit", target.Object.Oid, "", nil
		case "tag":
			commit, _, cerr := d.peelTagObject(ctx, rc, target.Object.Oid)
			if cerr != nil {
				return "", "", "", cerr
			}
			return "tag", commit, target.Object.Oid, nil
		default:
			return "", "", "", op.Errorf(op.CodeInvalid, "revision is not a commit")
		}
	}
	return "", "", "", op.Errorf(op.CodeNotFound, "revision was not found")
}

func gitHeadOid(head map[string]any) (string, error) {
	typ, _ := head["type"].(string)
	if typ == "git" {
		oid, _ := head["oid"].(string)
		if !isFullOid(oid) {
			return "", op.Errorf(op.CodeInternal, "ruk head oid is malformed")
		}
		return strings.ToLower(oid), nil
	}
	return "", op.Errorf(op.CodeInvalid, "revision names a conflicted head")
}

func (d Deps) peelToCommit(ctx context.Context, rc repoContext, oid string) (string, error) {
	kind, commit, _, cerr := d.classifyOid(ctx, rc, oid)
	if cerr != nil {
		return "", cerr
	}
	if kind != "commit" {
		return "", op.Errorf(op.CodeInvalid, "reference does not point at a commit")
	}
	return commit, nil
}

func (d Deps) peelTag(ctx context.Context, rc repoContext, oid string) (string, string, error) {
	kind, commit, tagObject, cerr := d.classifyOid(ctx, rc, oid)
	if cerr != nil {
		return "", "", cerr
	}
	if kind == "tag" {
		return commit, tagObject, nil
	}
	return commit, "", nil
}

func (d Deps) peelTagObject(ctx context.Context, rc repoContext, tagOid string) (string, string, error) {
	target, serr := d.showTarget(ctx, rc, map[string]any{"object": map[string]any{"oid": tagOid}})
	if serr != nil {
		return "", "", serr
	}
	if target.Object == nil || target.Object.Kind != "tag" {
		return "", "", op.Errorf(op.CodeInvalid, "tag object is not a tag")
	}
	content, cerr := byteSourceBytes(target.Object.Content)
	if cerr != nil {
		return "", "", cerr
	}
	peeled := parseTagObjectTarget(content)
	if peeled == "" {
		return "", "", op.Errorf(op.CodeInternal, "tag object names no target")
	}
	commit, qerr := d.peelToCommit(ctx, rc, peeled)
	if qerr != nil {
		return "", "", qerr
	}
	return commit, tagOid, nil
}

func (d Deps) followRedirect(ctx context.Context, ownerID int64, name string) (store.RepoRow, bool) {
	var redirect store.RedirectRow
	rerr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
		var ferr error
		redirect, ferr = tx.FindRedirect(ownerID, name)
		return ferr
	})
	if rerr != nil {
		return store.RepoRow{}, false
	}
	var target store.RepoRow
	terr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
		var ferr error
		target, ferr = tx.GetRepo(redirect.RepoID)
		return ferr
	})
	if terr != nil || target.DeletedAt.Valid {
		return store.RepoRow{}, false
	}
	return target, true
}

func parseTagObjectTarget(content []byte) string {
	for line := range strings.SplitSeq(string(content), "\n") {
		if line == "" {
			break
		}
		if target, ok := strings.CutPrefix(line, "object "); ok {
			fields := strings.Fields(target)
			if len(fields) > 0 && isFullOid(fields[0]) {
				return strings.ToLower(fields[0])
			}
		}
	}
	return ""
}

func (d Deps) queryRevCommit(ctx context.Context, rc repoContext, revset string) (string, error) {
	if d.Ruk == nil {
		return "", op.Errorf(op.CodeUnavailable, "ruk backend is not configured")
	}
	var reply queryReply
	rerr := d.callRuk(ctx, rc.daemon, wire.MethodQuery, map[string]any{"revset": revset}, &reply)
	if rerr != nil {
		return "", rerr
	}
	if len(reply.Matches.Items) == 0 {
		return "", op.Errorf(op.CodeNotFound, "revision was not found")
	}
	if len(reply.Matches.Items) > 1 {
		return "", op.Errorf(op.CodeInvalid, "revision is ambiguous")
	}
	match := reply.Matches.Items[0]
	if len(match.Heads) != 1 {
		return "", op.Errorf(op.CodeInvalid, "revision is ambiguous")
	}
	return gitHeadOid(match.Heads[0])
}

type queryReply struct {
	Matches PageItems[QueryMatch] `json:"matches"`
}

func (d Deps) showTarget(ctx context.Context, rc repoContext, target map[string]any) (ShowBody, error) {
	var reply struct {
		Body ShowBody `json:"body"`
	}
	rerr := d.callRuk(ctx, rc.daemon, wire.MethodShow, map[string]any{"target": target}, &reply)
	if rerr != nil {
		return ShowBody{}, rerr
	}
	return reply.Body, nil
}

func (d Deps) callRuk(ctx context.Context, repo ruk.Repo, method wire.Method, args map[string]any, out any) error {
	if d.Ruk == nil {
		return op.Errorf(op.CodeUnavailable, "ruk backend is not configured")
	}
	raw, rerr := ruk.Call[map[string]any, jsonv1.RawMessage](ctx, d.Ruk, repo, method, args)
	if rerr != nil {
		return mapRukError(rerr)
	}
	if uerr := unmarshalJSON([]byte(raw), out); uerr != nil {
		return op.Wrap(op.CodeInternal, uerr, "ruk reply was malformed")
	}
	return nil
}

func checkOwnerName(name string) error {
	if err := auth.CheckUsername(name); err != nil {
		return op.Errorf(op.CodeInvalid, "owner name is not valid")
	}
	for _, route := range Routes() {
		trimmed := strings.TrimPrefix(route.Pattern, "GET ")
		trimmed = strings.TrimPrefix(trimmed, "POST ")
		trimmed = strings.TrimPrefix(strings.TrimSpace(trimmed), "/")
		first, _, _ := strings.Cut(trimmed, "/")
		if first != "" && !strings.HasPrefix(first, "{") && first == name {
			return op.Errorf(op.CodeInvalid, "owner name is reserved")
		}
	}
	return nil
}

func checkRepoName(name string) error {
	if len(name) < 1 || len(name) > 100 {
		return op.Errorf(op.CodeValidation, "repository name must be 1 to 100 characters")
	}
	if name == "." || name == ".." || name == "-" {
		return op.Errorf(op.CodeValidation, "repository name is reserved")
	}
	if strings.HasSuffix(name, ".git") {
		return op.Errorf(op.CodeValidation, "repository name must not end in .git")
	}
	if strings.Contains(name, "..") {
		return op.Errorf(op.CodeValidation, "repository name must not contain ..")
	}
	for _, c := range name {
		if !isRepoNameByte(c) {
			return op.Errorf(op.CodeValidation, "repository name uses an invalid character")
		}
	}
	return nil
}

func checkBranchName(name string) error {
	if len(name) < 1 || len(name) > 255 {
		return op.Errorf(op.CodeValidation, "branch name must be 1 to 255 characters")
	}
	if strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") ||
		strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock") {
		return op.Errorf(op.CodeValidation, "branch name is not valid")
	}
	if strings.Contains(name, "..") || strings.Contains(name, "//") ||
		strings.Contains(name, "@{") || strings.Contains(name, "~") ||
		strings.Contains(name, "^") || strings.Contains(name, ":") ||
		strings.Contains(name, "?") || strings.Contains(name, "*") ||
		strings.Contains(name, "[") || strings.Contains(name, "\\") ||
		strings.Contains(name, " ") {
		return op.Errorf(op.CodeValidation, "branch name is not valid")
	}
	for _, c := range name {
		if c < 0x20 || c == 0x7f {
			return op.Errorf(op.CodeValidation, "branch name is not valid")
		}
	}
	return nil
}

func checkTagName(name string) error {
	return checkBranchName(name)
}

func checkVisibility(visibility string) error {
	switch visibility {
	case "public", "internal", "private":
		return nil
	default:
		return op.Errorf(op.CodeValidation, "visibility must be public, internal, or private")
	}
}

func checkTopics(topics []string) (string, error) {
	if len(topics) == 0 {
		return "[]", nil
	}
	if len(topics) > 20 {
		return "", op.Errorf(op.CodeValidation, "at most 20 topics are allowed")
	}
	seen := map[string]bool{}
	for _, topic := range topics {
		if len(topic) < 1 || len(topic) > 64 {
			return "", op.Errorf(op.CodeValidation, "topics must be 1 to 64 characters")
		}
		lower := strings.ToLower(topic)
		if seen[lower] {
			return "", op.Errorf(op.CodeValidation, "topics must be unique")
		}
		seen[lower] = true
	}
	encoded, jerr := marshalJSON(topics)
	if jerr != nil {
		return "", op.Wrap(op.CodeInternal, jerr, "topics could not be encoded")
	}
	return string(encoded), nil
}

func repoPurgeDelay() time.Duration {
	return 30 * 24 * time.Hour
}

func repoETag(row store.RepoRow) string {
	return fmt.Sprintf("\"repo-%d-%d\"", row.ID, row.UpdatedAt)
}

func matchRepoETag(ifMatch, owner string, row store.RepoRow) error {
	if ifMatch == "" {
		return op.Errorf(op.CodePreconditionReq, "If-Match is required")
	}
	named := fmt.Sprintf("\"repo-%s-%s-%d\"", owner, row.Name, row.UpdatedAt)
	if !etagMatches(ifMatch, repoETag(row)) && !etagMatches(ifMatch, named) {
		return op.Errorf(op.CodeStale, "repository changed under this request")
	}
	return nil
}

func etagMatches(header, etag string) bool {
	want := strings.Trim(etag, `"`)
	for part := range strings.SplitSeq(header, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed == "*" {
			return true
		}
		if strings.Trim(trimmed, `"`) == want {
			return true
		}
	}
	return false
}

func isRepoNameByte(c rune) bool {
	switch {
	case c >= 'a' && c <= 'z':
		return true
	case c >= 'A' && c <= 'Z':
		return true
	case c >= '0' && c <= '9':
		return true
	case c == '.' || c == '_' || c == '-':
		return true
	default:
		return false
	}
}

func clampLimit(limit, def, maximum int) int {
	if limit <= 0 {
		return def
	}
	if limit > maximum {
		return maximum
	}
	return limit
}

func rukLimit(limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	if limit > 1000 {
		return 0, op.Errorf(op.CodeLimitExceeded, "limit must not exceed 1000")
	}
	return limit, nil
}
