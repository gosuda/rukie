package sysops

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/review"
	"github.com/gosuda/rukie/internal/store"
)

func TestGCForecastsTerminalReviewJobsAfterSelectedPurges(t *testing.T) {
	for _, tc := range []struct {
		name        string
		filter      string
		purged      []string
		pending     []string
		daemonRepos []string
	}{
		{name: "all", purged: []string{"ada/first", "ada/second"}},
		{name: "selected", filter: "ada/first", purged: []string{"ada/first"}},
		{name: "missing", filter: "ada/missing"},
		{name: "daemon-stopped", daemonRepos: []string{"first", "second"}, pending: []string{"ada/first", "ada/second"}},
		{name: "mixed", daemonRepos: []string{"first"}, pending: []string{"ada/first"}, purged: []string{"ada/second"}},
		{name: "daemon-filtered", filter: "ada/first", daemonRepos: []string{"first", "second"}, pending: []string{"ada/first"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Ruk.Socket = ""
			cfg.Ruk.SocketPath = ""
			if len(tc.daemonRepos) != 0 {
				cfg.Ruk.Socket = filepath.Join(cfg.DataDir, "stopped-daemon.sock")
			}
			st := openTestStore(t, cfg.Store.SQLite.Path)
			ctx := t.Context()
			now := time.Now()
			old := now.Add(-3 * terminalJobRetention)
			owner, ownerErr := st.InsertAuthUser(ctx, store.AuthUserCreate{
				Name: "ada", Lower: "ada", Email: "ada@example.test",
				Hash: "test-hash", Algo: "argon2id-v19", Now: old.UnixMilli(),
			})
			if ownerErr != nil {
				t.Fatal(ownerErr)
			}
			jobsByRepo := make(map[string]string)
			if err := st.Write(ctx, func(tx *store.WriteTx) error {
				for _, name := range []string{"first", "second", "live"} {
					var rukPath string
					if slices.Contains(tc.daemonRepos, name) {
						rukPath = filepath.Join(cfg.Ruk.RepoRoot, name)
					}
					repoID, insertErr := tx.InsertRepo(old, store.RepoInsert{
						Name: name, Visibility: "private", DefaultBranch: "main",
						OwnerUserID: sql.NullInt64{Int64: owner.ID, Valid: true},
						RukPath:     rukPath,
					})
					if insertErr != nil {
						return fmt.Errorf("TestGCForecastsTerminalReviewJobsAfterSelectedPurges: %w", insertErr)
					}
					change, ref, insertErr := review.IngressChange(tx, old, review.Ingress{
						RepoID: repoID, AuthorID: owner.ID, Branch: "main", HeadOid: strings.Repeat("a", 40),
					})
					if insertErr != nil {
						return fmt.Errorf("TestGCForecastsTerminalReviewJobsAfterSelectedPurges: %w", insertErr)
					}
					jobID := "terminal-remap-" + name
					inserted, insertErr := tx.EnqueueJob(store.JobSpec{
						At: old, ID: jobID, Kind: "review_remap", RepoID: repoID,
						Payload: fmt.Sprintf(`{"repo":%d,"landed":[{"name":%q,"new":%q}]}`, repoID, ref, change.HeadOid),
					})
					if insertErr != nil {
						return fmt.Errorf("TestGCForecastsTerminalReviewJobsAfterSelectedPurges: %w", insertErr)
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
								return fmt.Errorf("TestGCForecastsTerminalReviewJobsAfterSelectedPurges: %w", err)
							}
						}
					}
					if name != "live" {
						if err := tx.SoftDeleteRepo(repoID, old, old.Add(terminalJobRetention)); err != nil {
							return fmt.Errorf("TestGCForecastsTerminalReviewJobsAfterSelectedPurges: %w", err)
						}
					}
				}
				jobs, claimErr := tx.ClaimJobs("gc-review", "lease", old, time.Minute, 5)
				if claimErr != nil {
					return fmt.Errorf("TestGCForecastsTerminalReviewJobsAfterSelectedPurges: %w", claimErr)
				}
				if len(jobs) != 5 {
					return fmt.Errorf("TestGCForecastsTerminalReviewJobsAfterSelectedPurges: claimed %d jobs", len(jobs))
				}
				for _, job := range jobs {
					if job.Kind == "history" {
						if err := tx.CompleteJob("gc-review", job.ID, "lease", old); err != nil {
							return fmt.Errorf("TestGCForecastsTerminalReviewJobsAfterSelectedPurges: %w", err)
						}
						continue
					}
					if err := tx.FailJobPermanently("gc-review", job.ID, "lease", "immutable alignment exceeds its bound", old); err != nil {
						return fmt.Errorf("TestGCForecastsTerminalReviewJobsAfterSelectedPurges: %w", err)
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
			before, beforeErr := os.ReadFile(cfg.Store.SQLite.Path)
			if beforeErr != nil {
				t.Fatal(beforeErr)
			}
			dry, dryErr := GC(ctx, cfg, true, tc.filter)
			if dryErr != nil {
				t.Fatal(dryErr)
			}
			after, afterErr := os.ReadFile(cfg.Store.SQLite.Path)
			if afterErr != nil {
				t.Fatal(afterErr)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("dry run changed metadata database bytes")
			}
			wantJobs := int64(len(tc.purged)) + 2
			if slices.Contains(tc.purged, "ada/first") {
				wantJobs--
			}
			wantPending := make([]GCPending, len(tc.pending))
			for i, repo := range tc.pending {
				wantPending[i] = GCPending{Repo: repo, Reason: "daemon cleanup unverified in dry run; metadata kept"}
			}
			if !slices.Equal(dry.ReposPurged, tc.purged) || !slices.Equal(dry.ReposPending, wantPending) ||
				len(dry.ReposFailed) != 0 || dry.JobsWouldDrop != wantJobs || dry.JobsDeleted != 0 {
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
						return fmt.Errorf("TestGCForecastsTerminalReviewJobsAfterSelectedPurges: %w", getErr)
					}
					if job.State != "dead" || job.TerminalError == "" {
						return fmt.Errorf("TestGCForecastsTerminalReviewJobsAfterSelectedPurges: dry run changed %s", id)
					}
				}
				due, readErr := tx.DuePurges(now)
				if readErr != nil {
					return fmt.Errorf("TestGCForecastsTerminalReviewJobsAfterSelectedPurges: %w", readErr)
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
			live, liveErr := GC(ctx, cfg, false, tc.filter)
			if liveErr != nil {
				t.Fatal(liveErr)
			}
			for i := range wantPending {
				wantPending[i].Reason = "daemon unreachable; metadata kept"
			}
			if !slices.Equal(live.ReposPurged, tc.purged) || !slices.Equal(live.ReposPending, wantPending) ||
				len(live.ReposFailed) != 0 || live.JobsDeleted != dry.JobsWouldDrop {
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
						return fmt.Errorf("TestGCForecastsTerminalReviewJobsAfterSelectedPurges: %w", getErr)
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
