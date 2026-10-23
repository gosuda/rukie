package webhook

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/store"
)

type fakeStore struct {
	now       time.Time
	endpoints map[string]*store.WebhookEndpointRow
	messages  map[string]*store.WebhookMessageRow
	attempts  []store.WebhookAttemptInsert
	mu        sync.Mutex
}

func newFakeStore(now time.Time) *fakeStore {
	return &fakeStore{now: now, endpoints: map[string]*store.WebhookEndpointRow{}, messages: map[string]*store.WebhookMessageRow{}}
}

func (f *fakeStore) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeStore) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

func (f *fakeStore) sealEndpoint(t *testing.T, id string, mutate func(*store.WebhookEndpointRow)) []byte {
	t.Helper()
	key := []byte("0123456789abcdef0123456789abcdef")
	raw := []byte("raw-secret-for-endpoint-" + id + "-pad!!")
	sealed, err := SealSecret(key, raw, secretAAD(id))
	if err != nil {
		t.Fatalf("SealSecret: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	ep := &store.WebhookEndpointRow{ID: id, Scope: "repo", RepoID: 7, URL: "https://example.com/hook", Active: true, Concurrency: 4, TimeoutSeconds: 20, LastStatus: "never", SecretEnc: sealed}
	if mutate != nil {
		mutate(ep)
	}
	f.endpoints[id] = ep
	return raw
}

func (f *fakeStore) addMessage(webhookID, endpointID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages[webhookID] = &store.WebhookMessageRow{ID: webhookID, EndpointID: endpointID, EventType: "ref.updated", EventTime: f.now.UnixMilli(), Payload: []byte(`{"id":"` + webhookID + `"}`), State: "pending", NextAttemptAt: f.now.UnixMilli(), UpdatedAt: f.now.UnixMilli()}
}

func (f *fakeStore) getMessage(id string) store.WebhookMessageRow {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.messages[id]
}

func (f *fakeStore) SweepWebhookLeases(_ context.Context, nowMillis int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for _, m := range f.messages {
		if m.State == "inflight" && m.LeaseExpiresAt <= nowMillis {
			m.State = "pending"
			m.LeaseExpiresAt = 0
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) ClaimWebhookMessages(_ context.Context, nowMillis int64, batch int, perEndpointCap int) ([]store.WebhookMessageRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if batch <= 0 {
		batch = 64
	}
	pool := make([]*store.WebhookMessageRow, 0, len(f.messages))
	for _, m := range f.messages {
		if m.State == "pending" && m.NextAttemptAt <= nowMillis {
			pool = append(pool, m)
		}
	}
	slices.SortFunc(pool, func(a, b *store.WebhookMessageRow) int {
		if c := cmp.Compare(a.NextAttemptAt, b.NextAttemptAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	inflight := map[string]int64{}
	for _, m := range f.messages {
		if m.State == "inflight" {
			inflight[m.EndpointID]++
		}
	}
	var out []store.WebhookMessageRow
	for _, m := range pool {
		if len(out) >= batch {
			break
		}
		ep := f.endpoints[m.EndpointID]
		if ep == nil || !ep.Active {
			continue
		}
		limit := int64(perEndpointCap)
		if limit <= 0 {
			limit = ep.Concurrency
		}
		if limit <= 0 {
			limit = 4
		}
		if inflight[m.EndpointID] >= limit {
			continue
		}
		m.State = "inflight"
		m.LeaseExpiresAt = nowMillis + 5*60*1000
		m.UpdatedAt = nowMillis
		inflight[m.EndpointID]++
		out = append(out, *m)
	}
	return out, nil
}

func (f *fakeStore) GetWebhookEndpoint(_ context.Context, id string) (store.WebhookEndpointRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ep, ok := f.endpoints[id]
	if !ok {
		return store.WebhookEndpointRow{}, store.ErrNotFound
	}
	return *ep, nil
}

func (f *fakeStore) RecordWebhookAttempt(_ context.Context, attempt store.WebhookAttemptInsert) error {
	if attempt.MessageID == "" || attempt.AttemptNo <= 0 {
		return store.ErrInvalid
	}
	if attempt.StartedAt <= 0 || attempt.FinishedAt <= 0 {
		return store.ErrInvalid
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts = append(f.attempts, attempt)
	return nil
}

func (f *fakeStore) MarkWebhookDelivered(_ context.Context, id string, nowMillis, statusCode int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.messages[id]
	if !ok {
		return store.ErrNotFound
	}
	m.State = "delivered"
	m.DeliveredAt = nowMillis
	m.LastStatusCode = statusCode
	m.LastError = ""
	m.UpdatedAt = nowMillis
	return nil
}

func (f *fakeStore) MarkWebhookDead(_ context.Context, id, errText string, nowMillis, statusCode int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.messages[id]
	if !ok {
		return store.ErrNotFound
	}
	m.State = "dead"
	m.LastError = errText
	m.LastStatusCode = statusCode
	m.UpdatedAt = nowMillis
	return nil
}

func (f *fakeStore) RequeueWebhookMessage(_ context.Context, id, errText string, statusCode, nextAttemptAt, nowMillis int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.messages[id]
	if !ok {
		return store.ErrNotFound
	}
	if nextAttemptAt <= 0 {
		return store.ErrInvalid
	}
	m.State = "pending"
	m.AttemptCount++
	m.LastError = errText
	m.LastStatusCode = statusCode
	m.NextAttemptAt = nextAttemptAt
	m.LeaseExpiresAt = 0
	m.UpdatedAt = nowMillis
	return nil
}

func (f *fakeStore) RedeliverWebhookMessage(_ context.Context, id string, nowMillis int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.messages[id]
	if !ok {
		return store.ErrNotFound
	}
	if m.State != "dead" && m.State != "delivered" {
		return store.ErrInvalid
	}
	m.State = "pending"
	m.AttemptCount = 0
	m.NextAttemptAt = nowMillis
	m.LastError = ""
	m.UpdatedAt = nowMillis
	return nil
}

func (f *fakeStore) NoteEndpointResult(_ context.Context, endpointID string, consecutiveFailures int64, lastStatus string, disable bool, disableReason string, nowMillis int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ep, ok := f.endpoints[endpointID]
	if !ok {
		return store.ErrNotFound
	}
	ep.ConsecutiveFailures = consecutiveFailures
	ep.LastStatus = lastStatus
	if disable {
		ep.Active = false
		ep.DisableReason = disableReason
		ep.DisabledAt = nowMillis
	}
	return nil
}

func (f *fakeStore) PruneWebhookHistory(_ context.Context, beforeMillis int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for id, m := range f.messages {
		if (m.State == "delivered" || m.State == "dead") && m.UpdatedAt < beforeMillis {
			delete(f.messages, id)
			n++
		}
	}
	return n, nil
}

func newHookServer(t *testing.T, id, ts, sig, contentType, body *string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if id != nil {
			*id = r.Header.Get("webhook-id")
		}
		if ts != nil {
			*ts = r.Header.Get("webhook-timestamp")
		}
		if sig != nil {
			*sig = r.Header.Get("webhook-signature")
		}
		if contentType != nil {
			*contentType = r.Header.Get("Content-Type")
		}
		if body != nil {
			*body = string(raw)
		}
		w.WriteHeader(status)
	}))
}

func testWorker(fake *fakeStore, send SendFunc) *Worker {
	return NewWorker(WorkerConfig{SealKey: []byte("0123456789abcdef0123456789abcdef"), Now: fake.clock, Send: send, backend: fake}, nil)
}

func statusSend(status int64) SendFunc {
	return func(_ context.Context, _ store.WebhookMessageRow, _ store.WebhookEndpointRow, _ DeliverySecrets, _ int64, body []byte) (int64, []byte, []byte, string) {
		return status, []byte(`{}`), bytes.Clone(body), ""
	}
}

func TestWorkerBackoffTable(t *testing.T) {
	want := []time.Duration{5 * time.Second, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 5 * time.Hour, 10 * time.Hour, 14 * time.Hour, 20 * time.Hour, 24 * time.Hour}
	for i, w := range want {
		if got := BackoffForAttempt(int64(i + 1)); got != w {
			t.Fatalf("BackoffForAttempt(%d) = %v, want %v", i+1, got, w)
		}
	}
	if got := BackoffForAttempt(0); got != want[0] {
		t.Fatalf("BackoffForAttempt(0) = %v, want %v", got, want[0])
	}
	if got := BackoffForAttempt(99); got != want[len(want)-1] {
		t.Fatalf("BackoffForAttempt(99) = %v, want %v", got, want[len(want)-1])
	}
}

func TestWorkerApplyJitterBounds(t *testing.T) {
	const d = 10 * time.Second
	var lo, hi time.Duration = d, 0
	for range 1000 {
		got := ApplyJitter(d)
		if got < 8*time.Second || got > 12*time.Second {
			t.Fatalf("ApplyJitter(%v) = %v, want within [0.8d,1.2d]", d, got)
		}
		if got < lo {
			lo = got
		}
		if got > hi {
			hi = got
		}
	}
	if lo >= d || hi <= d {
		t.Fatal("ApplyJitter never varied")
	}
}

func TestWorkerParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	at, ok := ParseRetryAfter("120", now)
	if !ok || !at.Equal(now.Add(120*time.Second)) {
		t.Fatalf("delta = %v,%v", at, ok)
	}
	at, ok = ParseRetryAfter("99999999", now)
	if !ok || !at.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("capped delta = %v,%v", at, ok)
	}
	at, ok = ParseRetryAfter("Tue, 20 Oct 2026 09:00:00 GMT", now)
	if !ok || !at.Equal(now.Add(time.Hour)) {
		t.Fatalf("date = %v,%v", at, ok)
	}
	if _, ok := ParseRetryAfter("soon", now); ok {
		t.Fatal("garbage accepted")
	}
	if _, ok := ParseRetryAfter("", now); ok {
		t.Fatal("empty accepted")
	}
	if _, ok := ParseRetryAfter("-5", now); ok {
		t.Fatal("negative accepted")
	}
}

func TestWorkerApplyFailureTransitions(t *testing.T) {
	cases := []struct {
		status  string
		in      int64
		disable bool
	}{{"", 49, false}, {"failing", 50, false}, {"failing", 51, false}, {"failing", 199, false}, {"disabled", 200, true}, {"disabled", 201, true}}
	for _, c := range cases {
		status, disable := applyFailure(c.in)
		if status != c.status || disable != c.disable {
			t.Fatalf("applyFailure(%d) = (%q,%v), want (%q,%v)", c.in, status, disable, c.status, c.disable)
		}
	}
}

func TestWorkerTenFailuresThenDead(t *testing.T) {
	now := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	fake := newFakeStore(now)
	fake.sealEndpoint(t, "ep1", nil)
	fake.addMessage(goldenMsgID, "ep1")
	w := testWorker(fake, statusSend(500))
	for i := int64(1); i <= 10; i++ {
		fake.mu.Lock()
		fake.now = time.UnixMilli(fake.messages[goldenMsgID].NextAttemptAt)
		fake.mu.Unlock()
		before := fake.clock()
		if err := w.DriveOnce(t.Context()); err != nil {
			t.Fatalf("drive %d: %v", i, err)
		}
		got := fake.getMessage(goldenMsgID)
		if i < 10 {
			if got.State != "pending" || got.AttemptCount != i {
				t.Fatalf("drive %d: state=%q attempts=%d", i, got.State, got.AttemptCount)
			}
			delay := time.Duration(got.NextAttemptAt-before.UnixMilli()) * time.Millisecond
			base := BackoffForAttempt(i)
			if delay < base*8/10 || delay > base*12/10 {
				t.Fatalf("drive %d: delay=%v, want within [0.8,1.2]*%v", i, delay, base)
			}
		} else if got.State != "dead" {
			t.Fatalf("drive 10: state=%q, want dead", got.State)
		}
	}
	if len(fake.attempts) != 10 {
		t.Fatalf("attempts = %d, want 10", len(fake.attempts))
	}
	for i, a := range fake.attempts {
		if a.AttemptNo != int64(i+1) {
			t.Fatalf("attempt %d has no %d", i, a.AttemptNo)
		}
	}
}

func TestWorkerRedeliverResets(t *testing.T) {
	fake := newFakeStore(time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC))
	fake.sealEndpoint(t, "ep1", nil)
	fake.addMessage(goldenMsgID, "ep1")
	status := int64(500)
	w := testWorker(fake, func(ctx context.Context, m store.WebhookMessageRow, e store.WebhookEndpointRow, secrets DeliverySecrets, ts int64, b []byte) (int64, []byte, []byte, string) {
		return statusSend(status)(ctx, m, e, secrets, ts, b)
	})
	for range 10 {
		fake.mu.Lock()
		fake.now = time.UnixMilli(fake.messages[goldenMsgID].NextAttemptAt)
		fake.mu.Unlock()
		if err := w.DriveOnce(t.Context()); err != nil {
			t.Fatalf("drive: %v", err)
		}
	}
	if got := fake.getMessage(goldenMsgID); got.State != "dead" {
		t.Fatalf("state=%q, want dead", got.State)
	}
	if err := fake.RedeliverWebhookMessage(t.Context(), goldenMsgID, fake.clock().UnixMilli()); err != nil {
		t.Fatalf("redeliver: %v", err)
	}
	status = 200
	if err := w.DriveOnce(t.Context()); err != nil {
		t.Fatalf("drive: %v", err)
	}
	if got := fake.getMessage(goldenMsgID); got.State != "delivered" || got.ID != goldenMsgID {
		t.Fatalf("after redeliver: %+v", got)
	}
}

func TestWorker410DisablesEndpoint(t *testing.T) {
	fake := newFakeStore(time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC))
	fake.sealEndpoint(t, "ep1", nil)
	fake.addMessage(goldenMsgID, "ep1")
	w := testWorker(fake, statusSend(410))
	if err := w.DriveOnce(t.Context()); err != nil {
		t.Fatalf("drive: %v", err)
	}
	if got := fake.getMessage(goldenMsgID); got.State != "dead" || got.LastError != "endpoint gone (410)" {
		t.Fatalf("message = %+v", got)
	}
	ep, _ := fake.GetWebhookEndpoint(t.Context(), "ep1")
	if ep.Active || ep.DisableReason != "410-gone" || ep.DisabledAt == 0 {
		t.Fatalf("endpoint = %+v", ep)
	}
}

func TestWorkerRetryAfterOverrides(t *testing.T) {
	now := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	fake := newFakeStore(now)
	fake.sealEndpoint(t, "ep1", nil)
	fake.addMessage(goldenMsgID, "ep1")
	w := testWorker(fake, func(context.Context, store.WebhookMessageRow, store.WebhookEndpointRow, DeliverySecrets, int64, []byte) (int64, []byte, []byte, string) {
		return 503, []byte(`{"retry-after":"120"}`), nil, ""
	})
	if err := w.DriveOnce(t.Context()); err != nil {
		t.Fatalf("drive: %v", err)
	}
	got := fake.getMessage(goldenMsgID)
	delay := time.Duration(got.NextAttemptAt-now.UnixMilli()) * time.Millisecond
	if delay < 108*time.Second || delay > 132*time.Second {
		t.Fatalf("retry-after delay = %v, want ~120s within 10%%", delay)
	}
	fake2 := newFakeStore(now)
	fake2.sealEndpoint(t, "ep1", nil)
	msgID2 := "msg_01J9Z8X7Q6W5E4R3T2Y179B77C"
	fake2.addMessage(msgID2, "ep1")
	w2 := testWorker(fake2, func(context.Context, store.WebhookMessageRow, store.WebhookEndpointRow, DeliverySecrets, int64, []byte) (int64, []byte, []byte, string) {
		return 503, []byte(`{"retry-after":"soon"}`), nil, ""
	})
	if err := w2.DriveOnce(t.Context()); err != nil {
		t.Fatalf("drive: %v", err)
	}
	got2 := fake2.getMessage(msgID2)
	delay2 := time.Duration(got2.NextAttemptAt-now.UnixMilli()) * time.Millisecond
	base := BackoffForAttempt(1)
	if delay2 < base*8/10 || delay2 > base*12/10 {
		t.Fatalf("garbage retry-after delay = %v, want table %v bounds", delay2, base)
	}
}

func TestWorkerSameIDAcrossAttempts(t *testing.T) {
	now := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	fake := newFakeStore(now)
	fake.sealEndpoint(t, "ep1", nil)
	fake.addMessage(goldenMsgID, "ep1")
	var ids []string
	var stamps []int64
	status := int64(500)
	w := testWorker(fake, func(_ context.Context, m store.WebhookMessageRow, _ store.WebhookEndpointRow, _ DeliverySecrets, ts int64, _ []byte) (int64, []byte, []byte, string) {
		ids = append(ids, m.ID)
		stamps = append(stamps, ts)
		return status, nil, nil, ""
	})
	if err := w.DriveOnce(t.Context()); err != nil {
		t.Fatalf("drive 1: %v", err)
	}
	fake.advance(61 * time.Second)
	fake.mu.Lock()
	fake.messages[goldenMsgID].NextAttemptAt = fake.now.UnixMilli()
	fake.mu.Unlock()
	status = 200
	if err := w.DriveOnce(t.Context()); err != nil {
		t.Fatalf("drive 2: %v", err)
	}
	if len(ids) != 2 || ids[0] != ids[1] || ids[0] != goldenMsgID {
		t.Fatalf("webhook ids across attempts = %q", ids)
	}
	if stamps[0] == stamps[1] {
		t.Fatal("webhook-timestamp did not rotate between attempts")
	}
	if got := fake.getMessage(goldenMsgID); got.State != "delivered" {
		t.Fatalf("state=%q, want delivered", got.State)
	}
}

func TestWorkerSuccessResetsCounters(t *testing.T) {
	fake := newFakeStore(time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC))
	fake.sealEndpoint(t, "ep1", func(ep *store.WebhookEndpointRow) { ep.ConsecutiveFailures = 7; ep.LastStatus = "failing" })
	fake.addMessage(goldenMsgID, "ep1")
	w := testWorker(fake, statusSend(200))
	if err := w.DriveOnce(t.Context()); err != nil {
		t.Fatalf("drive: %v", err)
	}
	if got := fake.getMessage(goldenMsgID); got.State != "delivered" {
		t.Fatalf("state=%q", got.State)
	}
	ep, _ := fake.GetWebhookEndpoint(t.Context(), "ep1")
	if ep.ConsecutiveFailures != 0 || ep.LastStatus != "ok" {
		t.Fatalf("endpoint = %+v", ep)
	}
}

