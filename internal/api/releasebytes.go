package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/store"
)

func (d Deps) releaseRoot() string {
	if d.DataDir == "" {
		return ""
	}
	return filepath.Join(d.DataDir, "releases")
}

func releaseContentPath(root, sum string) string {
	return filepath.Join(root, "sha256", sum[:2], sum[2:4], sum)
}

func releaseFilePath(root, rel string) string {
	return filepath.Join(root, filepath.FromSlash(rel))
}

func chmodReleaseFile(path string) {
	_ = os.Chmod(filepath.Clean(path), 0o400)
}

func assetStorageRel(sum string) string {
	return "sha256/" + sum[:2] + "/" + sum[2:4] + "/" + sum
}

type ReleaseBytes struct {
	next http.Handler
	Deps Deps
}

func (h ReleaseBytes) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{owner}/{repo}/releases/new", h.serveNewRedirect)
	mux.HandleFunc("GET /{owner}/{repo}/releases/latest", h.serveLatest)
	mux.HandleFunc("PUT /{owner}/{repo}/releases/assets/{rest...}", h.serveAssetUpload)
	mux.HandleFunc("GET /{owner}/{repo}/releases/assets/{rest...}", h.serveAssetDownload)
	mux.HandleFunc("GET /{owner}/{repo}/releases/attachments/{uuid}", h.serveAssetUUID)
	mux.HandleFunc("GET /{owner}/{repo}/archive/{rev}", h.serveArchive)
	return mux
}

func releaseBytesPath(path string) (string, bool) {
	trimmed := strings.TrimPrefix(path, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 4 {
		return "", false
	}
	rest := strings.Join(parts[2:], "/")
	switch {
	case rest == "releases/new":
		return "new", true
	case rest == "releases/latest":
		return "latest", true
	case strings.HasPrefix(rest, "releases/attachments/"):
		return "attachments", true
	case strings.HasPrefix(rest, "archive/"):
		return "archive", true
	case strings.HasPrefix(rest, "releases/assets/"):
		return "assets", true
	default:
		return "", false
	}
}

func (h ReleaseBytes) Wrap(next http.Handler) http.Handler {
	h.next = next
	routes := h.routes()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind, ok := releaseBytesPath(r.URL.Path)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		if kind == "assets" {
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPut {
				next.ServeHTTP(w, r)
				return
			}
			if r.Method != http.MethodPut && !wantReleaseBytes(r) {
				next.ServeHTTP(w, r)
				return
			}
		}
		if kind == "archive" {
			if !wantReleaseBytes(r) {
				next.ServeHTTP(w, r)
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				next.ServeHTTP(w, r)
				return
			}
		}
		if kind == "new" && r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		if kind == "latest" && r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		routes.ServeHTTP(w, r)
	})
}

