package cli_test

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
)

func seedGCAdmin(t *testing.T, dbPath, name string) int64 {
	t.Helper()
	st, openErr := store.Open(t.Context(), store.Options{Path: dbPath})
	if openErr != nil {
		t.Fatalf("open: %v", openErr)
	}
	defer func() { _ = st.Close(t.Context()) }()
	svc := auth.New(st)
	if _, bootErr := svc.BootstrapAdmin(t.Context(), name, testPassword(t), "127.0.0.1"); bootErr != nil {
		t.Fatalf("bootstrap: %v", bootErr)
	}
	meta, lookupErr := st.LookupUserByName(t.Context(), name)
	if lookupErr != nil {
		t.Fatalf("lookup: %v", lookupErr)
	}
	return meta.ID
}

func writeGCConfig(t *testing.T, dir, dbPath string) string {
	t.Helper()
	cfg := "data_dir = \"" + dir + "\"\nsecret_key_file = \"" + filepath.Join(dir, "secret.key") + "\"\n" +
		"[store.sqlite]\npath = \"" + dbPath + "\"\n"
	path := filepath.Join(dir, "rukie.toml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func seedTombstonedRepo(t *testing.T, dbPath string, ownerID int64, name string, horizon time.Duration) {
	t.Helper()
	st, err := store.Open(t.Context(), store.Options{Path: dbPath})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close(t.Context()) }()
	now := time.Now()
	var id int64
	if err := st.Write(t.Context(), func(tx *store.WriteTx) error {
		var werr error
		id, werr = tx.InsertRepo(now, store.RepoInsert{
			Name: name, Visibility: "public", DefaultBranch: "main",
			OwnerUserID: sql.NullInt64{Int64: ownerID, Valid: true},
		})
		return werr
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := st.Write(t.Context(), func(tx *store.WriteTx) error {
		return tx.SoftDeleteRepo(id, now, now.Add(horizon))
	}); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
}

func countLiveRepos(t *testing.T, dbPath string) int {
	t.Helper()
	st, err := store.Open(t.Context(), store.Options{Path: dbPath})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close(t.Context()) }()
	var n int
	if err := st.Read(t.Context(), func(tx *store.ReadTx) error {
		rows, _, lerr := tx.ListRepos(store.RepoListFilter{Limit: 1000})
		if lerr != nil {
			return lerr
		}
		n = len(rows)
		return nil
	}); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// seedGCAdminToken mints an admin:write token for the gc store admin and
// writes the secret to a token file.
func seedGCAdminToken(t *testing.T, dbPath string) string {
	t.Helper()
	st, err := store.Open(t.Context(), store.Options{Path: dbPath})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close(t.Context()) }()
	svc := auth.New(st)
	minted, err := svc.CreateToken(t.Context(), auth.CreateTokenInput{
		UserID: 1, Name: "gc", Scopes: op.Scopes(0).Add(op.ScopeAdminWrite),
	})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	path := filepath.Join(t.TempDir(), "gc-token")
	if err := os.WriteFile(path, []byte(minted.Secret+"\n"), 0o600); err != nil {
		t.Fatalf("token file: %v", err)
	}
	return path
}

// TestAdminGCDryRunKeepsRows pins the gc contract: collection needs an admin
// credential, a dry run reports due purges without writing, and a confirmed
// live run hard-deletes the tombstoned row.
func TestAdminGCDryRunKeepsRows(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "gc.sqlite")
	cfgPath := writeGCConfig(t, dir, dbPath)
	ownerID := seedGCAdmin(t, dbPath, "gcadmin")
	tokenFile := seedGCAdminToken(t, dbPath)
	seedTombstonedRepo(t, dbPath, ownerID, "gone", -time.Hour)

	anon := newIO()
	if code := runCLI(t, []string{"admin", "gc", "--config", cfgPath, "--dry-run"}, anon); code != 3 {
		t.Fatalf("anonymous gc exit %d, want 3", code)
	}
	unconfirmed := newIO()
	if code := runCLI(t, []string{"admin", "gc", "--config", cfgPath, "--token-file", tokenFile}, unconfirmed); code != 64 {
		t.Fatalf("unconfirmed gc exit %d, want 64", code)
	}
	dry := newIO()
	if code := runCLI(t, []string{"admin", "gc", "--config", cfgPath, "--dry-run", "--token-file", tokenFile}, dry); code != 0 {
		t.Fatalf("dry exit %d: %s", code, dry.stderr.String())
	}
	if !strings.Contains(dry.stdout.String(), "gone") {
		t.Fatalf("dry output names no repo: %q", dry.stdout.String())
	}
	if n := countLiveRepos(t, dbPath); n != 0 {
		t.Fatalf("live repos = %d, want 0 (tombstoned rows stay hidden)", n)
	}
	live := newIO()
	if code := runCLI(t, []string{"admin", "gc", "--config", cfgPath, "--confirm", "--token-file", tokenFile}, live); code != 0 {
		t.Fatalf("gc exit %d: %s", code, live.stderr.String())
	}
	if !strings.Contains(live.stdout.String(), "repos purged: 1") {
		t.Fatalf("gc output = %q, want one purged repo", live.stdout.String())
	}
	scoped := newIO()
	seedTombstonedRepo(t, dbPath, ownerID, "kept", -time.Hour)
	if code := runCLI(t, []string{"admin", "gc", "--config", cfgPath, "--repo", "nobody/else", "--confirm", "--token-file", tokenFile}, scoped); code != 0 {
		t.Fatalf("scoped exit %d: %s", code, scoped.stderr.String())
	}
	if !strings.Contains(scoped.stdout.String(), "repos purged: 0") {
		t.Fatalf("scoped output = %q, want zero purges", scoped.stdout.String())
	}
}