func TestWorkerMissingEndpointDead(t *testing.T) {
	fake := newFakeStore(time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC))
	msg := store.WebhookMessageRow{ID: goldenMsgID, EndpointID: "missing", EventType: "task.created", Payload: []byte(`{}`), State: "inflight", UpdatedAt: fake.clock().UnixMilli()}
	fake.mu.Lock()
	fake.messages[goldenMsgID] = &msg
	fake.mu.Unlock()
	w := testWorker(fake, statusSend(200))
	if err := w.deliverOne(t.Context(), msg); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if got := fake.getMessage(goldenMsgID); got.State != "dead" || got.LastError != "undeliverable-endpoint-removed" {
		t.Fatalf("message = %+v", got)
	}
	if len(fake.attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(fake.attempts))
	}
	orphan := fake.attempts[0]
	if orphan.AttemptNo != 1 || orphan.HTTPStatus != 0 || orphan.Error != "undeliverable-endpoint-removed" {
		t.Fatalf("orphan attempt = %+v", orphan)
	}
	if strings.Contains(string(orphan.ReqHeaders), "signature") {
		t.Fatalf("orphan req headers leak signature: %s", orphan.ReqHeaders)
	}
}

func TestWorkerInactiveEndpointRequeues(t *testing.T) {
	fake := newFakeStore(time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC))
	fake.sealEndpoint(t, "ep1", func(ep *store.WebhookEndpointRow) { ep.Active = false })
	now := fake.clock().UnixMilli()
	msg := store.WebhookMessageRow{ID: goldenMsgID, EndpointID: "ep1", EventType: "task.created", Payload: []byte(`{}`), State: "inflight", NextAttemptAt: now, UpdatedAt: now}
	fake.mu.Lock()
	fake.messages[goldenMsgID] = &msg
	fake.mu.Unlock()
	called := false
	w := testWorker(fake, func(context.Context, store.WebhookMessageRow, store.WebhookEndpointRow, DeliverySecrets, int64, []byte) (int64, []byte, []byte, string) {
		called = true
		return 200, nil, nil, ""
	})
	if err := w.deliverOne(t.Context(), msg); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if called {
		t.Fatal("Send ran for an inactive endpoint")
	}
	got := fake.getMessage(goldenMsgID)
	if got.State != "pending" || got.AttemptCount != 1 || got.NextAttemptAt <= now {
		t.Fatalf("message = %+v", got)
	}
	if len(fake.attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(fake.attempts))
	}
	unsent := fake.attempts[0]
	if unsent.AttemptNo != 1 || unsent.HTTPStatus != 0 || unsent.Error != "endpoint inactive" {
		t.Fatalf("unsent attempt = %+v", unsent)
	}
	if unsent.AttemptTimestamp != now/1000 || unsent.StartedAt != now || unsent.FinishedAt != now || unsent.DurationMs != 0 {
		t.Fatalf("unsent attempt times = %+v", unsent)
	}
	if string(unsent.ReqHeaders) != `{"webhook-id":"`+goldenMsgID+`"}` {
		t.Fatalf("unsent req headers = %s", unsent.ReqHeaders)
	}
	if strings.Contains(string(unsent.ReqHeaders), "signature") {
		t.Fatalf("unsent req headers leak signature: %s", unsent.ReqHeaders)
	}
}

