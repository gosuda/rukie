package api

import (
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/web/zerojs"
)

const socialCanary = "CANARY-7q2z-social-escape-probe"

func socialCtx(path string, query url.Values) op.ViewCtx {
	return op.ViewCtx{RequestID: "req-social-test", Path: path, CSRF: "csrf-social-test", Input: query}
}

func assertSocialZeroJS(t *testing.T, body string) {
	t.Helper()
	if err := zerojs.Assert([]byte(body)); err != nil {
		t.Fatalf("zero-js: %v", err)
	}
	if strings.Contains(body, "<"+socialCanary+">") {
		t.Fatalf("page carries the unescaped canary")
	}
}

func renderSocial(t *testing.T, c op.ViewCtx, f func(io.Writer, op.ViewCtx) error) string {
	t.Helper()
	var b strings.Builder
	if err := f(&b, c); err != nil {
		t.Fatalf("render: %v", err)
	}
	return b.String()
}

func TestSocialStargazersRender(t *testing.T) {
	c := socialCtx("/ada/repo/stargazers", url.Values{})
	out := StarListOut{
		Stars: []StarEntry{
			{User: "bob", Owner: "ada", Repo: "repo", StarredAt: 3},
			{User: "<" + socialCanary + ">", Owner: "ada", Repo: "repo", StarredAt: 2},
		},
	}
	body := renderSocial(t, c, func(w io.Writer, c op.ViewCtx) error {
		return writeStargazers(w, c, out)
	})
	assertSocialZeroJS(t, body)
	if !strings.Contains(body, `<a href="/bob">bob</a>`) {
		t.Fatalf("stargazers miss the starring user link:\n%s", body)
	}
	if !strings.Contains(body, `&lt;`+socialCanary+`&gt;`) {
		t.Fatalf("stargazers miss the escaped canary user:\n%s", body)
	}
	if !strings.Contains(body, `<a href="/ada/repo">Back to repository</a>`) {
		t.Fatalf("stargazers miss the repo back link:\n%s", body)
	}
}

func TestSocialStargazersZeroState(t *testing.T) {
	body := renderSocial(t, socialCtx("/ada/repo/stargazers", url.Values{}), func(w io.Writer, c op.ViewCtx) error {
		return writeStargazers(w, c, StarListOut{})
	})
	assertSocialZeroJS(t, body)
	if !strings.Contains(body, "No one has starred this repository yet.") {
		t.Fatalf("empty stargazers page has no zero-state copy:\n%s", body)
	}
	if strings.Contains(body, "Next page") {
		t.Fatalf("empty stargazers page offers a next page:\n%s", body)
	}
}

// TestSocialCursorLinks pins both continuation forms. A raw store cursor may
// never reach an href: the mount HMAC-decodes every cursor query, so a link
// carrying one fails the next request. Only a handler-signed href renders as
// a rel=next link; otherwise the page defers to the Link response header.
func TestSocialCursorLinks(t *testing.T) {
	c := socialCtx("/ada/repo/forks", url.Values{"limit": {"2"}})
	raw := renderSocial(t, c, func(w io.Writer, c op.ViewCtx) error {
		return writeForkList(w, c, ForkListOut{Next: "raw-store-cursor"})
	})
	if strings.Contains(raw, "raw-store-cursor") {
		t.Fatalf("fork list renders the unsigned store cursor:\n%s", raw)
	}
	if strings.Contains(raw, `rel="next"`) {
		t.Fatalf("fork list mints a next link without a signed href:\n%s", raw)
	}
	if !strings.Contains(raw, "Link header") {
		t.Fatalf("fork list drops the header continuation note:\n%s", raw)
	}
	signed := renderSocial(t, c, func(w io.Writer, c op.ViewCtx) error {
		return writeForkList(w, c, ForkListOut{
			Next:     "raw-store-cursor",
			NextHref: "/ada/repo/forks?cursor=c2lnbmVk&limit=2",
		})
	})
	if !strings.Contains(signed, `rel="next" href="/ada/repo/forks?cursor=c2lnbmVk&amp;limit=2"`) {
		t.Fatalf("fork list drops the signed continuation:\n%s", signed)
	}
	if strings.Contains(signed, "raw-store-cursor") {
		t.Fatalf("signed continuation leaks the raw store cursor:\n%s", signed)
	}
	if strings.Contains(signed, "Link header") {
		t.Fatalf("signed continuation still shows the header note:\n%s", signed)
	}
	explore := renderSocial(t, socialCtx("/-/explore/users", url.Values{"sort": {"created"}}), func(w io.Writer, c op.ViewCtx) error {
		return writeExploreUsers(w, c, ExploreUsersOut{NextHref: "/-/explore/users?cursor=c2lnbmVk&sort=created"})
	})
	if !strings.Contains(explore, `rel="next" href="/-/explore/users?cursor=c2lnbmVk&amp;sort=created"`) {
		t.Fatalf("explore continuation loses the signed cursor or the sort filter:\n%s", explore)
	}
}

