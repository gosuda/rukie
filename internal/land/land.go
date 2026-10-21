// Package land drives land plan execution keyed by repository and target
// ref. Runs enqueue as land queue jobs; the worker claims one job at a
// time and routes execution through the pending land.execute call site,
// which activates on the F2 pin. Failures requeue with the job backoff;
// poison jobs die in place instead of spinning the writer.
package land

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/review"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

// Key identifies one land worker lane.
type Key struct {
	Target string
	RepoID int64
}

// JobID returns the deterministic job id for one land run.
func (k Key) JobID(runPublicID string) string {
	return "land:" + strings.TrimSpace(k.Target) + ":" + runPublicID
}

// runPayload is the land job body.
type runPayload struct {
	Run  string `json:"run"`
	Plan string `json:"plan"`
}

// Worker claims land jobs and executes their runs.
type Worker struct {
	Ruk      *ruk.Client
	Store    *store.Store
	Queue    *queue.Queue
	Clock    func() time.Time
	RepoRoot string
}

func (w Worker) now() time.Time {
	if w.Clock != nil {
		return w.Clock()
	}
	return time.Now()
}

// EnqueueRun records one execution job for a land run.
func (w Worker) EnqueueRun(ctx context.Context, key Key, planPublicID, runPublicID string) (bool, error) {
	if w.Queue == nil {
		return false, fmt.Errorf("land: queue is not configured")
	}
	payload, merr := jsonv2.Marshal(runPayload{Run: runPublicID, Plan: planPublicID}, jsonv1.DefaultOptionsV1())
	if merr != nil {
		return false, merr
	}
	return w.Queue.Enqueue(ctx, queue.EnqueueSpec{
		ID:      key.JobID(runPublicID),
		Kind:    queue.KindLand,
		RepoID:  key.RepoID,
		Payload: string(payload),
	})
}

// DriveOnce claims one land job and executes its run. It returns false when
// no job is ready. Execution routes through the pending land.execute call
// site; until the F2 pin lands, the job requeues with the E_HOSTED detail
// and the run stays recorded for retry.
func (w Worker) DriveOnce(ctx context.Context, owner string) (bool, error) {
	if w.Queue == nil || w.Store == nil || w.Ruk == nil {
		return false, fmt.Errorf("land: worker is not configured")
	}
	claimed, cerr := w.Queue.Claim(ctx, owner, time.Minute, 1)
	if cerr != nil {
		return false, cerr
	}
	if len(claimed) == 0 {
		return false, nil
	}
	job := claimed[0]
	if job.Kind != string(queue.KindLand) {
		return false, fmt.Errorf("land: claimed job kind %q", job.Kind)
	}
	ectx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	derr := w.drive(ectx, job)
	if derr != nil {
		return true, derr
	}
	return true, nil
}

func (w Worker) drive(ctx context.Context, job store.Job) error {
	var payload runPayload
	if uerr := jsonv2.Unmarshal([]byte(job.Payload), &payload, jsonv1.DefaultOptionsV1()); uerr != nil {
		return w.finish(ctx, job, uerr)
	}
	if payload.Run == "" || payload.Plan == "" {
		return w.finish(ctx, job, fmt.Errorf("land: job %q names no run", job.ID))
	}
	var plan store.LandIntentRow
	var run store.LandRunRow
	var repo store.RepoRow
	rerr := w.Store.Read(ctx, func(tx *store.ReadTx) error {
		var gerr error
		plan, gerr = tx.ReadLandIntentByPublicID(job.RepoID, payload.Plan)
		if gerr != nil {
			return gerr
		}
		runs, gerr := tx.ListLandRunsByPlanID(plan.ID)
		if gerr != nil {
			return gerr
		}
		for _, candidate := range runs {
			if candidate.PublicID == payload.Run {
				run = candidate
			}
		}
		if run.ID == 0 {
			return fmt.Errorf("land: run %q is not recorded", payload.Run)
		}
		repo, gerr = tx.GetRepo(job.RepoID)
		return gerr
	})
	if rerr != nil {
		return w.finish(ctx, job, rerr)
	}
	daemon, oerr := w.Ruk.OpenRepo(ctx, daemonPath(w.RepoRoot, repo.RukPath))
	if oerr != nil {
		return w.finish(ctx, job, oerr)
	}
	serr := w.Store.Write(ctx, func(tx *store.WriteTx) error {
		return tx.SetLandRunState(w.now(), run.ID, "executing")
	})
	if serr != nil {
		return w.finish(ctx, job, serr)
	}
	var reply map[string]any
	cerr := review.PendingLandExecute(ctx, clientCaller{client: w.Ruk}, daemon, map[string]any{
		"candidate": plan.CandidateOid,
		"plan":      plan.PublicID,
	}, &reply)
	if cerr != nil {
		_ = w.Store.Write(ctx, func(tx *store.WriteTx) error {
			return tx.SetLandRunState(w.now(), run.ID, "awaiting")
		})
		return w.finish(ctx, job, cerr)
	}
	return w.finish(ctx, job, w.Store.Write(ctx, func(tx *store.WriteTx) error {
		if serr := tx.SetLandRunState(w.now(), run.ID, "landed"); serr != nil {
			return serr
		}
		if serr := tx.SetLandIntentState(w.now(), plan.ID, "landed"); serr != nil {
			return serr
		}
		if plan.ChangeID.Valid {
			return tx.SetChangeState(w.now(), plan.ChangeID.Int64, "landed")
		}
		return nil
	}))
}

func (w Worker) finish(ctx context.Context, job store.Job, err error) error {
	if err == nil {
		return w.Store.Write(ctx, func(tx *store.WriteTx) error {
			return tx.CompleteJob(job.LeaseOwner, job.ID, job.LeaseToken, w.now())
		})
	}
	rerr := w.Store.Write(ctx, func(tx *store.WriteTx) error {
		return tx.RequeueJob(job.LeaseOwner, job.ID, job.LeaseToken, w.now())
	})
	if rerr != nil {
		return rerr
	}
	return err
}

// clientCaller adapts a daemon client to the pending-call surface.
type clientCaller struct {
	client *ruk.Client
}

func (c clientCaller) CallRuk(ctx context.Context, repo ruk.Repo, method wire.Method, args map[string]any, out any) error {
	raw, rerr := ruk.Call[map[string]any, jsonv1.RawMessage](ctx, c.client, repo, method, args)
	if rerr != nil {
		return rerr
	}
	var decoded map[string]any
	if uerr := jsonv2.Unmarshal([]byte(raw), &decoded, jsonv1.DefaultOptionsV1()); uerr != nil {
		return uerr
	}
	encoded, _ := jsonv2.Marshal(decoded, jsonv1.DefaultOptionsV1())
	return jsonv2.Unmarshal(encoded, out, jsonv1.DefaultOptionsV1())
}

// daemonPath joins the repository root with a stored relative daemon path.
func daemonPath(root, rel string) string {
	return filepath.Join(root, filepath.Clean("/"+rel))
}
