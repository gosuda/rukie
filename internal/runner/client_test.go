package runner

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	jsonv2 "encoding/json/v2"
)

type stubGrant struct {
	attempt string
	token   string
	fence   int64
}

func stubServer(t *testing.T, grant stubGrant, seen *map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
	mux.HandleFunc("POST /-/ci/v1/lease", func(w http.ResponseWriter, r *http.Request) {
		(*seen)["lease-auth"] = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		(*seen)["lease-body"] = string(raw)
		write(w, `{"attempt":{"id":"`+grant.attempt+`","number":1,"fence":`+strconv.FormatInt(grant.fence, 10)+`,"token":"`+grant.token+`"},"job":{"name":"lint","spec_digest":"sha256:aa","action_digest":"sha256:bb","pure":true,"effects":"none","network":"none","timeout_s":600,"image":{"ref":"img","manifest":"img"},"steps":[],"env":{},"artifacts":[]},"run":{"id":"run_1","trust":"trusted","subject":"s","tree":"t","spec_source":"base"},"fetch":{"url":"/o/r.git","want":"s","expect_tree":"t","token":"fetch-secret"},"secrets":[],"timers":{"heartbeat_s":10}}`)
	})
	mux.HandleFunc("POST /-/ci/v1/attempts/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		(*seen)["heartbeat-body"] = string(raw)
		write(w, `{"state":"started","cancel":"","lease_ttl_s":30,"heartbeat_persisted_at_ms":1,"log_ack_offset":0}`)
	})
	mux.HandleFunc("POST /-/ci/v1/attempts/{id}/log.append", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		(*seen)["log-body"] = string(raw)
		if strings.Contains(r.Header.Get("Authorization"), "stale") {
			w.WriteHeader(http.StatusConflict)
			write(w, `{"type":"urn:rukie:problem:E_LEASE_LOST","code":"E_LEASE_LOST","status":409}`)
			return
		}
		write(w, `{"ack_offset":4,"next_seq":1}`)
	})
	mux.HandleFunc("PUT /-/ci/v1/attempts/{id}/artifact.put/{digest}", func(w http.ResponseWriter, r *http.Request) {
		(*seen)["artifact-fence"] = r.Header.Get("Rukie-Fence")
		(*seen)["artifact-digest"] = r.PathValue("digest")
		write(w, `{"digest":"`+r.PathValue("digest")+`","size":4}`)
	})
	mux.HandleFunc("POST /-/ci/v1/register", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		(*seen)["register-body"] = string(raw)
		write(w, `{"token":"tok-reg","runner":{"id":"rnr_9"}}`)
	})
	mux.HandleFunc("POST /-/ci/v1/runners/{id}/rotate", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		(*seen)["rotate-auth"] = r.Header.Get("Authorization")
		(*seen)["rotate-body"] = string(raw)
		write(w, `{"token":"tok-rot","fence":3}`)
	})
	mux.HandleFunc("POST /-/ci/v1/attempts/{id}/finish", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		(*seen)["finish-body"] = string(raw)
		write(w, `{"attempt":"completed","job":"passed","run":"succeeded","replayed":false}`)
	})
	return httptest.NewServer(mux)
}

func TestClientLeaseShape(t *testing.T) {
	seen := map[string]string{}
	server := stubServer(t, stubGrant{attempt: "att_1", fence: 7, token: "tok"}, &seen)
	defer server.Close()
	client := NewClient(server.URL, "rnr_1", "runner-token")
	grant, err := client.Lease(1)
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if grant.Attempt.ID != "att_1" || grant.Attempt.Fence != 7 || grant.Attempt.Token != "tok" {
		t.Fatalf("grant = %+v", grant.Attempt)
	}
	if seen["lease-auth"] != "Bearer runner-token" {
		t.Fatalf("lease auth = %q", seen["lease-auth"])
	}
	if grant.Fetch.Token != "fetch-secret" {
		t.Fatalf("grant fetch token = %q, want the fenced credential", grant.Fetch.Token)
	}
	var body map[string]any
	if err := jsonv2.Unmarshal([]byte(seen["lease-body"]), &body); err != nil {
		t.Fatalf("lease body is not JSON: %v", err)
	}
}

