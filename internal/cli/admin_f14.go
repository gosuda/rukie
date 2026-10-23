package cli

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/config"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
	"github.com/gosuda/rukie/internal/sysops"
)

// TryAdminGC runs admin gc with process stdio for the binary front end.
func TryAdminGC(argv []string) int {
	return runAdminGC(context.Background(), IO{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv}, argv)
}

// TryAdminRepoImport runs admin repo import with process stdio.
func TryAdminRepoImport(argv []string) int {
	return runAdminRepoImport(context.Background(), IO{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv}, argv)
}

// TryAdminHandwritten runs the hand-written admin gc and repo import
// commands. It reports whether argv matched, so both the binary front end
// and Run share one dispatch.
func TryAdminHandwritten(ctx context.Context, std IO, argv []string) (int, bool) {
	if len(argv) == 0 {
		return 0, false
	}
	switch argv[0] {
	case "gc":
		return runAdminGC(ctx, std, argv[1:]), true
	case "repo":
		if len(argv) > 1 && argv[1] == "import" {
			return runAdminRepoImport(ctx, std, argv[2:]), true
		}
		return 0, false
	default:
		return 0, false
	}
}

func adminConfigPath(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv("RUKIE_CONFIG"); env != "" {
		return env
	}
	return "/etc/rukie/config.toml"
}

func adminServerToken(std IO, tokenFlag, tokenFileFlag string) (string, int) {
	token := strings.TrimSpace(cmp.Or(tokenFlag, std.getenv("RUKIE_TOKEN")))
	if token != "" {
		return token, 0
	}
	path := strings.TrimSpace(cmp.Or(tokenFileFlag, std.getenv("RUKIE_TOKEN_FILE")))
	if path == "" {
		return "", 0
	}
	raw, err := readSecretFile(path)
	if err != nil {
		emit(std.Stderr, "rukie admin:", err.Error())
		return "", 64
	}
	return raw, 0
}

// adminLocalOperator resolves the invoking site admin for offline hand-written
// commands, mirroring the local-dispatch credential rule: a bearer token or a
// session is required, it must authenticate, and its account must be an
// active site admin.
func adminLocalOperator(ctx context.Context, std IO, svc *auth.Service, tokenFlag, tokenFileFlag, sessionFileFlag string) (auth.User, int) {
	token, code := adminServerToken(std, tokenFlag, tokenFileFlag)
	if code != 0 {
		return auth.User{}, code
	}
	auther := auth.NewAuthenticator(svc)
	if token != "" {
		p, err := auther.Bearer(ctx, token)
		if err != nil {
			emit(std.Stderr, "rukie admin: invalid token")
			return auth.User{}, 3
		}
		return adminOperatorUser(ctx, std, svc, p)
	}
	sessionFile := strings.TrimSpace(cmp.Or(sessionFileFlag, std.getenv("RUKIE_SESSION_FILE")))
	if sessionFile == "" {
		sessionFile = DefaultSessionFile(std.Getenv)
	}
	raw, err := readSecretFile(sessionFile)
	if err != nil {
		emit(std.Stderr, "rukie admin: --token-file or --session-file is required")
		return auth.User{}, 3
	}
	secret := strings.TrimSpace(raw)
	if _, value, ok := strings.Cut(secret, "="); ok {
		secret = strings.TrimSpace(value)
	}
	p, err := auther.Session(ctx, secret)
	if err != nil {
		emit(std.Stderr, "rukie admin: invalid session")
		return auth.User{}, 3
	}
	return adminOperatorUser(ctx, std, svc, p)
}

func adminOperatorUser(ctx context.Context, std IO, svc *auth.Service, p op.Principal) (auth.User, int) {
	u, err := svc.GetUserByID(ctx, p.UserID)
	if err != nil || !u.IsAdmin || !u.IsActive {
		emit(std.Stderr, "rukie admin: site admin required")
		return auth.User{}, 3
	}
	return u, 0
}

