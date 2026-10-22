package api_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/api"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
)

type socialEnv struct {
	*repoEnv
}

func buildSocialEnv(t *testing.T) *socialEnv {
	t.Helper()
	path := t.TempDir() + "/social.sqlite"
	st, err := store.Open(t.Context(), store.Options{Path: path})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	svc := auth.New(st)
	deps := api.Deps{
		Service: svc, Limiter: access.NewLimiter(), Reveals: auth.NewRevealCache(),
		Store: st, RepoRoot: t.TempDir(),
	}
	table := op.NewTable()
	for _, step := range []func() error{
		func() error { return api.RegisterAll(table, &api.DocStore{}) },
		func() error { return api.RegisterAuth(table, deps) },
		func() error { return api.RegisterUser(table, deps) },
		func() error { return api.RegisterAdmin(table, deps) },
		func() error { return api.RegisterRepos(table, deps) },
		func() error { return api.RegisterOrgs(table, deps) },
		func() error { return api.RegisterSocial(table, deps) },
	} {
		if serr := step(); serr != nil {
			t.Fatalf("register: %v", serr)
		}
	}
	if verr := table.Validate(); verr != nil {
		t.Fatalf("validate: %v", verr)
	}
	views := op.NewViews()
	api.RegisterViews(views)
	api.RegisterAuthViews(views)
	api.RegisterUserViews(views)
	api.RegisterAdminViews(views)
	api.RegisterReposViews(views)
	api.RegisterOrgsViews(views)
	api.RegisterSocialViews(views)
	var key [32]byte
	for i := range key {
		key[i] = byte(i + 7)
	}
	mux := http.NewServeMux()
	server, merr := op.Mount(mux, table, op.Config{
		Authenticator: auth.NewAuthenticator(svc),
		Idempotency:   op.NewMemoryIdempotency(),
		Audit:         op.FuncAuditor(func(context.Context, op.AuditRow) error { return nil }),
		Limiter:       deps.Limiter,
		CSRFKey:       key[:],
		Views:         views,
		Title:         "test",
		Version:       "0",
		IsSiteSegment: api.SiteSegment,
	})
	if merr != nil {
		t.Fatalf("mount: %v", merr)
	}
	return &socialEnv{repoEnv: &repoEnv{server: server, svc: svc, st: st, deps: deps}}
}

func socialSeed(t *testing.T, e *socialEnv, ownerID int64, name, visibility string) int64 {
	t.Helper()
	var id int64
	serr := e.st.Write(t.Context(), func(tx *store.WriteTx) error {
		var ierr error
		id, ierr = tx.InsertRepo(time.Now(), store.RepoInsert{
			OwnerUserID:   sql.NullInt64{Int64: ownerID, Valid: true},
			Name:          name,
			Visibility:    visibility,
			Topics:        "[]",
			DefaultBranch: "main",
			RukPath:       "seed/" + name + ".git",
		})
		return ierr
	})
	if serr != nil {
		t.Fatalf("seed repo: %v", serr)
	}
	return id
}

func socialJSON(t *testing.T, rec *httptest.ResponseRecorder, out any) {
	t.Helper()
	if uerr := jsonv2.Unmarshal(rec.Body.Bytes(), out, jsonv1.DefaultOptionsV1()); uerr != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), uerr)
	}
}

func TestSocialStarAddRemoveIdempotent(t *testing.T) {
	e := buildSocialEnv(t)
	rbootstrap(t, e.repoEnv, "ada", "long-test-password-01")
	ada, gerr := e.svc.GetUser(t.Context(), "ada")
	if gerr != nil {
		t.Fatal(gerr)
	}
	ruser(t, e.repoEnv, ada.ID, "bob")
	bobCookie := rlogin(t, e.repoEnv, "bob", "long-test-password-01")
	repoID := socialSeed(t, e, ada.ID, "hello", "public")

	for attempt := 1; attempt <= 2; attempt++ {
		rec := rdo(t, e.repoEnv, http.MethodPut, "/api/user/stars/ada/hello", bobCookie, "")
		if rec.Code != http.StatusNoContent {
			t.Fatalf("star.add attempt %d = %d: %s", attempt, rec.Code, rec.Body.String())
		}
		rec = rdo(t, e.repoEnv, http.MethodGet, "/api/stars?repo=ada/hello", bobCookie, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("star.list = %d: %s", rec.Code, rec.Body.String())
		}
		var listed struct {
			Stars []api.StarEntry `json:"stars"`
		}
		socialJSON(t, rec, &listed)
		if len(listed.Stars) != 1 {
			t.Fatalf("star.add attempt %d left %d rows, want 1", attempt, len(listed.Stars))
		}
		if listed.Stars[0].User != "bob" || listed.Stars[0].Owner != "ada" || listed.Stars[0].Repo != "hello" {
			t.Fatalf("star entry = %+v", listed.Stars[0])
		}
		var count int64
		cerr := e.st.Read(t.Context(), func(tx *store.ReadTx) error {
			row, rerr := tx.GetRepo(repoID)
			count = row.StarCount
			return rerr
		})
		if cerr != nil {
			t.Fatalf("count read: %v", cerr)
		}
		if count != 1 {
			t.Fatalf("star.add attempt %d count = %d, want 1", attempt, count)
		}
	}

	for attempt := 1; attempt <= 2; attempt++ {
		rec := rdo(t, e.repoEnv, http.MethodDelete, "/api/user/stars/ada/hello", bobCookie, "")
		if rec.Code != http.StatusNoContent {
			t.Fatalf("star.remove attempt %d = %d: %s", attempt, rec.Code, rec.Body.String())
		}
		var count int64
		cerr := e.st.Read(t.Context(), func(tx *store.ReadTx) error {
			row, rerr := tx.GetRepo(repoID)
			count = row.StarCount
			return rerr
		})
		if cerr != nil {
			t.Fatalf("count read: %v", cerr)
		}
		if count != 0 {
			t.Fatalf("star.remove attempt %d count = %d, want 0", attempt, count)
		}
	}
}

