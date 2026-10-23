//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/store"
	"github.com/gosuda/rukie/internal/webhook"
)

func webhookKillWorker(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	s := newStack(t, nil)
	h := mountAPI(t, s)
	admin := s.addAdmin(t, "ada", "repo:read", "repo:write")
	repoID := s.addRepo(t, "ada", "hooks", "private")
	session := apiLogin(t, h, "ada", "long-test-password-01")

	const sealKey = "0123456789abcdef0123456789abcdef"
	now := time.Now()
	endpointID := "wh_" + string(store.NewID(now))
	sealed, serr := webhook.SealSecret([]byte(sealKey), []byte("fault-secret-32-bytes-long!!!!!"), "webhook-endpoint-secret:"+endpointID)
	if serr != nil {
		t.Fatalf("seal: %v", serr)
	}
	var endpoint store.WebhookEndpointRow
	if werr := s.st.Write(ctx, func(tx *store.WriteTx) error {
		row, ierr := tx.InsertWebhookEndpoint(now, store.WebhookEndpointInsert{
			ID: endpointID, Scope: "repo", RepoID: repoID, URL: "https://hooks.example.test/hook",
			SecretEnc:  sealed,
			EventTypes: []string{"task.created"}, Active: true, Concurrency: 4, TimeoutSeconds: 20,
			Note: "fault", CreatedBy: admin.user.ID,
		})
		if ierr != nil {
			return ierr
		}
		endpoint = row
		return nil
	}); werr != nil {
		t.Fatalf("insert endpoint: %v", werr)
	}
	status, made := apiCall(t, h, "POST", "/ada/hooks/tasks/new", session, `{"title":"Hook the panel","body":"Lease the docs subtree.","lease_mode":"advise","claim_paths":["docs/"]}`)
	if status != http.StatusCreated {
		t.Fatalf("task.create = %d: %v", status, made)
	}
	msgID := ""
	if rerr := s.st.Read(ctx, func(tx *store.ReadTx) error {
		rows, _, lerr := tx.ListWebhookMessages(endpoint.ID, "", 10)
		if lerr != nil {
			return lerr
		}
		matched := 0
		for _, row := range rows {
			if row.EventType == "task.created" && row.State == "pending" {
				msgID = row.ID
				matched++
			}
		}
		if matched != 1 {
			t.Fatalf("task.created rows = %d, want exactly 1", matched)
		}
		return nil
	}); rerr != nil {
		t.Fatalf("find message: %v", rerr)
	}
	if msgID == "" {
		t.Fatal("no task.created message fanned out")
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
	worker1 := webhook.NewWorker(webhook.WorkerConfig{
		Store: s.st, SealKey: []byte(sealKey), Now: time.Now,
		Send: func(ctx context.Context, m store.WebhookMessageRow, _ store.WebhookEndpointRow, _ webhook.DeliverySecrets, _ int64, _ []byte) (int64, []byte, []byte, string) {
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
		var claimed []store.WebhookMessageRow
		if rerr := s.st.Read(ctx, func(tx *store.ReadTx) error {
			rows, _, lerr := tx.ListWebhookMessages(endpoint.ID, "", 10)
			if lerr != nil {
				return lerr
			}
			for _, row := range rows {
				if row.EventType == "task.created" {
					claimed = append(claimed, row)
				}
			}
			return nil
		}); rerr != nil {
			t.Fatalf("read claimed: %v", rerr)
		}
		if len(claimed) == 1 && claimed[0].State == "inflight" {
			claimedID = claimed[0].ID
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker1 never claimed inflight: %+v", claimed)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if claimedID != msgID {
		t.Fatalf("claimed id = %q, want fanned-out %q", claimedID, msgID)
	}

	warped := now.Add(6 * time.Minute)
	worker2 := webhook.NewWorker(webhook.WorkerConfig{
		Store: s.st, SealKey: []byte(sealKey),
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
	if err := s.st.Read(ctx, func(tx *store.ReadTx) error {
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
	driver := webhook.NewWorker(webhook.WorkerConfig{
		Store: s.st, SealKey: []byte(sealKey), Now: time.Now,
		Send: func(_ context.Context, m store.WebhookMessageRow, _ store.WebhookEndpointRow, _ webhook.DeliverySecrets, _ int64, _ []byte) (int64, []byte, []byte, string) {
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
	if err := s.st.Read(ctx, func(tx *store.ReadTx) error {
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
