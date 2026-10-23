package runner

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	jsonv2 "encoding/json/v2"
)

type shipLog struct {
	seqs   []int64
	bodies []string
	mu     sync.Mutex
	calls  int
	fail   int
}

func (s *shipLog) handler(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var in struct {
		Data   string `json:"data_b64"`
		Seq    int64  `json:"seq"`
		Offset int64  `json:"offset"`
	}
	_ = jsonv2.Unmarshal(raw, &in)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.seqs = append(s.seqs, in.Seq)
	if s.calls <= s.fail {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"type":"urn:rukie:problem:E_GATEWAY","code":"E_GATEWAY","status":500}`))
		return
	}
	data, _ := base64.StdEncoding.DecodeString(in.Data)
	s.bodies = append(s.bodies, string(data))
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ack_offset":` + strconv.FormatInt(in.Offset+int64(len(data)), 10) + `,"next_seq":` + strconv.FormatInt(in.Seq+1, 10) + `}`))
}

func writeLogFile(t *testing.T, dir, stream string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, stream), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLogShipperRetriesSameSeq(t *testing.T) {
	log := &shipLog{fail: 1}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /-/ci/v1/attempts/{id}/log.append", log.handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := NewClient(srv.URL, "rnr_1", "tok")
	logDir := t.TempDir()
	content := []byte("hello log bytes")
	writeLogFile(t, logDir, "job.stdout", content)
	stream := &logStreamer{client: client, attempt: AttemptRef{ID: "att_1", Fence: 7, Token: "tok"}, logDir: logDir, seq: -1}
	stream.ship()
	if stream.seq != -1 || stream.end != 0 {
		t.Fatalf("failed ship advanced to seq=%d end=%d, want -1 0", stream.seq, stream.end)
	}
	stream.ship()
	if stream.seq != 0 || stream.end != int64(len(content)) {
		t.Fatalf("retry ship = seq %d end %d, want 0 %d", stream.seq, stream.end, len(content))
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.seqs) != 2 || log.seqs[0] != 0 || log.seqs[1] != 0 {
		t.Fatalf("server seqs = %v, want [0 0] (same chunk resent)", log.seqs)
	}
	if Digest([]byte(strings.Join(log.bodies, ""))) != Digest(content) {
		t.Fatal("shipped bytes do not cover the log")
	}
}

func TestLogShipperShipsMultiChunk(t *testing.T) {
	log := &shipLog{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /-/ci/v1/attempts/{id}/log.append", log.handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := NewClient(srv.URL, "rnr_1", "tok")
	logDir := t.TempDir()
	full := []byte(strings.Repeat("x", 3*ChunkMax+12345))
	writeLogFile(t, logDir, "job.stdout", full)
	writeLogFile(t, logDir, "job.stderr", []byte("err"))
	stream := &logStreamer{client: client, attempt: AttemptRef{ID: "att_1", Fence: 7, Token: "tok"}, logDir: logDir, seq: -1}
	stream.ship()
	wantCalls := 4 + 1
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.calls != wantCalls {
		t.Fatalf("append calls = %d, want %d", log.calls, wantCalls)
	}
	if stream.end != int64(len(full)+3) {
		t.Fatalf("end = %d, want %d", stream.end, len(full)+3)
	}
	before := log.calls
	stream.ship()
	if log.calls != before {
		t.Fatal("quiescent ship sent more chunks")
	}
	if stream.digest() != Digest(append(bytes.Clone(full), []byte("err")...)) {
		t.Fatal("digest misses shipped bytes")
	}
}

func TestFinishRetriesShipBeforeCompleting(t *testing.T) {
	content := []byte("final chunk bytes")
	log := &shipLog{fail: 1}
	var mu sync.Mutex
	var ends []int64
	var finishes int
	mux := http.NewServeMux()
	mux.HandleFunc("POST /-/ci/v1/attempts/{id}/log.append", log.handler)
	mux.HandleFunc("POST /-/ci/v1/attempts/{id}/finish", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var in struct {
			Log struct {
				End int64 `json:"end_offset"`
			} `json:"log"`
		}
		_ = jsonv2.Unmarshal(raw, &in)
		mu.Lock()
		defer mu.Unlock()
		finishes++
		ends = append(ends, in.Log.End)
		if in.Log.End != int64(len(content)) {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"type":"urn:rukie:problem:E_LOG_INCOMPLETE","code":"E_LOG_INCOMPLETE","status":409}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := NewClient(srv.URL, "rnr_1", "tok")
	dir := t.TempDir()
	work := t.TempDir()
	writeLogFile(t, dir, "job.stdout", content)
	l := &Loop{Client: client}
	done := AttemptDone{
		Grant:   &Grant{Attempt: AttemptRef{ID: "att_1", Fence: 7, Token: "tok"}},
		Stream:  &logStreamer{client: client, attempt: AttemptRef{ID: "att_1", Fence: 7, Token: "tok"}, logDir: dir, seq: -1},
		Outcome: "passed", Class: "success",
		Work: AttemptWork{Dir: work, Work: work},
	}
	l.finish(t.Context(), done)
	mu.Lock()
	defer mu.Unlock()
	if finishes < 2 {
		t.Fatalf("finish attempts = %d, want >= 2 (stale first, complete retry)", finishes)
	}
	if ends[len(ends)-1] != int64(len(content)) {
		t.Fatalf("final end_offset = %d, want %d", ends[len(ends)-1], len(content))
	}
	if done.Stream.seq != 0 || done.Stream.end != int64(len(content)) {
		t.Fatalf("streamer = seq %d end %d, want 0 %d", done.Stream.seq, done.Stream.end, len(content))
	}
}
