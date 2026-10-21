//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/githttp"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

type trackFake struct {
	t        *testing.T
	opens    chan uint64
	replies  chan wire.HookReply
	streams  chan struct{}
	closes   chan struct{}
	hook     *wire.HookCall
	delay    time.Duration
	exitCode int32
}

func writeRawFrame(t *testing.T, conn net.Conn, kind byte, stream uint64, payload []byte) {
	t.Helper()
	var hdr [13]byte
	binary.BigEndian.PutUint32(hdr[0:4], uint32(9+len(payload)))
	hdr[4] = kind
	binary.BigEndian.PutUint64(hdr[5:13], stream)
	if _, err := conn.Write(hdr[:]); err != nil {
		return
	}
	if len(payload) > 0 {
		_, _ = conn.Write(payload)
	}
}

func (f *trackFake) handshake(conn net.Conn) error {
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	kind, stream, _ := readFrameSoft(conn)
	if kind != 1 || stream != 0 {
		return fmt.Errorf("handshake = kind %d stream %d", kind, stream)
	}
	accept := `{"profile":"rukie","object_formats":["sha1"],"features":["credits","hooks.v1","events.v1","archive.v1"],"protocol_major":1,"protocol_minor":1}`
	var hdr [13]byte
	binary.BigEndian.PutUint32(hdr[0:4], uint32(9+len(accept)))
	hdr[4] = 2
	binary.BigEndian.PutUint64(hdr[5:13], 0)
	if _, err := conn.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := conn.Write([]byte(accept)); err != nil {
		return err
	}
	return conn.SetDeadline(time.Time{})
}

func (f *trackFake) serve(t *testing.T, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	if err := f.handshake(conn); err != nil {
		return
	}
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return
	}
	for {
		kind, stream, payload := readFrameSoft(conn)
		if kind == 0 {
			return
		}
		if kind != 1 {
			continue
		}
		if bytes.Contains(payload, []byte(`"method"`)) {
			f.serveTyped(t, conn, stream, payload)
			continue
		}
		select {
		case f.opens <- stream:
		default:
		}
		if f.delay > 0 {
			time.Sleep(f.delay)
		}
		inv := fmt.Sprintf("inv-%d", stream)
		accepted := fmt.Sprintf(`{"invocation_id":%q,"stream_id":%d,"stdin_credit":1048576}`, inv, stream)
		writeRawFrame(t, conn, 2, stream, []byte(accepted))
		if bytes.Contains(payload, []byte(`"advertise":true`)) {
			writeRawFrame(t, conn, 10, stream, []byte(`{"exit_code":0,"cancelled":false,"signal":"","outcome":"","invocation_id":"inv","operation_ids":[]}`))
			continue
		}
		f.drainRound(t, conn, stream)
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		_, _, _ = readFrameSoft(conn)
		select {
		case f.closes <- struct{}{}:
		default:
		}
	}
}

func (f *trackFake) serveTyped(t *testing.T, conn net.Conn, stream uint64, payload []byte) {
	var open struct {
		Method string `json:"method"`
	}
	if err := jsonv2.Unmarshal(payload, &open, jsonv1.DefaultOptionsV1()); err != nil {
		return
	}
	accepted := fmt.Sprintf(`{"method":%q,"repo_handle":"h","stream_id":%d}`, open.Method, stream)
	writeRawFrame(t, conn, 2, stream, []byte(accepted))
	body := fmt.Sprintf(`{%q:{"session":"sess-1","reconnect_secret":"sec-1","principal":"u"}}`, open.Method)
	writeRawFrame(t, conn, 5, stream, []byte(body))
	writeRawFrame(t, conn, 10, stream, []byte(`{"code":"","outcome":"completed","completed_at":0,"status":0}`))
}

func readFrameSoft(conn net.Conn) (byte, uint64, []byte) {
	var hdr [13]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return 0, 0, nil
	}
	total := binary.BigEndian.Uint32(hdr[0:4])
	kind := hdr[4]
	stream := binary.BigEndian.Uint64(hdr[5:13])
	payload := make([]byte, int(total)-9)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return 0, 0, nil
	}
	return kind, stream, payload
}