func runAdminGC(ctx context.Context, std IO, argv []string) int {
	fs := flag.NewFlagSet("admin gc", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "config file path")
	dryRun := fs.Bool("dry-run", false, "list what would be collected without writing")
	confirm := fs.Bool("confirm", false, "acknowledge the destructive collection pass")
	repo := fs.String("repo", "", "limit collection to one owner/name repository")
	tokenFlag := fs.String("token", "", "admin bearer token (prefer --token-file)")
	tokenFileFlag := fs.String("token-file", "", "file holding the admin bearer token")
	sessionFileFlag := fs.String("session-file", "", "file holding the admin session cookie")
	asJSON := fs.Bool("json", false, "machine output")
	if err := fs.Parse(argv); err != nil {
		emit(std.Stderr, "rukie admin gc: flag parse failed")
		return 64
	}
	if rest := fs.Args(); len(rest) > 0 {
		emit(std.Stderr, "rukie admin gc: too many positional arguments")
		return 64
	}
	cfg, err := config.Load(adminConfigPath(*configFlag))
	if err != nil {
		emit(std.Stderr, "rukie admin gc:", err.Error())
		return 78
	}
	if !*dryRun && !*confirm {
		emit(std.Stderr, "rukie admin gc: --confirm is required without --dry-run")
		return 64
	}
	validate := func() int {
		st, openErr := store.Open(ctx, store.Options{Path: cfg.Store.SQLite.Path})
		if openErr != nil {
			emit(std.Stderr, "rukie admin gc:", openErr.Error())
			return 1
		}
		defer func() { _ = st.Close(ctx) }()
		svc := auth.New(st)
		if _, code := adminLocalOperator(ctx, std, svc, *tokenFlag, *tokenFileFlag, *sessionFileFlag); code != 0 {
			return code
		}
		return 0
	}
	if code := validate(); code != 0 {
		return code
	}
	rep, err := sysops.GC(ctx, cfg, *dryRun, *repo)
	if err != nil {
		emit(std.Stderr, "rukie admin gc:", err.Error())
		if sysops.IsDowngrade(err) {
			return sysops.ExitDowngradeRefused
		}
		return 1
	}
	if *asJSON {
		raw, merr := jsonv2.Marshal(map[string]any{
			"dry_run": rep.DryRun, "repos_purged": rep.ReposPurged,
			"repos_pending": rep.ReposPending, "repos_failed": rep.ReposFailed,
			"jobs_deleted": rep.JobsDeleted, "jobs_would_drop": rep.JobsWouldDrop,
		}, jsonv1.DefaultOptionsV1())
		if merr != nil {
			emit(std.Stderr, "rukie admin gc:", merr.Error())
			return 1
		}
		emit(std.Stdout, string(raw))
	} else {
		verb := "purged"
		if rep.DryRun {
			verb = "would purge"
		}
		emit(std.Stdout, fmt.Sprintf("repos %s: %d", verb, len(rep.ReposPurged)))
		for _, name := range rep.ReposPurged {
			emit(std.Stdout, "  "+name)
		}
		for _, pending := range rep.ReposPending {
			emit(std.Stdout, fmt.Sprintf("  pending %s: %s", pending.Repo, pending.Reason))
		}
		for _, failed := range rep.ReposFailed {
			emit(std.Stdout, fmt.Sprintf("  failed %s: %s", failed.Repo, failed.Reason))
		}
		if rep.DryRun {
			emit(std.Stdout, fmt.Sprintf("jobs would drop: %d", rep.JobsWouldDrop))
		} else {
			emit(std.Stdout, fmt.Sprintf("jobs deleted: %d", rep.JobsDeleted))
		}
	}
	if len(rep.ReposFailed) > 0 {
		return 1
	}
	return 0
}

// importHooks carries the outward effects of repo import so tests inject
// failures deterministically.
type importHooks struct {
	mirrorClone func(ctx context.Context, from, dir string) error
	mirrorPush  func(ctx context.Context, dir, remote, auth string) error
	verify      func(ctx context.Context, dir, remote, auth string) error
}

// gitAuthEnv carries the push credential as an extra HTTP header instead of
// embedding it in the URL, so the secret never appears in argv, error
// output, or the process table.
func gitAuthEnv(auth string) []string {
	return append(os.Environ(),
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraHeader",
		"GIT_CONFIG_VALUE_0="+auth,
	)
}