func TestWorkerTransportErrors(t *testing.T) {
	now := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	fake := newFakeStore(now)
	fake.sealEndpoint(t, "ep1", nil)
	deniedID := "msg_01J9Z8X7Q6W5E4R3T2Y179D99E"
	fake.addMessage(deniedID, "ep1")
	calls := map[string]string{deniedID: "denied by egress policy: address 10.0.0.1 is denied"}
	w := testWorker(fake, func(_ context.Context, m store.WebhookMessageRow, _ store.WebhookEndpointRow, _ DeliverySecrets, _ int64, _ []byte) (int64, []byte, []byte, string) {
		return 0, nil, nil, calls[m.ID]
	})
	if err := w.DriveOnce(t.Context()); err != nil {
		t.Fatalf("drive: %v", err)
	}
	if got := fake.getMessage(deniedID); got.State != "pending" || got.AttemptCount != 1 {
		t.Fatalf("denied message = %+v", got)
	}
	fake2 := newFakeStore(now)
	fake2.sealEndpoint(t, "ep1", nil)
	fake2.addMessage(goldenMsgID, "ep1")
	w2 := testWorker(fake2, func(context.Context, store.WebhookMessageRow, store.WebhookEndpointRow, DeliverySecrets, int64, []byte) (int64, []byte, []byte, string) {
		return 0, nil, nil, "invalid webhook url: bad port"
	})
	if err := w2.DriveOnce(t.Context()); err != nil {
		t.Fatalf("drive: %v", err)
	}
	if got := fake2.getMessage(goldenMsgID); got.State != "dead" {
		t.Fatalf("malformed message state=%q, want dead", got.State)
	}
	if len(fake2.attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(fake2.attempts))
	}
	bad := fake2.attempts[0]
	if bad.HTTPStatus != 0 || !strings.HasPrefix(bad.Error, "invalid webhook url") {
		t.Fatalf("malformed attempt = %+v", bad)
	}
	if got, want := strings.Count(string(bad.ReqHeaders), "v1,<omitted>"), 1; got != want {
		t.Fatalf("malformed req headers hold %d redacted entries, want %d: %s", got, want, bad.ReqHeaders)
	}
	if bad.AttemptTimestamp == 0 {
		t.Fatal("malformed attempt misses timestamp")
	}
}

