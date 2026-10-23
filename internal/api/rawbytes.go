package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
)

const rawPreviewCap = 64 << 10

func sniffRawContentType(content []byte) string {
	kind := http.DetectContentType(content)
	if strings.HasPrefix(kind, "text/") {
		return "text/plain; charset=utf-8"
	}
	if kind == "application/octet-stream" {
		return kind
	}
	if strings.HasSuffix(kind, "xml") || strings.HasPrefix(kind, "image/") {
		return kind
	}
	return "application/octet-stream"
}

func (d Deps) rawMetadata(ctx context.Context, rc repoContext, rev, path string) (RawGetOut, error) {
	if path == "" {
		return RawGetOut{}, op.Errorf(op.CodeValidation, "path must not be empty")
	}
	if perr := checkTreePath(path); perr != nil {
		return RawGetOut{}, perr
	}
	daemon, oerr := d.openDaemonRepo(ctx, rc.row)
	if oerr != nil {
		return RawGetOut{}, oerr
	}
	rc.daemon = daemon
	resolved, rerr := d.resolveRev(ctx, rc, rev)
	if rerr != nil {
		return RawGetOut{}, rerr
	}
	body, berr := d.readFileBody(ctx, daemon, resolved.commit, path)
	if berr != nil {
		return RawGetOut{}, berr
	}
	if body.Blob == nil {
		return RawGetOut{}, op.Errorf(op.CodeInvalid, "raw serves file blobs only")
	}
	content, cerr := byteSourceBytes(body.Blob.Content)
	if cerr != nil {
		return RawGetOut{}, cerr
	}
	out := RawGetOut{
		Oid:         body.Blob.Oid,
		Size:        body.Blob.Size,
		ContentType: sniffRawContentType(content),
	}
	if !isBinaryContent(content) {
		if len(content) > rawPreviewCap {
			out.Content = string(content[:rawPreviewCap])
			out.Truncated = true
		} else {
			out.Content = string(content)
		}
	}
	return out, nil
}

type RawBytes struct {
	mux  *http.ServeMux
	Deps Deps
}

