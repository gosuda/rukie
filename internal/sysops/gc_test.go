package sysops

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/review"
	"github.com/gosuda/rukie/internal/store"
)

func TestGCForecastsTerminalReviewJobsAfterSelectedPurges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		filter string
		purged []string
	}{
		{name: "all", purged: []string{"ada/first", "ada/second"}},
		{name: "selected", filter: "ada/first", purged: []string{"ada/first"}},
		{name: "missing", filter: "ada/missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Ruk.Socket = ""
			cfg.Ruk.SocketPath = ""
			st := openTestStore(t, cfg.Store.SQLite.Path)
			ctx := t.Context()
			now := time.Now()
			old := now.Add(-3 * terminalJobRetention)
			owner, err := st.InsertAuthUser(ctx, store.AuthUserCreate{
				Name: "ada", Lower: "ada", Email: "ada@example.test",
				Hash: "test-hash", Algo: "argon2id-v19", Now: old.UnixMilli(),
			})
			if err != nil {
				t.Fatal(err)
			}
			jobsByRepo := make(map[string]string)
			if err := st.Write(ctx, func(tx *store.WriteTx) error {
				for _, name := range []string{"first", "second", "live"} {
					repoID, insertErr := tx.InsertRepo(old, store.RepoInsert{
						Name: name, Visibility: "private", DefaultBranch: "main",
						OwnerUserID: sql.NullInt64{Int64: owner.ID, Valid: true},
					})
					if insertErr != nil {
						return insertErr
					}
					change, ref, insertErr := review.IngressChange(tx, old, review.Ingress{
						RepoID: repoID, AuthorID: owner.ID, Branch: "main", HeadOid: strings.Repeat("a", 40),
					})
					if insertErr != nil {
						return insertErr
					}
					jobID := "terminal-remap-" + name
					inserted, insertErr := tx.EnqueueJob(store.JobSpec{
						At: old, ID: jobID, Kind: "review_remap", RepoID: repoID,
						Payload: fmt.Sprintf(`{"repo":%d,"landed":[{"name":%q,"new":%q}]}`, repoID, ref, change.HeadOid),
					})
					if insertErr != nil {
						return insertErr
					}
					if !inserted {
						return fmt.Errorf("TestGCForecastsTerminalReviewJobsAfterSelectedPurges: duplicate job %s", jobID)
					}
					jobsByRepo["ada/"+name] = jobID
					if name == "first" {
						for _, historyID := range []string{fmt.Sprintf("repo-purge:%d", repoID), "ordinary-history"} {
							if _, err := tx.EnqueueJob(store.JobSpec{
								At: old, ID: historyID, Kind: "history", RepoID: repoID, Payload: `{}`,
							}); err != nil {
								return err
							}
						}
					}
					if name != "live" {
						if err := tx.SoftDeleteRepo(repoID, old, old.Add(terminalJobRetention)); err != nil {
							return err
						}
					}
				}
				jobs, claimErr := tx.ClaimJobs("gc-review", "lease", old, time.Minute, 5)
				if claimErr != nil {
					return claimErr
				}
				if len(jobs) != 5 {
					return fmt.Errorf("TestGCForecastsTerminalReviewJobsAfterSelectedPurges: claimed %d jobs", len(jobs))
				}
				for _, job := range jobs {
					if job.Kind == "history" {
						if err := tx.CompleteJob("gc-review", job.ID, "lease", old); err != nil {
							return err
						}
						continue
					}
					if err := tx.FailJobPermanently("gc-review", job.ID, "lease", "immutable alignment exceeds its bound", old); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			cutoff := now.Add(-terminalJobRetention).UnixMilli()
			if count, countErr := st.CountTerminalJobs(ctx, cutoff); countErr != nil || count != 2 {
				t.Fatalf("retained review jobs changed the history count: %d, %v", count, countErr)
			}
			if err := st.Close(ctx); err != nil {
				t.Fatal(err)
			}
			dry, err := GC(ctx, cfg, true, tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			wantJobs := int64(len(tc.purged)) + 2
			if slices.Contains(tc.purged, "ada/first") {
				wantJobs--
			}
			if !slices.Equal(dry.ReposPurged, tc.purged) || dry.JobsWouldDrop != wantJobs || dry.JobsDeleted != 0 {
				t.Fatalf("dry-run forecast = %+v, want selected repos %v and %d jobs", dry, tc.purged, wantJobs)
			}
			st = openTestStore(t, cfg.Store.SQLite.Path)
			if count, countErr := st.CountTerminalJobs(ctx, cutoff); countErr != nil || count != 2 {
				t.Fatalf("dry run changed the current history count: %d, %v", count, countErr)
			}
			if err := st.Read(ctx, func(tx *store.ReadTx) error {
				for _, id := range jobsByRepo {
					job, getErr := tx.GetJob(id)
					if getErr != nil {
						return getErr
					}
					if job.State != "dead" || job.TerminalError == "" {
						return fmt.Errorf("TestGCForecastsTerminalReviewJobsAfterSelectedPurges: dry run changed %s", id)
					}
				}
				due, readErr := tx.DuePurges(now)
				if readErr != nil {
					return readErr
				}
				if len(due) != 2 {
					return fmt.Errorf("TestGCForecastsTerminalReviewJobsAfterSelectedPurges: dry run left %d due repos", len(due))
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := st.Close(ctx); err != nil {
				t.Fatal(err)
			}
			live, err := GC(ctx, cfg, false, tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(live.ReposPurged, tc.purged) || live.JobsDeleted != dry.JobsWouldDrop {
				t.Fatalf("live collection = %+v, dry forecast = %+v", live, dry)
			}
			st = openTestStore(t, cfg.Store.SQLite.Path)
			if err := st.Read(ctx, func(tx *store.ReadTx) error {
				for repo, id := range jobsByRepo {
					job, getErr := tx.GetJob(id)
					if slices.Contains(tc.purged, repo) {
						if !errors.Is(getErr, sql.ErrNoRows) {
							return fmt.Errorf("TestGCForecastsTerminalReviewJobsAfterSelectedPurges: purged job %s remains: %w", id, getErr)
						}
						continue
					}
					if getErr != nil {
						return getErr
					}
					if job.State != "dead" || job.TerminalError == "" {
						return fmt.Errorf("TestGCForecastsTerminalReviewJobsAfterSelectedPurges: unselected job %s changed", id)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
