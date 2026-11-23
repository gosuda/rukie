//go:build e2e

package e2e

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/repowatch"
	"github.com/gosuda/rukie/internal/store"
)

func TestRepositoryPurgeUsesOpenedPath(t *testing.T) {
	for _, tc := range []struct {
		name  string
		admin bool
	}{
		{name: "allowed", admin: true},
		{name: "denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s *stack
			if tc.admin {
				s = newStack(t, nil)
			} else {
				s = newStackNoAdmin(t, nil)
			}
			admin := s.addAdmin(t, "ada", "admin:write")
			h := mountAPI(t, s)
			s.addRepo(t, "ada", "keep", "private")
			for _, path := range []string{"scheduled", "api"} {
				t.Run(path, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
					defer cancel()
					repoID := s.addRepo(t, "ada", path, "private")
					relative := "ada/" + path + ".git"
					storePath := filepath.Join(s.roots, filepath.FromSlash(relative), ".ruk")
					now := time.Now()
					if path == "scheduled" {
						if err := s.st.Write(ctx, func(tx *store.WriteTx) error {
							if deleteErr := tx.SoftDeleteRepo(repoID, now, now); deleteErr != nil {
								return fmt.Errorf("TestRepositoryPurgeUsesOpenedPath: %w", deleteErr)
							}
							return nil
						}); err != nil {
							t.Fatal(err)
						}
					}
					var before store.RepoRow
					if err := s.st.Read(ctx, func(tx *store.ReadTx) error {
						row, readErr := tx.GetRepo(repoID)
						if readErr != nil {
							return fmt.Errorf("TestRepositoryPurgeUsesOpenedPath: %w", readErr)
						}
						before = row
						return nil
					}); err != nil {
						t.Fatal(err)
					}
					if before.RukPath != relative || filepath.IsAbs(before.RukPath) {
						t.Fatalf("saved daemon path = %q, want relative %q", before.RukPath, relative)
					}
					opened, openErr := s.client.OpenRepo(ctx, storePath)
					if openErr != nil || opened.Path != storePath {
						t.Fatalf("open repository = %+v, %v; want %s", opened, openErr, storePath)
					}
					if path == "scheduled" {
						watcher := repowatch.New(repowatch.Config{
							Store: s.st, Client: s.client, RepoRoot: s.roots,
						})
						purgeErr := watcher.HandlePurge(ctx, store.Job{
							Kind: string(queue.KindRepoPurge), RepoID: repoID,
							Payload: fmt.Sprintf(`{"repo_id":%d,"path":%q,"not_before":0}`, repoID, relative),
						})
						if tc.admin {
							if purgeErr != nil {
								t.Fatalf("scheduled purge: %v", purgeErr)
							}
						} else if denied, ok := errors.AsType[*op.Error](purgeErr); !ok || denied.Code != op.CodeForbidden {
							t.Fatalf("scheduled purge refusal = %v, want forbidden", purgeErr)
						}
					} else {
						req, requestErr := http.NewRequestWithContext(ctx, http.MethodPost,
							fmt.Sprintf("%s/admin/repos/%d/purge", h.server.URL, repoID),
							strings.NewReader(fmt.Sprintf(`{"confirm":%q}`, "ada/"+path)))
						if requestErr != nil {
							t.Fatal(requestErr)
						}
						req.Header.Set("Authorization", "Bearer "+admin.token)
						req.Header.Set("Content-Type", "application/json")
						req.Header.Set("Accept", "application/json")
						req.Header.Set("If-Match", fmt.Sprintf("repo-ada-%s-%d", path, before.UpdatedAt))
						response, responseErr := http.DefaultClient.Do(req)
						if responseErr != nil {
							t.Fatal(responseErr)
						}
						body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
						closeErr := response.Body.Close()
						if readErr != nil || closeErr != nil || len(body) > 1<<20 {
							t.Fatalf("read purge response: %v, close: %v, bytes: %d", readErr, closeErr, len(body))
						}
						wantStatus := http.StatusForbidden
						if tc.admin {
							wantStatus = http.StatusAccepted
						}
						if response.StatusCode != wantStatus {
							t.Fatalf("API purge = %d, want %d: %s", response.StatusCode, wantStatus, body)
						}
					}
					if err := s.st.Read(ctx, func(tx *store.ReadTx) error {
						row, readErr := tx.GetRepo(repoID)
						if tc.admin {
							if !errors.Is(readErr, store.ErrNotFound) {
								return fmt.Errorf("TestRepositoryPurgeUsesOpenedPath: purged metadata remains: %w", readErr)
							}
							return nil
						}
						if readErr != nil {
							return fmt.Errorf("TestRepositoryPurgeUsesOpenedPath: %w", readErr)
						}
						if row.RukPath != before.RukPath || row.Name != before.Name || !row.DeletedAt.Valid {
							return fmt.Errorf("TestRepositoryPurgeUsesOpenedPath: refused deletion changed repository identity or lost its tombstone")
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
					if _, statErr := os.Stat(storePath); (tc.admin && !errors.Is(statErr, os.ErrNotExist)) || (!tc.admin && statErr != nil) {
						t.Fatalf("repository after purge: %v, daemon admin=%t", statErr, tc.admin)
					}
					if _, statErr := os.Stat(filepath.Join(s.roots, "ada", "keep.git", ".ruk")); statErr != nil {
						t.Fatalf("purge touched another repository: %v", statErr)
					}
				})
			}
		})
	}
}