func (f *trackFake) drainRound(t *testing.T, conn net.Conn, stream uint64) {
	_ = t
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	for {
		kind, _, _ := readFrameSoft(conn)
		if kind == 0 {
			return
		}
		if kind == 4 {
			break
		}
	}
	if f.hook != nil {
		raw, err := jsonv2.Marshal(f.hook)
		if err != nil {
			return
		}
		writeRawFrame(t, conn, 12, stream, raw)
		_, _, payload := readFrameSoft(conn)
		if payload == nil {
			return
		}
		var reply wire.HookReply
		if err := jsonv2.Unmarshal(payload, &reply, jsonv1.DefaultOptionsV1()); err != nil {
			return
		}
		select {
		case f.replies <- reply:
		default:
		}
	}
	exit := fmt.Sprintf(`{"exit_code":%d,"cancelled":false,"signal":"","outcome":"","invocation_id":"inv","operation_ids":[]}`, f.exitCode)
	writeRawFrame(t, conn, 10, stream, []byte(exit))
	select {
	case f.streams <- struct{}{}:
	default:
	}
}

type denyHarness struct {
	t       *testing.T
	handler http.Handler
	server  *httptest.Server
	url     string
	fake    *trackFake
	svc     *auth.Service
	db      *sql.DB
	ada     userRec
	repoID  int64
}

func newDenyHarness(t *testing.T, hook *wire.HookCall, delay time.Duration, visibility string) *denyHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	dir := t.TempDir()
	roots := filepath.Join(dir, "roots")
	if err := os.MkdirAll(filepath.Join(roots, "ada", "hello.git", ".ruk"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sqlite := filepath.Join(dir, "rukie.sqlite")
	st, err := store.Open(ctx, store.Options{Path: sqlite})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	svc, err := auth.Open(sqlite)
	if err != nil {
		t.Fatalf("auth open: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	admin, err := svc.BootstrapAdmin(ctx, "ada", "long-test-password-01", "127.0.0.1")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	parsed, err := auth.ParseScopeSet([]string{"repo:read", "repo:write"})
	if err != nil {
		t.Fatalf("scopes: %v", err)
	}
	secret, err := svc.CreateToken(ctx, auth.CreateTokenInput{Name: "e2e", Scopes: parsed, UserID: admin.ID})
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	ada := userRec{user: admin, token: secret.Secret}
	now := time.Now().UnixMilli()
	var repoID int64
	row := svc.DB().QueryRowContext(ctx, `INSERT INTO repos (owner_user_id, org_id, name, visibility, created_at, updated_at) VALUES (?, 0, ?, ?, ?, ?) RETURNING id`,
		admin.ID, "hello", visibility, now, now)
	if serr := row.Scan(&repoID); serr != nil {
		t.Fatalf("insert repo: %v", serr)
	}
	socket := filepath.Join(dir, "count.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	fake := &trackFake{
		t:       t,
		opens:   make(chan uint64, 64),
		replies: make(chan wire.HookReply, 16),
		streams: make(chan struct{}, 64),
		closes:  make(chan struct{}, 64),
		hook:    hook,
		delay:   delay,
	}
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go fake.serve(t, conn)
		}
	}()
	dialCtx, dialCancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer dialCancel()
	client, err := ruk.Dial(dialCtx, socket)
	if err != nil {
		t.Fatalf("dial fake daemon: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	git, err := githttp.New(githttp.Config{
		Auth:     svc,
		Limiter:  access.NewLimiter(),
		Client:   client,
		RepoRoot: roots,
		Limits:   wire.DefaultGitLimits(),
	})
	if err != nil {
		t.Fatalf("githttp: %v", err)
	}
	server := httptest.NewServer(git)
	t.Cleanup(server.Close)
	return &denyHarness{t: t, handler: git, server: server, url: server.URL, fake: fake, svc: svc, db: svc.DB(), ada: ada, repoID: repoID}
}

func (h *denyHarness) addUser(t *testing.T, name string, scopes ...string) userRec {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	var adminID int64
	if err := h.db.QueryRowContext(ctx, `SELECT id FROM users LIMIT 1`).Scan(&adminID); err != nil {
		t.Fatalf("admin: %v", err)
	}
	user, err := h.svc.CreateUser(ctx, adminID, auth.CreateUserInput{
		Name: name, Email: name + "@example.test", Password: "long-test-password-01",
	}, "127.0.0.1")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	parsed, err := auth.ParseScopeSet(scopes)
	if err != nil {
		t.Fatalf("scopes: %v", err)
	}
	secret, err := h.svc.CreateToken(ctx, auth.CreateTokenInput{Name: "e2e", Scopes: parsed, UserID: user.ID})
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	return userRec{user: user, token: secret.Secret}
}

func (h *denyHarness) grant(t *testing.T, userID, repoID int64, role string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if _, err := h.db.ExecContext(ctx, `INSERT INTO repo_grants (repo_id, user_id, role) VALUES (?, ?, ?)`, repoID, userID, role); err != nil {
		t.Fatalf("grant: %v", err)
	}
}

func (h *denyHarness) opens() int {
	return len(h.fake.opens)
}

func (h *denyHarness) lsRemote(t *testing.T, repoURL string) map[string]string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	out, err := gitRun(ctx, t, t.TempDir(), "ls-remote", repoURL)
	if err != nil {
		t.Fatalf("ls-remote: %v\n%s", err, out)
	}
	refs := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		oid, name, _ := strings.Cut(line, "\t")
		refs[name] = oid
	}
	return refs
}