func (h RawBytes) routes() *http.ServeMux {
	if h.mux != nil {
		return h.mux
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{owner}/{repo}/raw/{rev}/{path...}", h.serveRaw)
	mux.HandleFunc("GET /{owner}/{repo}/commit/{oid}/diff", func(w http.ResponseWriter, r *http.Request) {
		h.serveCommitDiff(w, r, "diff")
	})
	mux.HandleFunc("GET /{owner}/{repo}/commit/{oid}/patch", func(w http.ResponseWriter, r *http.Request) {
		h.serveCommitDiff(w, r, "patch")
	})
	h.mux = mux
	return mux
}

func looksLikeRaw(path string) bool {
	trimmed := strings.TrimPrefix(path, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 5 {
		return false
	}
	if parts[2] == "raw" && parts[3] != "" && parts[4] != "" {
		return true
	}
	if parts[2] == "commit" && len(parts) == 5 && (parts[4] == "diff" || parts[4] == "patch") {
		return true
	}
	return false
}

func (h RawBytes) Wrap(next http.Handler) http.Handler {
	routes := h.routes()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !looksLikeRaw(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if !wantRawBytes(r) {
			next.ServeHTTP(w, r)
			return
		}
		routes.ServeHTTP(w, r)
	})
}

func wantRawBytes(r *http.Request) bool {
	query := r.URL.Query()
	if query.Has("download") {
		return true
	}
	if r.Header.Get("Range") != "" {
		return true
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	if accept == "" || accept == "*/*" {
		return false
	}
	for _, item := range strings.Split(accept, ",") {
		media, _, _ := strings.Cut(strings.TrimSpace(item), ";")
		media = strings.TrimSpace(media)
		if media == "application/json" || media == "text/markdown" {
			return false
		}
	}
	return true
}

func (h RawBytes) principalFor(r *http.Request) op.Principal {
	auther := auth.NewAuthenticator(h.Deps.Service)
	if header := r.Header.Get("Authorization"); header != "" {
		scheme, value, _ := strings.Cut(header, " ")
		if strings.EqualFold(scheme, "Bearer") && value != "" {
			if p, err := auther.Bearer(r.Context(), value); err == nil {
				return p
			}
		}
		return op.AnonymousPrincipal()
	}
	if cookie, err := r.Cookie("__Host-rukie_session"); err == nil && cookie.Value != "" {
		if p, err := auther.Session(r.Context(), cookie.Value); err == nil {
			return p
		}
	}
	return op.AnonymousPrincipal()
}

func (h RawBytes) loadRepo(r *http.Request, owner, repo string) (repoContext, error) {
	ctx := r.Context()
	rq, rerr := h.Deps.requester(ctx, h.principalFor(r))
	if rerr != nil {
		return repoContext{}, rerr
	}
	rc, cerr := h.Deps.resolveRepo(ctx, rq, owner, repo, false)
	if cerr != nil {
		return repoContext{}, cerr
	}
	if derr := h.Deps.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
		return repoContext{}, derr
	}
	return rc, nil
}

func writeRawProblem(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if oerr, ok := errors.AsType[*op.Error](err); ok {
		status = problemStatus(oerr.Code)
	}
	http.Error(w, "raw: "+err.Error(), status)
}

func problemStatus(code op.Code) int {
	for _, problem := range op.Problems() {
		if problem.Code == code {
			return problem.Status
		}
	}
	return http.StatusInternalServerError
}

type rawPayload struct {
	oid         string
	contentType string
	commit      string
	content     []byte
	size        uint64
	fullOidRev  bool
}

func (h RawBytes) loadRawPayload(r *http.Request, rc repoContext, rev, path string) (rawPayload, error) {
	ctx := r.Context()
	daemon, oerr := h.Deps.openDaemonRepo(ctx, rc.row)
	if oerr != nil {
		return rawPayload{}, oerr
	}
	cc := rc
	cc.daemon = daemon
	resolved, rerr := h.Deps.resolveRev(ctx, cc, rev)
	if rerr != nil {
		return rawPayload{}, rerr
	}
	body, berr := h.Deps.readFileBody(ctx, daemon, resolved.commit, path)
	if berr != nil {
		return rawPayload{}, berr
	}
	if body.Blob == nil {
		return rawPayload{}, op.Errorf(op.CodeInvalid, "raw serves file blobs only")
	}
	content, cerr := byteSourceBytes(body.Blob.Content)
	if cerr != nil {
		return rawPayload{}, cerr
	}
	return rawPayload{
		content:     content,
		oid:         body.Blob.Oid,
		size:        body.Blob.Size,
		contentType: sniffRawContentType(content),
		commit:      resolved.commit,
		fullOidRev:  isFullOid(strings.TrimSpace(rev)),
	}, nil
}

func (h RawBytes) serveRaw(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("owner")
	repo := r.PathValue("repo")
	rev := r.PathValue("rev")
	path := r.PathValue("path")
	rc, lerr := h.loadRepo(r, owner, repo)
	if lerr != nil {
		writeRawProblem(w, lerr)
		return
	}
	if path == "" {
		writeRawProblem(w, op.Errorf(op.CodeValidation, "path must not be empty"))
		return
	}
	if perr := checkTreePath(path); perr != nil {
		writeRawProblem(w, perr)
		return
	}
	payload, perr := h.loadRawPayload(r, rc, rev, path)
	if perr != nil {
		writeRawProblem(w, perr)
		return
	}
	download := r.URL.Query().Has("download")
	etag := "\"raw-" + payload.oid + "\""
	if matchRawETag(r.Header.Get("If-None-Match"), etag) {
		h.setRawHeaders(w, payload, etag, download)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method == http.MethodHead {
		h.setRawHeaders(w, payload, etag, download)
		w.Header().Set("Content-Length", strconv.FormatUint(payload.size, 10))
		w.WriteHeader(http.StatusOK)
		return
	}
	if rng := r.Header.Get("Range"); rng != "" {
		h.serveRange(w, r, payload, etag, download, rng)
		return
	}
	h.setRawHeaders(w, payload, etag, download)
	w.Header().Set("Content-Length", strconv.FormatUint(payload.size, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload.content)
}

func matchRawETag(header, etag string) bool {
	for part := range strings.SplitSeq(header, ",") {
		if strings.TrimSpace(part) == etag {
			return true
		}
	}
	return false
}

func (h RawBytes) setRawHeaders(w http.ResponseWriter, payload rawPayload, etag string, download bool) {
	header := w.Header()
	header.Set("Content-Type", payload.contentType)
	header.Set("ETag", etag)
	header.Set("Accept-Ranges", "bytes")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", "sandbox")
	header.Set("Vary", "Accept")
	if payload.fullOidRev {
		header.Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		header.Set("Cache-Control", "private, no-cache")
	}
	if download {
		header.Set("Content-Disposition", "attachment")
	}
}

func (h RawBytes) serveRange(w http.ResponseWriter, _ *http.Request, payload rawPayload, etag string, download bool, rng string) {
	start, length, ok := parseSingleRange(rng, payload.size)
	if !ok {
		w.Header().Set("Content-Range", "bytes */"+strconv.FormatUint(payload.size, 10))
		http.Error(w, "raw: range is not satisfiable", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	h.setRawHeaders(w, payload, etag, download)
	w.Header().Set("Content-Range", "bytes "+strconv.FormatUint(start, 10)+"-"+
		strconv.FormatUint(start+length-1, 10)+"/"+strconv.FormatUint(payload.size, 10))
	w.Header().Set("Content-Length", strconv.FormatUint(length, 10))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(payload.content[start : start+length])
}

func parseSingleRange(header string, size uint64) (uint64, uint64, bool) {
	spec, ok := strings.CutPrefix(strings.TrimSpace(header), "bytes=")
	if !ok {
		return 0, 0, false
	}
	if strings.Contains(spec, ",") {
		return 0, 0, false
	}
	if size == 0 {
		return 0, 0, false
	}
	startRaw, endRaw, _ := strings.Cut(spec, "-")
	if startRaw == "" {
		suffix, serr := strconv.ParseUint(strings.TrimSpace(endRaw), 10, 64)
		if serr != nil || suffix == 0 {
			return 0, 0, false
		}
		if suffix > size {
			suffix = size
		}
		return size - suffix, suffix, true
	}
	start, serr := strconv.ParseUint(strings.TrimSpace(startRaw), 10, 64)
	if serr != nil || start >= size {
		return 0, 0, false
	}
	if strings.TrimSpace(endRaw) == "" {
		return start, size - start, true
	}
	end, eerr := strconv.ParseUint(strings.TrimSpace(endRaw), 10, 64)
	if eerr != nil || end < start {
		return 0, 0, false
	}
	if end >= size {
		end = size - 1
	}
	return start, end - start + 1, true
}

func (h RawBytes) serveCommitDiff(w http.ResponseWriter, r *http.Request, format string) {
	owner := r.PathValue("owner")
	repo := r.PathValue("repo")
	oid := r.PathValue("oid")
	rc, lerr := h.loadRepo(r, owner, repo)
	if lerr != nil {
		writeRawProblem(w, lerr)
		return
	}
	if !isFullOid(strings.TrimSpace(oid)) {
		writeRawProblem(w, op.Errorf(op.CodeValidation, "commit must be a full object id"))
		return
	}
	commit := strings.ToLower(strings.TrimSpace(oid))
	daemon, oerr := h.Deps.openDaemonRepo(r.Context(), rc.row)
	if oerr != nil {
		writeRawProblem(w, oerr)
		return
	}
	cc := rc
	cc.daemon = daemon
	parents, perr := h.Deps.commitParents(r.Context(), cc, commit)
	if perr != nil {
		writeRawProblem(w, perr)
		return
	}
	if len(parents) == 0 {
		writeRawProblem(w, op.Errorf(op.CodeInvalid, "the root commit has no diff base"))
		return
	}
	args := map[string]any{
		"left":  headRevisionRef(parents[0]),
		"right": headRevisionRef(commit),
		"paths": "all",
		"page":  map[string]any{"limit": 1000},
	}
	var out strings.Builder
	for {
		files, next, derr := h.Deps.readDiffPage(r.Context(), daemon, args)
		if derr != nil {
			writeRawProblem(w, derr)
			return
		}
		for _, file := range files {
			if file.Binary {
				out.WriteString("Binary files differ: " + file.Path + "\n")
				continue
			}
			out.WriteString(file.Patch)
			if !strings.HasSuffix(file.Patch, "\n") {
				out.WriteString("\n")
			}
		}
		if next == "" {
			break
		}
		cursor, cerr := parseRukCursor(next)
		if cerr != nil {
			writeRawProblem(w, cerr)
			return
		}
		args["page"] = map[string]any{"limit": 1000, "cursor": cursor}
	}
	contentType := "text/x-diff"
	if format == "patch" {
		contentType = "text/x-patch"
	}
	header := w.Header()
	header.Set("Content-Type", contentType)
	header.Set("ETag", "\"commit-diff-"+commit+"\"")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", "sandbox")
	header.Set("Vary", "Accept")
	header.Set("Cache-Control", "private, max-age=31536000, immutable")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(out.String()))
}
