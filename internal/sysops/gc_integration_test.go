//go:build integration

package sysops_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/config"
	"github.com/gosuda/rukie/internal/e2estack"
	"github.com/gosuda/rukie/internal/review"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/rukproc"
	"github.com/gosuda/rukie/internal/store"
	"github.com/gosuda/rukie/internal/sysops"
)

func TestGCRejectsUnavailableDaemonRepos(t *testing.T) {
	stack := e2estack.New(t, nil)
	forbidden := filepath.Join(t.TempDir(), ".ruk")
	if err := os.Mkdir(forbidden, 0o750); err != nil {
		t.Fatal(err)
	}
	relative := filepath.Join("ada", "valid.git")
	repoDir := filepath.Join(stack.Roots, relative)
	seedCtx, seedCancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer seedCancel()
	if out, err := rukproc.Run(seedCtx, e2estack.RukBin(t), []string{"git", "init", repoDir}, append(os.Environ(), "RUK_COORDINATOR=off")); err != nil {
		t.Fatalf("seed repository: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		want     error
		name     string
		path     string
		openPath string
	}{
		{want: ruk.ErrNotFound, name: "missing", path: "missing", openPath: filepath.Join(stack.Roots, "missing", ".ruk")},
		{want: ruk.ErrForbidden, name: "forbidden", path: forbidden, openPath: forbidden},
		{name: "relative", path: relative, openPath: filepath.Join(repoDir, ".ruk")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			if _, err := stack.Client.OpenRepo(ctx, tc.openPath); !errors.Is(err, tc.want) {
				t.Fatalf("daemon preflight = %v, want %v", err, tc.want)
			}
			cfg := config.Defaults()
			cfg.DataDir = t.TempDir()
			cfg.Store.SQLite.Path = filepath.Join(cfg.DataDir, "metadata.sqlite")
			cfg.Ruk.RepoRoot = stack.Roots
			cfg.Ruk.Socket = stack.Socket
			cfg.Ruk.SocketPath = stack.Socket
			st, openErr := store.Open(ctx, store.Options{Path: cfg.Store.SQLite.Path})
			if openErr != nil {
				t.Fatal(openErr)
			}
			t.Cleanup(func() { _ = st.Close(context.Background()) })
			now := time.Now()
			old := now.Add(-90 * 24 * time.Hour)
			cutoff := now.Add(-30 * 24 * time.Hour).UnixMilli()
			owner, ownerErr := st.InsertAuthUser(ctx, store.AuthUserCreate{
				Name: "ada", Lower: "ada", Email: "ada@example.test",
				Hash: "test-hash", Algo: "argon2id-v19", Now: old.UnixMilli(),
			})
			if ownerErr != nil {
				t.Fatal(ownerErr)
			}
			var repoID int64
			if err := st.Write(ctx, func(tx *store.WriteTx) error {
				var insertErr error
				repoID, insertErr = tx.InsertRepo(old, store.RepoInsert{
					Name: "broken", Visibility: "private", DefaultBranch: "main", RukPath: tc.path,
					OwnerUserID: sql.NullInt64{Int64: owner.ID, Valid: true},
				})
				if insertErr != nil {
					return fmt.Errorf("TestGCRejectsUnavailableDaemonRepos: %w", insertErr)
				}
				change, ref, ingressErr := review.IngressChange(tx, old, review.Ingress{
					RepoID: repoID, AuthorID: owner.ID, Branch: "main", HeadOid: strings.Repeat("a", 40),
				})
				if ingressErr != nil {
					return fmt.Errorf("TestGCRejectsUnavailableDaemonRepos: %w", ingressErr)
				}
				for _, spec := range []store.JobSpec{
					{At: old, ID: "terminal-remap", Kind: "review_remap", RepoID: repoID,
						Payload: fmt.Sprintf(`{"repo":%d,"landed":[{"name":%q,"new":%q}]}`, repoID, ref, change.HeadOid)},
					{At: old, ID: "ordinary-history", Kind: "history", RepoID: repoID, Payload: `{}`},
				} {
					inserted, enqueueErr := tx.EnqueueJob(spec)
					if enqueueErr != nil {
						return fmt.Errorf("TestGCRejectsUnavailableDaemonRepos: %w", enqueueErr)
					}
					if !inserted {
						return fmt.Errorf("TestGCRejectsUnavailableDaemonRepos: duplicate job %s", spec.ID)
					}
				}
				if deleteErr := tx.SoftDeleteRepo(repoID, old, old.Add(30*24*time.Hour)); deleteErr != nil {
					return fmt.Errorf("TestGCRejectsUnavailableDaemonRepos: %w", deleteErr)
				}
				jobs, claimErr := tx.ClaimJobs("gc-review", "lease", old, time.Minute, 2)
				if claimErr != nil {
					return fmt.Errorf("TestGCRejectsUnavailableDaemonRepos: %w", claimErr)
				}
				if len(jobs) != 2 {
					return fmt.Errorf("TestGCRejectsUnavailableDaemonRepos: claimed %d jobs", len(jobs))
				}
				for _, job := range jobs {
					if job.Kind == "history" {
						if completeErr := tx.CompleteJob("gc-review", job.ID, "lease", old); completeErr != nil {
							return fmt.Errorf("TestGCRejectsUnavailableDaemonRepos: %w", completeErr)
						}
						continue
					}
					if failErr := tx.FailJobPermanently("gc-review", job.ID, "lease", "alignment unavailable", old); failErr != nil {
						return fmt.Errorf("TestGCRejectsUnavailableDaemonRepos: %w", failErr)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if count, countErr := st.CountTerminalJobs(ctx, cutoff); countErr != nil || count != 1 {
				t.Fatalf("prunable jobs = %d, %v; want only history", count, countErr)
			}
			if err := st.Close(ctx); err != nil {
				t.Fatal(err)
			}
			for _, dryRun := range []bool{true, false} {
				report, gcErr := sysops.GC(ctx, cfg, dryRun, "ada/broken")
				if gcErr != nil {
					t.Fatal(gcErr)
				}
				wantJobs := int64(1)
				if tc.want == nil {
					wantJobs++
					if !slices.Equal(report.ReposPurged, []string{"ada/broken"}) || len(report.ReposPending) != 0 || len(report.ReposFailed) != 0 {
						t.Fatalf("dry=%t: cleanup classification = %+v", dryRun, report)
					}
					if _, statErr := os.Stat(tc.openPath); (dryRun && statErr != nil) || (!dryRun && !errors.Is(statErr, os.ErrNotExist)) {
						t.Fatalf("dry=%t: repository on disk: %v", dryRun, statErr)
					}
				} else if len(report.ReposPurged) != 0 || len(report.ReposPending) != 0 || len(report.ReposFailed) != 1 ||
					report.ReposFailed[0].Repo != "ada/broken" || !strings.Contains(report.ReposFailed[0].Reason, tc.want.Error()) {
					t.Fatalf("dry=%t: cleanup classification = %+v", dryRun, report)
				}
				wantDrop, wantDeleted := int64(0), wantJobs
				if dryRun {
					wantDrop, wantDeleted = wantJobs, 0
				}
				if report.JobsWouldDrop != wantDrop || report.JobsDeleted != wantDeleted {
					t.Fatalf("dry=%t: cleanup counted retained remap: %+v", dryRun, report)
				}
				opened, reopenErr := store.Open(ctx, store.Options{Path: cfg.Store.SQLite.Path})
				if reopenErr != nil {
					t.Fatal(reopenErr)
				}
				st = opened
				if err := st.Read(ctx, func(tx *store.ReadTx) error {
					job, getErr := tx.GetJob("terminal-remap")
					if tc.want == nil && !dryRun {
						if getErr == nil {
							return fmt.Errorf("TestGCRejectsUnavailableDaemonRepos: purged remap remains")
						}
						if !errors.Is(getErr, sql.ErrNoRows) {
							return fmt.Errorf("TestGCRejectsUnavailableDaemonRepos: %w", getErr)
						}
					} else {
						if getErr != nil {
							return fmt.Errorf("TestGCRejectsUnavailableDaemonRepos: %w", getErr)
						}
						if job.State != "dead" || job.TerminalError != "alignment unavailable" {
							return fmt.Errorf("TestGCRejectsUnavailableDaemonRepos: retained remap changed")
						}
					}
					due, dueErr := tx.DuePurges(now)
					if dueErr != nil {
						return fmt.Errorf("TestGCRejectsUnavailableDaemonRepos: %w", dueErr)
					}
					if tc.want == nil && !dryRun {
						if len(due) != 0 {
							return fmt.Errorf("TestGCRejectsUnavailableDaemonRepos: purged repository remains")
						}
					} else if len(due) != 1 || due[0].ID != repoID || due[0].RukPath != tc.path {
						return fmt.Errorf("TestGCRejectsUnavailableDaemonRepos: rejected repository metadata changed")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				wantCount := int64(0)
				if dryRun {
					wantCount = 1
				}
				if count, countErr := st.CountTerminalJobs(ctx, cutoff); countErr != nil || count != wantCount {
					t.Fatalf("dry=%t: remaining prunable jobs = %d, %v; want %d", dryRun, count, countErr, wantCount)
				}
				if err := st.Close(ctx); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
