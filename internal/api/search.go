package api

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	jsonv1 "encoding/json"

	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

const (
	searchDefaultPerPage = 30
	searchMaxPerPage     = 100
	searchMaxGlobs       = 32
	searchMaxGlobBytes   = 1024
	searchMaxQueryBytes  = 4096
	searchMaxRepos       = 50
	searchFanout         = 8
)

// SearchHitJSON is one code-search hit. Repo is always populated as
// "owner/name" so single-repo and multi-repo pages share one shape.
type SearchHitJSON struct {
	Repo    string     `json:"repo"`
	Path    string     `json:"path"`
	Preview ByteString `json:"preview"`
	Blob    string     `json:"blob"`
	Line    uint32     `json:"line"`
	Column  uint32     `json:"column"`
}

// SearchOut is the unified wire shape served by search.code. Type selects
// the result family: code search carries Hits with scan statistics, while
// repos and users searches carry the matching directory rows. Next holds
// the continuation in the body because search pages emit no Link header:
// a verbatim Ruk cursor for single-repo code pages, a composite envelope
// for multi-repo code pages, a keyset for directory pages, absent when the
// page exhausts its scope.
type SearchOut struct {
	Next         *string          `json:"next,omitempty"`
	Type         string           `json:"type"`
	Exhausted    string           `json:"exhausted,omitempty"`
	Hits         []SearchHitJSON  `json:"hits"`
	Repos        []RepoJSON       `json:"repos"`
	Users        []SearchUserJSON `json:"users"`
	ScannedBlobs uint64           `json:"scanned_blobs,omitzero"`
	ScannedBytes uint64           `json:"scanned_bytes,omitzero"`
	SkippedBlobs uint64           `json:"skipped_blobs,omitzero"`
	ReposScanned int              `json:"repos_scanned,omitzero"`
	Truncated    bool             `json:"truncated,omitzero"`
}

// SearchCodeOut aliases the unified shape for the code result family.
type SearchCodeOut = SearchOut

type searchCodeQuery struct {
	q            string
	owner        string
	repo         string
	rev          string
	path         string
	next         string
	globs        []string
	excludeGlobs []string
	perPage      int
	regex        bool
	caseFold     bool
}

type verbatimCursor string

func (v *verbatimCursor) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*v = ""
		return nil
	}
	var probe map[string]any
	if uerr := unmarshalJSON(data, &probe); uerr != nil {
		return uerr
	}
	*v = verbatimCursor(trimmed)
	return nil
}

func (v verbatimCursor) MarshalJSON() ([]byte, error) {
	if v == "" {
		return []byte("null"), nil
	}
	return []byte(v), nil
}

type searchHitReply struct {
	Path    ByteString `json:"path"`
	Preview ByteString `json:"preview"`
	Blob    string     `json:"blob"`
	Line    uint32     `json:"line"`
	Column  uint32     `json:"column"`
}

type searchReplyJSON struct {
	Exhausted string `json:"exhausted"`
	Hits      struct {
		Next  verbatimCursor   `json:"next"`
		Items []searchHitReply `json:"items"`
	} `json:"hits"`
	ScannedBlobs uint64 `json:"scanned_blobs"`
	ScannedBytes uint64 `json:"scanned_bytes"`
	SkippedBlobs uint64 `json:"skipped_blobs"`
	Truncated    bool   `json:"truncated"`
}

func parseSearchCodeQuery(q, owner, repo, rev string, regex, caseFold bool, globs, excludeGlobs, path, next string, perPage int) (searchCodeQuery, error) {
	out := searchCodeQuery{
		q: q, owner: owner, repo: repo, rev: rev,
		regex: regex, caseFold: caseFold,
		path: path, next: next, perPage: clampLimit(perPage, searchDefaultPerPage, searchMaxPerPage),
	}
	if strings.TrimSpace(q) == "" {
		return out, op.Errorf(op.CodeValidation, "q must not be empty")
	}
	if len(q) > searchMaxQueryBytes {
		return out, op.Errorf(op.CodeValidation, "q must not exceed 4 KiB")
	}
	var verr error
	if out.globs, verr = parseSearchGlobs(globs); verr != nil {
		return out, verr
	}
	if out.excludeGlobs, verr = parseSearchGlobs(excludeGlobs); verr != nil {
		return out, verr
	}
	if path != "" {
		if perr := checkTreePath(path); perr != nil {
			return out, perr
		}
	}
	if (owner == "") != (repo == "") {
		return out, op.Errorf(op.CodeValidation, "owner and repo must be given together")
	}
	if owner != "" {
		if oerr := checkOwnerName(strings.ToLower(owner)); oerr != nil {
			return out, oerr
		}
		if rerr := checkRepoName(repo); rerr != nil {
			return out, rerr
		}
	}
	if len(rev) > 512 {
		return out, op.Errorf(op.CodeValidation, "rev is too long")
	}
	return out, nil
}

