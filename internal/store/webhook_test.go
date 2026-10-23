package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"encoding/json/jsontext"
)

func assertDataKeys(t *testing.T, payload []byte, want []string) map[string]any {
	t.Helper()
	var env struct {
		Data      map[string]any `json:"data"`
		ID        string         `json:"id"`
		Type      string         `json:"type"`
		Timestamp string         `json:"timestamp"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		t.Fatalf("payload parses: %v", err)
	}
	if env.ID == "" || env.Type == "" || env.Timestamp == "" {
		t.Fatalf("envelope = %+v", env)
	}
	if _, err := time.Parse(time.RFC3339Nano, env.Timestamp); err != nil {
		t.Fatalf("timestamp %q: %v", env.Timestamp, err)
	}
	got := slices.Sorted(maps.Keys(env.Data))
	sorted := slices.Sorted(slices.Values(want))
	if !slices.Equal(got, sorted) {
		t.Fatalf("data keys = %v, want %v", got, sorted)
	}
	return env.Data
}

func openF8Store(t *testing.T) *Store {
	t.Helper()
	s, err := open(t.Context(), Options{Path: testPath(t)}, embeddedMigrations)
	if err != nil {
		t.Fatalf("open f8 store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

func seedF8User(t *testing.T, s *Store, name string) AuthUser {
	t.Helper()
	u, err := s.InsertAuthUser(t.Context(), AuthUserCreate{
		Name: name, Lower: strings.ToLower(name), Email: name + "@example.test",
		Hash: "test-hash", Algo: "argon2id-v19", Now: 1,
	})
	if err != nil {
		t.Fatalf("insert user %s: %v", name, err)
	}
	return u
}

func seedF8Repo(ctx context.Context, t *testing.T, s *Store, ownerID int64, name string) int64 {
	t.Helper()
	var id int64
	if err := s.Write(ctx, func(tx *WriteTx) error {
		var ierr error
		id, ierr = tx.InsertRepo(time.UnixMilli(1000), RepoInsert{
			Name: name, Visibility: "private", DefaultBranch: "main",
			OwnerUserID: sql.NullInt64{Int64: ownerID, Valid: true},
		})
		return ierr
	}); err != nil {
		t.Fatalf("insert repo %s: %v", name, err)
	}
	return id
}

func seedF8Endpoint(ctx context.Context, t *testing.T, s *Store, now time.Time, scope, kind string, repoID, ownerID int64, types []string) WebhookEndpointRow {
	t.Helper()
	var ep WebhookEndpointRow
	if err := s.Write(ctx, func(tx *WriteTx) error {
		var ierr error
		ep, ierr = tx.InsertWebhookEndpoint(now, WebhookEndpointInsert{
			Scope: scope, OwnerKind: kind, RepoID: repoID, OwnerID: ownerID,
			URL: "https://hooks.example.test/" + scope, SecretEnc: []byte("sealed-secret-0123456789abcdef"),
			EventTypes: types, Active: true, Concurrency: 4, TimeoutSeconds: 20,
			Note: "test", CreatedBy: 1,
		})
		return ierr
	}); err != nil {
		t.Fatalf("insert endpoint: %v", err)
	}
	return ep
}

func TestWebhookEndpointCRUD(t *testing.T) {
	ctx := t.Context()
	s := openF8Store(t)
	now := time.UnixMilli(2000)
	ada := seedF8User(t, s, "hookada")
	repo := seedF8Repo(ctx, t, s, ada.ID, "hooks")

	var ep WebhookEndpointRow
	if err := s.Write(ctx, func(tx *WriteTx) error {
		var ierr error
		ep, ierr = tx.InsertWebhookEndpoint(now, WebhookEndpointInsert{
			Scope: "repo", RepoID: repo, URL: "https://hooks.example.test/push",
			SecretEnc: []byte("sealed-1"), EventTypes: []string{"task.created", "ref.updated"},
			RefGlobs: []string{"refs/heads/*"}, Active: true, Concurrency: 4,
			TimeoutSeconds: 20, Note: "n", CreatedBy: ada.ID,
		})
		return ierr
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if !strings.HasPrefix(ep.ID, "wh_") {
		t.Fatalf("endpoint id = %q, want wh_ prefix", ep.ID)
	}
	if len(ep.URLHash) != 32 {
		t.Fatalf("url hash len = %d, want 32", len(ep.URLHash))
	}
	if ep.LastStatus != "never" || ep.CreatedAt != 2000 {
		t.Fatalf("defaults = %+v", ep)
	}

	if err := s.Read(ctx, func(tx *ReadTx) error {
		got, gerr := tx.GetWebhookEndpoint(ep.ID)
		if gerr != nil {
			return gerr
		}
		if got.URL != "https://hooks.example.test/push" || !got.Active || got.Concurrency != 4 {
			t.Fatalf("round trip = %+v", got)
		}
		if len(got.RefGlobs) != 1 || got.RefGlobs[0] != "refs/heads/*" {
			t.Fatalf("globs = %v", got.RefGlobs)
		}
		if _, gerr = tx.GetWebhookEndpoint("wh_missing"); !errors.Is(gerr, ErrNotFound) {
			t.Fatalf("missing get = %v, want ErrNotFound", gerr)
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}

	// Scope violations: both set, neither set, wrong scope for the id.
	badScopes := []WebhookEndpointInsert{
		{Scope: "repo", RepoID: repo, OwnerID: ada.ID, URL: "https://x.example.test/", SecretEnc: []byte("s"), EventTypes: []string{"task.created"}, CreatedBy: ada.ID},
		{Scope: "repo", URL: "https://x.example.test/", SecretEnc: []byte("s"), EventTypes: []string{"task.created"}, CreatedBy: ada.ID},
		{Scope: "owner", RepoID: repo, URL: "https://x.example.test/", SecretEnc: []byte("s"), EventTypes: []string{"task.created"}, CreatedBy: ada.ID},
		{Scope: "global", URL: "https://x.example.test/", SecretEnc: []byte("s"), EventTypes: []string{"task.created"}, CreatedBy: ada.ID},
		{Scope: "owner", OwnerID: ada.ID, URL: "https://x.example.test/", SecretEnc: []byte("s"), EventTypes: []string{"task.created"}, CreatedBy: ada.ID},
		{Scope: "owner", OwnerID: ada.ID, OwnerKind: "bogus", URL: "https://x.example.test/", SecretEnc: []byte("s"), EventTypes: []string{"task.created"}, CreatedBy: ada.ID},
		{Scope: "repo", RepoID: repo, OwnerKind: "user", URL: "https://x.example.test/", SecretEnc: []byte("s"), EventTypes: []string{"task.created"}, CreatedBy: ada.ID},
	}
	for i, in := range badScopes {
		if err := s.Write(ctx, func(tx *WriteTx) error {
			_, ierr := tx.InsertWebhookEndpoint(now, in)
			return ierr
		}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("scope case %d: err = %v, want ErrInvalid", i, err)
		}
	}

	// Bad event type and ref_globs without ref.updated.
	for i, in := range []WebhookEndpointInsert{
		{Scope: "repo", RepoID: repo, URL: "https://x.example.test/", SecretEnc: []byte("s"), EventTypes: []string{"nope.event"}, CreatedBy: ada.ID},
		{Scope: "repo", RepoID: repo, URL: "https://x.example.test/", SecretEnc: []byte("s"), EventTypes: []string{"task.created"}, RefGlobs: []string{"refs/heads/*"}, CreatedBy: ada.ID},
		{Scope: "repo", RepoID: repo, URL: "https://x.example.test/", SecretEnc: []byte("s"), EventTypes: []string{}, CreatedBy: ada.ID},
		{Scope: "repo", RepoID: repo, URL: "https://x.example.test/", SecretEnc: []byte("s"), EventTypes: []string{"task.created"}, Concurrency: 17, CreatedBy: ada.ID},
		{Scope: "repo", RepoID: repo, URL: "https://x.example.test/", SecretEnc: []byte("s"), EventTypes: []string{"task.created"}, TimeoutSeconds: 61, CreatedBy: ada.ID},
	} {
		if err := s.Write(ctx, func(tx *WriteTx) error {
			_, ierr := tx.InsertWebhookEndpoint(now, in)
			return ierr
		}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("validation case %d: err = %v, want ErrInvalid", i, err)
		}
	}

	// Update + rotate + counts + delete.
	if err := s.Write(ctx, func(tx *WriteTx) error {
		timeout := int64(30)
		active := false
		types := []string{"task.created"}
		emptyGlobs := []string{}
		updated, uerr := tx.UpdateWebhookEndpoint(ep.ID, now, WebhookEndpointUpdate{
			TimeoutSeconds: &timeout, Active: &active, EventTypes: &types, RefGlobs: &emptyGlobs,
		})
		if uerr != nil {
			return uerr
		}
		if updated.TimeoutSeconds != 30 || updated.Active || len(updated.EventTypes) != 1 {
			t.Fatalf("updated = %+v", updated)
		}
		bad := "bogus"
		if _, uerr = tx.UpdateWebhookEndpoint(ep.ID, now, WebhookEndpointUpdate{LastStatus: &bad}); !errors.Is(uerr, ErrInvalid) {
			t.Fatalf("bad status update = %v, want ErrInvalid", uerr)
		}
		rotated, rerr := tx.RotateWebhookEndpointSecret(ep.ID, now, []byte("sealed-2"))
		if rerr != nil {
			return rerr
		}
		if string(rotated.SecretEnc) != "sealed-2" || string(rotated.PrevSecretEnc) != "sealed-1" {
			t.Fatalf("rotated secrets = %q/%q", rotated.SecretEnc, rotated.PrevSecretEnc)
		}
		if rotated.PrevSecretExpiresAt != 2000+24*60*60*1000 {
			t.Fatalf("prev expiry = %d", rotated.PrevSecretExpiresAt)
		}
		if _, rerr = tx.RotateWebhookEndpointSecret(ep.ID, now, nil); !errors.Is(rerr, ErrInvalid) {
			t.Fatalf("empty rotate = %v, want ErrInvalid", rerr)
		}
		explicit, ierr := tx.InsertWebhookEndpoint(now, WebhookEndpointInsert{
			ID: "wh_explicitid00000000000001", Scope: "repo", RepoID: repo,
			URL: "https://hooks.example.test/explicit", SecretEnc: []byte("sealed-x"),
			EventTypes: []string{"task.created"}, Active: true, CreatedBy: ada.ID,
		})
		if ierr != nil || explicit.ID != "wh_explicitid00000000000001" {
			t.Fatalf("explicit id = %+v, %v", explicit, ierr)
		}
		longID := "wh_" + strings.Repeat("x", 70)
		if _, ierr = tx.InsertWebhookEndpoint(now, WebhookEndpointInsert{
			ID: longID, Scope: "repo", RepoID: repo, URL: "https://hooks.example.test/long",
			SecretEnc: []byte("sealed-x"), EventTypes: []string{"task.created"},
			Active: true, CreatedBy: ada.ID,
		}); !errors.Is(ierr, ErrInvalid) {
			t.Fatalf("long id = %v, want ErrInvalid", ierr)
		}
		return tx.DeleteWebhookEndpoint(explicit.ID)
	}); err != nil {
		t.Fatalf("update/rotate: %v", err)
	}

	if err := s.Read(ctx, func(tx *ReadTx) error {
		n, cerr := tx.CountWebhookEndpointsByRepo(repo)
		if cerr != nil || n != 1 {
			t.Fatalf("count by repo = %d, %v", n, cerr)
		}
		if n, cerr = tx.CountWebhookEndpointsByOwner(ada.ID); cerr != nil || n != 0 {
			t.Fatalf("count by owner = %d, %v", n, cerr)
		}
		page, next, lerr := tx.ListWebhookEndpointsByRepo(repo, "", 10)
		if lerr != nil || len(page) != 1 || next != "" {
			t.Fatalf("list = %d %q %v", len(page), next, lerr)
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}

	if err := s.Write(ctx, func(tx *WriteTx) error {
		return tx.DeleteWebhookEndpoint(ep.ID)
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.Write(ctx, func(tx *WriteTx) error {
		return tx.DeleteWebhookEndpoint(ep.ID)
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
}

func TestWebhookEndpointPagination(t *testing.T) {
	ctx := t.Context()
	s := openF8Store(t)
	now := time.UnixMilli(5000)
	ada := seedF8User(t, s, "pageada")
	repo := seedF8Repo(ctx, t, s, ada.ID, "pages")
	for i := range 3 {
		if err := s.Write(ctx, func(tx *WriteTx) error {
			_, ierr := tx.InsertWebhookEndpoint(now.Add(time.Duration(i)*time.Millisecond), WebhookEndpointInsert{
				Scope: "repo", RepoID: repo, URL: "https://hooks.example.test/e",
				SecretEnc: []byte("sealed"), EventTypes: []string{"task.created"},
				Active: true, CreatedBy: ada.ID,
			})
			return ierr
		}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	if err := s.Read(ctx, func(tx *ReadTx) error {
		first, next, lerr := tx.ListWebhookEndpointsByRepo(repo, "", 2)
		if lerr != nil || len(first) != 2 || next == "" {
			t.Fatalf("page1 = %d %q %v", len(first), next, lerr)
		}
		if _, _, lerr = tx.ListWebhookEndpointsByRepo(repo, "!!!", 2); lerr == nil {
			t.Fatalf("bad cursor accepted")
		}
		second, next2, lerr := tx.ListWebhookEndpointsByRepo(repo, next, 2)
		if lerr != nil || len(second) != 1 || next2 != "" {
			t.Fatalf("page2 = %d %q %v", len(second), next2, lerr)
		}
		if first[0].ID == first[1].ID || second[0].ID == first[0].ID || second[0].ID == first[1].ID {
			t.Fatalf("pages overlap")
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
}

func TestWebhookMessageDedupe(t *testing.T) {
	ctx := t.Context()
	s := openF8Store(t)
	now := time.UnixMilli(8000)
	ada := seedF8User(t, s, "msgada")
	repo := seedF8Repo(ctx, t, s, ada.ID, "msgs")
	ep := seedF8Endpoint(ctx, t, s, now, "repo", "", repo, 0, []string{"task.created"})
	if err := s.Write(ctx, func(tx *WriteTx) error {
		first := WebhookMessageInsert{ID: "msg_test1", EndpointID: ep.ID, EventType: "task.created",
			OutboxSeq: 7, EventTime: 8000, NextAttemptAt: 8000, Payload: []byte(`{"a":1}`)}
		if ierr := tx.InsertWebhookMessages([]WebhookMessageInsert{first}); ierr != nil {
			return ierr
		}
		dup := first
		dup.ID = "msg_test2"
		if ierr := tx.InsertWebhookMessages([]WebhookMessageInsert{dup}); ierr != nil {
			return ierr
		}
		other := first
		other.ID = "msg_test3"
		other.OutboxSeq = 8
		return tx.InsertWebhookMessages([]WebhookMessageInsert{other})
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s.Read(ctx, func(tx *ReadTx) error {
		msgs, _, lerr := tx.ListWebhookMessages(ep.ID, "", 10)
		if lerr != nil {
			return lerr
		}
		if len(msgs) != 2 {
			t.Fatalf("messages = %d, want 2 (same outbox_seq+endpoint deduped)", len(msgs))
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
	// Oversize payload rejected.
	if err := s.Write(ctx, func(tx *WriteTx) error {
		return tx.InsertWebhookMessages([]WebhookMessageInsert{{ID: "msg_big", EndpointID: ep.ID,
			EventType: "task.created", OutboxSeq: 9, EventTime: 8000, NextAttemptAt: 8000,
			Payload: make([]byte, 262145)}})
	}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize = %v, want ErrTooLarge", err)
	}
}

func TestClaimLeaseSweep(t *testing.T) {
	ctx := t.Context()
	s := openF8Store(t)
	now := time.UnixMilli(10000)
	ada := seedF8User(t, s, "claimada")
	repo := seedF8Repo(ctx, t, s, ada.ID, "claims")
	ep := seedF8Endpoint(ctx, t, s, now, "repo", "", repo, 0, []string{"task.created"})

	insertMsg := func(id string, at int64) {
		t.Helper()
		if err := s.Write(ctx, func(tx *WriteTx) error {
			return tx.InsertWebhookMessages([]WebhookMessageInsert{{ID: id, EndpointID: ep.ID,
				EventType: "task.created", OutboxSeq: at, EventTime: at, NextAttemptAt: 9000,
				Payload: []byte(`{}`)}})
		}); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	insertMsg("msg_c1", 11)
	insertMsg("msg_c2", 12)

	// Per-endpoint cap of 1 admits a single row.
	if err := s.Write(ctx, func(tx *WriteTx) error {
		if _, cerr := tx.ClaimWebhookMessages(9500, 10, 1, ""); !errors.Is(cerr, ErrInvalid) {
			t.Fatalf("empty owner claim = %v, want ErrInvalid", cerr)
		}
		if _, serr := tx.SweepWebhookLeases(9500, 0); !errors.Is(serr, ErrInvalid) {
			t.Fatalf("zero-budget sweep = %v, want ErrInvalid", serr)
		}
		claimed, cerr := tx.ClaimWebhookMessages(9500, 10, 1, "inst-a")
		if cerr != nil {
			return cerr
		}
		if len(claimed) != 1 || claimed[0].State != "inflight" {
			t.Fatalf("capped claim = %+v", claimed)
		}
		if claimed[0].LeaseExpiresAt != 9500+5*60*1000 {
			t.Fatalf("lease = %d", claimed[0].LeaseExpiresAt)
		}
		if claimed[0].LeaseOwner != "inst-a" {
			t.Fatalf("owner = %q, want inst-a", claimed[0].LeaseOwner)
		}
		return nil
	}); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Full claim takes the second row; sweep before expiry is a no-op (N-1).
	if err := s.Write(ctx, func(tx *WriteTx) error {
		claimed, cerr := tx.ClaimWebhookMessages(9500, 10, 0, "inst-a")
		if cerr != nil || len(claimed) != 1 {
			t.Fatalf("claim2 = %d, %v", len(claimed), cerr)
		}
		n, serr := tx.SweepWebhookLeases(9500+5*60*1000-1, 10)
		if serr != nil || n != 0 {
			t.Fatalf("early sweep = %d, %v; want 0", n, serr)
		}
		return nil
	}); err != nil {
		t.Fatalf("claim2/sweep: %v", err)
	}

	// Sweep after expiry returns both rows to pending with the same ids,
	// spending one attempt each and clearing the lease fields.
	if err := s.Write(ctx, func(tx *WriteTx) error {
		n, serr := tx.SweepWebhookLeases(9500+5*60*1000, 10)
		if serr != nil || n != 2 {
			t.Fatalf("sweep = %d, %v; want 2", n, serr)
		}
		for _, id := range []string{"msg_c1", "msg_c2"} {
			m, gerr := tx.GetWebhookMessage(id)
			if gerr != nil {
				return gerr
			}
			if m.State != "pending" || m.AttemptCount != 1 {
				t.Fatalf("%s = %+v", id, m)
			}
			if m.LeaseOwner != "" || m.LeaseExpiresAt != 0 {
				t.Fatalf("%s keeps lease %+v", id, m)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("sweep: %v", err)
	}
}

func TestSweepSpendsBudgetToDead(t *testing.T) {
	ctx := t.Context()
	s := openF8Store(t)
	now := time.UnixMilli(20000)
	ada := seedF8User(t, s, "sweepada")
	repo := seedF8Repo(ctx, t, s, ada.ID, "sweeps")
	ep := seedF8Endpoint(ctx, t, s, now, "repo", "", repo, 0, []string{"task.created"})
	if err := s.Write(ctx, func(tx *WriteTx) error {
		return tx.InsertWebhookMessages([]WebhookMessageInsert{{ID: "msg_loop", EndpointID: ep.ID,
			EventType: "task.created", OutboxSeq: 51, EventTime: 20000, NextAttemptAt: 20000,
			Payload: []byte(`{}`)}})
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Crash loop: claim, expire the lease, sweep — ten times. Each sweep
	// spends one attempt; the tenth exhausts the budget and dead-letters the
	// same row without a single delivery.
	for i := 0; i < 10; i++ {
		if err := s.Write(ctx, func(tx *WriteTx) error {
			claimed, cerr := tx.ClaimWebhookMessages(20000, 10, 0, "inst-loop")
			if cerr != nil {
				return cerr
			}
			if len(claimed) != 1 || claimed[0].ID != "msg_loop" {
				t.Fatalf("loop %d: claimed = %+v", i, claimed)
			}
			n, serr := tx.SweepWebhookLeases(20000+5*60*1000, 10)
			if serr != nil || n != 1 {
				t.Fatalf("loop %d: sweep = %d, %v; want 1", i, n, serr)
			}
			return nil
		}); err != nil {
			t.Fatalf("loop %d: %v", i, err)
		}
	}
	if err := s.Read(ctx, func(tx *ReadTx) error {
		m, gerr := tx.GetWebhookMessage("msg_loop")
		if gerr != nil {
			return gerr
		}
		if m.ID != "msg_loop" || m.State != "dead" {
			t.Fatalf("looped = %+v, want same id dead", m)
		}
		if m.LastError != "delivery lease expired repeatedly" {
			t.Fatalf("last_error = %q", m.LastError)
		}
		if m.DeliveredAt != 0 || m.LastStatusCode != 0 {
			t.Fatalf("looped shows a delivery: %+v", m)
		}
		if m.LeaseOwner != "" || m.LeaseExpiresAt != 0 {
			t.Fatalf("looped keeps lease %+v", m)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestStaleCompletionNoOp(t *testing.T) {
	ctx := t.Context()
	s := openF8Store(t)
	now := time.UnixMilli(30000)
	ada := seedF8User(t, s, "staleada")
	repo := seedF8Repo(ctx, t, s, ada.ID, "stales")
	ep := seedF8Endpoint(ctx, t, s, now, "repo", "", repo, 0, []string{"task.created"})
	if err := s.Write(ctx, func(tx *WriteTx) error {
		return tx.InsertWebhookMessages([]WebhookMessageInsert{{ID: "msg_stale", EndpointID: ep.ID,
			EventType: "task.created", OutboxSeq: 61, EventTime: 30000, NextAttemptAt: 30000,
			Payload: []byte(`{}`)}})
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s.Write(ctx, func(tx *WriteTx) error {
		if _, cerr := tx.ClaimWebhookMessages(30000, 10, 0, "owner-a"); cerr != nil {
			return cerr
		}
		// Owner A crashes; the sweep spends one attempt and frees the lease.
		if n, serr := tx.SweepWebhookLeases(30000+5*60*1000, 10); serr != nil || n != 1 {
			t.Fatalf("sweep = %d, %v; want 1", n, serr)
		}
		// Owner B claims the freed row.
		claimed, cerr := tx.ClaimWebhookMessages(30000+5*60*1000, 10, 0, "owner-b")
		if cerr != nil || len(claimed) != 1 || claimed[0].ID != "msg_stale" {
			t.Fatalf("reclaim = %+v, %v", claimed, cerr)
		}
		// Owner A's late completion is stale and changes nothing.
		if derr := tx.MarkWebhookDelivered("msg_stale", 30000+5*60*1000, 200, "owner-a"); !errors.Is(derr, ErrStale) {
			t.Fatalf("late deliver = %v, want ErrStale", derr)
		}
		if _, rerr := tx.RequeueWebhookMessage("msg_stale", "late", 500, 31000+5*60*1000, 30000+5*60*1000, "owner-a"); !errors.Is(rerr, ErrStale) {
			t.Fatalf("late requeue = %v, want ErrStale", rerr)
		}
		m, gerr := tx.GetWebhookMessage("msg_stale")
		if gerr != nil {
			return gerr
		}
		if m.State != "inflight" || m.LeaseOwner != "owner-b" || m.AttemptCount != 1 {
			t.Fatalf("stolen row = %+v, want inflight owned by B", m)
		}
		// Owner B completes normally.
		if derr := tx.MarkWebhookDelivered("msg_stale", 30000+5*60*1000, 200, "owner-b"); derr != nil {
			return derr
		}
		m, gerr = tx.GetWebhookMessage("msg_stale")
		if gerr != nil {
			return gerr
		}
		if m.State != "delivered" || m.LeaseOwner != "" {
			t.Fatalf("delivered = %+v", m)
		}
		return nil
	}); err != nil {
		t.Fatalf("stale: %v", err)
	}
}

func TestClearExpiredEndpointSecrets(t *testing.T) {
	ctx := t.Context()
	s := openF8Store(t)
	now := time.UnixMilli(40000)
	ada := seedF8User(t, s, "clearada")
	repo := seedF8Repo(ctx, t, s, ada.ID, "clears")
	ep := seedF8Endpoint(ctx, t, s, now, "repo", "", repo, 0, []string{"task.created"})
	if err := s.Write(ctx, func(tx *WriteTx) error {
		if _, rerr := tx.RotateWebhookEndpointSecret(ep.ID, now, []byte("new-sealed-secret-00000000000000")); rerr != nil {
			return rerr
		}
		if n, cerr := tx.ClearExpiredEndpointSecrets(now.UnixMilli()); cerr != nil || n != 0 {
			t.Fatalf("early clear = %d, %v; want 0", n, cerr)
		}
		got, gerr := tx.GetWebhookEndpoint(ep.ID)
		if gerr != nil {
			return gerr
		}
		if len(got.PrevSecretEnc) == 0 || got.PrevSecretExpiresAt != now.UnixMilli()+24*60*60*1000 {
			t.Fatalf("rotated = %+v", got)
		}
		if n, cerr := tx.ClearExpiredEndpointSecrets(now.UnixMilli() + 24*60*60*1000); cerr != nil || n != 1 {
			t.Fatalf("expiry clear = %d, %v; want 1", n, cerr)
		}
		got, gerr = tx.GetWebhookEndpoint(ep.ID)
		if gerr != nil {
			return gerr
		}
		if got.PrevSecretEnc != nil || got.PrevSecretExpiresAt != 0 {
			t.Fatalf("prev survives: %+v", got.PrevSecretEnc)
		}
		return nil
	}); err != nil {
		t.Fatalf("clear: %v", err)
	}
}

func TestAttemptHistoryPrune(t *testing.T) {
	ctx := t.Context()
	s := openF8Store(t)
	now := time.UnixMilli(12000)
	ada := seedF8User(t, s, "attada")
	repo := seedF8Repo(ctx, t, s, ada.ID, "attempts")
	ep := seedF8Endpoint(ctx, t, s, now, "repo", "", repo, 0, []string{"task.created"})
	if err := s.Write(ctx, func(tx *WriteTx) error {
		return tx.InsertWebhookMessages([]WebhookMessageInsert{{ID: "msg_a1", EndpointID: ep.ID,
			EventType: "task.created", OutboxSeq: 21, EventTime: 12000, NextAttemptAt: 12000,
			Payload: []byte(`{}`)}})
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	redacted := []byte(`{"webhook-id":"msg_a1","webhook-timestamp":"12001","webhook-signature":"v1,<omitted>","content-type":"application/json"}`)
	if err := s.Write(ctx, func(tx *WriteTx) error {
		for i := int64(1); i <= 12; i++ {
			if rerr := tx.RecordWebhookAttempt(WebhookAttemptInsert{
				MessageID: "msg_a1", AttemptNo: i, StartedAt: 12000 + i,
				FinishedAt: 12000 + i + 1, AttemptTimestamp: 12000 + i, HTTPStatus: 500,
				Error: "boom", ReqHeaders: redacted, DurationMs: 5,
			}); rerr != nil {
				return rerr
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := s.Read(ctx, func(tx *ReadTx) error {
		got, lerr := tx.ListWebhookAttempts("msg_a1", 20)
		if lerr != nil {
			return lerr
		}
		if len(got) != 10 {
			t.Fatalf("attempts = %d, want latest 10", len(got))
		}
		if got[0].AttemptNo != 12 || got[9].AttemptNo != 3 {
			t.Fatalf("window = %d..%d, want 12..3", got[0].AttemptNo, got[9].AttemptNo)
		}
		if got[0].AttemptTimestamp != 12012 || string(got[0].ReqHeaders) != string(redacted) {
			t.Fatalf("attempt fields = %+v", got[0])
		}
		if strings.Contains(string(got[0].ReqHeaders), "whsec_") {
			t.Fatalf("secret material in req_headers")
		}
		short, lerr := tx.ListWebhookAttempts("msg_a1", 3)
		if lerr != nil || len(short) != 3 || short[0].AttemptNo != 12 {
			t.Fatalf("limited = %+v, %v", short, lerr)
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
}

func TestRedeliverWalk(t *testing.T) {
	ctx := t.Context()
	s := openF8Store(t)
	now := time.UnixMilli(14000)
	ada := seedF8User(t, s, "redada")
	repo := seedF8Repo(ctx, t, s, ada.ID, "redeliver")
	ep := seedF8Endpoint(ctx, t, s, now, "repo", "", repo, 0, []string{"task.created"})
	mk := func(id string, seq int64) {
		t.Helper()
		if err := s.Write(ctx, func(tx *WriteTx) error {
			return tx.InsertWebhookMessages([]WebhookMessageInsert{{ID: id, EndpointID: ep.ID,
				EventType: "task.created", OutboxSeq: seq, EventTime: 14000, NextAttemptAt: 14000,
				Payload: []byte(`{}`)}})
		}); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	mk("msg_dead", 31)
	mk("msg_ok", 32)
	if err := s.Write(ctx, func(tx *WriteTx) error {
		if _, cerr := tx.ClaimWebhookMessages(14000, 10, 0, "inst-a"); cerr != nil {
			return cerr
		}
		if merr := tx.MarkWebhookDead("msg_dead", "gone", 14001, 410, "inst-a"); merr != nil {
			return merr
		}
		if merr := tx.MarkWebhookDelivered("msg_ok", 14001, 200, "inst-b"); !errors.Is(merr, ErrStale) {
			t.Fatalf("foreign-owner deliver = %v, want ErrStale", merr)
		}
		if merr := tx.MarkWebhookDelivered("msg_ok", 14001, 200, "inst-a"); merr != nil {
			return merr
		}
		red, rerr := tx.RedeliverWebhookMessage("msg_dead", 14002)
		if rerr != nil {
			return rerr
		}
		if red.ID != "msg_dead" || red.State != "pending" || red.AttemptCount != 0 || red.RedeliveryOf != "" {
			t.Fatalf("redelivered dead = %+v", red)
		}
		red, rerr = tx.RedeliverWebhookMessage("msg_ok", 14002)
		if rerr != nil {
			return rerr
		}
		if red.ID != "msg_ok" || red.State != "pending" || red.RedeliveryOf != "msg_ok" {
			t.Fatalf("redelivered delivered = %+v", red)
		}
		if _, rerr = tx.RedeliverWebhookMessage("msg_ok", 14003); !errors.Is(rerr, ErrInvalid) {
			t.Fatalf("redeliver pending = %v, want ErrInvalid", rerr)
		}
		if red.LeaseOwner != "" {
			t.Fatalf("redelivered keeps lease owner %q", red.LeaseOwner)
		}
		// Requeue needs a fresh claim; a foreign owner goes stale.
		if _, cerr := tx.ClaimWebhookMessages(14003, 10, 0, "inst-a"); cerr != nil {
			return cerr
		}
		if _, rerr = tx.RequeueWebhookMessage("msg_dead", "x", 503, 15000, 14003, "inst-b"); !errors.Is(rerr, ErrStale) {
			t.Fatalf("foreign-owner requeue = %v, want ErrStale", rerr)
		}
		// Requeue bumps the attempt count and reschedules.
		rq, rerr := tx.RequeueWebhookMessage("msg_dead", "again", 503, 15000, 14003, "inst-a")
		if rerr != nil || rq.AttemptCount != 1 || rq.NextAttemptAt != 15000 || rq.LastStatusCode != 503 {
			t.Fatalf("requeued = %+v, %v", rq, rerr)
		}
		if rq.LeaseOwner != "" || rq.State != "pending" {
			t.Fatalf("requeued keeps lease %+v", rq)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
}

func TestPruneWebhookHistory(t *testing.T) {
	ctx := t.Context()
	s := openF8Store(t)
	now := time.UnixMilli(16000)
	ada := seedF8User(t, s, "pruneada")
	repo := seedF8Repo(ctx, t, s, ada.ID, "prunes")
	ep := seedF8Endpoint(ctx, t, s, now, "repo", "", repo, 0, []string{"task.created"})
	seed := func(id string, seq int64) {
		t.Helper()
		if err := s.Write(ctx, func(tx *WriteTx) error {
			return tx.InsertWebhookMessages([]WebhookMessageInsert{{ID: id, EndpointID: ep.ID,
				EventType: "task.created", OutboxSeq: seq, EventTime: 16000, NextAttemptAt: 16000,
				Payload: []byte(`{}`)}})
		}); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	seed("msg_p1", 41)
	seed("msg_p2", 42)
	seed("msg_p3", 43)
	if err := s.Write(ctx, func(tx *WriteTx) error {
		if _, cerr := tx.ClaimWebhookMessages(16000, 10, 0, "inst-a"); cerr != nil {
			return cerr
		}
		if merr := tx.MarkWebhookDelivered("msg_p1", 1000, 200, "inst-a"); merr != nil {
			return merr
		}
		if merr := tx.MarkWebhookDead("msg_p2", "x", 2000, 500, "inst-a"); merr != nil {
			return merr
		}
		if merr := tx.MarkWebhookDelivered("msg_p3", 3000, 200, "inst-a"); merr != nil {
			return merr
		}
		n, perr := tx.PruneWebhookHistory(2000)
		if perr != nil || n != 1 {
			t.Fatalf("prune = %d, %v; want exactly the N-1 row", n, perr)
		}
		if _, gerr := tx.GetWebhookMessage("msg_p1"); !errors.Is(gerr, ErrNotFound) {
			t.Fatalf("p1 survives: %v", gerr)
		}
		for _, id := range []string{"msg_p2", "msg_p3"} {
			if _, gerr := tx.GetWebhookMessage(id); gerr != nil {
				t.Fatalf("%s pruned at boundary: %v", id, gerr)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
}

func TestFanoutPayloadShape(t *testing.T) {
	ctx := t.Context()
	s := openF8Store(t)
	now := time.UnixMilli(30000)
	ada := seedF8User(t, s, "shapeda")
	repo := seedF8Repo(ctx, t, s, ada.ID, "shapes")
	ep := seedF8Endpoint(ctx, t, s, now, "repo", "", repo, 0,
		[]string{"task.created", "ref.updated", "repo.created", "repo.deleted"})

	var publicID string
	if err := s.Write(ctx, func(tx *WriteTx) error {
		r, ierr := tx.InsertIntent(now, IntentRow{
			RepoID: repo, Title: "shaped task", Body: "b", CreatedBy: ada.ID,
		})
		if ierr != nil {
			return ierr
		}
		publicID = r.PublicID
		refBody := `{"repo":` + itoa(repo) + `,"name":"refs/heads/main","old":"0000","new":"abcd","source":"t","pusher":"shapeda"}`
		if _, eerr := tx.Outbox().Emit(Event{OccurredAt: now, Kind: "ref.updated", Body: jsontext.Value(refBody)}); eerr != nil {
			return eerr
		}
		// Internal keys (purge_after) must not leak into the envelope.
		delBody := `{"owner":"shapeda","name":"shapes","repo_id":` + itoa(repo) + `,"visibility":"private","purge_after":999}`
		_, eerr := tx.Outbox().Emit(Event{OccurredAt: now, Kind: "repo.deleted", Body: jsontext.Value(delBody)})
		return eerr
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.Read(ctx, func(tx *ReadTx) error {
		msgs, _, lerr := tx.ListWebhookMessages(ep.ID, "", 10)
		if lerr != nil || len(msgs) != 3 {
			t.Fatalf("messages = %d, %v", len(msgs), lerr)
		}
		byType := map[string]WebhookMessageRow{}
		for _, m := range msgs {
			byType[m.EventType] = m
			var env struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			}
			if jerr := json.Unmarshal(m.Payload, &env); jerr != nil || env.ID != m.ID || env.Type != m.EventType {
				t.Fatalf("envelope id/type mismatch: %+v %v", env, jerr)
			}
		}
		task := assertDataKeys(t, byType["task.created"].Payload,
			[]string{"task_id", "task_number", "title", "state", "assignee", "created_by", "repo", "task_url", "event_id"})
		if task["task_id"] != publicID || task["title"] != "shaped task" || task["state"] != "open" {
			t.Fatalf("task data = %v", task)
		}
		if task["assignee"] != nil {
			t.Fatalf("assignee = %v, want null", task["assignee"])
		}
		creator, ok := task["created_by"].(map[string]any)
		if !ok || creator["name"] != "shapeda" || creator["id"] != float64(ada.ID) {
			t.Fatalf("created_by = %v", task["created_by"])
		}
		if task["task_url"] != "/shapeda/shapes/tasks/"+publicID {
			t.Fatalf("task_url = %v", task["task_url"])
		}
		ref := assertDataKeys(t, byType["ref.updated"].Payload,
			[]string{"ref", "ref_kind", "old_oid", "new_oid", "change_kind", "pusher", "repo", "event_id"})
		if ref["ref"] != "refs/heads/main" || ref["ref_kind"] != "branch" || ref["change_kind"] != "created" {
			t.Fatalf("ref data = %v", ref)
		}
		pusher, ok := ref["pusher"].(map[string]any)
		if !ok || pusher["name"] != "shapeda" {
			t.Fatalf("pusher = %v", ref["pusher"])
		}
		del := assertDataKeys(t, byType["repo.deleted"].Payload,
			[]string{"repo_id", "full_name", "visibility", "default_branch", "fork_of", "deleted_by", "last_default_tip", "repo_url", "event_id"})
		if del["full_name"] != "shapeda/shapes" || del["repo_url"] != "/shapeda/shapes" {
			t.Fatalf("repo data = %v", del)
		}
		if _, leaked := del["purge_after"]; leaked {
			t.Fatalf("purge_after leaked into payload")
		}
		if del["fork_of"] != nil || del["last_default_tip"] != nil {
			t.Fatalf("nullables = %v %v", del["fork_of"], del["last_default_tip"])
		}
		by, ok := del["deleted_by"].(map[string]any)
		if !ok || by["name"] != "shapeda" {
			t.Fatalf("deleted_by = %v", del["deleted_by"])
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
}

func TestInboxFanOutSameTx(t *testing.T) {
	ctx := t.Context()
	s := openF8Store(t)
	now := time.UnixMilli(20000)
	reporter := seedF8User(t, s, "fanreporter")
	assignee := seedF8User(t, s, "fanassignee")
	watcher := seedF8User(t, s, "fanwatcher")
	repo := seedF8Repo(ctx, t, s, reporter.ID, "fanout")
	epTask := seedF8Endpoint(ctx, t, s, now, "repo", "", repo, 0, []string{"task.created"})
	epRef := seedF8Endpoint(ctx, t, s, now, "repo", "", repo, 0, []string{"ref.updated"})
	epOther := seedF8Endpoint(ctx, t, s, now, "repo", "", repo, 0, []string{"task.closed"})
	epOwner := seedF8Endpoint(ctx, t, s, now, "owner", "user", 0, reporter.ID, []string{"task.created"})
	epOwnerOrg := seedF8Endpoint(ctx, t, s, now, "owner", "org", 0, reporter.ID, []string{"task.created"})
	if err := s.Write(ctx, func(tx *WriteTx) error {
		_, werr := tx.SetWatch(watcher.ID, repo, "all", nil, 20000)
		return werr
	}); err != nil {
		t.Fatalf("watch: %v", err)
	}

	counts := func() (outbox, msgs, threads, items int64) {
		t.Helper()
		if err := s.Read(ctx, func(tx *ReadTx) error {
			inner, lerr := tx.live()
			if lerr != nil {
				return lerr
			}
			qs := []struct {
				dst *int64
				q   string
			}{
				{&outbox, `SELECT count(*) FROM outbox`},
				{&msgs, `SELECT count(*) FROM webhook_messages`},
				{&threads, `SELECT count(*) FROM inbox_threads`},
				{&items, `SELECT count(*) FROM inbox_items`},
			}
			for _, q := range qs {
				if serr := inner.QueryRowContext(tx.ctx, q.q).Scan(q.dst); serr != nil {
					return serr
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("counts: %v", err)
		}
		return outbox, msgs, threads, items
	}

	// (a) A rolled-back mutation removes its fan-out rows too.
	boom := errors.New("planned rollback")
	refBody := jsontext.Value(`{"repo":` + itoa(repo) + `,"name":"refs/heads/main","old":"000","new":"abc","source":"t"}`)
	o0, m0, th0, it0 := counts()
	if err := s.Write(ctx, func(tx *WriteTx) error {
		if _, eerr := tx.Outbox().Emit(Event{OccurredAt: now, Kind: "ref.updated", Body: refBody}); eerr != nil {
			return eerr
		}
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("write = %v, want rollback", err)
	}
	if o1, m1, th1, it1 := counts(); o1 != o0 || m1 != m0 || th1 != th0 || it1 != it0 {
		t.Fatalf("rollback leaked: outbox %d->%d messages %d->%d threads %d->%d items %d->%d",
			o0, o1, m0, m1, th0, th1, it0, it1)
	}

	// Inserting a task fans out in the same commit: two endpoint messages
	// (repo + owner scope; the task.closed endpoint stays silent) and threads
	// for reporter, assignee and the all-watcher.
	var publicID string
	var taskNum int64
	taskTime := time.UnixMilli(20100)
	if err := s.Write(ctx, func(tx *WriteTx) error {
		r, ierr := tx.InsertIntent(taskTime, IntentRow{
			RepoID: repo, Title: "fanout task", Body: "b", CreatedBy: reporter.ID,
			AssigneeUserID: sql.NullInt64{Int64: assignee.ID, Valid: true},
		})
		if ierr != nil {
			return ierr
		}
		publicID, taskNum = r.PublicID, r.Number
		return nil
	}); err != nil {
		t.Fatalf("insert intent: %v", err)
	}
	if err := s.Read(ctx, func(tx *ReadTx) error {
		msgs, _, lerr := tx.ListWebhookMessages(epTask.ID, "", 10)
		if lerr != nil || len(msgs) != 1 {
			t.Fatalf("task endpoint messages = %d, %v", len(msgs), lerr)
		}
		if msgs[0].EventType != "task.created" || msgs[0].State != "pending" {
			t.Fatalf("message = %+v", msgs[0])
		}
		if !strings.HasPrefix(msgs[0].ID, "msg_") {
			t.Fatalf("message id = %q", msgs[0].ID)
		}
		omsgs, _, lerr := tx.ListWebhookMessages(epOwner.ID, "", 10)
		if lerr != nil || len(omsgs) != 1 {
			t.Fatalf("owner endpoint messages = %d, %v", len(omsgs), lerr)
		}
		cross, _, lerr := tx.ListWebhookMessages(epOwnerOrg.ID, "", 10)
		if lerr != nil || len(cross) != 0 {
			t.Fatalf("cross-namespace owner endpoint messages = %d, %v; want 0", len(cross), lerr)
		}
		closed, _, lerr := tx.ListWebhookMessages(epOther.ID, "", 10)
		if lerr != nil || len(closed) != 0 {
			t.Fatalf("unsubscribed endpoint messages = %d, %v", len(closed), lerr)
		}
		refMsgs, _, lerr := tx.ListWebhookMessages(epRef.ID, "", 10)
		if lerr != nil || len(refMsgs) != 0 {
			t.Fatalf("ref endpoint messages for task event = %d", len(refMsgs))
		}
		for _, u := range []AuthUser{reporter, assignee, watcher} {
			threads, _, lerr := tx.ListInboxThreads(u.ID, "", "", 0, "", 10)
			if lerr != nil || len(threads) != 1 {
				t.Fatalf("user %d threads = %d, %v", u.ID, len(threads), lerr)
			}
			th := threads[0]
			if th.SubjectKind != "task" || th.SubjectID != publicID || th.State != "unread" {
				t.Fatalf("thread = %+v", th)
			}
			if th.UnreadItems != 1 || th.TotalItems != 1 {
				t.Fatalf("counters = %+v", th)
			}
			items, lerr := tx.ListInboxItems(th.ID, 10)
			if lerr != nil || len(items) != 1 {
				t.Fatalf("items = %d, %v", len(items), lerr)
			}
			if items[0].EventType != "task.created" || items[0].EventID == "" {
				t.Fatalf("item identity = %+v", items[0])
			}
			if items[0].ActorID != reporter.ID || items[0].ActorName != "fanreporter" {
				t.Fatalf("item actor = %d %q", items[0].ActorID, items[0].ActorName)
			}
			_ = taskNum
		}
		if n, cerr := tx.CountUnreadInbox(watcher.ID); cerr != nil || n != 1 {
			t.Fatalf("unread = %d, %v", n, cerr)
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}

	// (b)+(c): committing a ref.updated emit adds a message+thread+item in the
	// same read; replaying the identical event adds a message but no items.
	if err := s.Write(ctx, func(tx *WriteTx) error {
		_, eerr := tx.Outbox().Emit(Event{OccurredAt: now, Kind: "ref.updated", Body: refBody})
		return eerr
	}); err != nil {
		t.Fatalf("emit ref: %v", err)
	}
	var watcherThread string
	if err := s.Read(ctx, func(tx *ReadTx) error {
		msgs, _, lerr := tx.ListWebhookMessages(epRef.ID, "", 10)
		if lerr != nil || len(msgs) != 1 {
			t.Fatalf("ref messages = %d, %v", len(msgs), lerr)
		}
		threads, _, lerr := tx.ListInboxThreads(watcher.ID, "", "branch_view", 0, "", 10)
		if lerr != nil || len(threads) != 1 {
			t.Fatalf("branch threads = %d, %v", len(threads), lerr)
		}
		watcherThread = threads[0].ID
		items, lerr := tx.ListInboxItems(watcherThread, 10)
		if lerr != nil || len(items) != 1 {
			t.Fatalf("branch items = %d, %v", len(items), lerr)
		}
		// The reporter has no watch row (participate default) and the ref
		// body names no pusher, so only watchers get ref threads.
		mine, _, lerr := tx.ListInboxThreads(reporter.ID, "", "branch_view", 0, "", 10)
		if lerr != nil || len(mine) != 0 {
			t.Fatalf("reporter branch threads = %d", len(mine))
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
	_, mBefore, _, itBefore := counts()
	if err := s.Write(ctx, func(tx *WriteTx) error {
		_, eerr := tx.Outbox().Emit(Event{OccurredAt: now, Kind: "ref.updated", Body: refBody})
		return eerr
	}); err != nil {
		t.Fatalf("replay emit: %v", err)
	}
	// A second emission is a DISTINCT event row: new message and new item on
	// the same thread (only same-row double fan-out dedupes).
	if _, mAfter, _, itAfter := counts(); mAfter != mBefore+1 || itAfter != itBefore+1 {
		t.Fatalf("replay: messages %d->%d (want +1), items %d->%d (want +1)",
			mBefore, mAfter, itBefore, itAfter)
	}
	if err := s.Read(ctx, func(tx *ReadTx) error {
		th, gerr := tx.GetInboxThread(watcher.ID, watcherThread)
		if gerr != nil {
			return gerr
		}
		if th.UnreadItems != 2 || th.TotalItems != 2 {
			t.Fatalf("replayed counters = %+v", th)
		}
		items, lerr := tx.ListInboxItems(watcherThread, 10)
		if lerr != nil || len(items) != 2 {
			t.Fatalf("replayed items = %d, %v", len(items), lerr)
		}
		if items[0].EventID == items[1].EventID {
			t.Fatalf("distinct emissions share event id %q", items[0].EventID)
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
	// Unknown kinds fan out to nothing and never fail the mutation.
	if err := s.Write(ctx, func(tx *WriteTx) error {
		_, eerr := tx.Outbox().Emit(Event{OccurredAt: now, Kind: "push.received", Body: jsontext.Value(`{"x":1}`)})
		return eerr
	}); err != nil {
		t.Fatalf("unknown kind emit: %v", err)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
