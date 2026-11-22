package api

import (
	"cmp"
	"context"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
)

// Contract output types for the F13 social and explore operations.
//
// These types are claimed here by the UI slice (F13UI) so the Views bindings
// below and the op registrations share exact reflect types, which
// Views.check requires. F13Ops registers the operations with these Out types
// verbatim and does not redefine them elsewhere in this package.
type StarEntry struct {
	User      string `json:"user"`
	Owner     string `json:"owner"`
	Repo      string `json:"repo"`
	StarredAt int64  `json:"starred_at"`
}

// Next carries the raw store cursor. The operation's Pagination hands it to
// the framework, which signs it into the Link header. NextHref is the
// already-signed in-page continuation for browsers, which cannot read
// headers; a handler fills it with EncodeCursor(op.CursorKey(ctx), ...) and
// the page falls back to the header note when it is empty.
type StarListOut struct {
	Next     string      `json:"-"`
	NextHref string      `json:"-"`
	Stars    []StarEntry `json:"stars"`
}

type ForkListOut struct {
	Next     string     `json:"-"`
	NextHref string     `json:"-"`
	Forks    []RepoJSON `json:"forks"`
}

type ExploreReposOut struct {
	Next     string     `json:"-"`
	NextHref string     `json:"-"`
	Repos    []RepoJSON `json:"repos"`
}

type ExploreUser struct {
	Name     string `json:"name"`
	FullName string `json:"full_name,omitempty"`
}

type ExploreUsersOut struct {
	Next     string        `json:"-"`
	NextHref string        `json:"-"`
	Users    []ExploreUser `json:"users"`
}

type ExploreOrgsOut struct {
	Next     string    `json:"-"`
	NextHref string    `json:"-"`
	Orgs     []OrgJSON `json:"orgs"`
}

type ForkPageOut struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
}

func forkPageOutFromPath(path string) ForkPageOut {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 2 {
		return ForkPageOut{Owner: parts[0], Repo: parts[1]}
	}
	return ForkPageOut{}
}

func socialList(empty string, items []string, next, nextHref string) string {
	var b strings.Builder
	b.WriteString("<ul>\n")
	for _, item := range items {
		b.WriteString("<li>" + item + "</li>\n")
	}
	b.WriteString("</ul>\n")
	if len(items) == 0 {
		b.WriteString("<p>" + esc(empty) + "</p>\n")
	}
	if nextHref != "" {
		b.WriteString("<p><a rel=\"next\" href=\"" + esc(nextHref) + "\">Next page</a></p>\n")
	} else if next != "" {
		b.WriteString("<p>More results are available through the Link header.</p>\n")
	}
	return b.String()
}

func link(href, label string) string {
	return "<a href=\"" + esc(href) + "\">" + esc(label) + "</a>"
}

func writeStargazers(w io.Writer, c op.ViewCtx, out StarListOut) error {
	base := repoBase(c.Path)
	items := make([]string, 0, len(out.Stars))
	for _, entry := range out.Stars {
		items = append(items, link("/"+entry.User, entry.User))
	}
	body := heading("Stargazers of "+base) + errorList(c.Errors) +
		socialList("No one has starred this repository yet.", items, out.Next, out.NextHref) +
		"<p>" + link(base, "Back to repository") + "</p>\n"
	_, err := io.WriteString(w, pageShell("Stargazers", body))
	return err
}

func writeForkList(w io.Writer, c op.ViewCtx, out ForkListOut) error {
	base := repoBase(c.Path)
	items := make([]string, 0, len(out.Forks))
	for _, fork := range out.Forks {
		item := link("/"+fork.Owner+"/"+fork.Name, fork.Owner+" / "+fork.Name)
		if fork.State != "" && fork.State != "active" {
			item += " (" + esc(fork.State) + ")"
		}
		if fork.Description != "" {
			item += " — " + esc(fork.Description)
		}
		items = append(items, item)
	}
	body := heading("Forks of "+base) + errorList(c.Errors) +
		socialList("This repository has no forks yet.", items, out.Next, out.NextHref) +
		"<p>" + link(base+"/fork", "Fork this repository") + " — " + link(base, "Back to repository") + "</p>\n"
	_, err := io.WriteString(w, pageShell("Forks", body))
	return err
}

func writeForkPage(w io.Writer, c op.ViewCtx, out ForkPageOut) error {
	name := cmp.Or(c.Input.Get("name"), out.Repo)
	action := "/" + out.Owner + "/" + out.Repo + "/fork"
	body := heading("Fork "+out.Owner+" / "+out.Repo) + errorList(c.Errors) +
		"<p>Create your own copy of this repository. A fork cannot be more public than its source.</p>\n" +
		formOpen(action, c.CSRF) +
		textField("target_owner", "Destination owner (your username or an organization you can create in)", c.Input.Get("target_owner"), false) +
		textField("name", "Repository name", name, false) +
		checkField("default_branch_only", "Copy only the default branch and its tags; freeze markers do not carry over, so a fork's tags start mutable", c.Input.Get("default_branch_only") != "") +
		submitField("Create fork") +
		"<p>" + link("/"+out.Owner+"/"+out.Repo, "Back to repository") + "</p>\n"
	_, err := io.WriteString(w, pageShell("Fork repository", body))
	return err
}