func TestSocialUserStarsLinkRepos(t *testing.T) {
	stars := []StarEntry{
		{User: "bob", Owner: "ada", Repo: "repo", StarredAt: 3},
		{User: "bob", Owner: "<" + socialCanary + ">", Repo: "repo", StarredAt: 2},
	}
	body := writeUserStars("bob", stars, "", "")
	assertSocialZeroJS(t, body)
	if !strings.Contains(body, `<a href="/ada/repo">ada / repo</a>`) {
		t.Fatalf("stars tab links the starring user instead of the repository:\n%s", body)
	}
	if strings.Contains(body, `href="/bob/`) {
		t.Fatalf("stars tab builds repo links from the starring user:\n%s", body)
	}
	if !strings.Contains(body, `&lt;`+socialCanary+`&gt;`) {
		t.Fatalf("stars tab misses the escaped canary owner:\n%s", body)
	}
	empty := writeUserStars("bob", nil, "", "")
	if !strings.Contains(empty, "bob has not starred any repositories.") {
		t.Fatalf("empty stars tab has no zero-state copy:\n%s", empty)
	}
}

func TestSocialForkListRender(t *testing.T) {
	out := ForkListOut{
		Forks: []RepoJSON{
			{Owner: "bob", Name: "repo", State: "active", Description: "a fork"},
			{Owner: "bob", Name: socialCanary + "<fork>", State: "forking"},
		},
	}
	body := renderSocial(t, socialCtx("/ada/repo/forks", url.Values{}), func(w io.Writer, c op.ViewCtx) error {
		return writeForkList(w, c, out)
	})
	assertSocialZeroJS(t, body)
	if !strings.Contains(body, `<a href="/bob/repo">bob / repo</a>`) {
		t.Fatalf("fork list misses the fork link:\n%s", body)
	}
	if !strings.Contains(body, "(forking)") {
		t.Fatalf("fork list hides the forking state:\n%s", body)
	}
	if !strings.Contains(body, `<a href="/ada/repo/fork">Fork this repository</a>`) {
		t.Fatalf("fork list misses the fork entry point:\n%s", body)
	}
}

func TestSocialForkPageForm(t *testing.T) {
	out := ForkPageOut{Owner: "ada", Repo: "repo"}
	body := renderSocial(t, socialCtx("/ada/repo/fork", url.Values{}), func(w io.Writer, c op.ViewCtx) error {
		return writeForkPage(w, c, out)
	})
	assertSocialZeroJS(t, body)
	if !strings.Contains(body, `<form method="post" action="/ada/repo/fork">`) {
		t.Fatalf("fork page misses the form action:\n%s", body)
	}
	for _, field := range []string{`name="target_owner"`, `name="name"`, `name="default_branch_only"`, `name="_csrf"`, `name="_idem"`} {
		if !strings.Contains(body, field) {
			t.Fatalf("fork page misses %s:\n%s", field, body)
		}
	}
	if !strings.Contains(body, `value="repo"`) {
		t.Fatalf("fork page misses the default name prefill:\n%s", body)
	}
	echoed := renderSocial(t, socialCtx("/ada/repo/fork", url.Values{"name": {"myfork"}, "target_owner": {"bob"}}), func(w io.Writer, c op.ViewCtx) error {
		return writeForkPage(w, c, out)
	})
	if !strings.Contains(echoed, `value="myfork"`) || !strings.Contains(echoed, `value="bob"`) {
		t.Fatalf("fork page drops the echoed submission:\n%s", echoed)
	}
	if !strings.Contains(echoed, "Create fork") {
		t.Fatalf("fork page misses the submit control:\n%s", echoed)
	}
}