func TestSocialStarRequiresSession(t *testing.T) {
	e := buildSocialEnv(t)
	rbootstrap(t, e.repoEnv, "ada", "long-test-password-01")
	ada, gerr := e.svc.GetUser(t.Context(), "ada")
	if gerr != nil {
		t.Fatal(gerr)
	}
	socialSeed(t, e, ada.ID, "hello", "public")
	rassertProblem(t, rdo(t, e.repoEnv, http.MethodPut, "/api/user/stars/ada/hello", "", ""), 403, "forbidden")
}

func TestSocialStarMutationsRejectTokens(t *testing.T) {
	e := buildSocialEnv(t)
	rbootstrap(t, e.repoEnv, "ada", "long-test-password-01")
	ada, err := e.svc.GetUser(t.Context(), "ada")
	if err != nil {
		t.Fatal(err)
	}
	socialSeed(t, e, ada.ID, "hello", "public")
	bob := ruser(t, e.repoEnv, ada.ID, "bob")
	token := rtoken(t, e.repoEnv, bob, "repo:read")
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		rec := rdo(t, e.repoEnv, method, "/api/user/stars/ada/hello", token, "")
		rassertProblem(t, rec, http.StatusForbidden, "forbidden")
	}
}

func TestSocialStarListRequiresOneSelector(t *testing.T) {
	e := buildSocialEnv(t)
	rbootstrap(t, e.repoEnv, "ada", "long-test-password-01")
	ada, gerr := e.svc.GetUser(t.Context(), "ada")
	if gerr != nil {
		t.Fatal(gerr)
	}
	socialSeed(t, e, ada.ID, "hello", "public")
	for _, target := range []string{
		"/api/stars",
		"/api/stars?repo=ada/hello&user=ada",
		"/api/stars?repo=hello",
	} {
		rec := rdo(t, e.repoEnv, http.MethodGet, target, "", "")
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s = %d, want a validation problem: %s", target, rec.Code, rec.Body.String())
		}
	}
}

func TestSocialStarListHidesInvisibleRepos(t *testing.T) {
	e := buildSocialEnv(t)
	rbootstrap(t, e.repoEnv, "ada", "long-test-password-01")
	ada, gerr := e.svc.GetUser(t.Context(), "ada")
	if gerr != nil {
		t.Fatal(gerr)
	}
	ruser(t, e.repoEnv, ada.ID, "bob")
	bobCookie := rlogin(t, e.repoEnv, "bob", "long-test-password-01")
	publicID := socialSeed(t, e, ada.ID, "open", "public")
	privateID := socialSeed(t, e, ada.ID, "secret", "private")
	serr := e.st.Write(t.Context(), func(tx *store.WriteTx) error {
		now := time.Now()
		if aerr := tx.AddStar(publicID, ada.ID, now); aerr != nil {
			return aerr
		}
		return tx.AddStar(privateID, ada.ID, now)
	})
	if serr != nil {
		t.Fatalf("seed stars: %v", serr)
	}
	rec := rdo(t, e.repoEnv, http.MethodGet, "/api/stars?user=ada", bobCookie, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("star.list = %d: %s", rec.Code, rec.Body.String())
	}
	var listed struct {
		Stars []api.StarEntry `json:"stars"`
	}
	socialJSON(t, rec, &listed)
	if len(listed.Stars) != 1 || listed.Stars[0].Repo != "open" {
		t.Fatalf("viewer saw %+v, want only the public star", listed.Stars)
	}
	rec = rdo(t, e.repoEnv, http.MethodGet, "/api/stars?user=ada", "", "")
	var anon struct {
		Stars []api.StarEntry `json:"stars"`
	}
	socialJSON(t, rec, &anon)
	if len(anon.Stars) != 1 || anon.Stars[0].Repo != "open" {
		t.Fatalf("anonymous saw %+v, want only the public star", anon.Stars)
	}
}

