//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func f2Stack(t *testing.T) (*stack, *apiHarness, userRec) {
	t.Helper()
	s := newStack(t, nil)
	h := mountAPI(t, s)
	ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
	return s, h, ada
}

func f2SeedRepo(t *testing.T, s *stack, ada userRec, owner, name string, files map[string]string) {
	t.Helper()
	repoID := s.addRepo(t, owner, name, "private")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO bookmark_rules
(repo_id, pattern, mode, bypass_admin, push_allow, land_allow, required, version, created_at)
VALUES (?, ?, 'LandOnly', 1, ?, '[]', ?, 1, ?)`,
		repoID, "refs/heads/main", `[{"users":[`+strconv.FormatInt(ada.user.ID, 10)+`]}]`,
		`[{"kind":"review/approve","result":"approved","trusted":{},"min_signers":1}]`,
		time.Now().UnixMilli()); err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE repos SET ruk_path = ? WHERE id = ?`,
		owner+"/"+name+".git", repoID); err != nil {
		t.Fatalf("seed ruk path: %v", err)
	}
	work, _ := s.localRepo(t, files)
	pushCtx, pushCancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer pushCancel()
	if out, err := gitRun(pushCtx, t, work, "push", s.repoURL(owner, name, "ada", ada.token), "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("seed main: %v\n%s", err, out)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE bookmark_rules SET bypass_admin = 0, push_allow = '[]' WHERE repo_id = ?`,
		repoID); err != nil {
		t.Fatalf("tighten rule: %v", err)
	}
}

func f2PushFor(t *testing.T, s *stack, ada userRec, repo string, files map[string]string) string {
	t.Helper()
	work, _ := s.localRepo(t, files)
	pushURL := s.repoURL("ada", repo, "ada", ada.token)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	if out, err := gitRun(ctx, t, work, "push", pushURL, "HEAD:refs/for/main"); err != nil {
		t.Fatalf("refs/for push: %v\n%s", err, out)
	}
	var head string
	for ref, target := range s.lsRemote(t, pushURL) {
		if strings.HasPrefix(ref, "refs/changes/") {
			head = target
		}
	}
	if head == "" {
		t.Fatal("no change ref after refs/for push")
	}
	return head
}

func TestRefsForRequiresOnlyRead(t *testing.T) {
	s := newStack(t, nil)
	ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
	repoID := s.addRepo(t, "ada", "hello", "private")
	mallory := s.addUser(t, "mallory", "repo:read")
	s.addGrant(t, repoID, mallory.user.ID, "read")
	_ = ada
	work, _ := s.localRepo(t, map[string]string{"README.md": "hello\n"})
	pushURL := s.repoURL("ada", "hello", "mallory", mallory.token)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	if out, err := gitRun(ctx, t, work, "push", pushURL, "HEAD:refs/for/main"); err != nil {
		t.Fatalf("read-only refs/for push: %v\n%s", err, out)
	}
}

func TestLandRequirementsUnmetProblem(t *testing.T) {
	s, h, ada := f2Stack(t)
	f2SeedRepo(t, s, ada, "ada", "hello", map[string]string{"README.md": "hello\n"})
	f2PushFor(t, s, ada, "hello", map[string]string{"README.md": "hello\n"})
	session := apiLogin(t, h, "ada", "long-test-password-01")
	token := apiToken(t, h, session, "repo:read repo:write intents:read")
	key := "main~" + strconv.FormatInt(ada.user.ID, 10)
	planStatus, plan := apiCall(t, h, "POST", "/ada/hello/land/plans", token, `{"target_ref":"refs/heads/main","change":"`+key+`"}`)
	if planStatus != http.StatusCreated {
		t.Fatalf("land.plan.create = %d: %v", planStatus, plan)
	}
	planID, _ := plan["id"].(string)
	if planID == "" {
		if nested, ok := plan["plan"].(map[string]any); ok {
			planID, _ = nested["id"].(string)
		}
	}
	runStatus, run := apiCall(t, h, "POST", "/ada/hello/land/plans/"+planID+"/runs", token, `{}`)
	if runStatus != http.StatusConflict {
		t.Fatalf("unmet run = %d, want 409: %v", runStatus, run)
	}
	raw, _ := run["detail"].(string)
	if !strings.Contains(raw, "requirements-unmet") {
		t.Fatalf("conflict lacks requirements-unmet: %v", run)
	}
}

func TestCompareThreeDotAlias(t *testing.T) {
	t.Skip("needs-ruk-F2")
}

func TestLandStackRunsNSerialPlans(t *testing.T) {
	t.Skip("needs-ruk-F2")
}
