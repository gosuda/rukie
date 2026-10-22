package store

import (
	"database/sql"
	"testing"
	"time"
)

func TestSocialStarsAreIdempotent(t *testing.T) {
	fx := openMigrated(t)
	repo := insertRepo(t, fx.store, fx.owner, "social", "public")
	user, err := fx.store.InsertAuthUser(t.Context(), AuthUserCreate{Name: "other", Lower: "other", Email: "other@example.test", Hash: "hash", Algo: "argon2id-v19", Now: 1})
	if err != nil {
		t.Fatal(err)
	}
	at := time.UnixMilli(1000)
	for range 2 {
		if err := fx.store.Write(t.Context(), func(tx *WriteTx) error { return tx.AddStar(repo, user.ID, at) }); err != nil {
			t.Fatal(err)
		}
	}
	if got := readInt(t, fx.store, `SELECT star_count FROM repos WHERE id=?`, repo); got != 1 {
		t.Fatalf("star count=%d, want 1", got)
	}
	for range 2 {
		if err := fx.store.Write(t.Context(), func(tx *WriteTx) error { return tx.RemoveStar(repo, user.ID) }); err != nil {
			t.Fatal(err)
		}
	}
	if got := readInt(t, fx.store, `SELECT COUNT(*) FROM stars WHERE repo_id=?`, repo); got != 0 {
		t.Fatalf("stars=%d, want 0", got)
	}
	if got := readInt(t, fx.store, `SELECT star_count FROM repos WHERE id=?`, repo); got != 0 {
		t.Fatalf("star count=%d, want 0", got)
	}
}