func exploreSortNav(c op.ViewCtx, sorts []string) string {
	active := cmp.Or(c.Input.Get("sort"), sorts[0])
	items := make([]string, 0, len(sorts))
	for _, sort := range sorts {
		if sort == active {
			items = append(items, "<strong>"+esc(sort)+"</strong>")
			continue
		}
		items = append(items, link("?sort="+url.QueryEscape(sort), sort))
	}
	return "<p>Sort: " + strings.Join(items, " — ") + "</p>\n"
}

func writeExploreRepos(w io.Writer, c op.ViewCtx, out ExploreReposOut) error {
	items := make([]string, 0, len(out.Repos))
	for _, repo := range out.Repos {
		item := link("/"+repo.Owner+"/"+repo.Name, repo.Owner+" / "+repo.Name)
		if repo.Description != "" {
			item += " — " + esc(repo.Description)
		}
		if repo.Visibility != "" && repo.Visibility != "public" {
			item += " (" + esc(repo.Visibility) + ")"
		}
		items = append(items, item)
	}
	body := heading("Explore repositories") + errorList(c.Errors) +
		exploreSortNav(c, []string{"updated", "stars", "created", "name"}) +
		socialList("No public repositories match this listing yet.", items, out.Next, out.NextHref)
	_, err := io.WriteString(w, pageShell("Explore repositories", body))
	return err
}

func writeExploreUsers(w io.Writer, c op.ViewCtx, out ExploreUsersOut) error {
	items := make([]string, 0, len(out.Users))
	for _, user := range out.Users {
		item := link("/"+user.Name, user.Name)
		if user.FullName != "" {
			item += " — " + esc(user.FullName)
		}
		items = append(items, item)
	}
	body := heading("Explore users") + errorList(c.Errors) +
		exploreSortNav(c, []string{"name", "created"}) +
		socialList("No accounts match this listing yet.", items, out.Next, out.NextHref)
	_, err := io.WriteString(w, pageShell("Explore users", body))
	return err
}

func writeExploreOrgs(w io.Writer, c op.ViewCtx, out ExploreOrgsOut) error {
	items := make([]string, 0, len(out.Orgs))
	for _, org := range out.Orgs {
		item := link("/"+org.Name, org.Name)
		if org.DisplayName != "" {
			item += " — " + esc(org.DisplayName)
		}
		items = append(items, item)
	}
	body := heading("Explore organizations") + errorList(c.Errors) +
		exploreSortNav(c, []string{"name", "created"}) +
		socialList("No organizations match this listing yet.", items, out.Next, out.NextHref)
	_, err := io.WriteString(w, pageShell("Explore organizations", body))
	return err
}

func writeStarRerender(w io.Writer, c op.ViewCtx) error {
	base := repoBase(c.Path)
	body := heading("Star") + errorList(c.Errors) + "<p>" + link(base, "Back to repository") + "</p>\n"
	_, err := io.WriteString(w, pageShell("Star", body))
	return err
}

func repoSocialForm(base, csrf, action, label string) string {
	return formOpen(base+action, csrf) + submitField(label)
}

// RepoSocialNavAnonymous renders the social row for anonymous readers:
// listing links only, no mutation forms.
func RepoSocialNavAnonymous(base string) string {
	return "<p>" + link(base+"/stargazers", "Stargazers") + " — " +
		link(base+"/forks", "Forks") + " — " + link(base+"/fork", "Fork") + "</p>\n"
}

// RepoSocialNavStar renders the social row with the star form: the viewer
// has not starred this repository.
func RepoSocialNavStar(base, csrf string) string {
	return RepoSocialNavAnonymous(base) + repoSocialForm(base, csrf, "/star", "Star")
}

// RepoSocialNavUnstar renders the social row with the unstar form: the
// viewer has already starred this repository.
func RepoSocialNavUnstar(base, csrf string) string {
	return RepoSocialNavAnonymous(base) + repoSocialForm(base, csrf, "/unstar", "Unstar")
}

func writeUserTabs(name, tab string) string {
	profile, stars := link("/"+name, "Profile"), link("/"+name+"?tab=stars", "Stars")
	if tab == "stars" {
		stars = "<strong>Stars</strong>"
	} else {
		profile = "<strong>Profile</strong>"
	}
	return "<p>" + profile + " — " + stars + "</p>\n"
}

