//go:build integration

package sysops_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"maps"
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
	allowed := e2estack.New(t, nil)
	denied := e2estack.NewNoAdmin(t, nil)
	forbidden := filepath.Join(t.TempDir(), ".ruk")
	if err := os.Mkdir(forbidden, 0o750); err != nil {
		t.Fatal(err)
	}
	relative := filepath.Join("ada", "valid.git")
	for _, tc := range []struct {
		stack    *e2estack.Stack
		want     error
		name     string
		path     string
		openPath string
		seed     bool
	}{
		{stack: allowed, want: ruk.ErrNotFound, name: "missing", path: "missing", openPath: filepath.Join(allowed.Roots, "missing", ".ruk")},
		{stack: allowed, want: ruk.ErrForbidden, name: "forbidden", path: forbidden, openPath: forbidden},
		{stack: allowed, name: "relative", path: relative, openPath: filepath.Join(allowed.Roots, relative, ".ruk"), seed: true},
		{stack: denied, want: ruk.ErrForbidden, name: "denied", path: relative, openPath: filepath.Join(denied.Roots, relative, ".ruk"), seed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			segmentPath := filepath.Join(tc.openPath, "segments", "seg_0000000000000000")
			var committedSize int64
			if tc.seed {
				repoDir := filepath.Dir(tc.openPath)
				env := append(os.Environ(), "RUK_COORDINATOR=off")
				if out, err := rukproc.Run(ctx, e2estack.RukBin(t), []string{"git", "init", repoDir}, env); err != nil {
					t.Fatalf("seed repository: %v\n%s", err, out)
				}
				payload := filepath.Join(t.TempDir(), "payload")
				if err := os.WriteFile(payload, []byte("cold repository recovery probe\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if out, err := rukproc.Run(ctx, e2estack.RukBin(t), []string{"git", "--git-dir=" + repoDir, "hash-object", "-w", payload}, env); err != nil {
					t.Fatalf("seed blob: %v\n%s", err, out)
				}
				committed, readErr := os.ReadFile(segmentPath)
				if readErr != nil {
					t.Fatal(readErr)
				}
				committedSize = int64(len(committed))
				if err := os.WriteFile(segmentPath, append(committed, bytes.Repeat([]byte{0xaa}, 100)...), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := config.Defaults()
			cfg.DataDir = t.TempDir()
			cfg.Store.SQLite.Path = filepath.Join(cfg.DataDir, "metadata.sqlite")
			cfg.Ruk.RepoRoot = tc.stack.Roots
			cfg.Ruk.Socket = tc.stack.Socket
			cfg.Ruk.SocketPath = tc.stack.Socket
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
			before := gcRepositoryFiles(t, tc.openPath)
			metadataBefore, metadataErr := os.ReadFile(cfg.Store.SQLite.Path)
			if metadataErr != nil {
				t.Fatal(metadataErr)
			}
			for _, dryRun := range []bool{true, false} {
				report, gcErr := sysops.GC(ctx, cfg, dryRun, "ada/broken")
				if gcErr != nil {
					t.Fatal(gcErr)
				}
				wantJobs := int64(1)
				if dryRun {
					after := gcRepositoryFiles(t, tc.openPath)
					if !maps.EqualFunc(before, after, func(a, b gcFile) bool {
						return a.mode == b.mode && bytes.Equal(a.data, b.data)
					}) {
						t.Fatal("preview changed cold repository files")
					}
					metadataAfter, readErr := os.ReadFile(cfg.Store.SQLite.Path)
					if readErr != nil {
						t.Fatal(readErr)
					}
					if !bytes.Equal(metadataBefore, metadataAfter) {
						t.Fatal("preview changed metadata database bytes")
					}
					wantPending := []sysops.GCPending{{Repo: "ada/broken", Reason: "daemon cleanup unverified in dry run; metadata kept"}}
					if len(report.ReposPurged) != 0 || len(report.ReposFailed) != 0 || !slices.Equal(report.ReposPending, wantPending) {
						t.Fatalf("preview claimed daemon cleanup readiness: %+v", report)
					}
				} else if tc.want == nil {
					wantJobs++
					if !slices.Equal(report.ReposPurged, []string{"ada/broken"}) || len(report.ReposPending) != 0 || len(report.ReposFailed) != 0 {
						t.Fatalf("dry=%t: cleanup classification = %+v", dryRun, report)
					}
					if _, statErr := os.Stat(tc.openPath); !errors.Is(statErr, os.ErrNotExist) {
						t.Fatalf("purged repository on disk: %v", statErr)
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
				if dryRun {
					_, probeErr := tc.stack.Client.OpenRepo(ctx, tc.openPath)
					if tc.seed {
						if probeErr != nil {
							t.Fatal(probeErr)
						}
						info, statErr := os.Stat(segmentPath)
						if statErr != nil {
							t.Fatal(statErr)
						}
						if info.Size() != committedSize {
							t.Fatalf("cold open left segment size %d, want recovered size %d", info.Size(), committedSize)
						}
					} else if !errors.Is(probeErr, tc.want) {
						t.Fatalf("daemon probe = %v, want %v", probeErr, tc.want)
					}
				}
			}
		})
	}
}

type gcFile struct {
	data []byte
	mode fs.FileMode
}

func gcRepositoryFiles(t *testing.T, root string) map[string]gcFile {
	t.Helper()
	files := make(map[string]gcFile)
	if walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if path == root && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("gcRepositoryFiles: %w", err)
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return fmt.Errorf("gcRepositoryFiles: %w", infoErr)
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return fmt.Errorf("gcRepositoryFiles: %w", relErr)
		}
		file := gcFile{mode: info.Mode()}
		if !entry.IsDir() {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("gcRepositoryFiles: unexpected file type at %s", path)
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return fmt.Errorf("gcRepositoryFiles: %w", readErr)
			}
			file.data = data
		}
		files[relative] = file
		return nil
	}); walkErr != nil {
		t.Fatal(walkErr)
	}
	return files
}