func TestSocialExploreHidesPrivateReposBeforePagination(t *testing.T) {
	fx := openMigrated(t)
	insertRepo(t, fx.store, fx.owner, "hidden-private", "private")
	publicID := insertRepo(t, fx.store, fx.owner, "shown-public", "public")
	var rows []RepoRow
	err := fx.store.Read(t.Context(), func(tx *ReadTx) error {
		var e error
		rows, _, e = tx.ExploreRepos(ExploreRepoFilter{Limit: 1})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != publicID {
		t.Fatalf("anonymous explore rows=%+v", rows)
	}
}

func TestSocialForkVisibilityCapAndQueueAtomicity(t *testing.T) {
	fx := openMigrated(t)
	source := insertRepo(t, fx.store, fx.owner, "source", "internal")
	now := time.UnixMilli(2000)
	err := fx.store.Write(t.Context(), func(tx *WriteTx) error {
		_, e := tx.CreateFork(ForkSpec{SourceRepoID: source, Repo: RepoInsert{OwnerUserID: sql.NullInt64{Int64: fx.owner, Valid: true}, Name: "public-fork", Visibility: "public", DefaultBranch: "main", RukPath: "public-fork"}, Job: JobSpec{ID: "social-fork-job", Payload: `{"source":1}`, At: now}}, now)
		return e
	})
	if err == nil {
		t.Fatal("internal-to-public fork accepted")
	}
	if got := readInt(t, fx.store, `SELECT COUNT(*) FROM jobs WHERE id='social-fork-job'`); got != 0 {
		t.Fatalf("job count=%d after rejected fork", got)
	}
}

func TestSocialListForksFiltersInvisibleRows(t *testing.T) {
	fx := openMigrated(t)
	source := insertRepo(t, fx.store, fx.owner, "public-parent", "public")
	now := time.UnixMilli(5000)
	if err := fx.store.Write(t.Context(), func(tx *WriteTx) error {
		for _, entry := range []struct{ name, visibility string }{{"visible-child", "public"}, {"hidden-child", "private"}} {
			if _, e := tx.CreateFork(ForkSpec{SourceRepoID: source, Repo: RepoInsert{OwnerUserID: sql.NullInt64{Int64: fx.owner, Valid: true}, Name: entry.name, Visibility: entry.visibility, DefaultBranch: "main", RukPath: entry.name}, Job: JobSpec{ID: "job-" + entry.name, Payload: `{"source":1}`, At: now}}, now); e != nil {
				return e
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := fx.store.Read(t.Context(), func(tx *ReadTx) error {
		rows, _, e := tx.ListForks(source, 0, "", 10)
		if e != nil {
			return e
		}
		if len(rows) != 1 || rows[0].Name != "visible-child" {
			t.Fatalf("anonymous forks=%+v", rows)
		}
		rows, _, e = tx.ListForks(source, fx.owner, "", 10)
		if e != nil {
			return e
		}
		if len(rows) != 2 {
			t.Fatalf("owner forks=%+v", rows)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSocialExploreUsersAndOrgsKeysets(t *testing.T) {
	fx := openMigrated(t)
	if _, err := fx.store.InsertAuthUser(t.Context(), AuthUserCreate{Name: "alpha", Lower: "alpha", Email: "alpha@example.test", Hash: "hash", Algo: "argon2id-v19", Now: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.store.InsertAuthUser(t.Context(), AuthUserCreate{Name: "bravo", Lower: "bravo", Email: "bravo@example.test", Hash: "hash", Algo: "argon2id-v19", Now: 3}); err != nil {
		t.Fatal(err)
	}
	if err := fx.store.Write(t.Context(), func(tx *WriteTx) error {
		_, e := tx.InsertOrg(time.UnixMilli(4), OrgInsert{Name: "Public", Visibility: "public"})
		if e != nil {
			return e
		}
		_, e = tx.InsertOrg(time.UnixMilli(5), OrgInsert{Name: "Private", Visibility: "private"})
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if err := fx.store.Read(t.Context(), func(tx *ReadTx) error {
		users, c, e := tx.ExploreUsers("", 1, "name")
		if e != nil {
			return e
		}
		if len(users) != 1 || users[0].Name != "alpha" || c == "" {
			t.Fatalf("first users=%+v cursor=%q", users, c)
		}
		users, _, e = tx.ExploreUsers(c, 1, "name")
		if e != nil {
			return e
		}
		if len(users) != 1 || users[0].Name != "bravo" {
			t.Fatalf("second users=%+v", users)
		}
		orgs, _, e := tx.ExploreOrgs("", 10, "name")
		if e != nil {
			return e
		}
		if len(orgs) != 1 || orgs[0].Name != "Public" {
			t.Fatalf("public orgs=%+v", orgs)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSocialForkCreateAndFencedCompletion(t *testing.T) {
	fx := openMigrated(t)
	source := insertRepo(t, fx.store, fx.owner, "fork-source", "public")
	now := time.UnixMilli(6000)
	var forkID int64
	if err := fx.store.Write(t.Context(), func(tx *WriteTx) error {
		var e error
		forkID, e = tx.CreateFork(ForkSpec{SourceRepoID: source, Repo: RepoInsert{OwnerUserID: sql.NullInt64{Int64: fx.owner, Valid: true}, Name: "fork-child", Visibility: "public", DefaultBranch: "main", RukPath: "fork-child"}, Job: JobSpec{ID: "fork-job-complete", Payload: `{"source":1}`, At: now}}, now)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if got := readInt(t, fx.store, `SELECT COUNT(*) FROM jobs WHERE id='fork-job-complete' AND kind='repo_fork' AND repo_id=?`, forkID); got != 1 {
		t.Fatalf("fork job=%d", got)
	}
	if err := fx.store.Write(t.Context(), func(tx *WriteTx) error {
		return tx.CompleteFork(forkID, "worker", "fork-job-complete", "fence", now.Add(time.Second))
	}); err == nil {
		t.Fatal("unleased completion accepted")
	}
	if err := fx.store.Write(t.Context(), func(tx *WriteTx) error {
		jobs, e := tx.ClaimKindJobs("worker", "fence", []string{repoForkJobKind}, now.Add(time.Second), time.Minute, 1)
		if e != nil {
			return e
		}
		if len(jobs) != 1 {
			return ErrNotFound
		}
		return tx.CompleteFork(forkID, "worker", jobs[0].ID, jobs[0].LeaseToken, now.Add(2*time.Second))
	}); err != nil {
		t.Fatal(err)
	}
	if err := fx.store.Read(t.Context(), func(tx *ReadTx) error {
		row, e := tx.GetRepo(forkID)
		if e != nil {
			return e
		}
		if row.State != "active" || !row.ForkOfRepoID.Valid || row.ForkOfRepoID.Int64 != source {
			t.Fatalf("fork row=%+v", row)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSocialForkCountersFollowDeleteRestore(t *testing.T) {
	fx := openMigrated(t)
	source := insertRepo(t, fx.store, fx.owner, "fork-counter-source", "public")
	now := time.UnixMilli(6000)
	var forkID int64
	if err := fx.store.Write(t.Context(), func(tx *WriteTx) error {
		var e error
		forkID, e = tx.CreateFork(ForkSpec{SourceRepoID: source, Repo: RepoInsert{OwnerUserID: sql.NullInt64{Int64: fx.owner, Valid: true}, Name: "fork-counter-child", Visibility: "public", DefaultBranch: "main", RukPath: "fork-counter-child"}, Job: JobSpec{ID: "fork-counter-job", Payload: `{"source":1}`, At: now}}, now)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if got := readInt(t, fx.store, `SELECT forks_count FROM repos WHERE id=?`, source); got != 1 {
		t.Fatalf("forks count=%d, want 1", got)
	}
	if err := fx.store.Write(t.Context(), func(tx *WriteTx) error { return tx.SoftDeleteRepo(forkID, now.Add(time.Minute), now.Add(time.Hour)) }); err != nil {
		t.Fatal(err)
	}
	if got := readInt(t, fx.store, `SELECT forks_count FROM repos WHERE id=?`, source); got != 0 {
		t.Fatalf("forks count after delete=%d, want 0", got)
	}
	if err := fx.store.Write(t.Context(), func(tx *WriteTx) error { return tx.RestoreRepo(forkID, now.Add(2*time.Minute)) }); err != nil {
		t.Fatal(err)
	}
	if got := readInt(t, fx.store, `SELECT forks_count FROM repos WHERE id=?`, source); got != 1 {
		t.Fatalf("forks count after restore=%d, want 1", got)
	}
}

func TestSocialRepoLifecycleTracksState(t *testing.T) {
	fx := openMigrated(t)
	id := insertRepo(t, fx.store, fx.owner, "stateful", "private")
	now := time.UnixMilli(4000)
	if err := fx.store.Write(t.Context(), func(tx *WriteTx) error {
		return tx.SoftDeleteRepo(id, now, now.Add(time.Hour))
	}); err != nil {
		t.Fatal(err)
	}
	if got := readInt(t, fx.store, `SELECT COUNT(*) FROM repos WHERE id=? AND state='deleted'`, id); got != 1 {
		t.Fatal("soft delete left state active")
	}
	if err := fx.store.Write(t.Context(), func(tx *WriteTx) error {
		return tx.RestoreRepo(id, now.Add(time.Minute))
	}); err != nil {
		t.Fatal(err)
	}
	if got := readInt(t, fx.store, `SELECT COUNT(*) FROM repos WHERE id=? AND state='active'`, id); got != 1 {
		t.Fatal("restore left state deleted")
	}
}

func TestSocialStarsByUserFiltersPrivateRows(t *testing.T) {
	fx := openMigrated(t)
	privateRepo := insertRepo(t, fx.store, fx.owner, "private-star", "private")
	publicRepo := insertRepo(t, fx.store, fx.owner, "public-star", "public")
	if err := fx.store.Write(t.Context(), func(tx *WriteTx) error {
		if err := tx.AddStar(privateRepo, fx.owner, time.UnixMilli(3000)); err != nil {
			return err
		}
		return tx.AddStar(publicRepo, fx.owner, time.UnixMilli(3000))
	}); err != nil {
		t.Fatal(err)
	}
	var rows []StarRow
	if err := fx.store.Read(t.Context(), func(tx *ReadTx) error {
		var e error
		rows, _, e = tx.ListStarsByUser(t.Context(), fx.owner, 0, 1, "")
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].RepoID != publicRepo {
		t.Fatalf("anonymous rows=%+v", rows)
	}
}

func TestSocialStarTieBreakersPaginateFully(t *testing.T) {
	fx := openMigrated(t)
	at := time.UnixMilli(9000)
	repos := []int64{
		insertRepo(t, fx.store, fx.owner, "tie-a", "public"),
		insertRepo(t, fx.store, fx.owner, "tie-b", "public"),
		insertRepo(t, fx.store, fx.owner, "tie-c", "public"),
	}
	user, err := fx.store.InsertAuthUser(t.Context(), AuthUserCreate{Name: "tieuser", Lower: "tieuser", Email: "tieuser@example.test", Hash: "hash", Algo: "argon2id-v19", Now: 9})
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.store.Write(t.Context(), func(tx *WriteTx) error {
		for _, id := range repos {
			if err := tx.AddStar(id, user.ID, at); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	seenRepos := map[int64]bool{}
	cursor := ""
	for {
		var page []StarRow
		var next string
		if err := fx.store.Read(t.Context(), func(tx *ReadTx) error {
			var e error
			page, next, e = tx.ListStarsByUser(t.Context(), user.ID, user.ID, 2, cursor)
			return e
		}); err != nil {
			t.Fatal(err)
		}
		for _, row := range page {
			if seenRepos[row.RepoID] {
				t.Fatalf("duplicate repo %d across star pages", row.RepoID)
			}
			seenRepos[row.RepoID] = true
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seenRepos) != 3 {
		t.Fatalf("starred repos seen=%d, want 3", len(seenRepos))
	}
	stargazers := insertRepo(t, fx.store, fx.owner, "tie-fans", "public")
	fans := make([]int64, 0, 3)
	for _, name := range []string{"fan-a", "fan-b", "fan-c"} {
		fan, err := fx.store.InsertAuthUser(t.Context(), AuthUserCreate{Name: name, Lower: name, Email: name + "@example.test", Hash: "hash", Algo: "argon2id-v19", Now: 9})
		if err != nil {
			t.Fatal(err)
		}
		fans = append(fans, fan.ID)
	}
	if err := fx.store.Write(t.Context(), func(tx *WriteTx) error {
		for _, id := range fans {
			if err := tx.AddStar(stargazers, id, at); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	seenUsers := map[int64]bool{}
	cursor = ""
	for {
		var page []StarRow
		var next string
		if err := fx.store.Read(t.Context(), func(tx *ReadTx) error {
			var e error
			page, next, e = tx.ListStars(StarFilter{RepoID: stargazers, ViewerUserID: fx.owner, Limit: 2, Cursor: cursor})
			return e
		}); err != nil {
			t.Fatal(err)
		}
		for _, row := range page {
			if seenUsers[row.UserID] {
				t.Fatalf("duplicate user %d across stargazer pages", row.UserID)
			}
			seenUsers[row.UserID] = true
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seenUsers) != 3 {
		t.Fatalf("stargazers seen=%d, want 3", len(seenUsers))
	}
}
