package repowatch

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/events"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

func unmarshalPayload(data []byte, out any) error {
	return jsonv2.Unmarshal(data, out, jsonv1.DefaultOptionsV1())
}

type Config struct {
	Store    *store.Store
	Queue    *queue.Queue
	Client   *ruk.Client
	Clock    func() time.Time
	RepoRoot string
}

type Watcher struct {
	store    *store.Store
	queue    *queue.Queue
	client   *ruk.Client
	clock    func() time.Time
	cancel   map[int64]context.CancelFunc
	repoRoot string
	wg       sync.WaitGroup
	mu       sync.Mutex
}

func New(cfg Config) *Watcher {
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Watcher{
		store:    cfg.Store,
		queue:    cfg.Queue,
		client:   cfg.Client,
		clock:    clock,
		repoRoot: cfg.RepoRoot,
		cancel:   map[int64]context.CancelFunc{},
	}
}

func (w *Watcher) now() time.Time {
	return w.clock()
}

func (w *Watcher) StartAll(ctx context.Context) error {
	if w.store == nil {
		return fmt.Errorf("repowatch: store is not configured")
	}
	var repos []store.RepoRow
	rerr := w.store.Read(ctx, func(tx *store.ReadTx) error {
		var lerr error
		repos, lerr = tx.LiveRepos(10000)
		return lerr
	})
	if rerr != nil {
		return rerr
	}
	for _, repo := range repos {
		w.Start(ctx, repo.ID)
	}
	return nil
}

func (w *Watcher) Start(ctx context.Context, repoID int64) {
	w.mu.Lock()
	if _, running := w.cancel[repoID]; running {
		w.mu.Unlock()
		return
	}
	child, cancel := context.WithCancel(ctx)
	w.cancel[repoID] = cancel
	w.wg.Add(1)
	w.mu.Unlock()
	go func() {
		defer w.wg.Done()
		w.watch(child, repoID)
		w.mu.Lock()
		delete(w.cancel, repoID)
		w.mu.Unlock()
	}()
}

func (w *Watcher) Stop(repoID int64) {
	w.mu.Lock()
	cancel, ok := w.cancel[repoID]
	if ok {
		delete(w.cancel, repoID)
	}
	w.mu.Unlock()
	if ok {
		cancel()
	}
}

func (w *Watcher) StopAll(ctx context.Context) error {
	w.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(w.cancel))
	for id, fn := range w.cancel {
		cancels = append(cancels, fn)
		delete(w.cancel, id)
	}
	w.mu.Unlock()
	for _, fn := range cancels {
		fn()
	}
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (w *Watcher) daemonPath(row store.RepoRow) string {
	if filepath.IsAbs(row.RukPath) {
		return filepath.Join(row.RukPath, ".ruk")
	}
	return filepath.Join(w.repoRoot, row.RukPath, ".ruk")
}

func (w *Watcher) watch(ctx context.Context, repoID int64) {
	if w.store == nil || w.client == nil {
		return
	}
	var row store.RepoRow
	rerr := w.store.Read(ctx, func(tx *store.ReadTx) error {
		var gerr error
		row, gerr = tx.GetRepo(repoID)
		return gerr
	})
	if rerr != nil {
		return
	}
	if row.DeletedAt.Valid {
		return
	}
	if row.RukPath == "" {
		return
	}
	repo, oerr := w.client.OpenRepo(ctx, w.daemonPath(row))
	if oerr != nil {
		return
	}
	follower := events.OpenFollower(w.store, repoID, repo, w.client, w.clock)
	reconciler := events.OpenReconciler(w.store, repoID, func(ctx context.Context, prefix, after string) (ruk.RefsListReply, error) {
		args := ruk.RefsListArgs{Prefix: prefix}
		if after != "" {
			cursor := after
			args.After = &cursor
		}
		return ruk.RefsList(ctx, w.client, repo, args)
	}, w.clock)
	var inner sync.WaitGroup
	inner.Add(2)
	go func() {
		defer inner.Done()
		_ = follower.Run(ctx)
	}()
	go func() {
		defer inner.Done()
		_ = reconciler.Run(ctx)
	}()
	inner.Wait()
}

func (w *Watcher) RunSweeper(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Hour
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		_ = w.SweepDuePurges(ctx)
		timer.Reset(interval)
	}
}