func parseSearchGlobs(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	var out []string
	for part := range strings.SplitSeq(trimmed, ",") {
		glob := strings.TrimSpace(part)
		if glob == "" {
			continue
		}
		if len(glob) > searchMaxGlobBytes {
			return nil, op.Errorf(op.CodeValidation, "globs must not exceed 1 KiB each")
		}
		out = append(out, glob)
	}
	if len(out) > searchMaxGlobs {
		return out, op.Errorf(op.CodeValidation, "at most 32 globs are allowed")
	}
	return out, nil
}

type singleSearchResult struct {
	next         verbatimCursor
	exhausted    string
	hits         []searchHitReply
	scannedBlobs uint64
	scannedBytes uint64
	skippedBlobs uint64
	truncated    bool
}

func (d Deps) singleRepoSearch(ctx context.Context, daemon ruk.Repo, commit string, q searchCodeQuery, cursor verbatimCursor) (singleSearchResult, error) {
	var out singleSearchResult
	pattern := map[string]any{"literal": q.q}
	if q.regex {
		pattern = map[string]any{"regex": q.q}
	}
	var paths any = "all"
	if q.path != "" {
		paths = map[string]any{"paths": []string{q.path}}
	}
	page := map[string]any{"limit": q.perPage}
	if cursor != "" {
		page["cursor"] = cursor
	}
	args := map[string]any{
		"at":               headRevisionRef(commit),
		"pattern":          pattern,
		"paths":            paths,
		"page":             page,
		"case_insensitive": q.caseFold,
	}
	if len(q.globs) > 0 {
		args["globs"] = q.globs
	}
	if len(q.excludeGlobs) > 0 {
		args["exclude_globs"] = q.excludeGlobs
	}
	raw, rerr := ruk.Call[map[string]any, jsonv1.RawMessage](ctx, d.Ruk, daemon, wire.MethodSearch, args)
	if rerr != nil {
		return out, mapRukError(rerr)
	}
	var reply searchReplyJSON
	if uerr := unmarshalJSON([]byte(raw), &reply); uerr != nil {
		return out, op.Wrap(op.CodeInternal, uerr, "ruk search reply was malformed")
	}
	out.hits = reply.Hits.Items
	out.next = reply.Hits.Next
	out.truncated = reply.Truncated
	out.exhausted = reply.Exhausted
	out.scannedBlobs = reply.ScannedBlobs
	out.scannedBytes = reply.ScannedBytes
	out.skippedBlobs = reply.SkippedBlobs
	return out, nil
}

type compositeCursor struct {
	Cursors   map[string]verbatimCursor `json:"cursors"`
	Print     string                    `json:"fp"`
	AfterName string                    `json:"after_name"`
	Version   int                       `json:"v"`
	AfterID   int64                     `json:"after_id"`
}