func TestSocialExplorePagesRender(t *testing.T) {
	repos := renderSocial(t, socialCtx("/-/explore/repos", url.Values{}), func(w io.Writer, c op.ViewCtx) error {
		return writeExploreRepos(w, c, ExploreReposOut{
			Repos: []RepoJSON{{Owner: "ada", Name: "repo", Description: "d"}},
		})
	})
	assertSocialZeroJS(t, repos)
	if !strings.Contains(repos, "<strong>updated</strong>") {
		t.Fatalf("explore repos miss the default sort:\n%s", repos)
	}
	if !strings.Contains(repos, `<a href="/ada/repo">ada / repo</a>`) {
		t.Fatalf("explore repos miss the repo link:\n%s", repos)
	}
	sorted := renderSocial(t, socialCtx("/-/explore/repos", url.Values{"sort": {"stars"}}), func(w io.Writer, c op.ViewCtx) error {
		return writeExploreRepos(w, c, ExploreReposOut{})
	})
	if !strings.Contains(sorted, "<strong>stars</strong>") || !strings.Contains(sorted, `<a href="?sort=updated">updated</a>`) {
		t.Fatalf("explore repos ignore the requested sort:\n%s", sorted)
	}
	if !strings.Contains(sorted, "No public repositories match this listing yet.") {
		t.Fatalf("empty explore repos has no zero-state copy:\n%s", sorted)
	}
	users := renderSocial(t, socialCtx("/-/explore/users", url.Values{}), func(w io.Writer, c op.ViewCtx) error {
		return writeExploreUsers(w, c, ExploreUsersOut{
			Users: []ExploreUser{{Name: "ada", FullName: "Ada"}},
		})
	})
	assertSocialZeroJS(t, users)
	if !strings.Contains(users, `<a href="/ada">ada</a> — Ada`) {
		t.Fatalf("explore users miss the user link:\n%s", users)
	}
	orgs := renderSocial(t, socialCtx("/-/explore/orgs", url.Values{}), func(w io.Writer, c op.ViewCtx) error {
		return writeExploreOrgs(w, c, ExploreOrgsOut{
			Orgs: []OrgJSON{{Name: "acme", DisplayName: "Acme"}},
		})
	})
	assertSocialZeroJS(t, orgs)
	if !strings.Contains(orgs, `<a href="/acme">acme</a> — Acme`) {
		t.Fatalf("explore orgs miss the org link:\n%s", orgs)
	}
}

func TestSocialNavVariants(t *testing.T) {
	anon := RepoSocialNavAnonymous("/ada/repo")
	assertSocialZeroJS(t, anon)
	for _, href := range []string{"/ada/repo/stargazers", "/ada/repo/forks", "/ada/repo/fork"} {
		if !strings.Contains(anon, `href="`+href+`"`) {
			t.Fatalf("anonymous nav misses %s:\n%s", href, anon)
		}
	}
	if strings.Contains(anon, "<form") {
		t.Fatalf("anonymous nav carries a mutation form:\n%s", anon)
	}
	star := RepoSocialNavStar("/ada/repo", "csrf-x")
	assertSocialZeroJS(t, star)
	if !strings.Contains(star, `action="/ada/repo/star"`) || !strings.Contains(star, ">Star</button>") {
		t.Fatalf("star nav misses the star form:\n%s", star)
	}
	unstar := RepoSocialNavUnstar("/ada/repo", "csrf-x")
	assertSocialZeroJS(t, unstar)
	if !strings.Contains(unstar, `action="/ada/repo/unstar"`) || !strings.Contains(unstar, ">Unstar</button>") {
		t.Fatalf("unstar nav misses the unstar form:\n%s", unstar)
	}
	row := repoSocialRow(op.ViewCtx{Principal: op.AnonymousPrincipal()}, "/ada/repo", RepoGetOut{Starred: true})
	if strings.Contains(row, "<form") {
		t.Fatalf("anonymous row carries a form despite starred state:\n%s", row)
	}
	signedIn := op.ViewCtx{Principal: op.Principal{Kind: op.PrincipalHuman, UserID: 7}, CSRF: "csrf-y"}
	if got := repoSocialRow(signedIn, "/ada/repo", RepoGetOut{Starred: true}); !strings.Contains(got, `action="/ada/repo/unstar"`) {
		t.Fatalf("starred viewer row misses the unstar form:\n%s", got)
	}
	if got := repoSocialRow(signedIn, "/ada/repo", RepoGetOut{}); !strings.Contains(got, `action="/ada/repo/star"`) {
		t.Fatalf("unstarred viewer row misses the star form:\n%s", got)
	}
}

