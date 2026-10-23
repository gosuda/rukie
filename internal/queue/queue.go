package queue

import (
	"context"
	"errors"
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

// Claim leases up to limit jobs to owner for lease and stamps the batch
// with a fresh fencing token. The token travels on each returned Job:
// pass Job.LeaseToken to Renew, Complete, and Fail. A worker that lost
// its lease presents a stale token and gets ErrStale instead of
// completing another worker's job twice.
func (q *Queue) Claim(ctx context.Context, owner string, lease time.Duration, limit int) ([]store.Job, error) {
	token := string(store.NewID(q.clock()))
	var out []store.Job
	err := q.store.Write(ctx, func(tx *store.WriteTx) error {
		jobs, werr := tx.ClaimJobs(owner, token, q.clock(), lease, limit)
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

func (q *Queue) Renew(ctx context.Context, owner string, ids []string, token string, lease time.Duration) error {
	if len(token) == 0 {
		return fmt.Errorf("Renew: %w: lease token must not be empty", store.ErrInvalid)
	}
	return q.store.Write(ctx, func(tx *store.WriteTx) error {
		moved, werr := tx.RenewJobs(owner, token, ids, q.clock(), lease)
		if werr != nil {
			return werr
		}
		if moved != int64(len(ids)) {
			return fmt.Errorf("Renew: %w: renewed %d of %d leases", store.ErrStale, moved, len(ids))
		}
		return nil
	})
}

func (q *Queue) Complete(ctx context.Context, owner, id, token string) error {
	if len(token) == 0 {
		return fmt.Errorf("Complete: %w: lease token must not be empty", store.ErrInvalid)
	}
	return q.store.Write(ctx, func(tx *store.WriteTx) error {
		return tx.CompleteJob(owner, id, token, q.clock())
	})
}

func (q *Queue) Fail(ctx context.Context, owner, id, token string) error {
	if len(token) == 0 {
		return fmt.Errorf("Fail: %w: lease token must not be empty", store.ErrInvalid)
	}
	return q.store.Write(ctx, func(tx *store.WriteTx) error {
		return tx.RequeueJob(owner, id, token, q.clock())
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
	token := string(store.NewID(q.clock()))
	var out []store.Job
	err := q.store.Write(ctx, func(tx *store.WriteTx) error {
		jobs, werr := tx.ClaimKindJobs(owner, token, names, q.clock(), lease, limit)
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
		token := string(store.NewID(q.clock()))
		var out []store.Job
		err := q.store.Write(ctx, func(tx *store.WriteTx) error {
			jobs, werr := tx.ClaimKindJobs(cfg.Owner, token, kinds, q.clock(), cfg.Lease, cfg.Batch)
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
		if len(jobs) == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(cfg.Poll):
			}
			continue
		}
		failed := 0
		for _, job := range jobs {
			if rerr := q.Renew(ctx, cfg.Owner, []string{job.ID}, job.LeaseToken, cfg.Lease); rerr != nil {
				if errors.Is(rerr, store.ErrStale) {
					failed++
					continue
				}
				return rerr
			}
			if herr := handle(ctx, job); herr != nil {
				failed++
				if ferr := q.Fail(ctx, cfg.Owner, job.ID, job.LeaseToken); ferr != nil && !errors.Is(ferr, store.ErrStale) {
					return ferr
				}
				continue
			}
			if cerr := q.Complete(ctx, cfg.Owner, job.ID, job.LeaseToken); cerr != nil && !errors.Is(cerr, store.ErrStale) {
				return cerr
			}
		}
		if failed == len(jobs) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(cfg.Poll):
			}
		}
	}
}