func encodeComposite(v compositeCursor) (string, error) {
	raw, merr := marshalJSON(v)
	if merr != nil {
		return "", merr
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeComposite(raw string) (compositeCursor, error) {
	var out compositeCursor
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "{") {
		return out, op.Errorf(op.CodeInvalid, "cursor is not valid")
	}
	decoded, derr := base64.RawURLEncoding.DecodeString(trimmed)
	if derr != nil {
		return out, op.Errorf(op.CodeInvalid, "cursor is not valid")
	}
	if uerr := unmarshalJSON(decoded, &out); uerr != nil {
		return out, op.Errorf(op.CodeInvalid, "cursor is not valid")
	}
	if out.Version != 1 || out.Print == "" {
		return out, op.Errorf(op.CodeInvalid, "cursor is not valid")
	}
	if out.Cursors == nil {
		out.Cursors = map[string]verbatimCursor{}
	}
	return out, nil
}

func searchFingerprint(q searchCodeQuery, repos []searchRepoRef) string {
	var b strings.Builder
	b.WriteString(q.q)
	b.WriteByte(0)
	if q.regex {
		b.WriteByte('1')
	}
	b.WriteByte(0)
	if q.caseFold {
		b.WriteByte('1')
	}
	b.WriteByte(0)
	b.WriteString(strings.Join(q.globs, "\x01"))
	b.WriteByte(0)
	b.WriteString(strings.Join(q.excludeGlobs, "\x01"))
	b.WriteByte(0)
	b.WriteString(q.path)
	b.WriteByte(0)
	b.WriteString(q.rev)
	pairs := make([]string, 0, len(repos))
	for _, ref := range repos {
		pairs = append(pairs, fmt.Sprintf("%d=%s", ref.row.ID, ref.name))
	}
	for _, pair := range slices.Sorted(slices.Values(pairs)) {
		b.WriteByte(0)
		b.WriteString(pair)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

type searchRepoRef struct {
	owner string
	name  string
	row   store.RepoRow
}

func (d Deps) visibleSearchRepos(ctx context.Context, rq requester) ([]searchRepoRef, error) {
	if d.Store == nil {
		return nil, op.Wrap(op.CodeUnavailable, fmt.Errorf("store is not configured"), "repository listing failed")
	}
	var out []searchRepoRef
	cursor := ""
	for {
		var rows []store.RepoRow
		var next string
		rerr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			var lerr error
			rows, next, lerr = tx.ListRepos(store.RepoListFilter{Sort: "name", Limit: 100, Cursor: cursor})
			return lerr
		})
		if rerr != nil {
			return nil, op.Wrap(op.CodeInternal, rerr, "repository listing failed")
		}
		for _, row := range rows {
			owner := repoOwnerName(ctx, d.Store, row)
			if owner == "" {
				continue
			}
			if derr := d.checkRepoAccess(rq, row, "repo.view", ""); derr != nil {
				continue
			}
			out = append(out, searchRepoRef{owner: owner, name: row.Name, row: row})
		}
		if next == "" {
			break
		}
		cursor = next
	}
	slices.SortFunc(out, func(a, b searchRepoRef) int {
		return cmp.Or(strings.Compare(a.name, b.name), cmp.Compare(a.row.ID, b.row.ID))
	})
	return out, nil
}

type multiSlot struct {
	cursor verbatimCursor
	ref    searchRepoRef
	fresh  bool
}

type multiPlan struct {
	carried map[string]verbatimCursor
	prior   map[string]verbatimCursor
	after   string
	base    string
	slots   []multiSlot
	afterID int64
	baseID  int64
}

func planMultiPage(repos []searchRepoRef, resume compositeCursor, fp string) (multiPlan, error) {
	prior := make(map[string]verbatimCursor, len(resume.Cursors))
	for id, cursor := range resume.Cursors {
		prior[id] = cursor
	}
	plan := multiPlan{carried: map[string]verbatimCursor{}, after: resume.AfterName, afterID: resume.AfterID, base: resume.AfterName, baseID: resume.AfterID, prior: prior}
	if resume.Print != "" && resume.Print != fp {
		return plan, op.Errorf(op.CodeInvalid, "cursor is not valid for this query")
	}
	ordered := slices.Clone(repos)
	slices.SortFunc(ordered, func(a, b searchRepoRef) int {
		return cmp.Or(strings.Compare(a.name, b.name), cmp.Compare(a.row.ID, b.row.ID))
	})
	repos = ordered
	visible := map[int64]searchRepoRef{}
	for _, ref := range repos {
		visible[ref.row.ID] = ref
	}
	for idRaw, cursor := range resume.Cursors {
		var id int64
		if _, serr := fmt.Sscanf(idRaw, "%d", &id); serr != nil {
			return plan, op.Errorf(op.CodeInvalid, "cursor is not valid")
		}
		ref, ok := visible[id]
		if !ok {
			return plan, op.Errorf(op.CodeInvalid, "cursor is not valid")
		}
		if len(plan.slots) < searchMaxRepos {
			plan.slots = append(plan.slots, multiSlot{ref: ref, cursor: cursor})
		} else {
			plan.carried[idRaw] = cursor
		}
	}
	for _, ref := range repos {
		if len(plan.slots) >= searchMaxRepos {
			break
		}
		if ref.name < plan.after || (ref.name == plan.after && ref.row.ID <= plan.afterID) {
			continue
		}
		if _, ok := resume.Cursors[fmt.Sprintf("%d", ref.row.ID)]; ok {
			continue
		}
		plan.slots = append(plan.slots, multiSlot{ref: ref, fresh: true})
		plan.after, plan.afterID = ref.name, ref.row.ID
	}
	return plan, nil
}

type multiResult struct {
	err       error
	next      verbatimCursor
	exhausted string
	hits      []searchHitReply
	blobs     uint64
	bytes     uint64
	skipped   uint64
	truncated bool
	dropped   bool
}

func (d Deps) runMultiPlan(ctx context.Context, rq requester, plan []multiSlot, q searchCodeQuery) map[int64]multiResult {
	var mu sync.Mutex
	results := make(map[int64]multiResult, len(plan))
	put := func(id int64, res multiResult) {
		mu.Lock()
		results[id] = res
		mu.Unlock()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	sem := make(chan struct{}, searchFanout)
	for _, slot := range plan {
		wg.Go(func() {
			id := slot.ref.row.ID
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				put(id, multiResult{err: mapRukError(ctx.Err())})
				return
			}
			defer func() { <-sem }()
			daemon, oerr := d.openDaemonRepo(ctx, slot.ref.row)
			if oerr != nil {
				put(id, multiResult{err: oerr})
				return
			}
			rev := q.rev
			if strings.TrimSpace(rev) == "" {
				rev = slot.ref.row.DefaultBranch
			}
			rc, cerr := d.resolveRepo(ctx, rq, slot.ref.owner, slot.ref.name, false)
			if cerr != nil {
				if isSkippableSearchError(cerr) {
					put(id, multiResult{dropped: true})
					return
				}
				put(id, multiResult{err: cerr})
				return
			}
			if aerr := d.checkRepoAccess(rq, rc.row, "repo.view", ""); aerr != nil {
				if isSkippableSearchError(aerr) {
					put(id, multiResult{dropped: true})
					return
				}
				put(id, multiResult{err: aerr})
				return
			}
			rc.daemon = daemon
			resolved, rerr := d.resolveRev(ctx, rc, rev)
			if rerr != nil {
				if isSkippableSearchError(rerr) {
					put(id, multiResult{dropped: true})
					return
				}
				put(id, multiResult{err: rerr})
				return
			}
			got, serr := d.singleRepoSearch(ctx, daemon, resolved.commit, q, slot.cursor)
			if serr != nil {
				put(id, multiResult{err: serr})
				return
			}
			put(id, multiResult{
				hits: got.hits, next: got.next, truncated: got.truncated,
				exhausted: got.exhausted, blobs: got.scannedBlobs,
				bytes: got.scannedBytes, skipped: got.skippedBlobs,
			})
		})
	}
	wg.Wait()
	return results
}

func mergeMultiPage(plan multiPlan, results map[int64]multiResult, fp string, repos []searchRepoRef) (SearchCodeOut, error) {
	out := SearchCodeOut{Type: "code", Hits: []SearchHitJSON{}, Repos: []RepoJSON{}, Users: []SearchUserJSON{}, ReposScanned: len(plan.slots)}
	dropped := 0
	var firstErr, firstCancelErr error
	for _, slot := range plan.slots {
		res, ok := results[slot.ref.row.ID]
		if !ok {
			return SearchCodeOut{}, op.Errorf(op.CodeInternal, "search fan-out lost repository %d", slot.ref.row.ID)
		}
		if err := res.err; err != nil {
			if isCancelErr(err) {
				if firstCancelErr == nil {
					firstCancelErr = err
				}
			} else if firstErr == nil {
				firstErr = err
			}
		}
		if res.dropped {
			dropped++
			continue
		}
	}
	if firstErr != nil {
		return SearchCodeOut{}, firstErr
	}
	if firstCancelErr != nil {
		return SearchCodeOut{}, firstCancelErr
	}
	if len(plan.slots) > 0 && dropped == len(plan.slots) {
		return SearchCodeOut{}, op.Errorf(op.CodeInvalid, "revision resolves in no visible repository")
	}
	type orderedHit struct {
		name string
		hit  SearchHitJSON
		id   int64
	}
	var ordered []orderedHit
	anomaly := ""
	anyTruncated := false
	emitted := make([]bool, len(plan.slots))
	for i, slot := range plan.slots {
		res, ok := results[slot.ref.row.ID]
		if !ok {
			return SearchCodeOut{}, op.Errorf(op.CodeInternal, "search fan-out lost repository %d", slot.ref.row.ID)
		}
		if res.dropped {
			emitted[i] = true
			continue
		}
		if res.truncated {
			anyTruncated = true
		}
		if res.exhausted != "" && res.exhausted != "none" {
			anomaly = worseExhaustion(anomaly, res.exhausted)
		}
		out.ScannedBlobs += res.blobs
		out.ScannedBytes += res.bytes
		out.SkippedBlobs += res.skipped
		emitted[i] = true
		if len(res.next) > 0 {
			break
		}
	}
	for i, slot := range plan.slots {
		if !emitted[i] {
			continue
		}
		res := results[slot.ref.row.ID]
		if res.dropped {
			continue
		}
		full := slot.ref.owner + "/" + slot.ref.name
		for _, h := range res.hits {
			ordered = append(ordered, orderedHit{
				hit: SearchHitJSON{
					Repo: full, Path: h.Path.String(), Line: h.Line, Column: h.Column,
					Preview: h.Preview, Blob: h.Blob,
				},
				name: slot.ref.name, id: slot.ref.row.ID,
			})
		}
	}
	slices.SortFunc(ordered, func(a, b orderedHit) int {
		return cmp.Or(strings.Compare(a.name, b.name), cmp.Compare(a.id, b.id),
			strings.Compare(a.hit.Path, b.hit.Path), cmp.Compare(a.hit.Line, b.hit.Line))
	})
	for _, h := range ordered {
		out.Hits = append(out.Hits, h.hit)
	}
	nextCursors := map[string]verbatimCursor{}
	for idRaw, cursor := range plan.carried {
		nextCursors[idRaw] = cursor
	}
	coveredName, coveredID := plan.base, plan.baseID
	for i, slot := range plan.slots {
		res := results[slot.ref.row.ID]
		if !emitted[i] {
			if old, ok := plan.prior[fmt.Sprintf("%d", slot.ref.row.ID)]; ok {
				nextCursors[fmt.Sprintf("%d", slot.ref.row.ID)] = old
			}
			continue
		}
		if res.dropped {
			if slot.fresh {
				coveredName, coveredID = slot.ref.name, slot.ref.row.ID
			}
			continue
		}
		if len(res.next) > 0 {
			nextCursors[fmt.Sprintf("%d", slot.ref.row.ID)] = res.next
		}
		if slot.fresh {
			coveredName, coveredID = slot.ref.name, slot.ref.row.ID
		}
	}
	moreFresh := false
	for _, ref := range repos {
		if ref.name > coveredName || (ref.name == coveredName && ref.row.ID > coveredID) {
			moreFresh = true
			break
		}
	}
	out.Truncated = anyTruncated || len(nextCursors) > 0 || moreFresh
	if len(nextCursors) == 0 && !moreFresh {
		out.Exhausted = cmp.Or(anomaly, "none")
		return out, nil
	}
	out.Exhausted = cmp.Or(anomaly, "budget")
	encoded, merr := encodeComposite(compositeCursor{
		Version: 1, Print: fp, Cursors: nextCursors,
		AfterName: coveredName, AfterID: coveredID,
	})
	if merr != nil {
		return SearchCodeOut{}, op.Wrap(op.CodeInternal, merr, "search cursor could not be encoded")
	}
	out.Next = &encoded
	return out, nil
}

func worseExhaustion(a, b string) string {
	rank := func(s string) int {
		switch s {
		case "deadline":
			return 2
		case "budget":
			return 1
		default:
			return 0
		}
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}

func isCancelErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// normalizeSearchExhausted pins the single-page contract: the daemon may
// report an empty exhaustion mark, which readers observe as "none".
func normalizeSearchExhausted(raw string) string {
	if raw == "" {
		return "none"
	}
	return raw
}

func isSkippableSearchError(err error) bool {
	oerr, ok := errors.AsType[*op.Error](err)
	if !ok {
		return false
	}
	return oerr.Code == op.CodeNotFound || oerr.Code == op.CodeInvalid || oerr.Code == op.CodeForbidden
}

func (d Deps) singlePage(ctx context.Context, rq requester, owner, name string, q searchCodeQuery) (SearchCodeOut, error) {
	out := SearchCodeOut{Type: "code", Hits: []SearchHitJSON{}, Repos: []RepoJSON{}, Users: []SearchUserJSON{}, Exhausted: "none"}
	var cursor verbatimCursor
	if strings.TrimSpace(q.next) != "" {
		if !strings.HasPrefix(strings.TrimSpace(q.next), "{") {
			return out, op.Errorf(op.CodeInvalid, "cursor is not valid")
		}
		var probe map[string]any
		if uerr := unmarshalJSON([]byte(q.next), &probe); uerr != nil {
			return out, op.Errorf(op.CodeInvalid, "cursor is not valid")
		}
		cursor = verbatimCursor(strings.TrimSpace(q.next))
	}
	rc, cerr := d.resolveRepo(ctx, rq, owner, name, false)
	if cerr != nil {
		return out, cerr
	}
	if derr := d.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
		return out, derr
	}
	daemon, oerr := d.openDaemonRepo(ctx, rc.row)
	if oerr != nil {
		return out, oerr
	}
	rc.daemon = daemon
	rev := cmp.Or(strings.TrimSpace(q.rev), rc.row.DefaultBranch)
	resolved, rerr := d.resolveRev(ctx, rc, rev)
	if rerr != nil {
		return out, rerr
	}
	got, serr := d.singleRepoSearch(ctx, daemon, resolved.commit, q, cursor)
	if serr != nil {
		return out, serr
	}
	full := rc.owner + "/" + rc.name
	for _, h := range got.hits {
		out.Hits = append(out.Hits, SearchHitJSON{
			Repo: full, Path: h.Path.String(), Line: h.Line, Column: h.Column,
			Preview: h.Preview, Blob: h.Blob,
		})
	}
	if len(got.next) > 0 {
		raw := string(got.next)
		out.Next = &raw
	}
	out.Truncated = len(got.next) > 0
	out.Exhausted = normalizeSearchExhausted(got.exhausted)
	out.ScannedBlobs = got.scannedBlobs
	out.ScannedBytes = got.scannedBytes
	out.SkippedBlobs = got.skippedBlobs
	out.ReposScanned = 1
	return out, nil
}

func (d Deps) multiPage(ctx context.Context, rq requester, q searchCodeQuery) (SearchCodeOut, error) {
	var resume compositeCursor
	if strings.TrimSpace(q.next) != "" {
		var derr error
		resume, derr = decodeComposite(q.next)
		if derr != nil {
			return SearchCodeOut{Type: "code"}, derr
		}
	}
	repos, verr := d.visibleSearchRepos(ctx, rq)
	if verr != nil {
		return SearchCodeOut{Type: "code"}, verr
	}
	fp := searchFingerprint(q, repos)
	plan, perr := planMultiPage(repos, resume, fp)
	if perr != nil {
		return SearchCodeOut{Type: "code"}, perr
	}
	if len(plan.slots) == 0 {
		return SearchCodeOut{Type: "code", Hits: []SearchHitJSON{}, Repos: []RepoJSON{}, Users: []SearchUserJSON{}, Exhausted: "none"}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	results := d.runMultiPlan(ctx, rq, plan.slots, q)
	return mergeMultiPage(plan, results, fp, repos)
}

type SearchCodeIn struct {
	Q               string `query:"q"`
	Type            string `query:"type"`
	Owner           string `query:"owner"`
	Repo            string `query:"repo"`
	Rev             string `query:"rev"`
	Globs           string `query:"globs"`
	ExcludeGlobs    string `query:"exclude_globs"`
	Path            string `query:"path"`
	Next            string `query:"next"`
	PerPage         int    `query:"per_page"`
	Regex           bool   `query:"regex"`
	CaseInsensitive bool   `query:"case_insensitive"`
}

type SearchReposIn struct {
	Q       string `query:"q"`
	Next    string `query:"next"`
	PerPage int    `query:"per_page"`
}

type SearchReposOut struct {
	Next  *string    `json:"next,omitempty"`
	Repos []RepoJSON `json:"repos"`
}

func searchCodeETag(out SearchCodeOut) string {
	next := ""
	if out.Next != nil {
		next = *out.Next
	}
	return fmt.Sprintf("search-%s-%d-%x", out.Type, len(out.Hits)+len(out.Repos)+len(out.Users), etagHash(next))
}

func (d Deps) searchReposPage(ctx context.Context, rq requester, q, next string, perPage int) (SearchReposOut, error) {
	out := SearchReposOut{Repos: []RepoJSON{}}
	st, serr := requireStore(d)
	if serr != nil {
		return out, serr
	}
	trimmed := strings.TrimSpace(q)
	if trimmed == "" {
		return out, op.Errorf(op.CodeValidation, "q must not be empty")
	}
	if len(q) > searchMaxQueryBytes {
		return out, op.Errorf(op.CodeValidation, "q must not exceed 4 KiB")
	}
	limit := clampLimit(perPage, searchDefaultPerPage, searchMaxPerPage)
	afterName, afterID := "", int64(0)
	if strings.TrimSpace(next) != "" {
		name, id, cerr := parseSearchCursor(next)
		if cerr != nil {
			return out, op.Errorf(op.CodeInvalid, "cursor is not valid")
		}
		afterName, afterID = name, id
	}
	rawExhausted := false
	var lastVisible store.RepoRow
	haveVisible := false
	for len(out.Repos) < limit && !rawExhausted {
		window := limit - len(out.Repos) + 1
		if window < 2 {
			window = 2
		}
		var rows []store.RepoRow
		rderr := st.Read(ctx, func(tx *store.ReadTx) error {
			var qerr error
			rows, qerr = tx.SearchReposFTS(q, afterName, afterID, window-1)
			return qerr
		})
		if rderr != nil {
			return out, op.Wrap(op.CodeInternal, rderr, "repository search failed")
		}
		if len(rows) == 0 {
			rawExhausted = true
			break
		}
		hasMoreRaw := len(rows) == window
		examined := rows
		if hasMoreRaw {
			examined = rows[:window-1]
		}
		for _, row := range examined {
			afterName, afterID = row.Name, row.ID
			owner := repoOwnerName(ctx, st, row)
			if owner == "" {
				continue
			}
			if derr := d.checkRepoAccess(rq, row, "repo.view", ""); derr != nil {
				continue
			}
			out.Repos = append(out.Repos, repoJSON(owner, row))
			lastVisible, haveVisible = row, true
			if len(out.Repos) == limit {
				break
			}
		}
		if !hasMoreRaw {
			rawExhausted = true
		}
	}
	if !rawExhausted && haveVisible {
		encoded := encodeSearchCursor(lastVisible.Name, lastVisible.ID)
		out.Next = &encoded
	}
	return out, nil
}

func (d Deps) searchUsersPage(ctx context.Context, q, next string, perPage int) (SearchUsersOut, error) {
	out := SearchUsersOut{Users: []SearchUserJSON{}}
	st, serr := requireStore(d)
	if serr != nil {
		return out, serr
	}
	trimmed := strings.TrimSpace(q)
	if trimmed == "" {
		return out, op.Errorf(op.CodeValidation, "q must not be empty")
	}
	if len(q) > searchMaxQueryBytes {
		return out, op.Errorf(op.CodeValidation, "q must not exceed 4 KiB")
	}
	limit := clampLimit(perPage, searchDefaultPerPage, searchMaxPerPage)
	afterName, afterID := "", int64(0)
	if strings.TrimSpace(next) != "" {
		name, id, cerr := parseSearchCursor(next)
		if cerr != nil {
			return out, op.Errorf(op.CodeInvalid, "cursor is not valid")
		}
		afterName, afterID = name, id
	}
	var rows []store.SearchedUser
	rderr := st.Read(ctx, func(tx *store.ReadTx) error {
		var qerr error
		rows, qerr = tx.SearchUsersFTS(q, afterName, afterID, limit)
		return qerr
	})
	if rderr != nil {
		return out, op.Wrap(op.CodeInternal, rderr, "user search failed")
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	var last store.SearchedUser
	for _, row := range rows {
		last = row
		out.Users = append(out.Users, SearchUserJSON{Name: row.Name, FullName: row.FullName})
	}
	if hasMore {
		encoded := encodeSearchCursor(last.Name, last.ID)
		out.Next = &encoded
	}
	return out, nil
}

func searchCodeHandler(d Deps) func(context.Context, op.Principal, SearchCodeIn) (SearchCodeOut, error) {
	return func(ctx context.Context, p op.Principal, in SearchCodeIn) (SearchCodeOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return SearchCodeOut{Type: "code"}, rerr
		}
		switch in.Type {
		case "", "code":
		case "repos":
			got, gerr := d.searchReposPage(ctx, rq, in.Q, in.Next, in.PerPage)
			if gerr != nil {
				return SearchCodeOut{Type: "repos"}, gerr
			}
			return SearchCodeOut{Type: "repos", Hits: []SearchHitJSON{}, Repos: got.Repos, Users: []SearchUserJSON{}, Next: got.Next}, nil
		case "users":
			got, gerr := d.searchUsersPage(ctx, in.Q, in.Next, in.PerPage)
			if gerr != nil {
				return SearchCodeOut{Type: "users"}, gerr
			}
			return SearchCodeOut{Type: "users", Hits: []SearchHitJSON{}, Repos: []RepoJSON{}, Users: got.Users, Next: got.Next}, nil
		default:
			return SearchCodeOut{Type: "code"}, op.Errorf(op.CodeInvalid, "unknown search type")
		}
		q, qerr := parseSearchCodeQuery(in.Q, in.Owner, in.Repo, in.Rev, in.Regex, in.CaseInsensitive, in.Globs, in.ExcludeGlobs, in.Path, in.Next, in.PerPage)
		if qerr != nil {
			return SearchCodeOut{Type: "code"}, qerr
		}
		if q.owner != "" {
			return d.singlePage(ctx, rq, q.owner, q.repo, q)
		}
		return d.multiPage(ctx, rq, q)
	}
}

func searchReposHandler(d Deps) func(context.Context, op.Principal, SearchReposIn) (SearchReposOut, error) {
	return func(ctx context.Context, p op.Principal, in SearchReposIn) (SearchReposOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return SearchReposOut{}, rerr
		}
		return d.searchReposPage(ctx, rq, in.Q, in.Next, in.PerPage)
	}
}

type SearchUserJSON struct {
	Name     string `json:"name"`
	FullName string `json:"full_name,omitempty"`
}

type SearchUsersIn struct {
	Q       string `query:"q"`
	Next    string `query:"next"`
	PerPage int    `query:"per_page"`
}

type SearchUsersOut struct {
	Next  *string          `json:"next,omitempty"`
	Users []SearchUserJSON `json:"users"`
}

func encodeSearchCursor(value string, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s\x1f%d", value, id)))
}

func parseSearchCursor(raw string) (string, int64, error) {
	decoded, derr := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if derr != nil {
		return "", 0, derr
	}
	name, idRaw, ok := strings.Cut(string(decoded), "\x1f")
	if !ok {
		return "", 0, fmt.Errorf("malformed search cursor")
	}
	var id int64
	if _, serr := fmt.Sscanf(idRaw, "%d", &id); serr != nil {
		return "", 0, serr
	}
	return name, id, nil
}

func searchUsersHandler(d Deps) func(context.Context, op.Principal, SearchUsersIn) (SearchUsersOut, error) {
	return func(ctx context.Context, p op.Principal, in SearchUsersIn) (SearchUsersOut, error) {
		if _, rerr := d.requester(ctx, p); rerr != nil {
			return SearchUsersOut{}, rerr
		}
		return d.searchUsersPage(ctx, in.Q, in.Next, in.PerPage)
	}
}

// RegisterSearch registers search.code, search.repos, and search.users in
// brief order.
func RegisterSearch(t *op.Table, deps Deps) error {
	if err := op.Register(t, op.Op[SearchCodeIn, SearchCodeOut]{
		Name: "search.code", Kind: op.KindRead, Method: http.MethodGet, Path: "/search",
		Scope: op.ScopeRepoRead, Summary: "Search code across repositories",
		Description: "Multi-repo code search over Ruk with a composite continuation; " +
			"an owner and repo scope narrows to one repository with a verbatim Ruk cursor. " +
			"Pages carry the continuation in the body and emit no Link header; " +
			"an expired lease reports lease-conflict and the client restarts from page one. " +
			"Blobs over 512 KiB are skipped; rukie holds no result cache.",
		Problems: []op.Code{op.CodeValidation, op.CodeInvalid, op.CodeNotFound, op.CodeForbidden, op.CodeLeaseConflict, op.CodeUnavailable},
		ETag:     searchCodeETag,
		Handler:  searchCodeHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[SearchReposIn, SearchReposOut]{
		Name: "search.repos", Kind: op.KindRead, Method: http.MethodGet, Path: "/search/repos",
		Scope: op.ScopePublicRead, Summary: "Search repositories",
		Description: "Trigram FTS over repository metadata in name order. " +
			"Private rows stay invisible; the continuation rides the body.",
		Problems: []op.Code{op.CodeValidation, op.CodeInvalid, op.CodeUnavailable},
		ETag: func(out SearchReposOut) string {
			if len(out.Repos) == 0 {
				return "search-repos-empty"
			}
			first, last := out.Repos[0], out.Repos[len(out.Repos)-1]
			next := ""
			if out.Next != nil {
				next = *out.Next
			}
			return fmt.Sprintf("search-repos-%d-%s-%s-%x", len(out.Repos), first.Name, last.Name, etagHash(next))
		},
		Handler: searchReposHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[SearchUsersIn, SearchUsersOut]{
		Name: "search.users", Kind: op.KindRead, Method: http.MethodGet, Path: "/search/users",
		Scope: op.ScopePublicRead, Summary: "Search users",
		Description: "Trigram FTS over the public user directory in name order. " +
			"Only active users appear; the continuation rides the body.",
		Problems: []op.Code{op.CodeValidation, op.CodeInvalid, op.CodeUnavailable},
		ETag: func(out SearchUsersOut) string {
			if len(out.Users) == 0 {
				return "search-users-empty"
			}
			first, last := out.Users[0], out.Users[len(out.Users)-1]
			next := ""
			if out.Next != nil {
				next = *out.Next
			}
			return fmt.Sprintf("search-users-%d-%s-%s-%x", len(out.Users), first.Name, last.Name, etagHash(next))
		},
		Handler: searchUsersHandler(deps),
	}); err != nil {
		return err
	}
	return nil
}

// RewriteSearchPaths maps the brief's hand-authored UI routes onto the
// canonical search endpoints without adding ops. One path per op and the
// reserved explore prefix leave two routes homeless: /explore/code cannot
// host an op path, and /{owner}/{repo}/search would need a fourth op for
// the owner partition. The exact /explore/code route proxies to /search,
// and any /{owner}/{repo}/search owner route rewrites to the scoped
// /search query (path segments stay authoritative). Site routes never
// rewrite, so /search itself and its children pass through untouched.
func RewriteSearchPaths(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/explore/code" {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/search"
			r2.URL.RawPath = ""
			r2.RequestURI = ""
			next.ServeHTTP(w, r2)
			return
		}
		trimmed := strings.TrimPrefix(r.URL.Path, "/")
		parts := strings.Split(trimmed, "/")
		if len(parts) == 3 && parts[2] == "search" && parts[0] != "" && parts[1] != "" && !SiteSegment(parts[0]) {
			r2 := r.Clone(r.Context())
			query := r2.URL.Query()
			query.Set("owner", parts[0])
			query.Set("repo", parts[1])
			r2.URL.Path = "/search"
			r2.URL.RawPath = ""
			r2.URL.RawQuery = query.Encode()
			r2.RequestURI = ""
			next.ServeHTTP(w, r2)
			return
		}
		next.ServeHTTP(w, r)
	})
}
