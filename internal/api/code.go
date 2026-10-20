package api

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
)

type RevResolveIn struct {
	Owner string `path:"owner"`
	Repo  string `path:"repo"`
	Rev   string `path:"rev"`
}

type RevResolveOut struct {
	Rev       string  `json:"rev"`
	Kind      string  `json:"kind"`
	Ref       *string `json:"ref,omitempty"`
	Commit    string  `json:"commit"`
	TagObject *string `json:"tag_object,omitempty"`
}

type TreeGetIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	Rev    string `path:"rev"`
	Path   string `path:"path"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

type treeQuery struct {
	owner  string
	repo   string
	rev    string
	path   string
	cursor string
	limit  int
}

type TreeEntryJSON struct {
	Name string `json:"name"`
	Oid  string `json:"oid"`
	Kind string `json:"kind"`
}

type TreeGetOut struct {
	Commit string          `json:"commit"`
	Path   string          `json:"path"`
	Next   string          `json:"-"`
	Items  []TreeEntryJSON `json:"entries"`
}

type BlobGetIn struct {
	Owner string `path:"owner"`
	Repo  string `path:"repo"`
	Rev   string `path:"rev"`
	Path  string `path:"path"`
	Lines string `query:"lines"`
}

type BlobGetOut struct {
	Commit     string  `json:"commit"`
	Path       string  `json:"path"`
	Lines      string  `json:"lines,omitempty"`
	Oid        string  `json:"oid"`
	Size       uint64  `json:"size"`
	Content    string  `json:"content,omitempty"`
	Truncated  bool    `json:"truncated"`
	Binary     bool    `json:"binary"`
	Target     *string `json:"target,omitempty"`
	Gitlink    *string `json:"gitlink,omitempty"`
	Kind       string  `json:"kind"`
	Executable bool    `json:"executable"`
}

type RawGetIn struct {
	Owner    string `path:"owner"`
	Repo     string `path:"repo"`
	Rev      string `path:"rev"`
	Path     string `path:"path"`
	Download string `query:"download"`
}

type RawGetOut struct {
	Oid         string `json:"oid"`
	Size        uint64 `json:"size"`
	ContentType string `json:"content_type"`
	Content     string `json:"content,omitempty"`
	Truncated   bool   `json:"truncated"`
	DownloadURL string `json:"download_url,omitempty"`
}

type BlameGetIn struct {
	Owner     string `path:"owner"`
	Repo      string `path:"repo"`
	Rev       string `path:"rev"`
	Path      string `path:"path"`
	StartLine int    `query:"start_line"`
	EndLine   int    `query:"end_line"`
	Cursor    string `query:"cursor"`
	Limit     int    `query:"limit"`
}

type BlameHunkJSON struct {
	Head         string  `json:"head"`
	Author       string  `json:"author"`
	Email        string  `json:"email"`
	OriginalPath string  `json:"original_path"`
	Change       *string `json:"change,omitempty"`
	StartLine    uint32  `json:"start_line"`
	LineCount    uint32  `json:"line_count"`
}

type BlameGetOut struct {
	Commit string          `json:"commit"`
	Path   string          `json:"path"`
	Next   string          `json:"-"`
	Hunks  []BlameHunkJSON `json:"hunks"`
}

type LogListIn struct {
	Owner       string `path:"owner"`
	Repo        string `path:"repo"`
	Rev         string `path:"rev"`
	Path        string `query:"path"`
	FirstParent bool   `query:"first_parent"`
	Cursor      string `query:"cursor"`
	Limit       int    `query:"limit"`
}

type logQuery struct {
	owner       string
	repo        string
	rev         string
	path        string
	cursor      string
	limit       int
	firstParent bool
}

type CommitSummaryJSON struct {
	Head       string   `json:"head"`
	Parents    []string `json:"parents"`
	Author     string   `json:"author"`
	Email      string   `json:"email"`
	Subject    string   `json:"subject"`
	Change     *string  `json:"change,omitempty"`
	Conflicted bool     `json:"conflicted"`
	Divergent  bool     `json:"divergent"`
}

type LogListOut struct {
	Next    string              `json:"-"`
	Commits []CommitSummaryJSON `json:"commits"`
}

type CommitGetIn struct {
	Owner string `path:"owner"`
	Repo  string `path:"repo"`
	Oid   string `path:"oid"`
}

type CommitAttestationJSON struct {
	Kind     string `json:"kind"`
	Result   string `json:"result"`
	Signer   string `json:"signer"`
	SignedAt string `json:"signed_at"`
	Status   string `json:"status"`
}

type CommitGetOut struct {
	Oid         string                  `json:"oid"`
	Subject     string                  `json:"subject"`
	Body        string                  `json:"body"`
	Author      string                  `json:"author"`
	AuthorEmail string                  `json:"author_email"`
	Committer   string                  `json:"committer"`
	Parents     []string                `json:"parents"`
	Change      *string                 `json:"change,omitempty"`
	Attested    []CommitAttestationJSON `json:"attestations,omitempty"`
}

type DiffGetIn struct {
	Owner   string   `path:"owner"`
	Repo    string   `path:"repo"`
	Range   string   `path:"range"`
	Path    []string `query:"path"`
	Context int      `query:"context"`
	Renames string   `query:"renames"`
	Cursor  string   `query:"cursor"`
	Limit   int      `query:"limit"`
}

type DiffFileJSON struct {
	Path       string  `json:"path"`
	OldPath    *string `json:"old_path,omitempty"`
	Kind       string  `json:"kind"`
	Binary     bool    `json:"binary"`
	Patch      string  `json:"patch,omitempty"`
	Conflicted bool    `json:"conflicted"`
}

type DiffGetOut struct {
	Left  string         `json:"left"`
	Right string         `json:"right"`
	Next  string         `json:"-"`
	Files []DiffFileJSON `json:"files"`
}

const maxInlineBytes = 1 << 20

func revResolveHandler(d Deps) func(context.Context, op.Principal, RevResolveIn) (RevResolveOut, error) {
	return func(ctx context.Context, p op.Principal, in RevResolveIn) (RevResolveOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return RevResolveOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return RevResolveOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
			return RevResolveOut{}, derr
		}
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return RevResolveOut{}, oerr
		}
		rc.daemon = daemon
		resolved, rerr := d.resolveRev(ctx, rc, in.Rev)
		if rerr != nil {
			return RevResolveOut{}, rerr
		}
		out := RevResolveOut{Rev: in.Rev, Kind: resolved.kind, Commit: resolved.commit}
		if resolved.ref != "" {
			ref := resolved.ref
			out.Ref = &ref
		}
		if resolved.tagObject != "" {
			tagObject := resolved.tagObject
			out.TagObject = &tagObject
		}
		return out, nil
	}
}

func treeGetHandler(d Deps) func(context.Context, op.Principal, TreeGetIn) (TreeGetOut, error) {
	return func(ctx context.Context, p op.Principal, in TreeGetIn) (TreeGetOut, error) {
		return d.treePage(ctx, p, treeQuery{
			owner: in.Owner, repo: in.Repo, rev: in.Rev,
			path: in.Path, cursor: in.Cursor, limit: in.Limit,
		})
	}
}

func (d Deps) treePage(ctx context.Context, p op.Principal, q treeQuery) (TreeGetOut, error) {
	rq, rerr := d.requester(ctx, p)
	if rerr != nil {
		return TreeGetOut{}, rerr
	}
	rc, cerr := d.resolveRepo(ctx, rq, q.owner, q.repo, false)
	if cerr != nil {
		return TreeGetOut{}, cerr
	}
	if derr := d.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
		return TreeGetOut{}, derr
	}
	limit := clampLimit(q.limit, 100, 1000)
	if q.path != "" {
		if perr := checkTreePath(q.path); perr != nil {
			return TreeGetOut{}, perr
		}
	}
	daemon, oerr := d.openDaemonRepo(ctx, rc.row)
	if oerr != nil {
		return TreeGetOut{}, oerr
	}
	rc.daemon = daemon
	resolved, rerr := d.resolveRev(ctx, rc, q.rev)
	if rerr != nil {
		return TreeGetOut{}, rerr
	}
	args := map[string]any{"rev": map[string]any{"commit": resolved.commit}}
	if q.path != "" {
		args["path"] = q.path
	}
	if q.cursor != "" {
		args["after"] = q.cursor
	}
	if limit > 0 {
		args["limit"] = limit
	}
	var reply treeReply
	if rerr := d.callRuk(ctx, daemon, wire.MethodTree, args, &reply); rerr != nil {
		return TreeGetOut{}, rerr
	}
	out := TreeGetOut{Commit: resolved.commit, Path: q.path, Items: []TreeEntryJSON{}}
	for _, entry := range reply.Entries.Items {
		out.Items = append(out.Items, TreeEntryJSON{
			Name: entry.Name.String(), Oid: entry.Oid, Kind: entry.Kind,
		})
	}
	if reply.Entries.NextAfter != nil {
		out.Next = reply.Entries.NextAfter.String()
	}
	return out, nil
}

type treeReply struct {
	Entries KeysetPage `json:"entries"`
}

func checkTreePath(path string) error {
	if path == "" {
		return nil
	}
	if len(path) > 4096 {
		return op.Errorf(op.CodeValidation, "path is too long")
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || part == ".git" {
			return op.Errorf(op.CodeValidation, "path is not valid")
		}
	}
	return nil
}

func blobGetHandler(d Deps) func(context.Context, op.Principal, BlobGetIn) (BlobGetOut, error) {
	return func(ctx context.Context, p op.Principal, in BlobGetIn) (BlobGetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return BlobGetOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return BlobGetOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
			return BlobGetOut{}, derr
		}
		if in.Path == "" {
			return BlobGetOut{}, op.Errorf(op.CodeValidation, "path must not be empty")
		}
		if perr := checkTreePath(in.Path); perr != nil {
			return BlobGetOut{}, perr
		}
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return BlobGetOut{}, oerr
		}
		rc.daemon = daemon
		resolved, rerr := d.resolveRev(ctx, rc, in.Rev)
		if rerr != nil {
			return BlobGetOut{}, rerr
		}
		body, berr := d.readFileBody(ctx, daemon, resolved.commit, in.Path)
		if berr != nil {
			return BlobGetOut{}, berr
		}
		out := BlobGetOut{Commit: resolved.commit, Path: in.Path}
		if lo, hi, ok := parseLinesRange(in.Lines); ok {
			if lo == hi {
				out.Lines = strconv.Itoa(lo)
			} else {
				out.Lines = strconv.Itoa(lo) + "-" + strconv.Itoa(hi)
			}
		}
		switch {
		case body.Blob != nil:
			content, cerr := byteSourceBytes(body.Blob.Content)
			if cerr != nil {
				return BlobGetOut{}, cerr
			}
			out.Kind = "blob"
			out.Oid = body.Blob.Oid
			out.Size = body.Blob.Size
			out.Executable = body.Blob.Executable
			if isBinaryContent(content) {
				out.Binary = true
			} else if len(content) > maxInlineBytes {
				out.Truncated = true
				out.Content = string(content[:maxInlineBytes])
			} else {
				out.Content = string(content)
			}
		case body.Symlink != nil:
			out.Kind = "symlink"
			target := body.Symlink.Target.String()
			out.Target = &target
		case body.Gitlink != nil:
			out.Kind = "gitlink"
			gitlink := body.Gitlink.Commit
			out.Gitlink = &gitlink
		case body.Directory != nil:
			return BlobGetOut{}, op.Errorf(op.CodeConflict, "path is a directory")
		default:
			return BlobGetOut{}, op.Errorf(op.CodeConflict, "path is conflicted")
		}
		return out, nil
	}
}

func (d Deps) readFileBody(ctx context.Context, daemon ruk.Repo, commit, path string) (FileBody, error) {
	var reply struct {
		Body FileBody `json:"body"`
	}
	rerr := d.callRuk(ctx, daemon, wire.MethodFile, map[string]any{
		"at":   headRevisionRef(commit),
		"path": path,
	}, &reply)
	if rerr != nil {
		return FileBody{}, rerr
	}
	return reply.Body, nil
}

func isBinaryContent(content []byte) bool {
	for _, c := range content {
		if c == 0 {
			return true
		}
	}
	return false
}

func rawGetHandler(d Deps) func(context.Context, op.Principal, RawGetIn) (RawGetOut, error) {
	return func(ctx context.Context, p op.Principal, in RawGetIn) (RawGetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return RawGetOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return RawGetOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
			return RawGetOut{}, derr
		}
		meta, merr := d.rawMetadata(ctx, rc, in.Rev, in.Path)
		if merr != nil {
			return RawGetOut{}, merr
		}
		meta.DownloadURL = "/" + rc.owner + "/" + rc.name + "/raw/" + in.Rev + "/" + in.Path + "?download=1"
		return meta, nil
	}
}

func blameGetHandler(d Deps) func(context.Context, op.Principal, BlameGetIn) (BlameGetOut, error) {
	return func(ctx context.Context, p op.Principal, in BlameGetIn) (BlameGetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return BlameGetOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return BlameGetOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
			return BlameGetOut{}, derr
		}
		if in.Path == "" {
			return BlameGetOut{}, op.Errorf(op.CodeValidation, "path must not be empty")
		}
		if perr := checkTreePath(in.Path); perr != nil {
			return BlameGetOut{}, perr
		}
		if in.StartLine < 0 || in.EndLine < 0 {
			return BlameGetOut{}, op.Errorf(op.CodeValidation, "line bounds must not be negative")
		}
		if in.EndLine != 0 && in.StartLine != 0 && in.EndLine < in.StartLine {
			return BlameGetOut{}, op.Errorf(op.CodeValidation, "end line must not precede start line")
		}
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return BlameGetOut{}, oerr
		}
		rc.daemon = daemon
		resolved, rerr := d.resolveRev(ctx, rc, in.Rev)
		if rerr != nil {
			return BlameGetOut{}, rerr
		}
		limit := clampLimit(in.Limit, 100, 1000)
		args := map[string]any{
			"at":   headRevisionRef(resolved.commit),
			"path": in.Path,
		}
		if in.StartLine != 0 || in.EndLine != 0 {
			start := in.StartLine
			if start == 0 {
				start = 1
			}
			end := in.EndLine
			if end == 0 {
				end = 1 << 30
			}
			args["lines"] = map[string]any{"start": start, "end": end}
		}
		page := map[string]any{}
		if in.Cursor != "" {
			cursor, cerr := parseRukCursor(in.Cursor)
			if cerr != nil {
				return BlameGetOut{}, cerr
			}
			page["cursor"] = cursor
		}
		if limit > 0 {
			page["limit"] = limit
		}
		if len(page) > 0 {
			args["page"] = page
		}
		var reply blameReply
		if rerr := d.callRuk(ctx, daemon, wire.MethodBlame, args, &reply); rerr != nil {
			return BlameGetOut{}, rerr
		}
		out := BlameGetOut{Commit: resolved.commit, Path: in.Path, Hunks: []BlameHunkJSON{}}
		for _, hunk := range reply.Hunks.Items {
			headOid, herr := gitHeadOid(hunk.Head)
			if herr != nil {
				headOid = ""
			}
			out.Hunks = append(out.Hunks, BlameHunkJSON{
				Head:         headOid,
				Author:       hunk.Author.Name.String(),
				Email:        hunk.Author.Email.String(),
				OriginalPath: hunk.OriginalPath.String(),
				Change:       hunk.Change,
				StartLine:    hunk.StartLine,
				LineCount:    hunk.LineCount,
			})
		}
		if reply.Hunks.Next != nil {
			encoded, jerr := marshalJSON(reply.Hunks.Next)
			if jerr != nil {
				return BlameGetOut{}, op.Wrap(op.CodeInternal, jerr, "blame cursor could not be encoded")
			}
			out.Next = string(encoded)
		}
		return out, nil
	}
}

type blameReply struct {
	Hunks PageItems[BlameHunk] `json:"hunks"`
}

func parseRukCursor(raw string) (any, error) {
	var decoded any
	if uerr := unmarshalJSON([]byte(raw), &decoded); uerr != nil {
		return nil, op.Errorf(op.CodeInvalid, "cursor is not valid")
	}
	obj, ok := decoded.(map[string]any)
	if !ok {
		return nil, op.Errorf(op.CodeInvalid, "cursor is not valid")
	}
	return obj, nil
}

func logListHandler(d Deps) func(context.Context, op.Principal, LogListIn) (LogListOut, error) {
	return func(ctx context.Context, p op.Principal, in LogListIn) (LogListOut, error) {
		return d.logPage(ctx, p, logQuery{
			owner: in.Owner, repo: in.Repo, rev: in.Rev, path: in.Path,
			cursor: in.Cursor, limit: in.Limit, firstParent: in.FirstParent,
		})
	}
}

func (d Deps) logPage(ctx context.Context, p op.Principal, q logQuery) (LogListOut, error) {
	rq, rerr := d.requester(ctx, p)
	if rerr != nil {
		return LogListOut{}, rerr
	}
	rc, cerr := d.resolveRepo(ctx, rq, q.owner, q.repo, false)
	if cerr != nil {
		return LogListOut{}, cerr
	}
	if derr := d.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
		return LogListOut{}, derr
	}
	if q.firstParent {
		return LogListOut{}, op.Errorf(op.CodeInvalid, "first-parent filtering is not served")
	}
	if q.path != "" {
		if perr := checkTreePath(q.path); perr != nil {
			return LogListOut{}, perr
		}
	}
	daemon, oerr := d.openDaemonRepo(ctx, rc.row)
	if oerr != nil {
		return LogListOut{}, oerr
	}
	rc.daemon = daemon
	rev := cmp.Or(strings.TrimSpace(q.rev), rc.row.DefaultBranch)
	resolved, rerr := d.resolveRev(ctx, rc, rev)
	if rerr != nil {
		return LogListOut{}, rerr
	}
	revset := "ancestors(" + resolved.commit + ")"
	if q.path != "" {
		revset += " & files(" + quoteRevsetString(q.path) + ")"
	}
	limit, lerr := rukLimit(clampLimit(q.limit, 30, 100))
	if lerr != nil {
		return LogListOut{}, lerr
	}
	args := map[string]any{"revset": revset}
	page := map[string]any{}
	if q.cursor != "" {
		cursor, cerr := parseRukCursor(q.cursor)
		if cerr != nil {
			return LogListOut{}, cerr
		}
		page["cursor"] = cursor
	}
	if limit > 0 {
		page["limit"] = limit
	}
	if len(page) > 0 {
		args["page"] = page
	}
	var reply logReply
	if rerr := d.callRuk(ctx, daemon, wire.MethodLog, args, &reply); rerr != nil {
		return LogListOut{}, rerr
	}
	out := LogListOut{Commits: []CommitSummaryJSON{}}
	for _, entry := range reply.Entries.Items {
		summary, serr := summarizeCommit(entry)
		if serr != nil {
			return LogListOut{}, serr
		}
		out.Commits = append(out.Commits, summary)
	}
	if reply.Entries.Next != nil {
		encoded, jerr := marshalJSON(reply.Entries.Next)
		if jerr != nil {
			return LogListOut{}, op.Wrap(op.CodeInternal, jerr, "log cursor could not be encoded")
		}
		out.Next = string(encoded)
	}
	return out, nil
}

type logReply struct {
	Entries PageItems[CommitSummary] `json:"entries"`
}

func summarizeCommit(entry CommitSummary) (CommitSummaryJSON, error) {
	headOid, herr := gitHeadOid(entry.Head)
	if herr != nil {
		return CommitSummaryJSON{}, herr
	}
	out := CommitSummaryJSON{
		Head:       headOid,
		Author:     entry.Author.Name.String(),
		Email:      entry.Author.Email.String(),
		Subject:    entry.Subject.String(),
		Change:     entry.Change,
		Conflicted: entry.Conflicted,
		Divergent:  entry.Divergent,
		Parents:    []string{},
	}
	for _, parent := range entry.Parents {
		oid, perr := gitHeadOid(parent)
		if perr != nil {
			continue
		}
		out.Parents = append(out.Parents, oid)
	}
	return out, nil
}

func quoteRevsetString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		switch c {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(c)
		case '\n':
			b.WriteString("\\n")
		default:
			b.WriteRune(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func commitGetHandler(d Deps) func(context.Context, op.Principal, CommitGetIn) (CommitGetOut, error) {
	return func(ctx context.Context, p op.Principal, in CommitGetIn) (CommitGetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return CommitGetOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return CommitGetOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
			return CommitGetOut{}, derr
		}
		if !isFullOid(strings.TrimSpace(in.Oid)) {
			return CommitGetOut{}, op.Errorf(op.CodeValidation, "commit must be a full object id")
		}
		oid := strings.ToLower(strings.TrimSpace(in.Oid))
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return CommitGetOut{}, oerr
		}
		rc.daemon = daemon
		body, berr := d.showTarget(ctx, rc, map[string]any{"object": map[string]any{"oid": oid}})
		if berr != nil {
			return CommitGetOut{}, berr
		}
		if body.Commit == nil {
			return CommitGetOut{}, op.Errorf(op.CodeNotFound, "commit was not found")
		}
		headOid, herr := gitHeadOid(body.Commit.Head)
		if herr != nil {
			return CommitGetOut{}, herr
		}
		subject, messageBody, author, email := parseCommitRecord(body.Commit.Record)
		out := CommitGetOut{
			Oid: oid, Subject: subject, Body: messageBody,
			Author: author, AuthorEmail: email,
			Committer: author, Parents: []string{}, Change: body.Commit.Change,
		}
		_ = headOid
		parents, perr := d.commitParents(ctx, rc, oid)
		if perr == nil {
			out.Parents = parents
		}
		if records, aerr := d.commitAttestations(ctx, rc, oid); aerr == nil {
			out.Attested = records
		}
		return out, nil
	}
}

func parseCommitRecord(record ShowRecord) (subject, body, author, email string) {
	raw, ok := record.Encoded["git"]
	if !ok {
		return "", "", "", ""
	}
	var content []byte
	switch typed := raw.(type) {
	case string:
		content = []byte(typed)
	case map[string]any:
		hexRaw, ok := typed["hex"].(string)
		if !ok {
			return "", "", "", ""
		}
		decoded, herr := hexDecode(hexRaw)
		if herr != nil {
			return "", "", "", ""
		}
		content = decoded
	default:
		return "", "", "", ""
	}
	headers, message, _ := strings.Cut(string(content), "\n\n")
	for line := range strings.SplitSeq(headers, "\n") {
		if name, ok := strings.CutPrefix(line, "author "); ok {
			author, email = splitIdentity(name)
		}
	}
	message = strings.TrimSuffix(message, "\n")
	if i := strings.IndexByte(message, '\n'); i >= 0 {
		subject = message[:i]
		body = strings.TrimSpace(message[i+1:])
	} else {
		subject = message
	}
	return subject, body, author, email
}

func splitIdentity(line string) (string, string) {
	name, rest, ok := strings.Cut(line, "<")
	if !ok {
		return strings.TrimSpace(line), ""
	}
	email, _, _ := strings.Cut(rest, ">")
	return strings.TrimSpace(name), strings.TrimSpace(email)
}

func (d Deps) commitParents(ctx context.Context, rc repoContext, oid string) ([]string, error) {
	var reply logReply
	rerr := d.callRuk(ctx, rc.daemon, wire.MethodLog,
		map[string]any{"revset": "ancestors(" + oid + ")", "page": map[string]any{"limit": 1}}, &reply)
	if rerr != nil {
		return nil, rerr
	}
	if len(reply.Entries.Items) == 0 {
		return []string{}, nil
	}
	summary, serr := summarizeCommit(reply.Entries.Items[0])
	if serr != nil {
		return nil, serr
	}
	return summary.Parents, nil
}

func (d Deps) commitAttestations(ctx context.Context, rc repoContext, oid string) ([]CommitAttestationJSON, error) {
	var reply attestListReply
	rerr := d.callRuk(ctx, rc.daemon, wire.Method("attest.list"),
		map[string]any{"subject": oid, "page": map[string]any{"limit": 100}}, &reply)
	if rerr != nil {
		return nil, rerr
	}
	out := []CommitAttestationJSON{}
	for _, record := range reply.Records.Items {
		out = append(out, CommitAttestationJSON{
			Kind: record.Kind, Result: record.Result, Signer: record.Signer,
			SignedAt: record.SignedAt, Status: record.Status,
		})
	}
	return out, nil
}

type attestListReply struct {
	Records PageItems[AttestationRecord] `json:"records"`
}

func diffGetHandler(d Deps) func(context.Context, op.Principal, DiffGetIn) (DiffGetOut, error) {
	return func(ctx context.Context, p op.Principal, in DiffGetIn) (DiffGetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return DiffGetOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return DiffGetOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
			return DiffGetOut{}, derr
		}
		leftRev, rightRev, rerr := splitDiffRange(in.Range)
		if rerr != nil {
			return DiffGetOut{}, rerr
		}
		if in.Context != 0 && in.Context != 3 {
			return DiffGetOut{}, op.Errorf(op.CodeInvalid, "context other than 3 is not served")
		}
		switch in.Renames {
		case "", "true", "1":
		default:
			return DiffGetOut{}, op.Errorf(op.CodeInvalid, "disabling rename detection is not served")
		}
		if len(in.Path) > 32 {
			return DiffGetOut{}, op.Errorf(op.CodeValidation, "at most 32 paths are allowed")
		}
		for _, path := range in.Path {
			if perr := checkTreePath(path); perr != nil {
				return DiffGetOut{}, perr
			}
		}
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return DiffGetOut{}, oerr
		}
		rc.daemon = daemon
		left, lerr := d.resolveRev(ctx, rc, leftRev)
		if lerr != nil {
			return DiffGetOut{}, lerr
		}
		right, rerr := d.resolveRev(ctx, rc, rightRev)
		if rerr != nil {
			return DiffGetOut{}, rerr
		}
		limit := clampLimit(in.Limit, 100, 1000)
		args := map[string]any{
			"left":  headRevisionRef(left.commit),
			"right": headRevisionRef(right.commit),
			"paths": diffPathSelection(in.Path),
		}
		page := map[string]any{}
		if in.Cursor != "" {
			cursor, cerr := parseRukCursor(in.Cursor)
			if cerr != nil {
				return DiffGetOut{}, cerr
			}
			page["cursor"] = cursor
		}
		if limit > 0 {
			page["limit"] = limit
		}
		if len(page) > 0 {
			args["page"] = page
		}
		files, next, derr := d.readDiffPage(ctx, daemon, args)
		if derr != nil {
			return DiffGetOut{}, derr
		}
		return DiffGetOut{Left: left.commit, Right: right.commit, Next: next, Files: files}, nil
	}
}

func splitDiffRange(raw string) (string, string, error) {
	left, right, ok := strings.Cut(raw, "..")
	if !ok {
		return "", "", op.Errorf(op.CodeValidation, "range must join two revisions with ..")
	}
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return "", "", op.Errorf(op.CodeValidation, "range must name two revisions")
	}
	if strings.Contains(left, "..") || strings.Contains(right, "..") {
		return "", "", op.Errorf(op.CodeValidation, "range must name two revisions")
	}
	return left, right, nil
}

func diffPathSelection(paths []string) any {
	if len(paths) == 0 {
		return "all"
	}
	return map[string]any{"paths": paths}
}

func (d Deps) readDiffPage(ctx context.Context, daemon ruk.Repo, args map[string]any) ([]DiffFileJSON, string, error) {
	var reply diffReply
	if rerr := d.callRuk(ctx, daemon, wire.MethodDiff, args, &reply); rerr != nil {
		return nil, "", rerr
	}
	files := []DiffFileJSON{}
	for _, file := range reply.Files.Items {
		entry := DiffFileJSON{
			Path:       file.Path.String(),
			Kind:       file.Kind,
			Conflicted: file.Conflicted,
		}
		if file.OldPath != nil {
			old := file.OldPath.String()
			entry.OldPath = &old
		}
		if unified, ok := file.Body["unified"].(map[string]any); ok {
			if patchRaw, ok := unified["patch"].(map[string]any); ok {
				if patch, perr := byteSourceBytes(patchRaw); perr == nil {
					entry.Patch = string(patch)
				} else {
					entry.Binary = true
				}
			} else {
				entry.Binary = true
			}
		} else {
			entry.Binary = true
		}
		files = append(files, entry)
	}
	next := ""
	if reply.Files.Next != nil {
		encoded, jerr := marshalJSON(reply.Files.Next)
		if jerr != nil {
			return nil, "", op.Wrap(op.CodeInternal, jerr, "diff cursor could not be encoded")
		}
		next = string(encoded)
	}
	return files, next, nil
}

type diffReply struct {
	Files PageItems[FileDiff] `json:"files"`
}

func RegisterCode(t *op.Table, deps Deps) error {
	if err := op.Register(t, op.Op[RevResolveIn, RevResolveOut]{
		Name: "code.rev.resolve", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/revs/{rev}",
		Scope: op.ScopePublicRead, Summary: "Resolve a revision to a commit",
		Description: "Branches and tags resolve through the hosted ref listing; change ids, abbrev " +
			"prefixes, and full oids resolve through the revset. Ambiguous names fail, never guess.",
		Problems: []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out RevResolveOut) string {
			return fmt.Sprintf("\"rev-%s-%s\"", out.Kind, out.Commit)
		},
		Handler: revResolveHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[TreeGetIn, TreeGetOut]{
		Name: "code.tree.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/tree/{rev}/{path...}",
		Scope: op.ScopePublicRead, Summary: "List a subdirectory at a revision",
		Description: "Stateless keyset paging over the hosted tree. Ships without last-commit metadata. " +
			"The root directory reads through code.tree.list.",
		Problems: []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeValidation, op.CodeForbidden},
		ETag: func(out TreeGetOut) string {
			return fmt.Sprintf("\"tree-%s-%s-%x-%d\"", out.Commit, out.Path, etagHash(out.Next), len(out.Items))
		},
		Pagination: &op.Pagination[TreeGetOut]{Default: 100, Max: 1000, Next: func(out TreeGetOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: treeGetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[BlobGetIn, BlobGetOut]{
		Name: "code.blob.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/blob/{rev}/{path...}",
		Scope: op.ScopePublicRead, Summary: "Show a file at a revision",
		Description: "Text inlines to the readout cap; larger files truncate. Binary reports size only. " +
			"Line highlight is ?lines=.",
		Problems: []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden, op.CodeConflict},
		ETag: func(out BlobGetOut) string {
			if out.Oid == "" {
				return fmt.Sprintf("\"blob-%s-%s\"", out.Commit, out.Path)
			}
			return "\"blob-" + out.Oid + "\""
		},
		Handler: blobGetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[RawGetIn, RawGetOut]{
		Name: "code.raw.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/raw/{rev}/{path...}",
		Scope: op.ScopePublicRead, Summary: "Describe the raw bytes of a file",
		Description: "Metadata for the byte response served at this URL. Browsers and Range requests " +
			"receive bytes with a sniffed content type; JSON and Markdown receive this descriptor. " +
			"Full-oid revisions are immutable.",
		Problems: []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out RawGetOut) string {
			return "\"raw-" + out.Oid + "\""
		},
		Handler: rawGetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[BlameGetIn, BlameGetOut]{
		Name: "code.blame.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/blame/{rev}/{path...}",
		Scope: op.ScopePublicRead, Summary: "Blame a file at a revision",
		Description: "Line hunks with introducing commits. Binary files fail.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out BlameGetOut) string {
			return fmt.Sprintf("\"blame-%s-%s-%x-%d\"", out.Commit, out.Path, etagHash(out.Next), len(out.Hunks))
		},
		Pagination: &op.Pagination[BlameGetOut]{Default: 100, Max: 1000, Next: func(out BlameGetOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: blameGetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[LogListIn, LogListOut]{
		Name: "code.log.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/log/{rev...}",
		Scope: op.ScopePublicRead, Summary: "List commits under a revision",
		Description: "Ancestors of the resolved commit, optionally narrowed to one path. " +
			"An empty rev reads the default branch.",
		Problems: []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out LogListOut) string {
			if len(out.Commits) == 0 {
				return "\"log-empty\""
			}
			return fmt.Sprintf("\"log-%s-%x-%d\"", out.Commits[0].Head, etagHash(out.Next), len(out.Commits))
		},
		Pagination: &op.Pagination[LogListOut]{Default: 30, Max: 100, Next: func(out LogListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: logListHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[CommitGetIn, CommitGetOut]{
		Name: "code.commit.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/commit/{oid}",
		Scope: op.ScopePublicRead, Summary: "Show one commit",
		Description: "Message, parents, and live attestations. Immutable: full-oid revisions cache.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeValidation, op.CodeForbidden},
		ETag:        func(out CommitGetOut) string { return "\"commit-" + out.Oid + "\"" },
		Handler:     commitGetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[DiffGetIn, DiffGetOut]{
		Name: "code.diff.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/diff/{range}",
		Scope: op.ScopePublicRead, Summary: "Diff two revisions",
		Description: "Unified patches with rename detection. Ranges join two revisions with ... " +
			"Only the daemon default context and rename settings are served.",
		Problems: []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out DiffGetOut) string {
			return fmt.Sprintf("\"diff-%s-%s-%x-%d\"", out.Left, out.Right, etagHash(out.Next), len(out.Files))
		},
		Pagination: &op.Pagination[DiffGetOut]{Default: 100, Max: 1000, Next: func(out DiffGetOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: diffGetHandler(deps),
	}); err != nil {
		return err
	}
	return nil
}

func headRevisionRef(oid string) map[string]any {
	return map[string]any{"head": map[string]any{"head": map[string]any{"oid": oid, "type": "git"}}}
}

func etagHash(s string) uint64 {
	const (
		offset = 14695981039346656037
		prime  = 1099511628211
	)
	hash := uint64(offset)
	for i := range s {
		hash ^= uint64(s[i])
		hash *= prime
	}
	return hash
}

func RegisterCodeViews(v *op.Views) {
	op.PageView(v, "code.rev.resolve", func(w io.Writer, c op.ViewCtx, out RevResolveOut) error {
		return writeRevResolve(w, c, out)
	})
	op.PageView(v, "code.tree.get", func(w io.Writer, c op.ViewCtx, out TreeGetOut) error {
		return writeTree(w, c, out)
	})
	op.PageView(v, "code.blob.get", func(w io.Writer, c op.ViewCtx, out BlobGetOut) error {
		return writeBlob(w, c, out)
	})
	op.PageView(v, "code.raw.get", func(w io.Writer, c op.ViewCtx, out RawGetOut) error {
		return writeRaw(w, c, out)
	})
	op.PageView(v, "code.blame.get", func(w io.Writer, c op.ViewCtx, out BlameGetOut) error {
		return writeBlame(w, c, out)
	})
	op.PageView(v, "code.log.list", func(w io.Writer, c op.ViewCtx, out LogListOut) error {
		return writeLog(w, c, out)
	})
	op.PageView(v, "code.commit.get", func(w io.Writer, c op.ViewCtx, out CommitGetOut) error {
		return writeCommit(w, c, out)
	})
	op.PageView(v, "code.diff.get", func(w io.Writer, c op.ViewCtx, out DiffGetOut) error {
		return writeDiff(w, c, out)
	})
}
