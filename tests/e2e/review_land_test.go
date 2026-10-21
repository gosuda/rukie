//go:build e2e

package e2e

import (
	"context"
	"net/http"
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

func f2Repo(t *testing.T, h *apiHarness, ada userRec, owner, name string) {
	t.Helper()
	session := apiLogin(t, h, "ada", "long-test-password-01")
	token := apiToken(t, h, session, "repo:create repo:read repo:write intents:read intents:write")
	status, decoded := apiCall(t, h, "POST", "/new/repo", token, `{"owner":"`+owner+`","name":"`+name+`","visibility":"private"}`)
	if status != http.StatusCreated {
		t.Fatalf("repo.create = %d: %v", status, decoded)
	}
	_ = ada
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
	f2Repo(t, h, ada, "ada", "hello")
	f2PushFor(t, s, ada, "hello", map[string]string{"README.md": "hello\n"})
	session := apiLogin(t, h, "ada", "long-test-password-01")
	token := apiToken(t, h, session, "repo:read repo:write intents:read")
	key := "main~#1"
	planStatus, plan := apiCall(t, h, "POST", "/ada/hello/land/plans", token,
		`{"target_ref":"refs/heads/main","change":"`+key+`"}`)
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