func wantReleaseBytes(r *http.Request) bool {
	query := r.URL.Query()
	if query.Has("download") {
		return true
	}
	if r.Header.Get("Range") != "" {
		return true
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	if accept == "" || accept == "*/*" {
		return r.Method != http.MethodGet
	}
	for _, item := range strings.Split(accept, ",") {
		media, _, _ := strings.Cut(strings.TrimSpace(item), ";")
		media = strings.TrimSpace(media)
		if media == "application/json" || media == "text/markdown" || media == "text/html" {
			return false
		}
	}
	return true
}

func (h ReleaseBytes) principalFor(r *http.Request) op.Principal {
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

func (h ReleaseBytes) loadRepo(r *http.Request, owner, repo string) (repoContext, requester, error) {
	ctx := r.Context()
	rq, rerr := h.Deps.requester(ctx, h.principalFor(r))
	if rerr != nil {
		return repoContext{}, rq, rerr
	}
	rc, cerr := h.Deps.resolveRepo(ctx, rq, owner, repo, false)
	if cerr != nil {
		return repoContext{}, rq, cerr
	}
	return rc, rq, nil
}

func (h ReleaseBytes) serveNewRedirect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rq, rerr := h.Deps.requester(ctx, h.principalFor(r))
	if rerr != nil {
		writeRawProblem(w, rerr)
		return
	}
	rc, cerr := h.Deps.resolveRepo(ctx, rq, r.PathValue("owner"), r.PathValue("repo"), false)
	if cerr != nil {
		writeRawProblem(w, cerr)
		return
	}
	http.Redirect(w, r, "/"+rc.owner+"/"+rc.name+"/releases", http.StatusSeeOther)
}

func (h ReleaseBytes) serveLatest(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("owner")
	repo := r.PathValue("repo")
	rc, rq, lerr := h.loadRepo(r, owner, repo)
	if lerr != nil {
		writeRawProblem(w, lerr)
		return
	}
	canWrite := h.Deps.checkRepoAccess(rq, rc.row, "repo.push", "") == nil
	if derr := h.Deps.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
		writeRawProblem(w, derr)
		return
	}
	var rows []store.ReleaseRow
	rerr := h.Deps.Store.Read(r.Context(), func(tx *store.ReadTx) error {
		var lerr error
		if canWrite {
			rows, lerr = tx.ListReleasesIncludingDrafts(rc.row.ID, store.ReleasePage{Limit: 1})
		} else {
			rows, lerr = tx.ListPublishedReleases(rc.row.ID, store.ReleasePage{Limit: 1})
		}
		return lerr
	})
	if rerr != nil {
		writeRawProblem(w, op.Wrap(op.CodeInternal, rerr, "release lookup failed"))
		return
	}
	if len(rows) == 0 {
		writeRawProblem(w, op.Errorf(op.CodeNotFound, "no releases yet"))
		return
	}
	http.Redirect(w, r, releaseTagURL(rc.owner, rc.name, rows[0].TagName), http.StatusMovedPermanently)
}

func checkReleaseMediaType(raw string) (string, error) {
	media, _, _ := strings.Cut(raw, ";")
	media = strings.TrimSpace(strings.ToLower(media))
	kind, subtype, ok := strings.Cut(media, "/")
	if !ok || kind == "" || subtype == "" {
		return "", op.Errorf(op.CodeUnsupportedMedia, "asset content type must be type/subtype")
	}
	if len(media) > 255 {
		return "", op.Errorf(op.CodeUnsupportedMedia, "asset content type is too long")
	}
	for _, c := range media {
		if c <= 0x20 || c == 0x7f || c == '(' || c == ')' || c == '<' || c == '>' || c == '@' || c == ',' || c == ';' || c == ':' || c == '\\' || c == '"' || c == '[' || c == ']' || c == '?' || c == '=' {
			return "", op.Errorf(op.CodeUnsupportedMedia, "asset content type is not valid")
		}
	}
	return media, nil
}

func parseReprDigest(header string) ([]byte, bool, error) {
	if strings.TrimSpace(header) == "" {
		return nil, false, nil
	}
	for _, item := range strings.Split(header, ",") {
		name, value, ok := strings.Cut(strings.TrimSpace(item), "=")
		if !ok {
			continue
		}
		if strings.TrimSpace(name) != "sha-256" {
			continue
		}
		encoded := strings.Trim(strings.TrimSpace(value), ":")
		raw, derr := base64.StdEncoding.DecodeString(encoded)
		if derr != nil {
			return nil, false, op.Errorf(op.CodeValidation, "digest is not valid")
		}
		return raw, true, nil
	}
	return nil, false, nil
}

func splitAssetRest(rest string) (string, string, bool) {
	tag, name, ok := strings.CutLast(rest, "/")
	if !ok || tag == "" || name == "" {
		return "", "", false
	}
	return tag, name, true
}

