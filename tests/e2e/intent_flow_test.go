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

func intentAgentFlow(t *testing.T) {
	s := newStack(t, nil)
	h := mountAPI(t, s)
	s.addAdmin(t, "ada", "repo:read", "repo:write")
	session := apiLogin(t, h, "ada", "long-test-password-01")
	token := apiToken(t, h, session, "repo:create repo:read repo:write intents:read intents:write")

	status, decoded := apiCall(t, h, "POST", "/new/repo", token, `{"owner":"ada","name":"hello","visibility":"private"}`)
	if status != http.StatusCreated {
		t.Fatalf("repo.create = %d: %v", status, decoded)
	}

	work, _ := s.localRepo(t, map[string]string{"docs/guide.md": "guide\n"})
	pushCtx, pushCancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer pushCancel()
	if out, err := gitRun(pushCtx, t, work, "push", s.repoURL("ada", "hello", "ada", strings.TrimPrefix(token, "Bearer ")), "HEAD:refs/heads/docs"); err != nil {
		t.Fatalf("seed push: %v\n%s", err, out)
	}
	status, created := apiCall(t, h, "POST", "/ada/hello/tasks/new", token, `{"title":"Wire the claims panel","body":"Lease the docs subtree.","lease_mode":"advise","claim_paths":["docs/"]}`)
	if status != http.StatusCreated {
		t.Fatalf("intent.create = %d: %v", status, created)
	}
	intent, _ := created["intent"].(map[string]any)
	if intent["number"] != float64(1) {
		t.Fatalf("intent number = %v, want 1", intent["number"])
	}
	if intent["state"] != "open" {
		t.Fatalf("intent state = %v, want open", intent["state"])
	}

	status, listed := apiCall(t, h, "GET", "/ada/hello/tasks", token, "")
	if status != http.StatusOK {
		t.Fatalf("intent.list = %d: %v", status, listed)
	}
	if raw, _ := listed["intents"].([]any); len(raw) != 1 {
		t.Fatalf("intent.list = %v, want one row", listed)
	}

	status, got := apiCall(t, h, "GET", "/ada/hello/tasks/1", token, "")
	if status != http.StatusOK {
		t.Fatalf("intent.get = %d: %v", status, got)
	}

	version, _ := intent["version"].(float64)
	status, updated := apiCall(t, h, "POST", "/ada/hello/tasks/1/edit", token, `{"body":"Lease the docs subtree and the panel.","version":`+strconv.FormatInt(int64(version), 10)+`}`)
	if status != http.StatusOK {
		t.Fatalf("intent.update = %d: %v", status, updated)
	}

	status, lease := apiCall(t, h, "POST", "/ada/hello/tasks/1/claim", token, `{"ttl_s":60}`)
	if status != http.StatusOK {
		t.Fatalf("intent.lease.create = %d: %v", status, lease)
	}
	status, renewed := apiCall(t, h, "POST", "/ada/hello/tasks/1/claim/renew", token, `{"ttl_s":60}`)
	if status != http.StatusOK {
		t.Fatalf("intent.lease.update = %d: %v", status, renewed)
	}

	status, asked := apiCall(t, h, "POST", "/ada/hello/tasks/1/asks", token, `{"question":"Which subtree owns the panel?","blocks_claim":true}`)
	if status != http.StatusCreated {
		t.Fatalf("ask.create = %d: %v", status, asked)
	}
	ask, _ := asked["ask"].(map[string]any)
	askID, _ := ask["public_id"].(string)
	if askID == "" {
		t.Fatalf("ask public id missing: %v", asked)
	}
	status, blocked := apiCall(t, h, "GET", "/ada/hello/tasks/1", token, "")
	if status != http.StatusOK {
		t.Fatalf("intent.get blocked = %d: %v", status, blocked)
	}
	if row, _ := blocked["intent"].(map[string]any); row["state"] != "blocked" {
		t.Fatalf("intent state = %v, want blocked", row["state"])
	}
	status, denied := apiCall(t, h, "POST", "/ada/hello/tasks/1/claim", token, `{"ttl_s":60}`)
	if status != http.StatusConflict {
		t.Fatalf("blocked claim = %d, want 409: %v", status, denied)
	}
	status, _ = apiCall(t, h, "POST", "/ada/hello/asks/"+askID+"/answer", token, `{"answer":"docs"}`)
	if status != http.StatusForbidden {
		t.Fatalf("token ask.answer = %d, want 403 human-only", status)
	}
	status, answered := apiCall(t, h, "POST", "/ada/hello/asks/"+askID+"/answer", session, `{"answer":"docs"}`)
	if status != http.StatusOK {
		t.Fatalf("ask.answer.create = %d: %v", status, answered)
	}
	status, reopened := apiCall(t, h, "GET", "/ada/hello/tasks/1", token, "")
	if status != http.StatusOK {
		t.Fatalf("intent.get unblocked = %d: %v", status, reopened)
	}
	if row, _ := reopened["intent"].(map[string]any); row["state"] != "open" {
		t.Fatalf("intent state = %v, want open after answer", row["state"])
	}

	status, released := apiCall(t, h, "POST", "/ada/hello/tasks/1/release", token, `{}`)
	if status != http.StatusOK {
		t.Fatalf("intent.lease.delete = %d: %v", status, released)
	}
	status, enforced := apiCall(t, h, "POST", "/ada/hello/tasks/new", token, `{"title":"Guarded migration","lease_mode":"enforce","claim_paths":["src"]}`)
	if status != http.StatusCreated {
		t.Fatalf("enforce intent.create = %d: %v", status, enforced)
	}
	second, _ := enforced["intent"].(map[string]any)
	secondNum := strconv.FormatInt(int64(second["number"].(float64)), 10)
	status, lease = apiCall(t, h, "POST", "/ada/hello/tasks/"+secondNum+"/claim", token, `{"ttl_s":60}`)
	if status != http.StatusOK {
		t.Fatalf("enforce lease.create = %d: %v", status, lease)
	}
	if lease["mode"] != "enforce" || lease["holder"] == "" || lease["expires_at"] == float64(0) {
		t.Fatalf("enforce lease = %v, want mode, holder, and expiry", lease)
	}
	if rows := outboxKinds(t, s, "task.created"); len(rows) != 2 {
		t.Fatalf("task.created rows = %d, want 2", len(rows))
	}
}
