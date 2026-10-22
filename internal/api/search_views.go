package api

import (
	"io"
	"net/url"
	"strings"

	"github.com/gosuda/rukie/internal/op"
)

func searchForm(action, q, typ, owner, repo string) string {
	var b strings.Builder
	b.WriteString("<form method=\"get\" action=\"" + esc(action) + "\">\n")
	b.WriteString("<label>Query\n<input type=\"text\" name=\"q\" value=\"" + esc(q) + "\">\n</label>\n")
	b.WriteString("<label>Type\n<select name=\"type\">\n")
	for _, opt := range []string{"code", "repos", "users"} {
		selected := ""
		if (typ == "" && opt == "code") || typ == opt {
			selected = " selected"
		}
		b.WriteString("<option value=\"" + opt + "\"" + selected + ">" + opt + "</option>\n")
	}
	b.WriteString("</select>\n</label>\n")
	b.WriteString("<label>Owner\n<input type=\"text\" name=\"owner\" value=\"" + esc(owner) + "\">\n</label>\n")
	b.WriteString("<label>Repository\n<input type=\"text\" name=\"repo\" value=\"" + esc(repo) + "\">\n</label>\n")
	b.WriteString("<button type=\"submit\">Search</button>\n</form>\n")
	return b.String()
}

func searchTabs(active string) string {
	var b strings.Builder
	b.WriteString("<nav>\n")
	tabs := []struct{ label, href string }{
		{"Code", "/search"},
		{"Repositories", "/search?type=repos"},
		{"Users", "/search?type=users"},
	}
	for _, tab := range tabs {
		current := active
		if current == "" {
			current = "code"
		}
		marker := ""
		switch tab.label {
		case "Code":
			if current == "code" {
				marker = " aria-current=\"page\""
			}
		case "Repositories":
			if current == "repos" {
				marker = " aria-current=\"page\""
			}
		case "Users":
			if current == "users" {
				marker = " aria-current=\"page\""
			}
		}
		b.WriteString("<a href=\"" + esc(tab.href) + "\"" + marker + ">" + esc(tab.label) + "</a>\n")
	}
	b.WriteString("</nav>\n")
	return b.String()
}

func searchNextLink(c op.ViewCtx, next string) string {
	if next == "" {
		return ""
	}
	query := c.Input
	if query == nil {
		query = url.Values{}
	} else {
		query = query.Clone()
	}
	query.Set("next", next)
	return c.Path + "?" + query.Encode()
}

func writeSearchHitList(out SearchOut) string {
	var b strings.Builder
	if len(out.Hits) == 0 {
		b.WriteString("<p>No code matches. Refine the query or narrow the scope.</p>\n")
		return b.String()
	}
	b.WriteString("<ul>\n")
	for _, hit := range out.Hits {
		href := "/" + hit.Repo + "/blob/main/" + hit.Path
		b.WriteString("<li><a href=\"" + esc(href) + "\">" + esc(hit.Repo+" / "+hit.Path) + "</a>")
		b.WriteString(" — line " + esc(itoa(int(hit.Line))) + ", column " + esc(itoa(int(hit.Column))))
		b.WriteString("<pre>" + esc(hit.Preview.String()) + "</pre></li>\n")
	}
	b.WriteString("</ul>\n")
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}