type importStub struct {
	calls  []string
	bodies []string
	mu     sync.Mutex
	purge  bool
}

func (s *importStub) record(call, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
	s.bodies = append(s.bodies, body)
}

func (s *importStub) saw(call string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if c == call {
			return true
		}
	}
	return false
}

func (s *importStub) sawBody(fragment string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.bodies {
		if strings.Contains(b, fragment) {
			return true
		}
	}
	return false
}

func newImportStub(t *testing.T) (*importStub, string) {
	t.Helper()
	stub := &importStub{}
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/users/o/tokens", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		stub.record("mint", string(raw))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"prefix":"oneshot12","secret":"oneshot-secret"}`))
	})
	mux.HandleFunc("/admin/users/o/tokens/revoke", func(w http.ResponseWriter, _ *http.Request) {
		stub.record("revoke", "")
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/new/repo", func(w http.ResponseWriter, _ *http.Request) {
		stub.record("create", "")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"repo":{"default_branch":"main"}}`))
	})
	mux.HandleFunc("/admin/repos", func(w http.ResponseWriter, _ *http.Request) {
		stub.record("list", "")
		_, _ = w.Write([]byte(`{"repos":[{"id":7,"full_name":"o/r","updated_at":1700000000000}]}`))
	})
	mux.HandleFunc("/admin/repos/7/purge", func(w http.ResponseWriter, _ *http.Request) {
		stub.record("purge", "")
		stub.purge = true
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"repo":"o/r","job":{"id":"repo-purge:7","state":"done"}}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return stub, server.URL
}

func gitPresent() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// TestAdminRepoImportRollback pins ENG-50: a failed push deletes the created
// repository again. The stub records every step; the push target refuses the
// connection so the rollback path runs deterministically.
func TestAdminRepoImportRollback(t *testing.T) {
	if !gitPresent() {
		t.Skip("git is not installed")
	}
	stub, base := newImportStub(t)
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("operator-token\n"), 0o600); err != nil {
		t.Fatalf("token: %v", err)
	}
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(source, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	init := exec.Command("git", "init", "-b", "main", source)
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, args := range [][]string{{"-C", source, "add", "README.md"}, {"-C", source, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	c := newIO()
	argv := []string{"admin", "repo", "import", "--server", base, "--token-file", tokenPath,
		"--from-git", source, "--owner", "o", "--name", "r"}
	if code := runCLI(t, argv, c); code != 1 {
		t.Fatalf("exit %d, want 1:\nstdout: %s\nstderr: %s", code, c.stdout.String(), c.stderr.String())
	}
	for _, want := range []string{"mint", "create", "list", "purge", "revoke"} {
		if !stub.saw(want) {
			t.Fatalf("rollback skipped %q (calls: %v)", want, stub.calls)
		}
	}
	if !stub.purge {
		t.Fatalf("purge was not called (calls: %v)", stub.calls)
	}
	if !stub.sawBody(`"scopes":"repo:create"`) {
		t.Fatalf("create token is not create-scoped (bodies: %v)", stub.bodies)
	}
	if !stub.sawBody(`"repos":[7]`) {
		t.Fatalf("push token is not scoped to the new repo (bodies: %v)", stub.bodies)
	}
	if stub.sawBody("admin:write") {
		t.Fatalf("one-shot token carries admin:write (bodies: %v)", stub.bodies)
	}
	if strings.Contains(c.stderr.String(), "oneshot-secret") {
		t.Fatalf("secret leaked into diagnostics: %s", c.stderr.String())
	}
}

func TestAdminRepoImportNoServer(t *testing.T) {
	c := newIO()
	argv := []string{"admin", "repo", "import", "--server", "http://127.0.0.1:9",
		"--token", "x", "--from-git", "/nonexistent", "--owner", "o", "--name", "r"}
	if code := runCLI(t, argv, c); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
}

func TestAdminRepoImportUsage(t *testing.T) {
	c := newIO()
	if code := runCLI(t, []string{"admin", "repo", "import", "--server", "http://x"}, c); code != 64 {
		t.Fatalf("exit %d, want 64", code)
	}
	gc := newIO()
	if code := runCLI(t, []string{"admin", "gc", "--config", filepath.Join(t.TempDir(), "missing.toml")}, gc); code != 78 {
		t.Fatalf("gc exit %d, want 78", code)
	}
}