func TestSocialExploreVisibilityMatrix(t *testing.T) {
	e := buildSocialEnv(t)
	rbootstrap(t, e.repoEnv, "ada", "long-test-password-01")
	ada, gerr := e.svc.GetUser(t.Context(), "ada")
	if gerr != nil {
		t.Fatal(gerr)
	}
	ruser(t, e.repoEnv, ada.ID, "bob")
	bobCookie := rlogin(t, e.repoEnv, "bob", "long-test-password-01")
	socialSeed(t, e, ada.ID, "open", "public")
	socialSeed(t, e, ada.ID, "inner", "internal")
	socialSeed(t, e, ada.ID, "secret", "private")

	names := func(cookie string) map[string]bool {
		t.Helper()
		rec := rdo(t, e.repoEnv, http.MethodGet, "/api/explore/repos", cookie, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("explore.repos = %d: %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Repos []api.RepoJSON `json:"repos"`
		}
		socialJSON(t, rec, &out)
		got := map[string]bool{}
		for _, repo := range out.Repos {
			got[repo.Owner+"/"+repo.Name] = true
		}
		return got
	}

	anon := names("")
	if !anon["ada/open"] || anon["ada/inner"] || anon["ada/secret"] {
		t.Fatalf("anonymous explore = %v, want public only", anon)
	}
	bob := names(bobCookie)
	if !bob["ada/open"] || !bob["ada/inner"] || bob["ada/secret"] {
		t.Fatalf("member explore = %v, want public and internal only", bob)
	}
	adaCookie := rlogin(t, e.repoEnv, "ada", "long-test-password-01")
	owner := names(adaCookie)
	if !owner["ada/open"] || !owner["ada/inner"] || !owner["ada/secret"] {
		t.Fatalf("owner explore = %v, want every own repository", owner)
	}
}

func TestSocialExploreUsersAndOrgs(t *testing.T) {
	e := buildSocialEnv(t)
	rbootstrap(t, e.repoEnv, "ada", "long-test-password-01")
	ada, gerr := e.svc.GetUser(t.Context(), "ada")
	if gerr != nil {
		t.Fatal(gerr)
	}
	ruser(t, e.repoEnv, ada.ID, "bob")
	rec := rdo(t, e.repoEnv, http.MethodGet, "/api/explore/users", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("explore.users = %d: %s", rec.Code, rec.Body.String())
	}
	var users struct {
		Users []api.ExploreUser `json:"users"`
	}
	socialJSON(t, rec, &users)
	if len(users.Users) != 2 {
		t.Fatalf("explore.users = %+v, want ada and bob", users.Users)
	}
	rec = rdo(t, e.repoEnv, http.MethodGet, "/api/explore/orgs", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("explore.orgs = %d: %s", rec.Code, rec.Body.String())
	}
	rec = rdo(t, e.repoEnv, http.MethodGet, "/api/explore/repos?sort=bogus", "", "")
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bogus sort = %d, want a validation problem", rec.Code)
	}
}

func TestSocialForkVisibilityCap(t *testing.T) {
	e := buildSocialEnv(t)
	rbootstrap(t, e.repoEnv, "ada", "long-test-password-01")
	ada, gerr := e.svc.GetUser(t.Context(), "ada")
	if gerr != nil {
		t.Fatal(gerr)
	}
	bob := ruser(t, e.repoEnv, ada.ID, "bob")
	bobCookie := rlogin(t, e.repoEnv, "bob", "long-test-password-01")
	socialSeed(t, e, ada.ID, "open", "public")
	innerID := socialSeed(t, e, ada.ID, "inner", "internal")
	secretID := socialSeed(t, e, ada.ID, "secret", "private")
	serr := e.st.Write(t.Context(), func(tx *store.WriteTx) error {
		if gerr := tx.PutGrant(innerID, bob.ID, "read"); gerr != nil {
			return gerr
		}
		return tx.PutGrant(secretID, bob.ID, "read")
	})
	if serr != nil {
		t.Fatalf("grant read: %v", serr)
	}

	for _, tc := range []struct{ source, name, visibility string }{
		{"inner", "inner-public", "public"},
		{"secret", "secret-public", "public"},
		{"secret", "secret-internal", "internal"},
	} {
		body := `{"name":"` + tc.name + `","visibility":"` + tc.visibility + `"}`
		rec := rdo(t, e.repoEnv, http.MethodPost, "/api/repos/ada/"+tc.source+"/forks", bobCookie, body)
		rassertProblem(t, rec, http.StatusForbidden, "forbidden")
	}
}