func TestWorkerCounterSuspension(t *testing.T) {
	now := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	fake := newFakeStore(now)
	fake.sealEndpoint(t, "ep1", func(ep *store.WebhookEndpointRow) { ep.ConsecutiveFailures = 49 })
	fake.addMessage(goldenMsgID, "ep1")
	w := testWorker(fake, statusSend(500))
	if err := w.DriveOnce(t.Context()); err != nil {
		t.Fatalf("drive: %v", err)
	}
	ep, _ := fake.GetWebhookEndpoint(t.Context(), "ep1")
	if ep.ConsecutiveFailures != 50 || ep.LastStatus != "failing" || !ep.Active {
		t.Fatalf("endpoint at 50 = %+v", ep)
	}
	fake.advance(time.Hour)
	fake.mu.Lock()
	fake.messages[goldenMsgID].NextAttemptAt = fake.now.UnixMilli()
	fake.endpoints["ep1"].ConsecutiveFailures = 199
	fake.mu.Unlock()
	if err := w.DriveOnce(t.Context()); err != nil {
		t.Fatalf("drive: %v", err)
	}
	ep, _ = fake.GetWebhookEndpoint(t.Context(), "ep1")
	if ep.ConsecutiveFailures != 200 || ep.Active || ep.DisableReason != "undeliverable" {
		t.Fatalf("endpoint at 200 = %+v", ep)
	}
}