func writeUserStars(user string, stars []StarEntry, next, nextHref string) string {
	items := make([]string, 0, len(stars))
	for _, entry := range stars {
		items = append(items, link("/"+entry.Owner+"/"+entry.Repo, entry.Owner+" / "+entry.Repo))
	}
	return heading("Stars of "+user) + socialList(user+" has not starred any repositories.", items, next, nextHref)
}

func socialHref(ctx context.Context, path string, filters url.Values, next string) string {
	if next == "" {
		return ""
	}
	encoded, err := op.EncodeCursor(op.CursorKey(ctx), op.CursorPayload{Sort: next})
	if err != nil {
		return ""
	}
	query := url.Values{}
	for key, values := range filters {
		query[key] = values
	}
	query.Set("cursor", encoded)
	return path + "?" + query.Encode()
}

func listUserStars(ctx context.Context, d Deps, p op.Principal, target int64, cursor string) ([]StarEntry, string, error) {
	if d.Store == nil {
		return nil, "", op.Errorf(op.CodeUnavailable, "metadata store is not configured")
	}
	rq, rerr := d.requester(ctx, p)
	if rerr != nil {
		return nil, "", rerr
	}
	viewer := int64(0)
	if rq.authed && !rq.caller.PublicOnly {
		viewer = rq.userID
	}
	fetch := func(fetchCursor string, window int) ([]store.StarRow, string, error) {
		var rows []store.StarRow
		var rawNext string
		rerr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			var err error
			rows, rawNext, err = tx.ListStarsByUser(ctx, target, viewer, window, fetchCursor)
			return err
		})
		return rows, rawNext, rerr
	}
	rows, next, qerr := collectTokenVisible(store.SocialDefaultLimit, cursor,
		func(row store.StarRow) bool { return rq.tokenAllows(row.RepoID) },
		store.StarUserPageCursor, fetch)
	if qerr != nil {
		return nil, "", storeToOp(qerr, "stars were not found")
	}
	entries := make([]StarEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, StarEntry{
			User: row.UserName, Owner: row.OwnerName, Repo: row.RepoName, StarredAt: row.StarredAt,
		})
	}
	return entries, next, nil
}

func fillRepoSocial(ctx context.Context, d Deps, rq requester, row store.RepoRow, out *RepoGetOut) error {
	out.Stars, out.Forks = row.StarCount, row.ForksCount
	if !rq.authed || rq.userID == 0 || d.Store == nil {
		return nil
	}
	var starred bool
	rerr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		starred, err = tx.IsStarred(ctx, rq.userID, row.ID)
		return err
	})
	if rerr != nil {
		return storeToOp(rerr, "star was not found")
	}
	out.Starred = starred
	return nil
}

func repoSocialRow(c op.ViewCtx, base string, out RepoGetOut) string {
	if c.Principal.Kind == op.PrincipalAnonymous || c.Principal.UserID == 0 {
		return RepoSocialNavAnonymous(base)
	}
	if out.Starred {
		return RepoSocialNavUnstar(base, c.CSRF)
	}
	return RepoSocialNavStar(base, c.CSRF)
}

func userStarsETagSuffix(out UserGetOut) string {
	if out.Tab == "" {
		return ""
	}
	head := int64(0)
	if len(out.Stars) > 0 {
		head = out.Stars[0].StarredAt
	}
	return "-" + out.Tab + "-" + strconv.Itoa(len(out.Stars)) + "-" + strconv.FormatInt(head, 10) + "-" + out.StarsNext
}

func RegisterSocialViews(v *op.Views) {
	op.PageView(v, "star.list", writeStargazers)
	op.PageView(v, "star.list.page", writeStargazers)
	op.PageView(v, "repo.fork.list", writeForkList)
	op.PageView(v, "repo.forks.page", writeForkList)
	op.PageView(v, "repo.fork.page", writeForkPage)
	op.PageView(v, "explore.repos", writeExploreRepos)
	op.PageView(v, "explore.repos.page", writeExploreRepos)
	op.PageView(v, "explore.users", writeExploreUsers)
	op.PageView(v, "explore.users.page", writeExploreUsers)
	op.PageView(v, "explore.orgs", writeExploreOrgs)
	op.PageView(v, "explore.orgs.page", writeExploreOrgs)
	v.Form("star.add", writeStarRerender)
	v.Form("star.add.page", writeStarRerender)
	v.Form("star.remove", writeStarRerender)
	v.Form("star.remove.page", writeStarRerender)
	v.Form("repo.fork.create", func(w io.Writer, c op.ViewCtx) error {
		return writeForkPage(w, c, forkPageOutFromPath(c.Path))
	})
	v.Form("repo.fork.form", func(w io.Writer, c op.ViewCtx) error {
		return writeForkPage(w, c, forkPageOutFromPath(c.Path))
	})
}