func (h *denyHarness) repoURL(username, token string) string {
	plain, _ := strings.CutPrefix(h.url, "http://")
	creds := ""
	if username != "" {
		creds = username + ":" + token + "@"
	}
	return "http://" + creds + plain + "/ada/hello.git"
}

func TestGitDenialsOpenNoStream(t *testing.T) {
	h := newDenyHarness(t, nil, 0, "private")
	stranger := h.addUser(t, "mallory", "repo:read", "repo:write")
	excluded := h.addUser(t, "trent", "repo:read", "repo:write")
	h.grant(t, excluded.user.ID, h.repoID, "read")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var hash string
	if err := h.db.QueryRowContext(ctx, `SELECT token_hash FROM scoped_tokens WHERE user_id = ? AND name = 'e2e'`, excluded.user.ID).Scan(&hash); err != nil {
		t.Fatalf("token hash: %v", err)
	}
	if _, err := h.db.ExecContext(ctx, `INSERT INTO token_repos (token_hash, repo_id) VALUES (?, ?)`, hash, h.repoID+99999); err != nil {
		t.Fatalf("token repos: %v", err)
	}
	infoURL := h.url + "/ada/hello.git/info/refs?service=git-upload-pack"
	if status, _, _ := httpGet(t, infoURL, "", ""); status != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", status)
	}
	if status, _, _ := httpGet(t, infoURL, "mallory", stranger.token); status != http.StatusNotFound {
		t.Fatalf("stranger status = %d, want 404", status)
	}
	if status, _, _ := httpGet(t, infoURL, "trent", excluded.token); status != http.StatusForbidden {
		t.Fatalf("excluded status = %d, want 403", status)
	}
	if n := h.opens(); n != 0 {
		t.Fatalf("denied requests opened %d daemon streams, want 0", n)
	}
}