func TestWorkerSecretOpenFailure(t *testing.T) {
	fake := newFakeStore(time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC))
	fake.sealEndpoint(t, "ep1", func(ep *store.WebhookEndpointRow) { ep.SecretEnc = []byte("bogus") })
	fake.addMessage(goldenMsgID, "ep1")
	called := false
	w := testWorker(fake, func(context.Context, store.WebhookMessageRow, store.WebhookEndpointRow, DeliverySecrets, int64, []byte) (int64, []byte, []byte, string) {
		called = true
		return 200, nil, nil, ""
	})
	if err := w.DriveOnce(t.Context()); err != nil {
		t.Fatalf("drive: %v", err)
	}
	if called {
		t.Fatal("Send ran with an undecryptable secret")
	}
	if got := fake.getMessage(goldenMsgID); got.State != "pending" || got.AttemptCount != 1 {
		t.Fatalf("message = %+v", got)
	}
	if len(fake.attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(fake.attempts))
	}
	sealed := fake.attempts[0]
	if sealed.HTTPStatus != 0 || sealed.Error != "webhook secret unavailable" {
		t.Fatalf("unsealed attempt = %+v", sealed)
	}
	if strings.Contains(string(sealed.ReqHeaders), "signature") {
		t.Fatalf("unsealed req headers leak signature: %s", sealed.ReqHeaders)
	}
}

func TestWorkerSweepAndJanitor(t *testing.T) {
	now := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	fake := newFakeStore(now)
	fake.sealEndpoint(t, "ep1", nil)
	fake.addMessage(goldenMsgID, "ep1")
	fake.mu.Lock()
	fake.messages[goldenMsgID].State = "inflight"
	fake.messages[goldenMsgID].LeaseExpiresAt = now.Add(-time.Minute).UnixMilli()
	fake.mu.Unlock()
	w := testWorker(fake, statusSend(200))
	swept, err := w.SweepOnce(t.Context())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if swept != 1 || fake.getMessage(goldenMsgID).State != "pending" {
		t.Fatalf("swept=%d state=%q", swept, fake.getMessage(goldenMsgID).State)
	}
	oldID := "msg_01J9Z8X7Q6W5E4R3T2Y179F11A"
	fake.addMessage(oldID, "ep1")
	fake.mu.Lock()
	fake.messages[oldID].State = "delivered"
	fake.messages[oldID].UpdatedAt = now.Add(-2 * time.Hour).UnixMilli()
	fake.mu.Unlock()
	freshID := "msg_01J9Z8X7Q6W5E4R3T2Y179A22B"
	fake.addMessage(freshID, "ep1")
	fake.mu.Lock()
	fake.messages[freshID].State = "delivered"
	fake.messages[freshID].UpdatedAt = now.UnixMilli()
	fake.mu.Unlock()
	pruned, err := w.JanitorOnce(t.Context(), time.Hour)
	if err != nil {
		t.Fatalf("janitor: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned=%d, want 1", pruned)
	}
	fake.mu.Lock()
	_, freshKept := fake.messages[freshID]
	fake.mu.Unlock()
	if !freshKept {
		t.Fatal("janitor pruned a fresh row")
	}
}

func TestWorkerDefaultSendVerifiesSignature(t *testing.T) {
	var gotID, gotTS, gotSig, gotBody string
	var gotCT string
	server := newHookServer(t, &gotID, &gotTS, &gotSig, &gotCT, &gotBody, http.StatusOK)
	now := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	fake := newFakeStore(now)
	raw := fake.sealEndpoint(t, "ep1", func(ep *store.WebhookEndpointRow) { ep.URL = server.URL + "/ok" })
	fake.addMessage(goldenMsgID, "ep1")
	policy := &Policy{AllowInsecureHTTP: true, AllowPrivateTargets: true}
	w := NewWorker(WorkerConfig{SealKey: []byte("0123456789abcdef0123456789abcdef"), Now: fake.clock, backend: fake}, policy)
	if err := w.DriveOnce(t.Context()); err != nil {
		t.Fatalf("drive: %v", err)
	}
	if gotID != goldenMsgID {
		t.Fatalf("webhook-id = %q", gotID)
	}
	if gotCT != "application/json; charset=utf-8" {
		t.Fatalf("content-type = %q", gotCT)
	}
	ts, err := strconv.ParseInt(gotTS, 10, 64)
	if err != nil {
		t.Fatalf("webhook-timestamp = %q", gotTS)
	}
	if want := Sign(raw, goldenMsgID, ts, []byte(gotBody)); gotSig != want {
		t.Fatalf("signature = %q, want %q", gotSig, want)
	}
	if !Verify([][]byte{raw}, goldenMsgID, ts, []byte(gotBody), gotSig, time.Unix(ts, 0)) {
		t.Fatal("default Send signature does not verify")
	}
	if got := fake.getMessage(goldenMsgID); got.State != "delivered" {
		t.Fatalf("state=%q, want delivered", got.State)
	}
	moved := newHookServer(t, nil, nil, nil, nil, nil, http.StatusMovedPermanently)
	fake.sealEndpoint(t, "ep2", func(ep *store.WebhookEndpointRow) { ep.URL = moved.URL })
	msgID2 := "msg_01J9Z8X7Q6W5E4R3T2Y179C44D"
	fake.addMessage(msgID2, "ep2")
	if err := w.DriveOnce(t.Context()); err != nil {
		t.Fatalf("drive: %v", err)
	}
	if got := fake.getMessage(msgID2); got.State != "pending" {
		t.Fatalf("301 message state=%q, want pending retry", got.State)
	}
}

