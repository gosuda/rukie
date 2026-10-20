package githttp

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"strings"

	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
)

func advertisementType(service string) string {
	return "application/x-git-" + service + "-advertisement"
}

func resultType(service string) string {
	return "application/x-git-" + service + "-result"
}

func requestType(service string) string {
	return "application/x-git-" + service + "-request"
}

func (s *Server) serveInfoRefs(w http.ResponseWriter, r *http.Request, rt route) {
	service := r.URL.Query().Get("service")
	var name string
	switch service {
	case "git-upload-pack":
		name = uploadServiceName
	case "git-receive-pack":
		name = receiveServiceName
	case "":
		writeText(w, http.StatusBadRequest, "service is required")
		return
	default:
		writeText(w, http.StatusForbidden, "service not enabled")
		return
	}
	id, ok := s.authenticate(r)
	if !ok {
		s.failAuth(w, r)
		return
	}
	if !s.checkEntryRate(w, r, id) {
		return
	}
	repoID, found := s.lookupRepo(r.Context(), rt.owner, rt.repo)
	if !found {
		if !id.authed {
			s.failAuth(w, r)
			return
		}
		writeText(w, http.StatusNotFound, "repository not found")
		return
	}
	if name == receiveServiceName {
		if !id.authed {
			s.failAuth(w, r)
			return
		}
		if decision := s.authorize(id, repoID, "repo.push", ""); decision.NotFound {
			writeText(w, http.StatusNotFound, "repository not found")
			return
		} else if !decision.Allow {
			writeText(w, http.StatusForbidden, "push access is required")
			return
		}
	} else {
		decision := s.authorize(id, repoID, "repo.clone", "")
		if decision.NotFound || !decision.Allow {
			if !id.authed {
				s.failAuth(w, r)
				return
			}
			if decision.NotFound {
				writeText(w, http.StatusNotFound, "repository not found")
				return
			}
			writeText(w, http.StatusForbidden, "read access is required")
			return
		}
	}
	if !s.tokenReposAllow(id, repoID) {
		writeText(w, http.StatusForbidden, "token is not granted this repository")
		return
	}
	path := s.storePath(rt)
	if _, err := os.Stat(path); err != nil {
		writeText(w, http.StatusNotFound, "repository not found")
		return
	}
	budget := defaultUploadCap
	if name == receiveServiceName {
		budget = defaultReceiveCap
	}
	ctx, stop := context.WithTimeout(r.Context(), budget)
	defer stop()
	client, err := s.clientFor(ctx)
	if err != nil {
		writeUnavailable(w)
		return
	}
	open := wire.ServiceOpen{
		Service:     name,
		Repository:  path,
		GitProtocol: r.Header.Get("Git-Protocol"),
		Advertise:   true,
		Limits:      s.limits,
	}
	if name == receiveServiceName {
		session, request, ok := s.openPushSession(ctx, client, path, w)
		if !ok {
			return
		}
		open.SessionID = &session
		open.RequestID = &request
	}
	v2 := strings.Contains(open.GitProtocol, "version=2")
	w.Header().Set("Content-Type", advertisementType(name))
	w.Header().Set("Cache-Control", "no-cache")
	out := &flushWriter{w: w}
	if !v2 {
		line := "# service=git-" + name + "\n"
		if _, ferr := fmt.Fprintf(out, "%04x%s0000", len(line)+4, line); ferr != nil {
			return
		}
	}
	report, _, err := ruk.RunCommand(ctx, client, open, nil, out, nil)
	if err != nil {
		out.fail(mapCommandError(err))
		return
	}
	if report.ExitCode != 0 || report.Cancelled {
		out.fail(http.StatusBadGateway, "advertisement failed")
		return
	}
}