func writeSearch(w io.Writer, c op.ViewCtx, out SearchOut) error {
	active := out.Type
	if active == "" {
		active = "code"
	}
	var b strings.Builder
	b.WriteString(heading("Search"))
	b.WriteString(searchTabs(active))
	b.WriteString(searchForm("/search", c.Input.Get("q"), active, c.Input.Get("owner"), c.Input.Get("repo")))
	if strings.TrimSpace(c.Input.Get("q")) == "" {
		b.WriteString(para("Enter a query to search code, repositories, and users."))
		_, err := io.WriteString(w, pageShell("Search", b.String()))
		return err
	}
	switch out.Type {
	case "repos":
		b.WriteString(heading("Repositories"))
		if len(out.Repos) == 0 {
			b.WriteString("<p>No repositories match.</p>\n")
		} else {
			b.WriteString("<ul>\n")
			for _, repo := range out.Repos {
				href := "/" + repo.Owner + "/" + repo.Name
				b.WriteString("<li><a href=\"" + esc(href) + "\">" + esc(repo.Owner+" / "+repo.Name) + "</a>")
				if repo.Description != "" {
					b.WriteString(" — " + esc(repo.Description))
				}
				b.WriteString("</li>\n")
			}
			b.WriteString("</ul>\n")
		}
	case "users":
		b.WriteString(heading("Users"))
		if len(out.Users) == 0 {
			b.WriteString("<p>No users match.</p>\n")
		} else {
			b.WriteString("<ul>\n")
			for _, user := range out.Users {
				href := "/" + user.Name
				label := user.Name
				if user.FullName != "" {
					label += " (" + user.FullName + ")"
				}
				b.WriteString("<li><a href=\"" + esc(href) + "\">" + esc(label) + "</a></li>\n")
			}
			b.WriteString("</ul>\n")
		}
	default:
		b.WriteString(writeSearchHitList(out))
	}
	if out.Next != nil && *out.Next != "" {
		b.WriteString("<p><a href=\"" + esc(searchNextLink(c, *out.Next)) + "\">Next page</a></p>\n")
	} else if strings.TrimSpace(c.Input.Get("q")) != "" {
		b.WriteString("<p>No more pages.</p>\n")
	}
	_, err := io.WriteString(w, pageShell("Search", b.String()))
	return err
}

func writeSearchRepos(w io.Writer, c op.ViewCtx, out SearchReposOut) error {
	var b strings.Builder
	b.WriteString(heading("Search repositories"))
	b.WriteString(searchForm("/search/repos", c.Input.Get("q"), "repos", "", ""))
	if strings.TrimSpace(c.Input.Get("q")) == "" {
		b.WriteString(para("Enter a query to search repository names, descriptions, topics, and websites."))
		_, err := io.WriteString(w, pageShell("Search repositories", b.String()))
		return err
	}
	if len(out.Repos) == 0 {
		b.WriteString("<p>No repositories match.</p>\n")
	} else {
		b.WriteString("<ul>\n")
		for _, repo := range out.Repos {
			href := "/" + repo.Owner + "/" + repo.Name
			b.WriteString("<li><a href=\"" + esc(href) + "\">" + esc(repo.Owner+" / "+repo.Name) + "</a>")
			if repo.Description != "" {
				b.WriteString(" — " + esc(repo.Description))
			}
			b.WriteString("</li>\n")
		}
		b.WriteString("</ul>\n")
	}
	if out.Next != nil && *out.Next != "" {
		b.WriteString("<p><a href=\"" + esc(searchNextLink(c, *out.Next)) + "\">Next page</a></p>\n")
	}
	_, err := io.WriteString(w, pageShell("Search repositories", b.String()))
	return err
}

func writeSearchUsers(w io.Writer, c op.ViewCtx, out SearchUsersOut) error {
	var b strings.Builder
	b.WriteString(heading("Search users"))
	b.WriteString(searchForm("/search/users", c.Input.Get("q"), "users", "", ""))
	if strings.TrimSpace(c.Input.Get("q")) == "" {
		b.WriteString(para("Enter a query to search the public user directory."))
		_, err := io.WriteString(w, pageShell("Search users", b.String()))
		return err
	}
	if len(out.Users) == 0 {
		b.WriteString("<p>No users match.</p>\n")
	} else {
		b.WriteString("<ul>\n")
		for _, user := range out.Users {
			href := "/" + user.Name
			label := user.Name
			if user.FullName != "" {
				label += " (" + user.FullName + ")"
			}
			b.WriteString("<li><a href=\"" + esc(href) + "\">" + esc(label) + "</a></li>\n")
		}
		b.WriteString("</ul>\n")
	}
	if out.Next != nil && *out.Next != "" {
		b.WriteString("<p><a href=\"" + esc(searchNextLink(c, *out.Next)) + "\">Next page</a></p>\n")
	}
	_, err := io.WriteString(w, pageShell("Search users", b.String()))
	return err
}

// RegisterSearchViews binds the three search pages through the Views
// contract. All markup is hand-authored zero-JS: forms use GET, paging
// rides plain links, and no page emits script.
func RegisterSearchViews(v *op.Views) {
	op.PageView(v, "search.code", writeSearch)
	op.PageView(v, "search.repos", writeSearchRepos)
	op.PageView(v, "search.users", writeSearchUsers)
}