func TestGitRefsForPostOpensOneStream(t *testing.T) {
	hook := &wire.HookCall{
		Phase:      wire.HookPreReceive,
		Commands:   []wire.HookCommand{{Name: "refs/for/main", Old: strings.Repeat("0", 40), New: strings.Repeat("1", 40), Authorized: true}},
		DeadlineMs: 5000,
	}
	h := newDenyHarness(t, hook, 0, "private")
	mallory := h.addUser(t, "mallory", "repo:read")
	h.grant(t, mallory.user.ID, h.repoID, "read")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url+"/ada/hello.git/git-receive-pack", strings.NewReader("0000"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.SetBasicAuth("mallory", mallory.token)
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if n := h.opens(); n != 1 {
		t.Fatalf("refs/for POST opened %d daemon streams, want exactly 1", n)
	}
	reply, ok := waitFor(t, h.fake.replies, 10*time.Second)
	if !ok {
		t.Fatal("hook reply never arrived")
	}
	if len(reply.Verdicts) != 1 {
		t.Fatalf("hook verdicts = %+v, want one", reply.Verdicts)
	}
	if reply.Verdicts[0].Verdict.Kind != wire.HookReject {
		t.Fatalf("refs/for verdict = %q, want reject", reply.Verdicts[0].Verdict.Kind)
	}
	if !strings.Contains(reply.Verdicts[0].Verdict.Reason, "change review is not available") {
		t.Fatalf("refs/for reason = %q, want the pinned rejection", reply.Verdicts[0].Verdict.Reason)
	}
}

func TestGitClientDisconnectCancelsStream(t *testing.T) {
	h := newDenyHarness(t, nil, 0, "private")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	pr, pw := io.Pipe()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url+"/ada/hello.git/git-receive-pack", pr)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.ContentLength = -1
	req.SetBasicAuth("ada", h.ada.token)
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	done := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- err
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		done <- nil
	}()
	if _, err := pw.Write([]byte("0000")); err != nil {
		t.Fatalf("write: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for h.opens() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if h.opens() == 0 {
		t.Fatal("daemon never opened the push stream")
	}
	time.Sleep(200 * time.Millisecond)
	cancel()
// refused_by: merge_ladder_exhausted · collision: none (1 contested hunk(s), neither side deleted the other's lines)
	derr, ok := waitFor(t, done, 15*time.Second)
	if !ok {
		t.Fatal("disconnected POST never returned")
	}
	if derr != nil {
		t.Logf("disconnected POST ended: %v", derr)
	}

	_ = pw.Close()
	if _, ok := waitFor(t, h.fake.closes, 10*time.Second); !ok {
		t.Fatal("daemon stream never closed after client disconnect")
	}
}

func waitFor[T any](t *testing.T, ch <-chan T, timeout time.Duration) (T, bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !time.Now().After(deadline) {
		select {
		case got := <-ch:
			return got, true
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	var zero T
	return zero, false
}

func serveWithTimeouts(t *testing.T, h http.Handler) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{ReadTimeout: time.Second, WriteTimeout: time.Second, Handler: h}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "http://" + ln.Addr().String(), func() { _ = srv.Close() }
}

func TestGitServerTimeoutsDoNotCutStreams(t *testing.T) {
	h := newDenyHarness(t, nil, 0, "private")
	url, _ := serveWithTimeouts(t, h.handler)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	pr, pw := io.Pipe()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/ada/hello.git/git-receive-pack", pr)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.ContentLength = 256
	req.SetBasicAuth("ada", h.ada.token)
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	done := make(chan *http.Response, 1)
	failed := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			failed <- err
			return
		}
		done <- resp
	}()
	chunk := bytes.Repeat([]byte("a"), 16)
	for range 16 {
		time.Sleep(150 * time.Millisecond)
		if _, err := pw.Write(chunk); err != nil {
			t.Fatalf("paced write: %v", err)
		}
	}
	_ = pw.Close()
	select {
	case err := <-failed:
		t.Fatalf("paced POST died under the 1 s server timeouts: %v", err)
	case resp := <-done:
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode == 0 {
			t.Fatal("paced POST got no status")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("paced POST never returned")
	}
	select {
	case <-h.fake.streams:
	case <-time.After(10 * time.Second):
		t.Fatal("daemon never finished the paced round")
	}
}

func TestGitSlowAdvertisementSurvivesWriteTimeout(t *testing.T) {
	h := newDenyHarness(t, nil, 1500*time.Millisecond, "public")
	url, _ := serveWithTimeouts(t, h.handler)
	status, _, _ := httpGet(t, url+"/ada/hello.git/info/refs?service=git-upload-pack", "", "")
	if status != http.StatusOK {
		t.Fatalf("slow advertisement status = %d, want 200 past the 1 s write timeout", status)
	}
}
