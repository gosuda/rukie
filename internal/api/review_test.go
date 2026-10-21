package api_test

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/store"
)

func rseedChange(t *testing.T, e *repoEnv, repoID, authorID int64, key, head string) {
	t.Helper()
	if err := e.st.Write(t.Context(), func(tx *store.WriteTx) error {
		_, ierr := tx.InsertChange(time.Now(), store.ChangeRow{
			RepoID: repoID, ChangeKey: key, TargetBranch: "main",
			AuthorUserID: authorID, HeadOid: head,
		})
		return ierr
	}); err != nil {
		t.Fatalf("seed change: %v", err)
	}
}

func rgrant(t *testing.T, e *repoEnv, repoID, userID int64, role string) {
	t.Helper()
	if err := e.st.Write(t.Context(), func(tx *store.WriteTx) error {
		return tx.PutGrant(repoID, userID, role)
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}
}

func TestDefaultPolicyIsData(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	rseedRepo(t, e, admin.ID, "hello", "private")
	token := rtoken(t, e, admin, "repo:read", "repo:write")
	got := rdo(t, e, "GET", "/ada/hello/settings/protected", token, "")
	if got.Code != http.StatusOK {
		t.Fatalf("policy list = %d: %s", got.Code, got.Body.String())
	}
	body := got.Body.String()
	if !strings.Contains(body, "LandOnly") {
		t.Fatalf("default policy lacks LandOnly: %s", body)
	}
	if !strings.Contains(body, "review/approve") {
		t.Fatalf("default policy lacks the approval requirement: %s", body)
	}
}

func TestReviewChangeListEmpty(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	rseedRepo(t, e, admin.ID, "hello", "private")
	token := rtoken(t, e, admin, "repo:read", "repo:write")
	got := rdo(t, e, "GET", "/ada/hello/changes", token, "")
	if got.Code != http.StatusOK {
		t.Fatalf("change list = %d: %s", got.Code, got.Body.String())
	}
	if !strings.Contains(got.Body.String(), `"changes":[]`) {
		t.Fatalf("change list is not empty: %s", got.Body.String())
	}
}

func TestReviewCommentCreateAndList(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	repoID := rseedRepo(t, e, admin.ID, "hello", "private")
	head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rseedChange(t, e, repoID, admin.ID, "main~#1", head)
	token := rtoken(t, e, admin, "repo:read", "repo:write")
	created := rdo(t, e, "POST", "/ada/hello/changes/main~%231/comments", token,
		`{"path":"a.txt","line":3,"body":"nit"}`)
	if created.Code != http.StatusCreated && created.Code != http.StatusOK {
		t.Fatalf("comment create = %d: %s", created.Code, created.Body.String())
	}
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/changes/main~%231/comments", token, `{"body":""}`), 422, "validation")
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/changes/main~%231/comments", token, `{"line":3,"body":"x"}`), 422, "validation")
	listed := rdo(t, e, "GET", "/ada/hello/changes/main~%231/comments", token, "")
	if listed.Code != http.StatusOK {
		t.Fatalf("comment list = %d: %s", listed.Code, listed.Body.String())
	}
	if !strings.Contains(listed.Body.String(), "nit") {
		t.Fatalf("comment list misses the comment: %s", listed.Body.String())
	}
}

func TestNoSelfApproval(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	mallory := ruser(t, e, admin.ID, "mallory")
	repoID := rseedRepo(t, e, admin.ID, "hello", "private")
	rgrant(t, e, repoID, mallory.ID, "write")
	head := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	rseedChange(t, e, repoID, admin.ID, "main~#1", head)
	adaToken := rtoken(t, e, admin, "repo:read", "repo:write")
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/changes/main~%231/reviews", adaToken,
		`{"verdict":"approve"}`), 403, "forbidden")
	malloryToken := rtoken(t, e, mallory, "repo:read", "repo:write")
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/changes/main~%231/reviews", malloryToken,
		`{"verdict":"approve"}`), 503, "unavailable")
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/changes/main~%231/reviews", malloryToken,
		`{"verdict":"smash"}`), 422, "validation")
}

func TestChangeStateAbandonClosesComments(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	repoID := rseedRepo(t, e, admin.ID, "hello", "private")
	head := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	rseedChange(t, e, repoID, admin.ID, "main~#1", head)
	token := rtoken(t, e, admin, "repo:read", "repo:write")
	moved := rdo(t, e, "POST", "/ada/hello/changes/main~%231/state", token, `{"state":"abandoned"}`)
	if moved.Code != http.StatusOK && moved.Code != http.StatusCreated {
		t.Fatalf("state set = %d: %s", moved.Code, moved.Body.String())
	}
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/changes/main~%231/state", token, `{"state":"landed"}`), 422, "validation")
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/changes/main~%231/comments", token, `{"body":"late"}`), 409, "conflict")
}

func TestLandRunRequiresDaemon(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	repoID := rseedRepo(t, e, admin.ID, "hello", "private")
	head := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	rseedChange(t, e, repoID, admin.ID, "main~#1", head)
	var planPublic string
	if err := e.st.Write(t.Context(), func(tx *store.WriteTx) error {
		row, gerr := tx.GetChangeByKey(repoID, "main~#1")
		if gerr != nil {
			return gerr
		}
		plan, perr := tx.InsertLandIntent(time.Now(), store.LandIntentRow{
			RepoID: repoID, TargetRef: "refs/heads/main", ChangeID: sql.NullInt64{Int64: row.ID, Valid: true},
			CandidateOid: head, BaseOid: head, PolicyGeneration: 1, Gap: "[]", CreatedBy: admin.ID,
		})
		if perr != nil {
			return perr
		}
		planPublic = plan.PublicID
		return nil
	}); err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	token := rtoken(t, e, admin, "repo:read", "repo:write")
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/land/plans/"+planPublic+"/runs", token, ""), 503, "unavailable")
}

func TestViewPredicateGate(t *testing.T) {
	e := buildRepoEnv(t)
	rbootstrap(t, e, "ada", "long-test-password-01")
	admin, _ := e.svc.GetUser(t.Context(), "ada")
	rseedRepo(t, e, admin.ID, "hello", "private")
	token := rtoken(t, e, admin, "repo:read", "repo:write", "intents:read", "intents:write")
	created := rdo(t, e, "POST", "/ada/hello/views", token, `{"name":"gaps","predicates":"{\"gap:*\":true}"}`)
	if created.Code != http.StatusCreated && created.Code != http.StatusOK {
		t.Fatalf("view create = %d: %s", created.Code, created.Body.String())
	}
	rassertProblem(t, rdo(t, e, "POST", "/ada/hello/views", token, `{"name":"bad","predicates":"{\"nope\":1}"}`), 422, "validation")
}