func (h ReleaseBytes) serveAssetUpload(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("owner")
	repo := r.PathValue("repo")
	tag, name, ok := splitAssetRest(r.PathValue("rest"))
	if !ok {
		if h.next != nil {
			h.next.ServeHTTP(w, r)
			return
		}
		writeRawProblem(w, op.Errorf(op.CodeNotFound, "asset was not found"))
		return
	}
	if nerr := checkAssetName(name); nerr != nil {
		writeRawProblem(w, nerr)
		return
	}
	if r.Header.Get("Authorization") == "" {
		writeRawProblem(w, op.Errorf(op.CodeForbidden, "asset upload needs a scoped API token"))
		return
	}
	if r.Header.Get("Content-Type") == "" {
		writeRawProblem(w, op.Errorf(op.CodeUnsupportedMedia, "asset content type is required"))
		return
	}
	if _, merr := checkReleaseMediaType(r.Header.Get("Content-Type")); merr != nil {
		writeRawProblem(w, merr)
		return
	}
	if r.ContentLength > releaseMaxAssetBytes {
		writeRawProblem(w, op.Errorf(op.CodeTooLarge, "asset exceeds the size limit"))
		return
	}
	ctx := r.Context()
	rq, rerr := h.Deps.requester(ctx, h.principalFor(r))
	if rerr != nil {
		writeRawProblem(w, rerr)
		return
	}
	rc, cerr := h.Deps.resolveRepo(ctx, rq, owner, repo, false)
	if cerr != nil {
		writeRawProblem(w, cerr)
		return
	}
	if derr := h.Deps.checkRepoAccess(rq, rc.row, "release.publish", ""); derr != nil {
		writeRawProblem(w, derr)
		return
	}
	if rc.row.Archived {
		writeRawProblem(w, op.Errorf(op.CodeArchived, "repository is archived"))
		return
	}
	row, rerr := h.Deps.Store.ReleaseByTag(ctx, rc.row.ID, tag)
	if rerr != nil {
		if errors.Is(rerr, store.ErrNotFound) {
			writeRawProblem(w, op.Errorf(op.CodeNotFound, "release was not found"))
			return
		}
		writeRawProblem(w, op.Wrap(op.CodeInternal, rerr, "release lookup failed"))
		return
	}
	if row.State != "draft" {
		writeRawProblem(w, op.Errorf(op.CodeConflict, "published releases are immutable"))
		return
	}
	ifMatch := r.Header.Get("If-Match")
	ifNoneMatch := r.Header.Get("If-None-Match")
	if ifMatch == "" && ifNoneMatch == "" {
		writeRawProblem(w, op.Errorf(op.CodePreconditionReq, "If-Match or If-None-Match is required"))
		return
	}
	root := h.Deps.releaseRoot()
	if root == "" {
		writeRawProblem(w, op.Errorf(op.CodeConfigUnavailable, "release storage is not configured"))
		return
	}
	expected, hasDigest, derr := parseReprDigest(r.Header.Get("Repr-Digest"))
	if derr != nil {
		writeRawProblem(w, derr)
		return
	}
	sum, size, contentType, serr := streamAssetToStore(root, r.Body, expected, hasDigest)
	if serr != nil {
		writeRawProblem(w, serr)
		return
	}
	now := h.Deps.now()
	publicID := string(store.NewID(now))
	status := http.StatusCreated
	werr := h.Deps.Store.Write(ctx, func(tx *store.WriteTx) error {
		live, found, ferr := tx.GetAssetForUpdate(row.ID, name)
		if ferr != nil {
			return ferr
		}
		if found {
			status = http.StatusOK
			if ifNoneMatch != "" {
				return op.Errorf(op.CodeConflict, "asset already exists")
			}
			if ifMatch != "" && !matchAssetETag(ifMatch, live.SHA256) {
				return op.Errorf(op.CodeStale, "asset changed under this request")
			}
			if derr := tx.DeleteAssetByPublicID(live.PublicID); derr != nil {
				return derr
			}
		} else if ifMatch != "" && ifNoneMatch == "" {
			return op.Errorf(op.CodeNotFound, "asset was not found")
		}
		return tx.InsertAsset(store.AssetRow{
			PublicID:    publicID,
			ReleaseID:   row.ID,
			UploaderID:  rq.userID,
			Name:        name,
			SHA256:      sum,
			Size:        size,
			ContentType: contentType,
			State:       "draft",
			Path:        assetStorageRel(sum),
			CreatedAt:   now,
		})
	})
	if werr != nil {
		if oerr, ok := errors.AsType[*op.Error](werr); ok {
			writeRawProblem(w, oerr)
			return
		}
		if errors.Is(werr, store.ErrConflict) {
			writeRawProblem(w, op.Errorf(op.CodeConflict, "published releases are immutable"))
			return
		}
		writeRawProblem(w, op.Wrap(op.CodeInternal, werr, "asset store failed"))
		return
	}
	item := ReleaseAssetJSON{
		ID:          publicID,
		Name:        name,
		Size:        size,
		ContentType: contentType,
		SHA256:      sum,
		UploadedBy:  rq.userID,
		CreatedAt:   now.Unix(),
		URL:         "/" + owner + "/" + repo + "/releases/assets/" + tag + "/" + name,
	}
	raw, merr := marshalJSON(item)
	if merr != nil {
		writeRawProblem(w, op.Wrap(op.CodeInternal, merr, "asset encode failed"))
		return
	}
	header := w.Header()
	header.Set("ETag", assetETag(sum))
	header.Set("Repr-Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(mustDecodeHex(sum))+":")
	header.Set("Content-Type", "application/json")
	header.Set("Location", item.URL)
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func mustDecodeHex(sum string) []byte {
	raw := make([]byte, sha256.Size)
	for i := range raw {
		hi := hexVal(sum[2*i])
		lo := hexVal(sum[2*i+1])
		raw[i] = hi<<4 | lo
	}
	return raw
}

func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return 0
	}
}