func (w *Watcher) SweepDuePurges(ctx context.Context) error {
	if w.store == nil || w.queue == nil {
		return fmt.Errorf("repowatch: store and queue are required")
	}
	var due []store.RepoRow
	rerr := w.store.Read(ctx, func(tx *store.ReadTx) error {
		var lerr error
		due, lerr = tx.DuePurges(w.now())
		return lerr
	})
	if rerr != nil {
		return rerr
	}
	for _, repo := range due {
		id := fmt.Sprintf("repo-purge:%d", repo.ID)
		_, gerr := w.queue.Job(ctx, id)
		if gerr == nil {
			continue
		}
		payload := fmt.Sprintf(`{"repo_id":%d,"path":%q,"not_before":%d}`,
			repo.ID, repo.RukPath, purgeTime(repo))
		_, eerr := w.queue.Enqueue(ctx, queue.EnqueueSpec{
			ID:      id,
			Kind:    queue.KindRepoPurge,
			RepoID:  repo.ID,
			Payload: payload,
		})
		if eerr != nil {
			return eerr
		}
	}
	return nil
}

func purgeTime(row store.RepoRow) int64 {
	if row.PurgeAfter.Valid {
		return row.PurgeAfter.Int64
	}
	return 0
}

type purgePayload struct {
	Path      string `json:"path"`
	NotBefore int64  `json:"not_before"`
	RepoID    int64  `json:"repo_id"`
}

func (w *Watcher) HandlePurge(ctx context.Context, job store.Job) error {
	if job.Kind != string(queue.KindRepoPurge) {
		return fmt.Errorf("repowatch: purge handler saw kind %q", job.Kind)
	}
	var payload purgePayload
	if uerr := unmarshalPayload([]byte(job.Payload), &payload); uerr != nil {
		return uerr
	}
	if payload.RepoID == 0 {
		payload.RepoID = job.RepoID
	}
	if w.now().UnixMilli() < payload.NotBefore {
		return fmt.Errorf("repowatch: purge for repo %d is not due", payload.RepoID)
	}
	if w.store == nil || w.client == nil {
		return fmt.Errorf("repowatch: store and client are required")
	}
	var row store.RepoRow
	rerr := w.store.Read(ctx, func(tx *store.ReadTx) error {
		var gerr error
		row, gerr = tx.GetRepo(payload.RepoID)
		return gerr
	})
	if rerr != nil {
		if errors.Is(rerr, store.ErrNotFound) {
			return nil
		}
		return rerr
	}
	if !row.DeletedAt.Valid {
		return nil
	}
	path := payload.Path
	if path == "" {
		path = row.RukPath
	}
	repo, oerr := w.client.OpenRepo(ctx, w.absRepo(path))
	if oerr != nil {
		return mapDaemonError(oerr)
	}
	if derr := w.deleteDaemonRepo(ctx, repo, path); derr != nil && !isDaemonNotFound(derr) {
		return derr
	}
	return w.store.Write(ctx, func(tx *store.WriteTx) error {
		return tx.HardDeleteRepo(row.ID)
	})
}

func (w *Watcher) absRepo(rel string) string {
	if filepath.IsAbs(rel) {
		return filepath.Join(rel, ".ruk")
	}
	return filepath.Join(w.repoRoot, rel, ".ruk")
}

type repoDeleteReply struct {
	DeletedPath string `json:"deleted_path"`
}

func (w *Watcher) deleteDaemonRepo(ctx context.Context, repo ruk.Repo, path string) error {
	_, rerr := ruk.Call[map[string]any, repoDeleteReply](ctx, w.client, repo, wire.MethodRepoDelete,
		map[string]any{"repo": path, "confirm_path": path})
	if rerr != nil {
		return mapDaemonError(rerr)
	}
	return nil
}

func mapDaemonError(err error) error {
	if err == nil {
		return nil
	}
	rerr, ok := errors.AsType[*ruk.Error](err)
	if !ok {
		return err
	}
	if mapped, ok := op.FromRuk(string(rerr.Code)); ok {
		return &op.Error{Code: mapped, Detail: rerr.Message}
	}
	return err
}

func isDaemonNotFound(err error) bool {
	rerr, ok := errors.AsType[*ruk.Error](err)
	if !ok {
		return false
	}
	return rerr.Code == wire.CodeNotFound
}

func PurgeKinds() []queue.Kind {
	return []queue.Kind{queue.KindRepoPurge}
}

func WorkerConfig(owner string) queue.WorkerConfig {
	return queue.WorkerConfig{
		Owner: owner,
		Poll:  time.Minute,
		Lease: 5 * time.Minute,
		Batch: 16,
	}
}