func liveImportHooks() importHooks {
	return importHooks{
		mirrorClone: gitMirrorClone,
		mirrorPush:  gitMirrorPush,
		verify:      verifyMirrorPush,
	}
}

func gitMirrorClone(ctx context.Context, from, dir string) error {
	cmd := exec.CommandContext(ctx, "git", "clone", "--mirror", "--", from, dir)
	cmd.Env = append([]string{}, os.Environ()...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("clone --mirror: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func gitMirrorPush(ctx context.Context, dir, remote, auth string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "push", "--mirror", "--", remote)
	cmd.Env = gitAuthEnv(auth)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("push --mirror: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func gitLsRemote(ctx context.Context, remote, auth string, tags bool) (string, error) {
	args := []string{"ls-remote", "--", remote}
	if tags {
		args = []string{"ls-remote", "--tags", "--", remote}
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = gitAuthEnv(auth)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ls-remote: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func verifyMirrorPush(ctx context.Context, dir, remote, auth string) error {
	headCmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD")
	headCmd.Env = append([]string{}, os.Environ()...)
	headOut, err := headCmd.Output()
	if err != nil {
		return fmt.Errorf("source HEAD: %w", err)
	}
	sourceHEAD := strings.TrimSpace(string(headOut))
	remotes, err := gitLsRemote(ctx, remote, auth, false)
	if err != nil {
		return err
	}
	found := false
	for line := range strings.Lines(remotes) {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == sourceHEAD && fields[1] == "HEAD" {
			found = true
		}
	}
	if !found {
		for line := range strings.Lines(remotes) {
			if fields := strings.Fields(line); len(fields) == 2 && fields[0] == sourceHEAD {
				found = true
			}
		}
	}
	if !found {
		return fmt.Errorf("server HEAD does not match source HEAD %s", sourceHEAD)
	}
	tagCmd := exec.CommandContext(ctx, "git", "-C", dir, "tag", "--list")
	tagCmd.Env = append([]string{}, os.Environ()...)
	tagOut, err := tagCmd.Output()
	if err != nil {
		return fmt.Errorf("source tags: %w", err)
	}
	sourceTags := 0
	for range strings.Lines(strings.TrimSpace(string(tagOut))) {
		sourceTags++
	}
	remoteTags, err := gitLsRemote(ctx, remote, auth, true)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	remoteCount := 0
	for line := range strings.Lines(remoteTags) {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.HasSuffix(fields[1], "^{}") {
			continue
		}
		if !seen[fields[1]] {
			seen[fields[1]] = true
			remoteCount++
		}
	}
	if remoteCount != sourceTags {
		return fmt.Errorf("tag count mismatch: server %d, source %d", remoteCount, sourceTags)
	}
	return nil
}

type importAPI struct {
	client  *http.Client
	headers map[string]string
	base    string
	token   string
}

func (a importAPI) withHeader(key, value string) importAPI {
	next := map[string]string{}
	for k, v := range a.headers {
		next[k] = v
	}
	next[key] = value
	a.headers = next
	return a
}

func (a importAPI) do(ctx context.Context, method, path string, in any, out any) (int, error) {
	var body io.Reader
	if in != nil {
		raw, err := jsonv2.Marshal(in, jsonv1.DefaultOptionsV1())
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, body)
	if err != nil {
		return 0, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	for k, v := range a.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("server %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		if uerr := jsonv2.Unmarshal(raw, out, jsonv1.DefaultOptionsV1()); uerr != nil {
			return resp.StatusCode, uerr
		}
	}
	return resp.StatusCode, nil
}

func runAdminRepoImport(ctx context.Context, std IO, argv []string) int {
	fs := flag.NewFlagSet("admin repo import", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	serverFlag := fs.String("server", "", "server base URL")
	tokenFlag := fs.String("token", "", "admin bearer token (prefer --token-file)")
	tokenFileFlag := fs.String("token-file", "", "file holding the admin bearer token")
	fromGit := fs.String("from-git", "", "source git URL or local path")
	owner := fs.String("owner", "", "destination owner")
	name := fs.String("name", "", "destination repository name")
	defBranch := fs.String("default-branch", "", "default branch (defaults to the source HEAD)")
	asJSON := fs.Bool("json", false, "machine output")
	if err := fs.Parse(argv); err != nil {
		emit(std.Stderr, "rukie admin repo import: flag parse failed")
		return 64
	}
	if rest := fs.Args(); len(rest) > 0 {
		emit(std.Stderr, "rukie admin repo import: too many positional arguments")
		return 64
	}
	base := strings.TrimSuffix(strings.TrimSpace(cmp.Or(*serverFlag, std.getenv("RUKIE_SERVER"))), "/")
	if base == "" {
		emit(std.Stderr, "rukie admin repo import: --server is required")
		return 64
	}
	if *fromGit == "" || *owner == "" || *name == "" {
		emit(std.Stderr, "rukie admin repo import: --from-git, --owner, and --name are required")
		return 64
	}
	token, code := adminServerToken(std, *tokenFlag, *tokenFileFlag)
	if code != 0 {
		return code
	}
	if token == "" {
		emit(std.Stderr, "rukie admin repo import: an admin token is required (--token-file or RUKIE_TOKEN)")
		return 64
	}
	return importRepo(ctx, std, importAPI{base: base, client: &http.Client{Timeout: 60 * time.Second}, token: token},
		importSpec{From: *fromGit, Owner: *owner, Name: *name, DefaultBranch: *defBranch, AsJSON: *asJSON}, liveImportHooks())
}

// importSpec carries one repo import request.
type importSpec struct {
	From          string
	Owner         string
	Name          string
	DefaultBranch string
	AsJSON        bool
}

// importRepo imports one stock-git repo and purges it again when any step
// after creation fails (ENG-50). Tokens are minted least-privilege: a
// create-only token for the owner namespace first, then a push token scoped
// to the new repository id. Every outstanding token is revoked before return,
// and a failed revoke is fatal because the token may still be valid.
func importRepo(ctx context.Context, std IO, api importAPI, spec importSpec, hooks importHooks) (code int) {
	owner, name := spec.Owner, spec.Name
	full := owner + "/" + name
	var created bool
	type oneShot struct {
		secret string
		prefix string
	}
	var held []oneShot
	defer func() {
		for _, tk := range held {
			if _, err := api.do(ctx, http.MethodPost, "/admin/users/"+owner+"/tokens/revoke",
				map[string]any{"prefix": tk.prefix}, nil); err != nil {
				emit(std.Stderr, "rukie admin repo import: revoke one-shot token: "+err.Error()+": the token may still be valid")
				code = 1
			}
		}
	}()
	drop := func(prefix string) {
		kept := make([]oneShot, 0, len(held))
		for _, tk := range held {
			if tk.prefix != prefix {
				kept = append(kept, tk)
			}
		}
		held = kept
	}
	revokeFatal := func(prefix string) bool {
		_, err := api.do(ctx, http.MethodPost, "/admin/users/"+owner+"/tokens/revoke",
			map[string]any{"prefix": prefix}, nil)
		drop(prefix)
		if err != nil {
			emit(std.Stderr, "rukie admin repo import: revoke one-shot token: "+err.Error()+": the token may still be valid")
			return true
		}
		return false
	}
	fail := func(step string, err error) int {
		emit(std.Stderr, "rukie admin repo import: "+step+": "+err.Error())
		if created {
			if rerr := rollbackImport(ctx, api, owner, name); rerr != nil {
				emit(std.Stderr, "rukie admin repo import: rollback: "+rerr.Error())
			}
		}
		return 1
	}
	mint := func(label, scopes string, repos []int64) (string, string, error) {
		var out struct {
			Prefix string `json:"prefix"`
			Secret string `json:"secret"`
		}
		tokBody := map[string]any{"name": label, "scopes": scopes, "ttl": "15m"}
		if repos != nil {
			tokBody["repos"] = repos
		}
		if _, err := api.do(ctx, http.MethodPost, "/admin/users/"+owner+"/tokens", tokBody, &out); err != nil {
			return "", "", err
		}
		if out.Secret == "" {
			return "", "", fmt.Errorf("server returned no secret")
		}
		held = append(held, oneShot{secret: out.Secret, prefix: out.Prefix})
		return out.Secret, out.Prefix, nil
	}
	createSecret, createPrefix, err := mint("repo-import-create", "repo:create", nil)
	if err != nil {
		return fail("mint one-shot token", err)
	}
	creator := importAPI{base: api.base, client: api.client, token: createSecret}
	body := map[string]any{"owner": owner, "name": name}
	if spec.DefaultBranch != "" {
		body["default_branch"] = spec.DefaultBranch
	}
	var createdOut struct {
		Repo struct {
			DefaultBranch string `json:"default_branch"`
		} `json:"repo"`
	}
	if _, cerr := creator.do(ctx, http.MethodPost, "/new/repo", body, &createdOut); cerr != nil {
		return fail("create repository", cerr)
	}
	created = true
	if revokeFatal(createPrefix) {
		if rerr := rollbackImport(ctx, api, owner, name); rerr != nil {
			emit(std.Stderr, "rukie admin repo import: rollback: "+rerr.Error())
		}
		return 1
	}
	repoID, err := lookupRepoID(ctx, api, owner, full)
	if err != nil {
		return fail("resolve repository", err)
	}
	pushSecret, pushPrefix, err := mint("repo-import-push", "repo:read repo:write", []int64{repoID})
	if err != nil {
		return fail("mint push token", err)
	}
	dir, err := os.MkdirTemp("", "rukie-import-")
	if err != nil {
		return fail("stage import", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	stage := filepath.Join(dir, "mirror.git")
	if err := hooks.mirrorClone(ctx, spec.From, stage); err != nil {
		return fail("clone source", err)
	}
	remote := api.base + "/" + full + ".git"
	auth := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(owner+":"+pushSecret))
	if err := hooks.mirrorPush(ctx, stage, remote, auth); err != nil {
		return fail("push mirror", err)
	}
	if err := hooks.verify(ctx, stage, remote, auth); err != nil {
		return fail("verify import", err)
	}
	if revokeFatal(pushPrefix) {
		return 1
	}
	if spec.AsJSON {
		emit(std.Stdout, fmt.Sprintf("{\"repo\":%q,\"default_branch\":%q}", full, createdOut.Repo.DefaultBranch))
	} else {
		emit(std.Stdout, "imported "+full)
	}
	return 0
}

type listedRepo struct {
	FullName  string `json:"full_name"`
	ID        int64  `json:"id"`
	UpdatedAt int64  `json:"updated_at"`
}

func listAdminRepos(ctx context.Context, api importAPI, owner string) ([]listedRepo, error) {
	var listed struct {
		Repos []listedRepo `json:"repos"`
	}
	if _, err := api.do(ctx, http.MethodGet, "/admin/repos?owner="+owner, nil, &listed); err != nil {
		return nil, err
	}
	return listed.Repos, nil
}

func lookupRepoID(ctx context.Context, api importAPI, owner, full string) (int64, error) {
	repos, err := listAdminRepos(ctx, api, owner)
	if err != nil {
		return 0, err
	}
	for _, repo := range repos {
		if repo.FullName == full {
			return repo.ID, nil
		}
	}
	return 0, fmt.Errorf("repository %s was not found", full)
}

// rollbackImport purges the half-imported repo with the operator token, so a
// failed import leaves no repository behind. The purge carries a fresh
// If-Match taken from the admin listing, so a concurrent change fails
// instead of purging something new.
func rollbackImport(ctx context.Context, adminAPI importAPI, owner, name string) error {
	full := owner + "/" + name
	repos, err := listAdminRepos(ctx, adminAPI, owner)
	if err != nil {
		return err
	}
	var target *listedRepo
	for i := range repos {
		if repos[i].FullName == full {
			target = &repos[i]
		}
	}
	if target == nil {
		return fmt.Errorf("repository %s was not found", full)
	}
	purger := adminAPI.withHeader("If-Match", fmt.Sprintf("repo-%s-%s-%d", owner, name, target.UpdatedAt))
	_, err = purger.do(ctx, http.MethodPost, "/admin/repos/"+strconv.FormatInt(target.ID, 10)+"/purge",
		map[string]any{"confirm": full}, nil)
	return err
}