func TestSocialUserTabs(t *testing.T) {
	profile := writeUserTabs("ada", "")
	if !strings.Contains(profile, "<strong>Profile</strong>") || !strings.Contains(profile, `href="/ada?tab=stars"`) {
		t.Fatalf("profile tab row is wrong:\n%s", profile)
	}
	stars := writeUserTabs("ada", "stars")
	if !strings.Contains(stars, "<strong>Stars</strong>") || !strings.Contains(stars, `href="/ada"`) {
		t.Fatalf("stars tab row is wrong:\n%s", stars)
	}
	other := writeUserTabs("ada", "Stars")
	if !strings.Contains(other, "<strong>Profile</strong>") {
		t.Fatalf("non-exact tab must not activate the stars tab:\n%s", other)
	}
}

func TestSocialStarRerender(t *testing.T) {
	c := socialCtx("/ada/repo/star", url.Values{})
	c.Errors = []op.FieldError{{Location: "body", Message: "boom"}}
	body := renderSocial(t, c, writeStarRerender)
	assertSocialZeroJS(t, body)
	if !strings.Contains(body, "boom") || !strings.Contains(body, `<a href="/ada/repo">Back to repository</a>`) {
		t.Fatalf("star rerender misses errors or the back link:\n%s", body)
	}
}

func TestSocialProfileETagVaries(t *testing.T) {
	plain := userStarsETagSuffix(UserGetOut{})
	tab := userStarsETagSuffix(UserGetOut{Tab: "stars"})
	if plain == tab {
		t.Fatalf("profile and stars tab share the ETag suffix %q", plain)
	}
	one := userStarsETagSuffix(UserGetOut{Tab: "stars", Stars: []StarEntry{{StarredAt: 5}}})
	two := userStarsETagSuffix(UserGetOut{Tab: "stars", Stars: []StarEntry{{StarredAt: 6}}})
	if one == two {
		t.Fatalf("star changes do not move the tab ETag: %q", one)
	}
	paged := userStarsETagSuffix(UserGetOut{Tab: "stars", Stars: []StarEntry{{StarredAt: 5}}, StarsNext: "c1"})
	if one == paged {
		t.Fatalf("page changes do not move the tab ETag: %q", one)
	}
}

func TestSocialForkPageOutFromPath(t *testing.T) {
	got := forkPageOutFromPath("/ada/repo/fork")
	if got.Owner != "ada" || got.Repo != "repo" {
		t.Fatalf("path parse = %+v", got)
	}
	if empty := forkPageOutFromPath("/"); empty.Owner != "" || empty.Repo != "" {
		t.Fatalf("short path parse = %+v", empty)
	}
}

func TestSocialRouteTable(t *testing.T) {
	byPattern := map[string]string{}
	for _, r := range Routes() {
		byPattern[r.Pattern] = r.Op
	}
	for pattern, want := range map[string]string{
		"GET /-/explore/repos":           "explore.repos",
		"GET /-/explore/users":           "explore.users",
		"GET /-/explore/orgs":            "explore.orgs",
		"GET /{owner}/{repo}/stargazers": "star.list",
		"POST /{owner}/{repo}/star":      "star.add",
		"POST /{owner}/{repo}/unstar":    "star.remove",
		"GET /{owner}/{repo}/forks":      "repo.fork.list",
		"GET /{owner}/{repo}/fork":       "repo.fork.page",
		"POST /{owner}/{repo}/fork":      "repo.fork.create",
	} {
		if got := byPattern[pattern]; got != want {
			t.Fatalf("route %q maps to %q, want %q", pattern, got, want)
		}
	}
	reserved := map[string]bool{}
	for _, name := range Reserved() {
		reserved[name] = true
	}
	if !reserved["explore"] {
		t.Fatalf("explore is not a reserved owner name: %v", Reserved())
	}
}