func TestClientHeartbeatSendsFence(t *testing.T) {
	seen := map[string]string{}
	server := stubServer(t, stubGrant{attempt: "att_1", fence: 7, token: "tok"}, &seen)
	defer server.Close()
	client := NewClient(server.URL, "rnr_1", "runner-token")
	reply, err := client.Heartbeat(AttemptRef{ID: "att_1", Fence: 7, Token: "tok"})
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if reply.State != "started" {
		t.Fatalf("state = %q", reply.State)
	}
	if !strings.Contains(seen["heartbeat-body"], `"fence":7`) {
		t.Fatalf("heartbeat body misses fence: %s", seen["heartbeat-body"])
	}
}

func TestClientLogChunkDigest(t *testing.T) {
	seen := map[string]string{}
	server := stubServer(t, stubGrant{attempt: "att_1", fence: 7, token: "tok"}, &seen)
	defer server.Close()
	client := NewClient(server.URL, "rnr_1", "runner-token")
	ack, err := client.AppendLog(AttemptRef{ID: "att_1", Fence: 7, Token: "tok"}, 0, 0, []byte("data"), nil)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if ack != 4 {
		t.Fatalf("ack = %d", ack)
	}
	if !strings.Contains(seen["log-body"], Digest([]byte("data"))) {
		t.Fatalf("chunk misses digest: %s", seen["log-body"])
	}
}

func TestClientLeaseLostMaps(t *testing.T) {
	seen := map[string]string{}
	server := stubServer(t, stubGrant{attempt: "att_1", fence: 7, token: "stale-token"}, &seen)
	defer server.Close()
	client := NewClient(server.URL, "rnr_1", "runner-token")
	_, err := client.AppendLog(AttemptRef{ID: "att_1", Fence: 7, Token: "stale-token"}, 0, 0, []byte("data"), nil)
	lost, ok := errors.AsType[*LeaseLostError](err)
	if !ok || lost.Code != "E_LEASE_LOST" {
		t.Fatalf("stale write = %v, want E_LEASE_LOST", err)
	}
}

