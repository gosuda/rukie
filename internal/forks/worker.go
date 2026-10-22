package forks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/store"
)

const claimLease = time.Minute

// Worker completes repo_fork jobs. The Ruk copy runs in the fork request
// before the row and job commit, so the job only flips the fork row from
// forking to active and closes the job in the same transaction.
type Worker struct {
	Store *store.Store
	Queue *queue.Queue
	Clock func() time.Time
}

func (w Worker) now() time.Time {
	if w.Clock != nil {
		return w.Clock()
	}
	return time.Now()
}

func Kinds() []queue.Kind {
	return []queue.Kind{queue.KindRepoFork}
}

func Config(owner string) queue.WorkerConfig {
	return queue.WorkerConfig{
		Owner: owner,
		Poll:  time.Second,
		Lease: claimLease,
		Batch: 8,
	}
}

// Run claims repo_fork jobs until ctx ends.
func (w Worker) Run(ctx context.Context, owner string) error {
	if w.Queue == nil {
		return fmt.Errorf("Run: fork worker is not configured")
	}
	return w.Queue.RunKinds(ctx, Config(owner), Kinds(), w.Handle)
}

// Handle completes one claimed repo_fork job. A lease that moved on or a
// row another writer already finished is not an error.
func (w Worker) Handle(ctx context.Context, job store.Job) error {
	if w.Store == nil {
		return fmt.Errorf("Handle: fork worker is not configured")
	}
	err := w.Store.Write(ctx, func(tx *store.WriteTx) error {
		return tx.CompleteFork(job.RepoID, job.LeaseOwner, job.ID, job.LeaseToken, w.now())
	})
	if err != nil && !errors.Is(err, store.ErrStale) {
		return fmt.Errorf("Handle: %w", err)
	}
	return nil
}

// DriveOnce claims and completes at most one repo_fork job. It reports
// whether a job was available.
func (w Worker) DriveOnce(ctx context.Context, owner string) (bool, error) {
	if w.Queue == nil || w.Store == nil {
		return false, fmt.Errorf("DriveOnce: fork worker is not configured")
	}
	jobs, err := w.Queue.ClaimKinds(ctx, owner, Kinds(), claimLease, 1)
	if err != nil {
		return false, fmt.Errorf("DriveOnce: %w", err)
	}
	if len(jobs) == 0 {
		return false, nil
	}
	if herr := w.Handle(ctx, jobs[0]); herr != nil {
		if ferr := w.Queue.Fail(ctx, owner, jobs[0].ID, jobs[0].LeaseToken); ferr != nil && !errors.Is(ferr, store.ErrStale) {
			return true, ferr
		}
		return true, herr
	}
	return true, nil
}
