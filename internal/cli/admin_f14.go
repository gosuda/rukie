package cli

import (
	"bytes"
	"cmp"
	"context"
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

	"github.com/gosuda/rukie/internal/config"
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

func runAdminGC(ctx context.Context, std IO, argv []string) int {
	fs := flag.NewFlagSet("admin gc", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "config file path")
	dryRun := fs.Bool("dry-run", false, "list what would be collected without writing")
	repo := fs.String("repo", "", "limit collection to one owner/name repository")
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
	mirrorPush  func(ctx context.Context, dir, remote, username, secret string) error
	verify      func(ctx context.Context, dir, remote, username, secret string) error
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

func gitMirrorPush(ctx context.Context, dir, remote, username, secret string) error {
	url := remote
	if username != "" {
		url = strings.Replace(remote, "://", "://"+username+":"+secret+"@", 1)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "push", "--mirror", "--", url)
	cmd.Env = append([]string{}, os.Environ()...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("push --mirror: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func gitLsRemote(ctx context.Context, remote, username, secret string, tags bool) (string, error) {
	url := remote
	if username != "" {
		url = strings.Replace(remote, "://", "://"+username+":"+secret+"@", 1)
	}
	args := []string{"ls-remote", "--", url}
	if tags {
		args = []string{"ls-remote", "--tags", "--", url}
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append([]string{}, os.Environ()...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ls-remote: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func verifyMirrorPush(ctx context.Context, dir, remote, username, secret string) error {
	headCmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD")
	headCmd.Env = append([]string{}, os.Environ()...)
	headOut, err := headCmd.Output()
	if err != nil {
		return fmt.Errorf("source HEAD: %w", err)
	}
	sourceHEAD := strings.TrimSpace(string(headOut))
	remotes, err := gitLsRemote(ctx, remote, username, secret, false)
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
	remoteTags, err := gitLsRemote(ctx, remote, username, secret, true)
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

// importRepo imports one stock-git repo and deletes it again when any step
// after creation fails (ENG-50). The one-shot token is revoked on the way
// out whether the import succeeded or not.
func importRepo(ctx context.Context, std IO, api importAPI, spec importSpec, hooks importHooks) int {
	owner, name := spec.Owner, spec.Name
	full := owner + "/" + name
	var created bool
	revoke := func(secret, prefix string) {
		if secret == "" {
			return
		}
		_, _ = api.do(ctx, http.MethodPost, "/admin/users/"+owner+"/tokens/revoke",
			map[string]any{"prefix": prefix}, nil)
	}
	fail := func(step string, err error) int {
		emit(std.Stderr, "rukie admin repo import: "+step+": "+err.Error())
		return 1
	}
	rollback := func(step string, err error, shot importAPI) int {
		emit(std.Stderr, "rukie admin repo import: "+step+": "+err.Error())
		if created {
			if rerr := rollbackImport(ctx, api, shot, owner, name); rerr != nil {
				emit(std.Stderr, "rukie admin repo import: rollback: "+rerr.Error())
			}
		}
		return 1
	}
	var minted struct {
		Prefix string `json:"prefix"`
		Secret string `json:"secret"`
	}
	// The one-shot token carries admin:write so the rollback can soft-delete
	// the repo it just created. It lives 15 minutes and is revoked below.
	if _, err := api.do(ctx, http.MethodPost, "/admin/users/"+owner+"/tokens",
		map[string]any{"name": "repo-import", "scopes": "repo:create repo:read repo:write admin:write", "ttl": "15m"}, &minted); err != nil {
		return fail("mint one-shot token", err)
	}
	defer revoke(minted.Secret, minted.Prefix)
	if minted.Secret == "" {
		return fail("mint one-shot token", fmt.Errorf("server returned no secret"))
	}
	oneShot := importAPI{base: api.base, client: api.client, token: minted.Secret}
	body := map[string]any{"owner": owner, "name": name}
	if spec.DefaultBranch != "" {
		body["default_branch"] = spec.DefaultBranch
	}
	var createdOut struct {
		Repo struct {
			DefaultBranch string `json:"default_branch"`
		} `json:"repo"`
	}
	if _, err := oneShot.do(ctx, http.MethodPost, "/new/repo", body, &createdOut); err != nil {
		return fail("create repository", err)
	}
	created = true
	dir, err := os.MkdirTemp("", "rukie-import-")
	if err != nil {
		return rollback("stage import", err, oneShot)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	stage := filepath.Join(dir, "mirror.git")
	if err := hooks.mirrorClone(ctx, spec.From, stage); err != nil {
		return rollback("clone source", err, oneShot)
	}
	remote := api.base + "/" + full + ".git"
	if err := hooks.mirrorPush(ctx, stage, remote, owner, minted.Secret); err != nil {
		return rollback("push mirror", err, oneShot)
	}
	if err := hooks.verify(ctx, stage, remote, owner, minted.Secret); err != nil {
		return rollback("verify import", err, oneShot)
	}
	revoke(minted.Secret, minted.Prefix)
	if spec.AsJSON {
		emit(std.Stdout, fmt.Sprintf("{\"repo\":%q,\"default_branch\":%q}", full, createdOut.Repo.DefaultBranch))
	} else {
		emit(std.Stdout, "imported "+full)
	}
	return 0
}

// rollbackImport soft-deletes the half-imported repo with the one-shot token
// and purges the tombstone with the operator token, so a failed import
// leaves no repository behind.
func rollbackImport(ctx context.Context, adminAPI, oneShot importAPI, owner, name string) error {
	full := owner + "/" + name
	var got struct {
		Repo struct {
			UpdatedAt int64 `json:"updated_at"`
		} `json:"repo"`
	}
	if _, err := oneShot.do(ctx, http.MethodGet, "/"+full, nil, &got); err != nil {
		return err
	}
	deleter := oneShot.withHeader("If-Match", fmt.Sprintf("repo-%s-%s-%d", owner, name, got.Repo.UpdatedAt))
	if _, err := deleter.do(ctx, http.MethodPost, "/"+full+"/settings/delete",
		map[string]any{"confirm": full}, nil); err != nil {
		return err
	}
	var listed struct {
		Repos []struct {
			FullName string `json:"full_name"`
			ID       int64  `json:"id"`
		} `json:"repos"`
	}
	if _, err := adminAPI.do(ctx, http.MethodGet, "/admin/repos?state=deleted&owner="+owner, nil, &listed); err != nil {
		return err
	}
	purgeID := int64(0)
	for _, repo := range listed.Repos {
		if repo.FullName == full {
			purgeID = repo.ID
		}
	}
	if purgeID == 0 {
		return fmt.Errorf("tombstoned repository %s was not found", full)
	}
	_, err := adminAPI.do(ctx, http.MethodPost, "/admin/repos/"+strconv.FormatInt(purgeID, 10)+"/purge",
		map[string]any{"confirm": full}, nil)
	return err
}
