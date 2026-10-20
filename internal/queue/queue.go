package queue

import (
	"context"
	"fmt"
	"time"

	"github.com/gosuda/rukie/internal/store"
)

type Kind string

const (
	KindHookDelivery Kind = "hook_delivery"
	KindMirrorRun    Kind = "mirror_run"
	KindRepoFork     Kind = "repo_fork"
	KindRepoPurge    Kind = "repo_purge"
	KindArchiveBuild Kind = "archive_build"
	KindLand         Kind = "land"
	KindCIPlan       Kind = "ci_plan"
)

var validKinds = map[Kind]struct{}{
	KindHookDelivery: {},
	KindMirrorRun:    {},
	KindRepoFork:     {},
	KindRepoPurge:    {},
	KindArchiveBuild: {},
	KindLand:         {},
	KindCIPlan:       {},
}

func ValidKind(kind Kind) bool {
	_, ok := validKinds[kind]
	return ok
}

func AllKinds() []Kind {
	return []Kind{
		KindHookDelivery,
		KindMirrorRun,
		KindRepoFork,
		KindRepoPurge,
		KindArchiveBuild,
		KindLand,
		KindCIPlan,
	}
}

type Queue struct {
	clock func() time.Time
	store *store.Store
}

func Open(st *store.Store, clock func() time.Time) *Queue {
	if clock == nil {
		clock = time.Now
	}
	return &Queue{store: st, clock: clock}
}

type EnqueueSpec struct {
	Payload string
	ID      string
	Kind    Kind
	RepoID  int64
}

func (q *Queue) Enqueue(ctx context.Context, spec EnqueueSpec) (bool, error) {
	if !ValidKind(spec.Kind) {
		return false, fmt.Errorf("Enqueue: unknown job kind %q", string(spec.Kind))
	}
	var inserted bool
	err := q.store.Write(ctx, func(tx *store.WriteTx) error {
		ok, werr := tx.EnqueueJob(store.JobSpec{
			Payload: spec.Payload,
			ID:      spec.ID,
			Kind:    string(spec.Kind),
			RepoID:  spec.RepoID,
			At:      q.clock(),
		})
		if werr != nil {
			return werr
		}
		inserted = ok
		return nil
	})
	if err != nil {
		return false, err
	}
	return inserted, nil
}

func (q *Queue) Claim(ctx context.Context, owner string, lease time.Duration, limit int) ([]store.Job, error) {
	var out []store.Job
	err := q.store.Write(ctx, func(tx *store.WriteTx) error {
		jobs, werr := tx.ClaimJobs(owner, q.clock(), lease, limit)
		if werr != nil {
			return werr
		}
		out = jobs
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (q *Queue) Renew(ctx context.Context, owner string, ids []string, lease time.Duration) error {
	return q.store.Write(ctx, func(tx *store.WriteTx) error {
		moved, werr := tx.RenewJobs(owner, ids, q.clock(), lease)
		if werr != nil {
			return werr
		}
		if moved != int64(len(ids)) {
			return fmt.Errorf("Renew: %w: renewed %d of %d leases", store.ErrStale, moved, len(ids))
		}
		return nil
	})
}

func (q *Queue) Complete(ctx context.Context, owner, id string) error {
	return q.store.Write(ctx, func(tx *store.WriteTx) error {
		return tx.CompleteJob(owner, id, q.clock())
	})
}

func (q *Queue) Fail(ctx context.Context, owner, id string) error {
	return q.store.Write(ctx, func(tx *store.WriteTx) error {
		return tx.RequeueJob(owner, id, q.clock())
	})
}

func (q *Queue) Job(ctx context.Context, id string) (store.Job, error) {
	var out store.Job
	err := q.store.Read(ctx, func(tx *store.ReadTx) error {
		job, rerr := tx.GetJob(id)
		if rerr != nil {
			return rerr
		}
		out = job
		return nil
	})
	if err != nil {
		return store.Job{}, err
	}
	return out, nil
}

type Handler func(ctx context.Context, job store.Job) error

type WorkerConfig struct {
	Owner string
	Poll  time.Duration
	Lease time.Duration
	Batch int
}

func (q *Queue) Run(ctx context.Context, cfg WorkerConfig, handle Handler) error {
	return q.runKinds(ctx, cfg, nil, handle)
}

func (q *Queue) RunKinds(ctx context.Context, cfg WorkerConfig, kinds []Kind, handle Handler) error {
	names := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		if !ValidKind(kind) {
			return fmt.Errorf("RunKinds: unknown job kind %q", string(kind))
		}
		names = append(names, string(kind))
	}
	return q.runKinds(ctx, cfg, names, handle)
}

func (q *Queue) ClaimKinds(ctx context.Context, owner string, kinds []Kind, lease time.Duration, limit int) ([]store.Job, error) {
	names := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		names = append(names, string(kind))
	}
	var out []store.Job
	err := q.store.Write(ctx, func(tx *store.WriteTx) error {
		jobs, werr := tx.ClaimKindJobs(owner, names, q.clock(), lease, limit)
		if werr != nil {
			return werr
		}
		out = jobs
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (q *Queue) runKinds(ctx context.Context, cfg WorkerConfig, kinds []string, handle Handler) error {
	if cfg.Batch <= 0 {
		return fmt.Errorf("Run: batch must be positive")
	}
	if cfg.Lease <= 0 {
		return fmt.Errorf("Run: lease must be positive")
	}
	if cfg.Poll <= 0 {
		return fmt.Errorf("Run: poll interval must be positive")
	}
	if len(cfg.Owner) == 0 {
		return fmt.Errorf("Run: owner must not be empty")
	}
	if handle == nil {
		return fmt.Errorf("Run: handler must not be nil")
	}
	claim := func() ([]store.Job, error) {
		if len(kinds) == 0 {
			return q.Claim(ctx, cfg.Owner, cfg.Lease, cfg.Batch)
		}
		var out []store.Job
		err := q.store.Write(ctx, func(tx *store.WriteTx) error {
			jobs, werr := tx.ClaimKindJobs(cfg.Owner, kinds, q.clock(), cfg.Lease, cfg.Batch)
			if werr != nil {
				return werr
			}
			out = jobs
			return nil
		})
		return out, err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		jobs, err := claim()
		if err != nil {
			return err
		}
		for _, job := range jobs {
			if herr := handle(ctx, job); herr != nil {
				if ferr := q.Fail(ctx, cfg.Owner, job.ID); ferr != nil {
					return ferr
				}
				continue
			}
			if cerr := q.Complete(ctx, cfg.Owner, job.ID); cerr != nil {
				return cerr
			}
		}
		if len(jobs) == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(cfg.Poll):
			}
		}
	}
}
