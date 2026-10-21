package api

import (
	"fmt"
	"testing"

	"github.com/gosuda/rukie/internal/store"
)

func TestRepoCommitETagAccepted(t *testing.T) {
	row := store.RepoRow{ID: 7, Name: "hello", UpdatedAt: 12345}
	owner := "ada"
	commit := "0123456789abcdef0123456789abcdef01234567"
	withCommit := fmt.Sprintf("\"repo-%s-%s-%d-%s\"", owner, row.Name, row.UpdatedAt, commit)
	if err := matchRepoETag(withCommit, owner, row); err != nil {
		t.Fatalf("commit-bearing ETag must match: %v", err)
	}
	named := fmt.Sprintf("\"repo-%s-%s-%d\"", owner, row.Name, row.UpdatedAt)
	if err := matchRepoETag(named, owner, row); err != nil {
		t.Fatalf("named ETag must still match: %v", err)
	}
	stale := fmt.Sprintf("\"repo-%s-%s-%d-%s\"", owner, row.Name, 99999, commit)
	if err := matchRepoETag(stale, owner, row); err == nil {
		t.Fatalf("stale updated must not match")
	}
}