func TestClientArtifactPut(t *testing.T) {
	seen := map[string]string{}
	server := stubServer(t, stubGrant{attempt: "att_1", fence: 9, token: "tok"}, &seen)
	defer server.Close()
	client := NewClient(server.URL, "rnr_1", "runner-token")
	if err := client.PutArtifact(AttemptRef{ID: "att_1", Fence: 9, Token: "tok"}, []byte("data")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if seen["artifact-fence"] != "9" {
		t.Fatalf("artifact fence = %q", seen["artifact-fence"])
	}
	if seen["artifact-digest"] != Digest([]byte("data")) {
		t.Fatalf("artifact digest = %q", seen["artifact-digest"])
	}
}

func TestClientRegisterConsumesToken(t *testing.T) {
	seen := map[string]string{}
	server := stubServer(t, stubGrant{attempt: "att_1", fence: 7, token: "tok"}, &seen)
	defer server.Close()
	client := NewClient(server.URL, "", "")
	_, pub, err := GenerateSealKey()
	if err != nil {
		t.Fatalf("seal key: %v", err)
	}
	if len(pub) != 32 {
		t.Fatalf("seal pubkey = %d bytes, want 32", len(pub))
	}
	id, token, err := client.Register("rkt_test", pub)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if id != "rnr_9" || token != "tok-reg" {
		t.Fatalf("register = %s %s", id, token)
	}
	if !strings.Contains(seen["register-body"], "rkt_test") {
		t.Fatalf("register misses the token: %s", seen["register-body"])
	}
	if !strings.Contains(seen["register-body"], "pubkey_b64") {
		t.Fatalf("register misses the seal pubkey: %s", seen["register-body"])
	}
}

func TestSealKeyRoundTrip(t *testing.T) {
	priv, pub, err := GenerateSealKey()
	if err != nil {
		t.Fatalf("seal key: %v", err)
	}
	if len(priv) != 32 || len(pub) != 32 {
		t.Fatalf("seal key = %d/%d bytes, want 32/32", len(priv), len(pub))
	}
	priv2, pub2, err := GenerateSealKey()
	if err != nil {
		t.Fatalf("seal key: %v", err)
	}
	if string(priv) == string(priv2) || string(pub) == string(pub2) {
		t.Fatal("seal key generation is not random")
	}
	dir := t.TempDir()
	cred := Credential{RunnerID: "rnr_1", Token: "tok", SealPrivate: priv}
	if err := StoreCredential(dir, cred); err != nil {
		t.Fatalf("store: %v", err)
	}
	loaded, lerr := LoadCredential(dir)
	if lerr != nil {
		t.Fatalf("load: %v", lerr)
	}
	if string(loaded.SealPrivate) != string(priv) {
		t.Fatal("seal private key did not survive the credential round trip")
	}
}

func TestFetchBearerHeader(t *testing.T) {
	grant := &Grant{
		Attempt: AttemptRef{Token: "attempt-token"},
		Fetch:   FetchPayload{Token: "fetch-secret"},
	}
	if got := fetchBearerHeader(grant); got != "AUTHORIZATION: Bearer fetch-secret" {
		t.Fatalf("bearer = %q, want the fetch credential", got)
	}
	grant.Fetch.Token = ""
	if got := fetchBearerHeader(grant); got != "AUTHORIZATION: Bearer attempt-token" {
		t.Fatalf("bearer = %q, want the attempt-token fallback", got)
	}
}

func TestClientRotateFences(t *testing.T) {
	seen := map[string]string{}
	server := stubServer(t, stubGrant{attempt: "att_1", fence: 7, token: "tok"}, &seen)
	defer server.Close()
	client := NewClient(server.URL, "rnr_1", "tok-old")
	token, fence, err := client.Rotate(2)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if token != "tok-rot" || fence != 3 {
		t.Fatalf("rotate = %s %d", token, fence)
	}
	if seen["rotate-auth"] != "Bearer tok-old" {
		t.Fatalf("rotate auth = %q", seen["rotate-auth"])
	}
	if !strings.Contains(seen["rotate-body"], `"fence":2`) {
		t.Fatalf("rotate misses the fence: %s", seen["rotate-body"])
	}
}

func TestClientFinishVerdict(t *testing.T) {
	seen := map[string]string{}
	server := stubServer(t, stubGrant{attempt: "att_1", fence: 9, token: "tok"}, &seen)
	defer server.Close()
	client := NewClient(server.URL, "rnr_1", "runner-token")
	reply, err := client.Finish(AttemptRef{ID: "att_1", Fence: 9, Token: "tok"}, FinishInput{
		Outcome: "passed", Fence: 9, LogSHA: "sha256:aa", LogEnd: 4,
	})
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if reply.Job != "passed" || reply.Replayed {
		t.Fatalf("finish = %+v", reply)
	}
	if !strings.Contains(seen["finish-body"], `"outcome":"passed"`) {
		t.Fatalf("finish body misses outcome: %s", seen["finish-body"])
	}
}

func TestMapVerdict(t *testing.T) {
	for _, row := range []struct {
		outcome   string
		class     string
		exit      int
		cancelled bool
	}{
		{"passed", "", 0, false},
		{"failed", "script_failure", 1, false},
		{"errored", "timeout", 3, false},
		{"errored", "infra", 4, false},
		{"errored", "cancelled", 0, true},
		{"errored", "cancelled", 1, true},
	} {
		outcome, class := MapVerdict(row.exit, row.cancelled)
		if outcome != row.outcome || class != row.class {
			t.Errorf("exit %d cancelled %v = %s/%s, want %s/%s", row.exit, row.cancelled, outcome, class, row.outcome, row.class)
		}
	}
}