func (s *Server) serveRound(w http.ResponseWriter, r *http.Request, rt route, name string) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != requestType(name) {
		writeText(w, http.StatusBadRequest, "unexpected content type")
		return
	}
	id, ok := s.authenticate(r)
	if !ok {
		s.failAuth(w, r)
		return
	}
	if !s.checkEntryRate(w, r, id) {
		return
	}
	repoID, found := s.lookupRepo(r.Context(), rt.owner, rt.repo)
	if !found {
		if !id.authed {
			s.failAuth(w, r)
			return
		}
		writeText(w, http.StatusNotFound, "repository not found")
		return
	}
	if name == receiveServiceName {
		if !id.authed {
			s.failAuth(w, r)
			return
		}
	}
	decision := s.authorize(id, repoID, "repo.clone", "")
	if decision.NotFound || !decision.Allow {
		if !id.authed {
			s.failAuth(w, r)
			return
		}
		if decision.NotFound {
			writeText(w, http.StatusNotFound, "repository not found")
			return
		}
		if name == receiveServiceName {
			writeText(w, http.StatusForbidden, "push access is required")
			return
		}
		writeText(w, http.StatusForbidden, "read access is required")
		return
	}
	if !s.tokenReposAllow(id, repoID) {
		writeText(w, http.StatusForbidden, "token is not granted this repository")
		return
	}
	path := s.storePath(rt)
	if _, serr := os.Stat(path); serr != nil {
		writeText(w, http.StatusNotFound, "repository not found")
		return
	}
	maxStdin := s.limits.MaxStdinBytes
	if r.ContentLength >= 0 && uint64(r.ContentLength) > maxStdin {
		writeText(w, http.StatusRequestEntityTooLarge, "request body is too large")
		return
	}
	var body io.Reader = http.MaxBytesReader(w, r.Body, maxBodyBytes(maxStdin))
	if strings.Contains(r.Header.Get("Content-Encoding"), "gzip") {
		gz, gerr := gzip.NewReader(body)
		if gerr != nil {
			writeText(w, http.StatusBadRequest, "invalid gzip body")
			return
		}
		defer func() { _ = gz.Close() }()
		body = gz
	}
	counted := &countReader{reader: body, limit: maxStdin + 1}
	budget := defaultUploadCap
	if name == receiveServiceName {
		budget = defaultReceiveCap
	}
	ctx, stop := context.WithTimeout(r.Context(), budget)
	defer stop()
	client, err := s.clientFor(ctx)
	if err != nil {
		writeUnavailable(w)
		return
	}
	open := wire.ServiceOpen{
		Service:      name,
		Repository:   path,
		GitProtocol:  r.Header.Get("Git-Protocol"),
		StdoutCredit: 1 << 20,
		Limits:       s.limits,
	}
	var handle ruk.HookHandler
	if name == receiveServiceName {
		session, request, ok := s.openPushSession(ctx, client, path, w)
		if !ok {
			return
		}
		open.SessionID = &session
		open.RequestID = &request
		handle = s.hookHandler(id.caller, repoID)
	}
	w.Header().Set("Content-Type", resultType(name))
	w.Header().Set("Cache-Control", "no-cache")
	out := &flushWriter{w: w}
	report, stderr, err := ruk.RunCommand(ctx, client, open, counted, out, handle)
	if counted.overflow {
		out.fail(http.StatusRequestEntityTooLarge, "request body is too large")
		return
	}
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		out.fail(http.StatusRequestEntityTooLarge, "request body is too large")
		return
	}
	if err != nil {
		status, msg := mapCommandError(err)
		if len(stderr) > 0 && status == http.StatusInternalServerError {
			msg = firstLine(stderr)
		}
		out.fail(status, msg)
		return
	}
	if report.ExitCode != 0 || report.Cancelled {
		out.fail(http.StatusBadGateway, "git service failed")
		return
	}
}

func (s *Server) openPushSession(ctx context.Context, client *ruk.Client, path string, w http.ResponseWriter) (string, string, bool) {
	opened, err := ruk.SessionOpen(ctx, client, ruk.Repo{Path: path}, wire.SessionOpenArgs{})
	if err != nil {
		writeText(w, http.StatusBadGateway, "session failed")
		return "", "", false
	}
	return opened.Session, s.newRequestID(), true
}

func mapCommandError(err error) (int, string) {
	if rerr, ok := errors.AsType[*ruk.Error](err); ok {
		switch rerr.Code {
		case wire.CodeNotFound:
			return http.StatusNotFound, "repository not found"
		case wire.CodeForbidden:
			return http.StatusForbidden, "access is required"
		case wire.CodeLimit:
			return http.StatusRequestEntityTooLarge, "request body is too large"
		case wire.CodeInvalid:
			return http.StatusBadRequest, "invalid git service"
		case wire.CodeStale:
			return http.StatusPreconditionFailed, "stale: " + firstLine([]byte(rerr.Message))
		default:
			return http.StatusInternalServerError, "git service failed: " + firstLine([]byte(rerr.Message))
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout, "git service timed out"
	}
	return http.StatusBadGateway, "git service failed"
}

func maxBodyBytes(maxStdin uint64) int64 {
	if maxStdin > uint64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(maxStdin)
}

func writeUnavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "5")
	writeText(w, http.StatusServiceUnavailable, "daemon is unavailable")
}

func firstLine(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if line, _, ok := strings.Cut(text, "\n"); ok {
		return line
	}
	if text == "" {
		return "git service failed"
	}
	return text
}

type flushWriter struct {
	w    http.ResponseWriter
	sent bool
}

func (f *flushWriter) Write(p []byte) (int, error) {
	if !f.sent {
		f.sent = true
		f.w.WriteHeader(http.StatusOK)
	}
	n, err := f.w.Write(p)
	if flusher, ok := f.w.(http.Flusher); ok {
		flusher.Flush()
	}
	return n, err
}

func (f *flushWriter) fail(status int, msg string) {
	if f.sent {
		return
	}
	f.sent = true
	writeText(f.w, status, msg)
}

type countReader struct {
	reader   io.Reader
	limit    uint64
	total    uint64
	overflow bool
}

func (c *countReader) Read(p []byte) (int, error) {
	if c.overflow {
		return 0, errTooLarge
	}
	allowed := c.limit - 1 - c.total
	if allowed == 0 {
		c.overflow = true
		return 0, errTooLarge
	}
	if uint64(len(p)) > allowed {
		p = p[:allowed]
	}
	n, err := c.reader.Read(p)
	if n < 0 {
		return 0, fmt.Errorf("githttp: negative read")
	}
	c.total += uint64(n)
	if c.total >= c.limit {
		c.overflow = true
		return n, errTooLarge
	}
	return n, err
}

var errTooLarge = errTooLargeValue{}

type errTooLargeValue struct{}

func (errTooLargeValue) Error() string {
	return "request body exceeds its bound"
}