func TestSocialForkTokenNamespaceOnly(t *testing.T) {
	e := buildSocialEnv(t)
	rbootstrap(t, e.repoEnv, "ada", "long-test-password-01")
	ada, gerr := e.svc.GetUser(t.Context(), "ada")
	if gerr != nil {
		t.Fatal(gerr)
	}
	bob := ruser(t, e.repoEnv, ada.ID, "bob")
	ruser(t, e.repoEnv, ada.ID, "cyd")
	socialSeed(t, e, ada.ID, "open", "public")
	token := rtoken(t, e.repoEnv, bob, "repo:read", "repo:create")

	rec := rdo(t, e.repoEnv, http.MethodPost, "/api/repos/ada/open/forks", token, `{"name":"stolen","target_owner":"cyd"}`)
	rassertProblem(t, rec, http.StatusForbidden, "forbidden")

	noCreate := rtoken(t, e.repoEnv, bob, "repo:read")
	rec = rdo(t, e.repoEnv, http.MethodPost, "/api/repos/ada/open/forks", noCreate, `{"name":"noscope"}`)
	rassertProblem(t, rec, http.StatusForbidden, "forbidden")
}

func TestSocialForkRequiresRukBackend(t *testing.T) {
	e := buildSocialEnv(t)
	rbootstrap(t, e.repoEnv, "ada", "long-test-password-01")
	ada, gerr := e.svc.GetUser(t.Context(), "ada")
	if gerr != nil {
		t.Fatal(gerr)
	}
	ruser(t, e.repoEnv, ada.ID, "bob")
	bobCookie := rlogin(t, e.repoEnv, "bob", "long-test-password-01")
	socialSeed(t, e, ada.ID, "open", "public")
	rec := rdo(t, e.repoEnv, http.MethodPost, "/api/repos/ada/open/forks", bobCookie, `{"name":"copy"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("fork without a Ruk client = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}

func TestSocialForkListVisibility(t *testing.T) {
	e := buildSocialEnv(t)
	rbootstrap(t, e.repoEnv, "ada", "long-test-password-01")
	ada, gerr := e.svc.GetUser(t.Context(), "ada")
	if gerr != nil {
		t.Fatal(gerr)
	}
	bob := ruser(t, e.repoEnv, ada.ID, "bob")
	socialSeed(t, e, ada.ID, "hello", "public")
	var forkID int64
	serr := e.st.Write(t.Context(), func(tx *store.WriteTx) error {
		now := time.Now()
		var ierr error
		forkID, ierr = tx.CreateFork(store.ForkSpec{
			SourceRepoID: 1,
			Repo: store.RepoInsert{
				OwnerUserID:   sql.NullInt64{Int64: bob.ID, Valid: true},
				Name:          "hello-fork",
				Visibility:    "public",
				DefaultBranch: "main",
				RukPath:       "bob/hello-fork.git",
			},
			Job: store.JobSpec{
				ID: "repo-fork:test", Kind: "repo_fork", RepoID: 1,
				Payload: `{"path":"bob/hello-fork.git"}`, At: now,
			},
		}, now)
		return ierr
	})
	if serr != nil {
		t.Fatalf("create fork row: %v", serr)
	}
	rec := rdo(t, e.repoEnv, http.MethodGet, "/api/repos/ada/hello/forks", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("fork list = %d: %s", rec.Code, rec.Body.String())
	}
	var listed api.ForkListOut
	socialJSON(t, rec, &listed)
	if len(listed.Forks) != 1 || listed.Forks[0].Name != "hello-fork" || listed.Forks[0].Owner != "bob" {
		t.Fatalf("fork list = %+v", listed.Forks)
	}
	if listed.Forks[0].State != "forking" {
		t.Fatalf("fresh fork state = %q, want forking", listed.Forks[0].State)
	}
	rec = rdo(t, e.repoEnv, http.MethodGet, "/ada/hello", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("repo.get = %d: %s", rec.Code, rec.Body.String())
	}
	var detail api.RepoGetOut
	socialJSON(t, rec, &detail)
	if detail.Forks != 1 {
		t.Fatalf("forks count = %d, want 1", detail.Forks)
	}
	_ = forkID
}

func TestSocialForkPageRenders(t *testing.T) {
	e := buildSocialEnv(t)
	rbootstrap(t, e.repoEnv, "ada", "long-test-password-01")
	ada, gerr := e.svc.GetUser(t.Context(), "ada")
	if gerr != nil {
		t.Fatal(gerr)
	}
	socialSeed(t, e, ada.ID, "hello", "public")
	rec := rdoFull(t, e.repoEnv, http.MethodGet, "/ada/hello/fork", "", "text/html", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("fork page = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "ada") || strings.Contains(body, "<script") {
		t.Fatalf("fork page body:\n%s", body)
	}
}
