//go:build e2e

package e2e

import (
	"encoding/base64"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/store"
	"github.com/gosuda/rukie/internal/webhook"
)

type webhookHit struct {
	id        string
	timestamp string
	signature string
	body      []byte
	method    string
	peer      string
	clen      string
	keys      string
	ua        string
	path      string
}

func webhookAgentFlow(t *testing.T) {
	s := newStack(t, nil)
	h := mountAPI(t, s)
	s.addAdmin(t, "ada", "repo:read", "repo:write")
	session := apiLogin(t, h, "ada", "long-test-password-01")
	token := apiToken(t, h, session, "repo:create repo:read repo:write intents:read webhooks:read webhooks:write")

	status, decoded := apiCall(t, h, "POST", "/new/repo", token, `{"owner":"ada","name":"hooks","visibility":"private"}`)
	if status != http.StatusCreated {
		t.Fatalf("repo.create = %d: %v", status, decoded)
	}

	var mu sync.Mutex
	var hits []webhookHit
	failOpen := true
	record := func(w http.ResponseWriter, r *http.Request) (body []byte, first bool) {
		body, _ = io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		hits = append(hits, webhookHit{
			id:        r.Header.Get("webhook-id"),
			timestamp: r.Header.Get("webhook-timestamp"),
			signature: r.Header.Get("webhook-signature"),
			body:      body,
			method:    r.Method,
			peer:      r.RemoteAddr,
			clen:      r.Header.Get("Content-Length"),
			keys:      strings.Join(slices.Sorted(maps.Keys(r.Header)), ","),
			ua:        r.Header.Get("User-Agent"),
			path:      r.URL.Path,
		})
		first = failOpen
		failOpen = false
		return body, first
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /{$}", func(w http.ResponseWriter, r *http.Request) {
		_, first := record(w, r)
		if first {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		record(w, r)
		t.Logf("non-POST probe ignored: method=%q ua=%q path=%q", r.Method, r.Header.Get("User-Agent"), r.URL.Path)
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	fake := httptest.NewServer(mux)
	defer fake.Close()

	status, created := apiCall(t, h, "POST", "/ada/hooks/settings/hooks", token,
		`{"scope":"repo","url":"`+fake.URL+`","event_types":["task.created"]}`)
	if status != http.StatusCreated {
		t.Fatalf("endpoint.create = %d: %v", status, created)
	}
	endpoint, _ := created["endpoint"].(map[string]any)
	endpointID, _ := endpoint["id"].(string)
	secret, _ := endpoint["secret"].(string)
	if endpointID == "" || secret == "" {
		t.Fatalf("endpoint misses id or secret: %v", created)
	}
	rawSecret, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil {
		t.Fatalf("secret decode: %v", err)
	}

	status, made := apiCall(t, h, "POST", "/ada/hooks/tasks/new", session,
		`{"title":"Hook the panel","body":"Lease the docs subtree.","lease_mode":"advise","claim_paths":["docs/"]}`)
	if status != http.StatusCreated {
		t.Fatalf("intent.create = %d: %v", status, made)
	}

	worker := webhook.NewWorker(webhook.WorkerConfig{
		Store: s.st, SealKey: h.sealKey,
		Workers: 2, GlobalMaxInflight: 8,
		PollInterval: 200 * time.Millisecond, LeaseDuration: 5 * time.Minute,
		Now: time.Now,
	}, &webhook.Policy{AllowInsecureHTTP: true, AllowPrivateTargets: true})
	delivered := false
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if derr := worker.DriveOnce(t.Context()); derr != nil {
			t.Fatalf("drive: %v", derr)
		}
		if webhookMessageState(t, s, endpointID) == "delivered" {
			delivered = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !delivered {
		t.Fatal("task.created was not delivered within 30s")
	}

	mu.Lock()
	defer mu.Unlock()
	var posts []webhookHit
	for _, h := range hits {
		if h.method == http.MethodPost {
			posts = append(posts, h)
		}
	}
	if len(posts) < 2 {
		for i, h := range hits {
			t.Logf("hit %d: id=%q ts=%q sig=%.12q body=%dB method=%q peer=%q clen=%q keys=%q ua=%q path=%q", i, h.id, h.timestamp, h.signature, len(h.body), h.method, h.peer, h.clen, h.keys, h.ua, h.path)
		}
		t.Fatalf("fake posts = %d, want at least 2 (retry after the 500)", len(posts))
	}
	first, second := posts[0], posts[1]
	if first.id == "" || first.id != second.id {
		for i, h := range hits {
			t.Logf("hit %d: id=%q ts=%q sig=%.12q body=%dB method=%q peer=%q clen=%q keys=%q ua=%q path=%q", i, h.id, h.timestamp, h.signature, len(h.body), h.method, h.peer, h.clen, h.keys, h.ua, h.path)
		}
		t.Fatalf("webhook ids differ: %q vs %q (%d posts)", first.id, second.id, len(posts))
	}
	if first.timestamp == second.timestamp {
		t.Fatal("attempt timestamps must differ per attempt")
	}
	for i, hit := range posts {
		ts, terr := strconv.ParseInt(hit.timestamp, 10, 64)
		if terr != nil {
			t.Fatalf("hit %d timestamp: %v", i, terr)
		}
		if !webhook.Verify([][]byte{rawSecret}, hit.id, ts, hit.body, hit.signature, time.Now()) {
			t.Fatalf("hit %d signature does not verify", i)
		}
		var envelope map[string]any
		if uerr := jsonv2.Unmarshal(hit.body, &envelope, jsonv1.DefaultOptionsV1()); uerr != nil {
			t.Fatalf("hit %d envelope: %v", i, uerr)
		}
		if envelope["type"] != "task.created" {
			t.Fatalf("hit %d type = %v, want task.created", i, envelope["type"])
		}
	}

	status, listed := apiCall(t, h, "GET", "/inbox?state=unread", token, "")
	if status != http.StatusOK {
		t.Fatalf("inbox.list = %d: %v", status, listed)
	}
	threads, _ := listed["threads"].([]any)
	foundTask := false
	for _, thread := range threads {
		row, _ := thread.(map[string]any)
		if row["subject_kind"] == "task" {
			foundTask = true
		}
	}
	if !foundTask {
		t.Fatalf("inbox misses the task thread: %v", listed)
	}

	status, rotated := apiCall(t, h, "POST", "/ada/hooks/settings/hooks/"+endpointID+"/rotate-secret", token, "")
	if status != http.StatusCreated {
		t.Fatalf("rotate = %d: %v", status, rotated)
	}
	fresh, _ := rotated["endpoint"].(map[string]any)
	if fresh["secret"] == secret || fresh["secret"] == "" {
		t.Fatalf("rotate must mint a fresh shown-once secret: %v", rotated)
	}
	status, got := apiCall(t, h, "GET", "/ada/hooks/settings/hooks/"+endpointID, token, "")
	if status != http.StatusOK {
		t.Fatalf("endpoint.get = %d: %v", status, got)
	}
	if strings.Contains(string(mustJSON(t, got)), `"secret"`) {
		t.Fatalf("second get shows the secret: %v", got)
	}

	status, redelivered := apiCall(t, h, "POST", "/ada/hooks/settings/hooks/"+endpointID+"/messages/"+first.id+"/redeliver", token, "")
	if status != http.StatusCreated {
		t.Fatalf("redeliver = %d: %v", status, redelivered)
	}
	message, _ := redelivered["message"].(map[string]any)
	if message["id"] != first.id || message["state"] != "pending" {
		t.Fatalf("redelivered = %v, want pending %s", message, first.id)
	}
}

func webhookMessageState(t *testing.T, s *stack, endpointID string) string {
	t.Helper()
	var state string
	if err := s.st.Read(t.Context(), func(tx *store.ReadTx) error {
		rows, _, lerr := tx.ListWebhookMessages(endpointID, "", 10)
		if lerr != nil {
			return lerr
		}
		for _, row := range rows {
			if row.EventType == "task.created" {
				state = row.State
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("message state: %v", err)
	}
	return state
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := jsonv2.Marshal(v, jsonv1.DefaultOptionsV1())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}