type cappedReader struct {
	reader io.Reader
	left   int64
	over   bool
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		c.over = true
		return 0, io.EOF
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.reader.Read(p)
	c.left -= int64(n)
	if c.left == 0 {
		c.over = true
	}
	return n, err
}

func streamAssetToStore(root string, body io.Reader, expected []byte, hasDigest bool) (string, int64, string, error) {
	tmpDir := filepath.Clean(filepath.Join(root, "tmp"))
	if merr := os.MkdirAll(tmpDir, 0o700); merr != nil {
		return "", 0, "", op.Wrap(op.CodeInternal, merr, "asset staging failed")
	}
	tmp, terr := os.CreateTemp(tmpDir, "*.part")
	if terr != nil {
		return "", 0, "", op.Wrap(op.CodeInternal, terr, "asset staging failed")
	}
	tmpName := filepath.Clean(tmp.Name())
	failed := true
	defer func() {
		if failed {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	hash := sha256.New()
	capped := &cappedReader{reader: body, left: releaseMaxAssetBytes + 1}
	buf := make([]byte, 32*1024)
	var size int64
	for {
		n, rerr := capped.Read(buf)
		if n > 0 {
			size += int64(n)
			hash.Write(buf[:n])
			if _, werr := tmp.Write(buf[:n]); werr != nil {
				return "", 0, "", op.Wrap(op.CodeInternal, werr, "asset staging failed")
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			return "", 0, "", op.Wrap(op.CodeInvalid, rerr, "asset body ended early")
		}
	}
	if size > releaseMaxAssetBytes {
		return "", 0, "", op.Errorf(op.CodeTooLarge, "asset exceeds the size limit")
	}
	sum := hash.Sum(nil)
	if hasDigest && !bytes.Equal(sum, expected) {
		return "", 0, "", op.Errorf(op.CodeValidation, "digest mismatch")
	}
	if cerr := tmp.Close(); cerr != nil {
		return "", 0, "", op.Wrap(op.CodeInternal, cerr, "asset staging failed")
	}
	sniffed, serr := sniffAssetContentType(tmpName)
	if serr != nil {
		return "", 0, "", serr
	}
	hex := fmt.Sprintf("%x", sum)
	final := filepath.Clean(releaseContentPath(root, hex))
	if merr := os.MkdirAll(filepath.Clean(filepath.Dir(final)), 0o700); merr != nil {
		return "", 0, "", op.Wrap(op.CodeInternal, merr, "asset store failed")
	}
	if rerr := os.Rename(tmpName, final); rerr != nil {
		if cerr := copyAssetFile(tmpName, final); cerr != nil {
			return "", 0, "", cerr
		}
		_ = os.Remove(tmpName)
	}
	failed = false
	return hex, size, sniffed, nil
}

func sniffAssetContentType(path string) (string, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", op.Wrap(op.CodeInternal, err, "asset inspect failed")
	}
	head := make([]byte, 512)
	n, _ := f.Read(head)
	_ = f.Close()
	return http.DetectContentType(head[:n]), nil
}

func copyAssetFile(src, dst string) error {
	in, err := os.Open(filepath.Clean(src))
	if err != nil {
		return op.Wrap(op.CodeInternal, err, "asset store failed")
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(filepath.Clean(dst), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return op.Wrap(op.CodeInternal, err, "asset store failed")
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return op.Wrap(op.CodeInternal, err, "asset store failed")
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return op.Wrap(op.CodeInternal, err, "asset store failed")
	}
	if err := out.Close(); err != nil {
		return op.Wrap(op.CodeInternal, err, "asset store failed")
	}
	return nil
}

func (h ReleaseBytes) openAssetFile(asset store.AssetRow) (*os.File, error) {
	root := h.Deps.releaseRoot()
	if root == "" {
		return nil, op.Errorf(op.CodeConfigUnavailable, "release storage is not configured")
	}
	f, err := os.Open(filepath.Clean(releaseFilePath(root, asset.Path)))
	if err != nil {
		return nil, op.Errorf(op.CodeNotFound, "asset bytes are missing")
	}
	return f, nil
}

func serveAssetContent(w http.ResponseWriter, r *http.Request, asset store.AssetRow, f *os.File, name string) {
	defer func() { _ = f.Close() }()
	info, serr := f.Stat()
	if serr != nil {
		writeRawProblem(w, op.Wrap(op.CodeInternal, serr, "asset inspect failed"))
		return
	}
	etag := assetETag(asset.SHA256)
	if matchRawETag(r.Header.Get("If-None-Match"), etag) {
		header := w.Header()
		header.Set("ETag", etag)
		header.Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method == http.MethodHead {
		header := w.Header()
		header.Set("Content-Type", asset.ContentType)
		header.Set("ETag", etag)
		header.Set("Accept-Ranges", "bytes")
		header.Set("Content-Length", strconv.FormatInt(info.Size(), 10))
		header.Set("Content-Disposition", "attachment; filename=\""+strings.ReplaceAll(name, "\"", "")+"\"")
		header.Set("Repr-Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(mustDecodeHex(asset.SHA256))+":")
		header.Set("Cache-Control", "private, max-age=31536000, immutable")
		w.WriteHeader(http.StatusOK)
		return
	}
	if rng := r.Header.Get("Range"); rng != "" {
		size := info.Size()
		if size < 0 {
			writeRawProblem(w, op.Wrap(op.CodeInternal, errors.New("asset size is negative"), "asset inspect failed"))
			return
		}
		start, length, ok := parseAssetRange(rng, size)
		if !ok {
			w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(size, 10))
			http.Error(w, "asset: range is not satisfiable", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		setAssetHeaders(w, asset, etag, name)
		w.Header().Set("Content-Range", "bytes "+strconv.FormatInt(start, 10)+"-"+strconv.FormatInt(start+length-1, 10)+"/"+strconv.FormatInt(size, 10))
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		w.WriteHeader(http.StatusPartialContent)
		if _, serr := f.Seek(start, io.SeekStart); serr != nil {
			return
		}
		_, _ = io.CopyN(w, f, length)
		return
	}
	setAssetHeaders(w, asset, etag, name)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

func matchAssetETag(header, sum string) bool {
	for part := range strings.SplitSeq(header, ",") {
		if strings.TrimSpace(part) == assetETag(sum) {
			return true
		}
	}
	return false
}

func parseAssetRange(header string, size int64) (int64, int64, bool) {
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
	startRaw, endRaw, found := strings.Cut(spec, "-")
	if !found {
		return 0, 0, false
	}
	if startRaw == "" {
		suffix, serr := strconv.ParseInt(strings.TrimSpace(endRaw), 10, 64)
		if serr != nil || suffix <= 0 {
			return 0, 0, false
		}
		if suffix > size {
			suffix = size
		}
		return size - suffix, suffix, true
	}
	start, serr := strconv.ParseInt(strings.TrimSpace(startRaw), 10, 64)
	if serr != nil || start < 0 || start >= size {
		return 0, 0, false
	}
	if strings.TrimSpace(endRaw) == "" {
		return start, size - start, true
	}
	end, eerr := strconv.ParseInt(strings.TrimSpace(endRaw), 10, 64)
	if eerr != nil || end < start {
		return 0, 0, false
	}
	if end >= size {
		end = size - 1
	}
	return start, end - start + 1, true
}

func setAssetHeaders(w http.ResponseWriter, asset store.AssetRow, etag, name string) {
	header := w.Header()
	header.Set("Content-Type", asset.ContentType)
	header.Set("ETag", etag)
	header.Set("Accept-Ranges", "bytes")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Disposition", "attachment; filename=\""+strings.ReplaceAll(name, "\"", "")+"\"")
	header.Set("Repr-Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(mustDecodeHex(asset.SHA256))+":")
	header.Set("Cache-Control", "private, max-age=31536000, immutable")
	header.Set("Vary", "Accept")
}

func (h ReleaseBytes) serveAssetDownload(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("owner")
	repo := r.PathValue("repo")
	tag, name, ok := splitAssetRest(r.PathValue("rest"))
	if !ok {
		if h.next != nil {
			h.next.ServeHTTP(w, r)
			return
		}
		writeRawProblem(w, op.Errorf(op.CodeNotFound, "asset was not found"))
		return
	}
	if nerr := checkAssetName(name); nerr != nil {
		writeRawProblem(w, nerr)
		return
	}
	ctx := r.Context()
	rq, rerr := h.Deps.requester(ctx, h.principalFor(r))
	if rerr != nil {
		writeRawProblem(w, rerr)
		return
	}
	rc, cerr := h.Deps.resolveRepo(ctx, rq, owner, repo, false)
	if cerr != nil {
		writeRawProblem(w, cerr)
		return
	}
	row, _, lerr := h.Deps.loadReleaseForRead(ctx, rq, rc, tag)
	if lerr != nil {
		writeRawProblem(w, lerr)
		return
	}
	asset, aerr := h.Deps.findReleaseAsset(ctx, row.ID, name)
	if aerr != nil {
		writeRawProblem(w, aerr)
		return
	}
	f, ferr := h.openAssetFile(asset)
	if ferr != nil {
		writeRawProblem(w, ferr)
		return
	}
	serveAssetContent(w, r, asset, f, asset.Name)
}

func (h ReleaseBytes) serveAssetUUID(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("owner")
	repo := r.PathValue("repo")
	uuid := r.PathValue("uuid")
	if strings.TrimSpace(uuid) == "" {
		writeRawProblem(w, op.Errorf(op.CodeInvalid, "asset id must not be empty"))
		return
	}
	ctx := r.Context()
	rq, rerr := h.Deps.requester(ctx, h.principalFor(r))
	if rerr != nil {
		writeRawProblem(w, rerr)
		return
	}
	rc, cerr := h.Deps.resolveRepo(ctx, rq, owner, repo, false)
	if cerr != nil {
		writeRawProblem(w, cerr)
		return
	}
	asset, aerr := h.Deps.Store.AssetByPublicID(ctx, uuid)
	if aerr != nil {
		if errors.Is(aerr, store.ErrNotFound) {
			writeRawProblem(w, op.Errorf(op.CodeNotFound, "asset was not found"))
			return
		}
		writeRawProblem(w, op.Wrap(op.CodeInternal, aerr, "asset lookup failed"))
		return
	}
	var row store.ReleaseRow
	rerr = h.Deps.Store.Read(ctx, func(tx *store.ReadTx) error {
		var gerr error
		row, gerr = tx.GetReleaseByID(rc.row.ID, asset.ReleaseID)
		return gerr
	})
	if rerr != nil {
		if errors.Is(rerr, store.ErrNotFound) {
			writeRawProblem(w, op.Errorf(op.CodeNotFound, "asset was not found"))
			return
		}
		writeRawProblem(w, op.Wrap(op.CodeInternal, rerr, "asset lookup failed"))
		return
	}
	if _, _, lerr := h.Deps.loadReleaseForRead(ctx, rq, rc, row.TagName); lerr != nil {
		writeRawProblem(w, lerr)
		return
	}
	http.Redirect(w, r, releaseAssetJSON(rc, row.TagName, asset).URL, http.StatusSeeOther)
}

func (h ReleaseBytes) serveArchive(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("owner")
	repo := r.PathValue("repo")
	rev := r.PathValue("rev")
	format, label, gzipped, ferr := normalizeArchiveFormat(r.URL.Query().Get("format"))
	if ferr != nil {
		writeRawProblem(w, ferr)
		return
	}
	ctx := r.Context()
	rq, rerr := h.Deps.requester(ctx, h.principalFor(r))
	if rerr != nil {
		writeRawProblem(w, rerr)
		return
	}
	rc, cerr := h.Deps.resolveRepo(ctx, rq, owner, repo, false)
	if cerr != nil {
		writeRawProblem(w, cerr)
		return
	}
	if derr := h.Deps.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
		writeRawProblem(w, derr)
		return
	}
	if h.Deps.Ruk == nil {
		writeRawProblem(w, op.Errorf(op.CodeUnavailable, "ruk backend is not configured"))
		return
	}
	daemon, oerr := h.Deps.openDaemonRepo(ctx, rc.row)
	if oerr != nil {
		writeRawProblem(w, oerr)
		return
	}
	cc := rc
	cc.daemon = daemon
	resolved, rverr := h.Deps.resolveRev(ctx, cc, rev)
	if rverr != nil {
		writeRawProblem(w, rverr)
		return
	}
	etag := "\"" + resolved.commit + ":" + label + "\""
	if matchRawETag(r.Header.Get("If-None-Match"), etag) {
		header := w.Header()
		header.Set("ETag", etag)
		header.Set("Accept-Ranges", "none")
		w.WriteHeader(http.StatusNotModified)
		return
	}
	suffix := ".tar"
	if label == "tar.gz" {
		suffix = ".tar.gz"
	} else if format == ruk.ArchiveZip {
		suffix = ".zip"
	}
	short := resolved.commit
	if len(short) > 12 {
		short = short[:12]
	}
	header := w.Header()
	header.Set("Content-Type", archiveContentType(format, gzipped))
	header.Set("ETag", etag)
	header.Set("Content-Disposition", "attachment; filename=\""+repo+"-"+short+suffix+"\"")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Cache-Control", "private, max-age=31536000, immutable")
	header.Set("Vary", "Accept")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	if f, ok := w.(http.Flusher); ok {
		defer f.Flush()
	}
	out := io.Writer(w)
	if gzipped {
		gz := gzip.NewWriter(w)
		defer func() { _ = gz.Close() }()
		out = gz
	}
	args := ruk.ArchiveArgs{Format: &format, Rev: ruk.ArchiveCommitRev(resolved.commit)}
	if _, aerr := ruk.Archive(ctx, h.Deps.Ruk, daemon, args, out); aerr != nil {
		if errors.Is(aerr, context.Canceled) || errors.Is(aerr, context.DeadlineExceeded) {
			return
		}
		writeRawProblem(w, mapRukError(aerr))
		return
	}
}