func TestWorkerStoreTxIntegration(t *testing.T) {
	ctx := t.Context()
	s, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "webhook.db")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	key := []byte("0123456789abcdef0123456789abcdef")
	now := time.Now()
	user, err := s.InsertAuthUser(ctx, store.AuthUserCreate{Name: "hook", Lower: "hook", Email: "hook@example.test", Hash: "test-hash", Algo: "argon2id-v19", Now: now.UnixMilli()})
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	var repoID int64
	if err := s.Write(ctx, func(tx *store.WriteTx) error {
		var err error
		repoID, err = tx.InsertRepo(now, store.RepoInsert{Name: "hooks", Visibility: "private", DefaultBranch: "main", OwnerUserID: sql.NullInt64{Int64: user.ID, Valid: true}})
		return err
	}); err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	insertEndpoint := func(url string) store.WebhookEndpointRow {
		t.Helper()
		var ep store.WebhookEndpointRow
		if err := s.Write(ctx, func(tx *store.WriteTx) error {
			var err error
			ep, err = tx.InsertWebhookEndpoint(now, store.WebhookEndpointInsert{
				Scope: "repo", RepoID: repoID, URL: url,
				SecretEnc:  []byte("placeholder-sealed-secret-000000"),
				EventTypes: []string{"task.created"}, Active: true, Concurrency: 4, TimeoutSeconds: 20,
				Note: "test", CreatedBy: user.ID,
			})
			return err
		}); err != nil {
			t.Fatalf("insert endpoint: %v", err)
		}
		sealed, err := SealSecret(key, []byte("endpoint-secret-32-bytes-long!!"), secretAAD(ep.ID))
		if err != nil {
			t.Fatalf("seal: %v", err)
		}
		if err := s.Write(ctx, func(tx *store.WriteTx) error {
			_, err := tx.RotateWebhookEndpointSecret(ep.ID, now, sealed)
			return err
		}); err != nil {
			t.Fatalf("rotate: %v", err)
		}
		return ep
	}
	epOK := insertEndpoint("https://hooks.example.test/ok")
	epFail := insertEndpoint("https://hooks.example.test/fail")
	epGone := insertEndpoint("https://hooks.example.test/gone")
	if err := s.Write(ctx, func(tx *store.WriteTx) error {
		return tx.InsertWebhookMessages([]store.WebhookMessageInsert{
			{ID: "msg-ok-1", EndpointID: epOK.ID, EventType: "task.created", EventTime: now.UnixMilli(), NextAttemptAt: now.UnixMilli(), Payload: []byte(`{"n":1}`)},
			{ID: "msg-fail-1", EndpointID: epFail.ID, EventType: "task.created", EventTime: now.UnixMilli(), NextAttemptAt: now.UnixMilli(), Payload: []byte(`{"n":2}`)},
			{ID: "msg-gone-1", EndpointID: epGone.ID, EventType: "task.created", EventTime: now.UnixMilli(), NextAttemptAt: now.UnixMilli(), Payload: []byte(`{"n":3}`)},
		})
	}); err != nil {
		t.Fatalf("insert messages: %v", err)
	}
	statuses := map[string]int64{"msg-ok-1": 200, "msg-fail-1": 500, "msg-gone-1": 410}
	w := NewWorker(WorkerConfig{Store: s, SealKey: key, Now: time.Now, Send: func(_ context.Context, m store.WebhookMessageRow, _ store.WebhookEndpointRow, _ DeliverySecrets, _ int64, body []byte) (int64, []byte, []byte, string) {
		return statuses[m.ID], []byte(`{}`), bytes.Clone(body), ""
	}}, nil)
	if err := w.DriveOnce(ctx); err != nil {
		t.Fatalf("drive: %v", err)
	}
	if err := s.Read(ctx, func(tx *store.ReadTx) error {
		ok, err := tx.GetWebhookMessage("msg-ok-1")
		if err != nil {
			return err
		}
		if ok.State != "delivered" || ok.LastStatusCode != 200 {
			t.Fatalf("ok message = %+v", ok)
		}
		fail, err := tx.GetWebhookMessage("msg-fail-1")
		if err != nil {
			return err
		}
		if fail.State != "pending" || fail.AttemptCount != 1 || fail.NextAttemptAt <= now.UnixMilli() {
			t.Fatalf("fail message = %+v", fail)
		}
		gone, err := tx.GetWebhookMessage("msg-gone-1")
		if err != nil {
			return err
		}
		if gone.State != "dead" {
			t.Fatalf("gone message = %+v", gone)
		}
		attempts, err := tx.ListWebhookAttempts("msg-ok-1", 10)
		if err != nil {
			return err
		}
		if len(attempts) != 1 || attempts[0].AttemptNo != 1 || attempts[0].HTTPStatus != 200 {
			t.Fatalf("attempts = %+v", attempts)
		}
		if attempts[0].AttemptTimestamp <= 0 {
			t.Fatal("stored attempt misses timestamp")
		}
		stored := string(attempts[0].ReqHeaders)
		if !strings.Contains(stored, `"webhook-id":"msg-ok-1"`) || strings.Count(stored, "v1,<omitted>") != 1 {
			t.Fatalf("stored req headers = %s", stored)
		}
		if strings.Contains(stored, "endpoint-secret") {
			t.Fatalf("stored req headers leak secret: %s", stored)
		}
		got, err := tx.GetWebhookEndpoint(epGone.ID)
		if err != nil {
			return err
		}
		if got.Active || got.DisableReason != "410-gone" || got.ConsecutiveFailures != 1 {
			t.Fatalf("endpoint = %+v", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestWorkerRotationDualSigns(t *testing.T) {
	now := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	fake := newFakeStore(now)
	cur := fake.sealEndpoint(t, "ep1", nil)
	key := []byte("0123456789abcdef0123456789abcdef")
	prevRaw := []byte("previous-secret-for-ep1-00000000")
	sealed, err := SealSecret(key, prevRaw, secretAAD("ep1"))
	if err != nil {
		t.Fatalf("seal prev: %v", err)
	}
	fake.mu.Lock()
	fake.endpoints["ep1"].PrevSecretEnc = sealed
	fake.endpoints["ep1"].PrevSecretExpiresAt = now.Add(time.Hour).UnixMilli()
	fake.mu.Unlock()
	fake.addMessage(goldenMsgID, "ep1")
	var got DeliverySecrets
	var gotTS int64
	var gotBody []byte
	w := testWorker(fake, func(_ context.Context, _ store.WebhookMessageRow, _ store.WebhookEndpointRow, secrets DeliverySecrets, ts int64, body []byte) (int64, []byte, []byte, string) {
		got, gotTS, gotBody = secrets, ts, bytes.Clone(body)
		return 200, nil, nil, ""
	})
	if err := w.DriveOnce(t.Context()); err != nil {
		t.Fatalf("drive: %v", err)
	}
	if !bytes.Equal(got.Current, cur) || !bytes.Equal(got.Previous, prevRaw) {
		t.Fatal("Send did not receive both rotation secrets")
	}
	header := SignHeader(got.Current, got.Previous, goldenMsgID, gotTS, gotBody)
	if len(strings.Split(header, " ")) != 2 {
		t.Fatalf("rotation header = %q, want two entries", header)
	}
	if fake.attempts[0].AttemptTimestamp != gotTS {
		t.Fatalf("attempt timestamp = %d, want %d", fake.attempts[0].AttemptTimestamp, gotTS)
	}
	if got, want := strings.Count(string(fake.attempts[0].ReqHeaders), "v1,<omitted>"), 2; got != want {
		t.Fatalf("req headers hold %d redacted entries, want %d: %s", got, want, fake.attempts[0].ReqHeaders)
	}
	at := time.Unix(gotTS, 0)
	if !Verify([][]byte{cur}, goldenMsgID, gotTS, gotBody, header, at) {
		t.Fatal("rotation header rejected under current secret")
	}
	if !Verify([][]byte{prevRaw}, goldenMsgID, gotTS, gotBody, header, at) {
		t.Fatal("rotation header rejected under previous secret")
	}
	fake.mu.Lock()
	fake.endpoints["ep1"].PrevSecretExpiresAt = now.Add(-time.Hour).UnixMilli()
	fake.mu.Unlock()
	secondID := "msg_01J9Z8X7Q6W5E4R3T2Y179E55F"
	fake.addMessage(secondID, "ep1")
	got = DeliverySecrets{}
	if err := w.DriveOnce(t.Context()); err != nil {
		t.Fatalf("drive: %v", err)
	}
	if got.Previous != nil {
		t.Fatal("expired previous secret still sent")
	}
}

func TestWorkerDefaultSendRotationDualSigns(t *testing.T) {
	var sigs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sigs = append(sigs, r.Header.Get("webhook-signature"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	now := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	fake := newFakeStore(now)
	cur := fake.sealEndpoint(t, "ep1", func(ep *store.WebhookEndpointRow) { ep.URL = server.URL + "/hook" })
	key := []byte("0123456789abcdef0123456789abcdef")
	prevRaw := []byte("previous-secret-for-ep1-00000000")
	sealed, err := SealSecret(key, prevRaw, secretAAD("ep1"))
	if err != nil {
		t.Fatalf("seal prev: %v", err)
	}
	fake.mu.Lock()
	fake.endpoints["ep1"].PrevSecretEnc = sealed
	fake.endpoints["ep1"].PrevSecretExpiresAt = now.Add(time.Hour).UnixMilli()
	fake.mu.Unlock()
	fake.addMessage(goldenMsgID, "ep1")
	policy := &Policy{AllowInsecureHTTP: true, AllowPrivateTargets: true}
	w := NewWorker(WorkerConfig{SealKey: key, Now: fake.clock, backend: fake}, policy)
	if err := w.DriveOnce(t.Context()); err != nil {
		t.Fatalf("drive: %v", err)
	}
	if len(sigs) != 1 {
		t.Fatalf("captured %d signatures, want 1", len(sigs))
	}
	entries := strings.Split(sigs[0], " ")
	if len(entries) != 2 {
		t.Fatalf("default send header has %d entries, want 2", len(entries))
	}
	msg := fake.getMessage(goldenMsgID)
	ts := fake.clock().Unix()
	if !Verify([][]byte{cur}, goldenMsgID, ts, msg.Payload, sigs[0], time.Unix(ts, 0)) {
		t.Fatal("wire header rejected under current secret")
	}
	if !Verify([][]byte{prevRaw}, goldenMsgID, ts, msg.Payload, sigs[0], time.Unix(ts, 0)) {
		t.Fatal("wire header rejected under previous secret")
	}
	fake.mu.Lock()
	fake.endpoints["ep1"].PrevSecretExpiresAt = now.UnixMilli()
	fake.mu.Unlock()
	secondID := "msg_01J9Z8X7Q6W5E4R3T2Y179E66A"
	fake.addMessage(secondID, "ep1")
	if err := w.DriveOnce(t.Context()); err != nil {
		t.Fatalf("drive: %v", err)
	}
	if len(sigs) != 2 {
		t.Fatalf("captured %d signatures, want 2", len(sigs))
	}
	if len(strings.Split(sigs[1], " ")) != 1 {
		t.Fatalf("expired-prev header = %q, want single entry", sigs[1])
	}
}

func TestWorkerLeaseHandoff(t *testing.T) {
	ctx := t.Context()
	s, oerr := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "handoff.db")})
	if oerr != nil {
		t.Fatalf("open store: %v", oerr)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	key := []byte("0123456789abcdef0123456789abcdef")
	now := time.Now()
	user, uerr := s.InsertAuthUser(ctx, store.AuthUserCreate{Name: "hook", Lower: "hook", Email: "hook@example.test", Hash: "test-hash", Algo: "argon2id-v19", Now: now.UnixMilli()})
	if uerr != nil {
		t.Fatalf("insert user: %v", uerr)
	}
	var repoID int64
	if werr := s.Write(ctx, func(tx *store.WriteTx) error {
		id, ierr := tx.InsertRepo(now, store.RepoInsert{Name: "hooks", Visibility: "private", DefaultBranch: "main", OwnerUserID: sql.NullInt64{Int64: user.ID, Valid: true}})
		if ierr != nil {
			return ierr
		}
		repoID = id
		return nil
	}); werr != nil {
		t.Fatalf("insert repo: %v", werr)
	}
	var ep store.WebhookEndpointRow
	if werr := s.Write(ctx, func(tx *store.WriteTx) error {
		row, ierr := tx.InsertWebhookEndpoint(now, store.WebhookEndpointInsert{
			Scope: "repo", RepoID: repoID, URL: "https://hooks.example.test/hook",
			SecretEnc:  []byte("placeholder-sealed-secret-000000"),
			EventTypes: []string{"task.created"}, Active: true, Concurrency: 4, TimeoutSeconds: 20,
			Note: "handoff", CreatedBy: user.ID,
		})
		if ierr != nil {
			return ierr
		}
		ep = row
		return nil
	}); werr != nil {
		t.Fatalf("insert endpoint: %v", werr)
	}
	sealed, serr := SealSecret(key, []byte("handoff-secret-32-bytes-long!!!"), secretAAD(ep.ID))
	if serr != nil {
		t.Fatalf("seal: %v", serr)
	}
	if werr := s.Write(ctx, func(tx *store.WriteTx) error {
		_, ierr := tx.RotateWebhookEndpointSecret(ep.ID, now, sealed)
		return ierr
	}); werr != nil {
		t.Fatalf("rotate: %v", werr)
	}
	const handoffID = "msg_01J9Z8X7Q6W5E4R3T2Y179H99D"
	if werr := s.Write(ctx, func(tx *store.WriteTx) error {
		return tx.InsertWebhookMessages([]store.WebhookMessageInsert{
			{ID: handoffID, EndpointID: ep.ID, EventType: "task.created", EventTime: now.UnixMilli(), NextAttemptAt: now.UnixMilli(), Payload: []byte(`{"n":1}`)},
		})
	}); werr != nil {
		t.Fatalf("insert message: %v", werr)
	}
	block := make(chan struct{})
	release := sync.OnceFunc(func() { close(block) })
	worker1Done := make(chan error, 1)
	t.Cleanup(func() {
		release()
		select {
		case err := <-worker1Done:
			_ = err
		case <-time.After(10 * time.Second):
		}
	})
	var worker1ID atomic.Value
	worker1 := NewWorker(WorkerConfig{
		Store: s, SealKey: key, Now: time.Now,
		Send: func(ctx context.Context, m store.WebhookMessageRow, _ store.WebhookEndpointRow, _ DeliverySecrets, _ int64, _ []byte) (int64, []byte, []byte, string) {
			worker1ID.Store(m.ID)
			select {
			case <-block:
				return 0, nil, nil, "transport: killed mid-send"
			case <-ctx.Done():
				return 0, nil, nil, "transport: test context done"
			}
		},
	}, nil)
	go func() {
		worker1Done <- worker1.DriveOnce(ctx)
	}()
	claimedID := ""
	deadline := time.Now().Add(10 * time.Second)
	for {
		var got store.WebhookMessageRow
		if rerr := s.Read(ctx, func(tx *store.ReadTx) error {
			var err error
			got, err = tx.GetWebhookMessage(handoffID)
			return err
		}); rerr != nil {
			t.Fatalf("read message: %v", rerr)
		}
		if got.State == "inflight" {
			claimedID = got.ID
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker1 never claimed inflight: %+v", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
	warped := now.Add(6 * time.Minute)
	worker2 := NewWorker(WorkerConfig{
		Store: s, SealKey: key,
		Now: func() time.Time { return warped },
	}, nil)
	swept, err := worker2.SweepOnce(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if swept != 1 {
		t.Fatalf("swept = %d, want 1", swept)
	}
	var afterSweep store.WebhookMessageRow
	if err := s.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		afterSweep, err = tx.GetWebhookMessage(claimedID)
		return err
	}); err != nil {
		t.Fatalf("read after sweep: %v", err)
	}
	if afterSweep.State != "pending" || afterSweep.ID != claimedID || afterSweep.AttemptCount != 0 {
		t.Fatalf("after sweep = %+v, want pending same id unchanged attempts", afterSweep)
	}
	var sends atomic.Int64
	driver := NewWorker(WorkerConfig{
		Store: s, SealKey: key, Now: time.Now,
		Send: func(_ context.Context, m store.WebhookMessageRow, _ store.WebhookEndpointRow, _ DeliverySecrets, _ int64, _ []byte) (int64, []byte, []byte, string) {
			sends.Add(1)
			if m.ID != claimedID {
				t.Errorf("driver sent %q, want %q", m.ID, claimedID)
			}
			return 200, nil, nil, ""
		},
	}, nil)
	if err := driver.DriveOnce(ctx); err != nil {
		t.Fatalf("drive: %v", err)
	}
	var final store.WebhookMessageRow
	if err := s.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		final, err = tx.GetWebhookMessage(claimedID)
		return err
	}); err != nil {
		t.Fatalf("read final: %v", err)
	}
	if final.State != "delivered" || final.ID != claimedID {
		t.Fatalf("final = %+v, want delivered same id", final)
	}
	if n := sends.Load(); n != 1 {
		t.Fatalf("post-sweep sends = %d, want exactly 1", n)
	}
	release()
	if err := <-worker1Done; err == nil {
		t.Fatal("worker1 should fail closed on the delivered message")
	}
	if id, ok := worker1ID.Load().(string); !ok || id != claimedID {
		t.Fatalf("worker1 id = %q, want %q", id, claimedID)
	}
}

func TestWorkerNoStoreErrors(t *testing.T) {
	w := NewWorker(WorkerConfig{}, nil)
	if err := w.DriveOnce(t.Context()); err == nil {
		t.Fatal("DriveOnce without store succeeded")
	}
	if _, err := w.SweepOnce(t.Context()); err == nil {
		t.Fatal("SweepOnce without store succeeded")
	}
	if _, err := w.JanitorOnce(t.Context(), time.Hour); err == nil {
		t.Fatal("JanitorOnce without store succeeded")
	}
}

func TestWorkerRunStopsOnCancel(t *testing.T) {
	fake := newFakeStore(time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC))
	fake.sealEndpoint(t, "ep1", nil)
	fake.addMessage(goldenMsgID, "ep1")
	w := NewWorker(WorkerConfig{SealKey: []byte("0123456789abcdef0123456789abcdef"), Now: fake.clock, Send: statusSend(200), PollInterval: 5 * time.Millisecond, Workers: 2, backend: fake}, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := fake.getMessage(goldenMsgID); got.State != "delivered" {
		t.Fatalf("state=%q, want delivered", got.State)
	}
}
