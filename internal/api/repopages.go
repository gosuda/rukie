package api

import (
	"html"
	"io"
	"strconv"
	"strings"

	"github.com/gosuda/rukie/internal/op"
)

func esc(s string) string {
	return html.EscapeString(s)
}

func repoBase(requestPath string) string {
	parts := strings.Split(requestPath, "/")
	if len(parts) < 3 || parts[1] == "" || parts[2] == "" {
		return ""
	}
	return "/" + parts[1] + "/" + parts[2]
}

func pageLink(base, suffix string) string {
	return base + suffix
}

func parseLinesRange(spec string) (int, int, bool) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return 0, 0, false
	}
	if start, end, ok := strings.Cut(trimmed, "-"); ok {
		lo, lerr := strconv.Atoi(strings.TrimSpace(start))
		hi, herr := strconv.Atoi(strings.TrimSpace(end))
		if lerr != nil || herr != nil || lo < 1 || hi < lo {
			return 0, 0, false
		}
		return lo, hi, true
	}
	n, nerr := strconv.Atoi(trimmed)
	if nerr != nil || n < 1 {
		return 0, 0, false
	}
	return n, n, true
}

func writeRepoNewPage(w io.Writer, c op.ViewCtx, _ RepoNewPageOut) error {
	body := heading("New repository") + errorList(c.Errors) +
		formOpen("/new/repo", c.CSRF) +
		textField("owner", "Owner", c.Input.Get("owner"), false) +
		textField("name", "Name", c.Input.Get("name"), false) +
		textField("description", "Description", c.Input.Get("description"), false) +
		textField("website", "Website", c.Input.Get("website"), false) +
		textField("visibility", "Visibility (public, internal, private)", c.Input.Get("visibility"), false) +
		textField("default_branch", "Default branch", c.Input.Get("default_branch"), false) +
		checkField("init_readme", "Initialize with a README", c.Input.Get("init_readme") != "") +
		submitField("Create repository")
	_, err := io.WriteString(w, pageShell("New repository", body))
	return err
}

func writeRepoHome(w io.Writer, c op.ViewCtx, out RepoGetOut) error {
	base := repoBase(c.Path)
	var b strings.Builder
	b.WriteString(heading(out.Repo.Owner + " / " + out.Repo.Name))
	if out.Repo.Description != "" {
		b.WriteString(para(out.Repo.Description))
	}
	b.WriteString("<p>Visibility: " + esc(out.Repo.Visibility) + " — Default branch: " + esc(out.Repo.DefaultBranch) + "</p>\n")
	if out.Repo.State == "deleted" {
		b.WriteString("<p>This repository is deleted and restorable until its purge horizon.</p>\n")
		b.WriteString("<p><a href=\"" + esc(base+"/settings") + "\">Settings</a></p>\n")
		_, err := io.WriteString(w, pageShell(out.Repo.Name, b.String()))
		return err
	}
	if out.Empty {
		b.WriteString("<p>This repository is empty. Push a branch to see it here.</p>\n")
		b.WriteString("<p><a href=\"" + esc(base+"/branches") + "\">Branches</a> — " +
			"<a href=\"" + esc(base+"/settings") + "\">Settings</a></p>\n")
		_, err := io.WriteString(w, pageShell(out.Repo.Name, b.String()))
		return err
	}
	b.WriteString("<p><a href=\"" + esc(pageLink(base, "/log/"+out.Repo.DefaultBranch)) + "\">Log</a> — " +
		"<a href=\"" + esc(pageLink(base, "/branches")) + "\">Branches</a> — " +
		"<a href=\"" + esc(pageLink(base, "/tags")) + "\">Tags</a> — " +
		"<a href=\"" + esc(pageLink(base, "/settings")) + "\">Settings</a></p>\n")
	b.WriteString("<h2>Files</h2>\n<ul>\n")
	for _, entry := range out.Tree {
		href := pageLink(base, "/tree/"+out.Commit+"/"+entry.Name)
		if entry.Kind != "tree" {
			href = pageLink(base, "/blob/"+out.Commit+"/"+entry.Name)
		}
		b.WriteString("<li><a href=\"" + esc(href) + "\">" + esc(entry.Name) + "</a> (" + esc(entry.Kind) + ")</li>\n")
	}
	b.WriteString("</ul>\n")
	if out.Readme != nil {
		b.WriteString("<h2>" + esc(out.Readme.Path) + "</h2>\n<pre>" + esc(out.Readme.Content) + "</pre>\n")
		if out.Readme.Truncated {
			b.WriteString("<p>Truncated at 64 KiB.</p>\n")
		}
	}
	_, err := io.WriteString(w, pageShell(out.Repo.Owner+" / "+out.Repo.Name, b.String()))
	return err
}

func writeRepoList(w io.Writer, c op.ViewCtx, out RepoListOut) error {
	_ = c
	var b strings.Builder
	b.WriteString(heading("Repositories"))
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
	if out.Next != "" {
		b.WriteString("<p>More results are available through the Link header.</p>\n")
	}
	b.WriteString("<p><a href=\"/new/repo\">New repository</a></p>\n")
	_, err := io.WriteString(w, pageShell("Repositories", b.String()))
	return err
}

func writeRepoSettings(w io.Writer, c op.ViewCtx, out RepoSettingsPageOut) error {
	repo := out.Repo
	etag := out.ETag
	var b strings.Builder
	b.WriteString(heading("Settings for " + repo.Owner + " / " + repo.Name))
	b.WriteString(errorList(c.Errors))
	if repo.State == "deleted" {
		b.WriteString("<p>This repository is deleted.</p>\n")
		b.WriteString(formOpen("/"+repo.Owner+"/"+repo.Name+"/settings/restore", c.CSRF) +
			hiddenField("_etag", etag) + submitField("Restore repository"))
		_, err := io.WriteString(w, pageShell("Settings", b.String()))
		return err
	}
	b.WriteString("<h2>General</h2>\n" +
		formOpen("/"+repo.Owner+"/"+repo.Name+"/settings/general", c.CSRF) +
		hiddenField("_etag", etag) +
		textField("description", "Description", repo.Description, false) +
		textField("website", "Website", repo.Website, false) +
		textField("default_branch", "Default branch", repo.DefaultBranch, false) +
		checkField("archived", "Archived", repo.Archived) +
		submitField("Save settings"))
	b.WriteString("<h2>Visibility</h2>\n" +
		formOpen("/"+repo.Owner+"/"+repo.Name+"/settings/visibility", c.CSRF) +
		hiddenField("_etag", etag) +
		textField("visibility", "Visibility (public, internal, private)", repo.Visibility, false) +
		textField("confirm", "Confirm full name", "", false) +
		submitField("Change visibility"))
	b.WriteString("<h2>Rename</h2>\n" +
		formOpen("/"+repo.Owner+"/"+repo.Name+"/settings/rename", c.CSRF) +
		hiddenField("_etag", etag) +
		textField("name", "New name", repo.Name, false) +
		submitField("Rename repository"))
	b.WriteString("<h2>Delete</h2>\n" +
		formOpen("/"+repo.Owner+"/"+repo.Name+"/settings/delete", c.CSRF) +
		hiddenField("_etag", etag) +
		textField("confirm", "Confirm full name", "", false) +
		submitField("Delete repository"))
	b.WriteString("<p><a href=\"/" + esc(repo.Owner+"/"+repo.Name+"/settings/collaborators") + "\">Collaborators</a></p>\n")
	_, err := io.WriteString(w, pageShell("Settings", b.String()))
	return err
}

func writeCollaborators(w io.Writer, c op.ViewCtx, out CollaboratorListOut) error {
	base := repoBase(c.Path)
	settingsBase := strings.TrimSuffix(base, "/settings/collaborators") + "/" + "x"
	_ = settingsBase
	var b strings.Builder
	b.WriteString(heading("Collaborators"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<ul>\n")
	for _, grant := range out.Collaborators {
		b.WriteString("<li>" + esc(grant.User) + " — " + esc(grant.Permission) + "</li>\n")
	}
	b.WriteString("</ul>\n")
	if out.Next != "" {
		b.WriteString("<p>More results are available through the Link header.</p>\n")
	}
	repoBase, _ := strings.CutSuffix(base, "/settings/collaborators")
	b.WriteString("<h2>Grant a role</h2>\n" +
		formOpen(repoBase+"/settings/collaborators/_user_", c.CSRF) +
		"<p>Post to " + esc(repoBase+"/settings/collaborators/{user}") + " with user and permission.</p>\n" +
		textField("user", "User", "", false) +
		textField("permission", "Permission (read, triage, write, maintain, admin)", "", false) +
		submitField("Grant role"))
	_, err := io.WriteString(w, pageShell("Collaborators", b.String()))
	return err
}

func writeRevResolve(w io.Writer, c op.ViewCtx, out RevResolveOut) error {
	_ = c
	var b strings.Builder
	b.WriteString(heading("Revision " + out.Rev))
	b.WriteString("<dl>\n<dt>Kind</dt><dd>" + esc(out.Kind) + "</dd>\n")
	if out.Ref != nil {
		b.WriteString("<dt>Ref</dt><dd>" + esc(*out.Ref) + "</dd>\n")
	}
	b.WriteString("<dt>Commit</dt><dd>" + esc(out.Commit) + "</dd>\n")
	if out.TagObject != nil {
		b.WriteString("<dt>Tag object</dt><dd>" + esc(*out.TagObject) + "</dd>\n")
	}
	b.WriteString("</dl>\n")
	_, err := io.WriteString(w, pageShell("Revision", b.String()))
	return err
}

func writeTree(w io.Writer, c op.ViewCtx, out TreeGetOut) error {
	base := repoBase(c.Path)
	var b strings.Builder
	b.WriteString(heading("Tree at " + out.Commit))
	b.WriteString("<p>" + esc(out.Path) + "</p>\n")
	b.WriteString("<table>\n<tr><th>Name</th><th>Kind</th><th>Oid</th></tr>\n")
	for _, entry := range out.Items {
		joined, _ := strings.CutPrefix(out.Path+"/"+entry.Name, "/")
		href := base + "/blob/" + out.Commit + "/" + joined
		if entry.Kind == "tree" {
			href = base + "/tree/" + out.Commit + "/" + joined
		}
		b.WriteString("<tr><td><a href=\"" + esc(href) + "\">" + esc(entry.Name) + "</a></td>" +
			"<td>" + esc(entry.Kind) + "</td><td>" + esc(entry.Oid) + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	if out.Next != "" {
		b.WriteString("<p>More results are available through the Link header.</p>\n")
	}
	_, err := io.WriteString(w, pageShell("Tree", b.String()))
	return err
}

func writeBlob(w io.Writer, c op.ViewCtx, out BlobGetOut) error {
	base := repoBase(c.Path)
	var b strings.Builder
	b.WriteString(heading(out.Path))
	lo, hi, marked := parseLinesRange(out.Lines)
	switch out.Kind {
	case "symlink":
		target := ""
		if out.Target != nil {
			target = *out.Target
		}
		b.WriteString("<p>Symlink to " + esc(target) + "</p>\n")
	case "gitlink":
		gitlink := ""
		if out.Gitlink != nil {
			gitlink = *out.Gitlink
		}
		b.WriteString("<p>Submodule at " + esc(gitlink) + "</p>\n")
	default:
		if out.Binary {
			b.WriteString("<p>Binary file, " + esc(strconv.FormatUint(out.Size, 10)) + " bytes. " +
				"<a href=\"" + esc(base+"/raw/"+out.Commit+"/"+out.Path+"?download=1") + "\">Download</a></p>\n")
			break
		}
		rawURL := base + "/raw/" + out.Commit + "/" + out.Path
		b.WriteString("<p><a href=\"" + esc(rawURL) + "\">Raw</a> — " +
			"<a href=\"" + esc(base+"/blame/"+out.Commit+"/"+out.Path) + "\">Blame</a></p>\n")
		b.WriteString("<table>\n")
		lines := strings.Split(out.Content, "\n")
		for i, line := range lines {
			n := i + 1
			row := "<tr><td>" + strconv.Itoa(n) + "</td><td>"
			if marked && n >= lo && n <= hi {
				row += "<mark>" + esc(line) + "</mark>"
			} else {
				row += esc(line)
			}
			row += "</td></tr>\n"
			b.WriteString(row)
		}
		b.WriteString("</table>\n")
		if out.Truncated {
			b.WriteString("<p>Truncated at 1 MiB.</p>\n")
		}
	}
	_, err := io.WriteString(w, pageShell(out.Path, b.String()))
	return err
}

func writeRaw(w io.Writer, c op.ViewCtx, out RawGetOut) error {
	var b strings.Builder
	b.WriteString(heading("Raw bytes"))
	b.WriteString("<dl>\n<dt>Oid</dt><dd>" + esc(out.Oid) + "</dd>\n" +
		"<dt>Size</dt><dd>" + esc(strconv.FormatUint(out.Size, 10)) + "</dd>\n" +
		"<dt>Content type</dt><dd>" + esc(out.ContentType) + "</dd>\n</dl>\n")
	if out.DownloadURL != "" {
		b.WriteString("<p><a href=\"" + esc(out.DownloadURL) + "\">Download</a></p>\n")
	}
	if out.Content != "" {
		b.WriteString("<pre>" + esc(out.Content) + "</pre>\n")
		if out.Truncated {
			b.WriteString("<p>Truncated at 64 KiB.</p>\n")
		}
	}
	_ = c
	_, err := io.WriteString(w, pageShell("Raw", b.String()))
	return err
}

func writeBlame(w io.Writer, c op.ViewCtx, out BlameGetOut) error {
	_ = c
	var b strings.Builder
	b.WriteString(heading("Blame for " + out.Path))
	b.WriteString("<table>\n<tr><th>Lines</th><th>Commit</th><th>Author</th><th>Path</th></tr>\n")
	for _, hunk := range out.Hunks {
		b.WriteString("<tr><td>" + esc(strconv.FormatUint(uint64(hunk.StartLine), 10)) + "+" +
			esc(strconv.FormatUint(uint64(hunk.LineCount), 10)) + "</td>" +
			"<td>" + esc(hunk.Head) + "</td><td>" + esc(hunk.Author) + "</td>" +
			"<td>" + esc(hunk.OriginalPath) + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	if out.Next != "" {
		b.WriteString("<p>More results are available through the Link header.</p>\n")
	}
	_, err := io.WriteString(w, pageShell("Blame", b.String()))
	return err
}

func writeLog(w io.Writer, c op.ViewCtx, out LogListOut) error {
	_ = c
	var b strings.Builder
	b.WriteString(heading("Commits"))
	b.WriteString("<ul>\n")
	for _, commit := range out.Commits {
		b.WriteString("<li>" + esc(commit.Head) + " — " + esc(commit.Subject) +
			" (" + esc(commit.Author) + ")</li>\n")
	}
	b.WriteString("</ul>\n")
	if out.Next != "" {
		b.WriteString("<p>More results are available through the Link header.</p>\n")
	}
	_, err := io.WriteString(w, pageShell("Commits", b.String()))
	return err
}

func writeCommit(w io.Writer, c op.ViewCtx, out CommitGetOut) error {
	base := repoBase(c.Path)
	var b strings.Builder
	b.WriteString(heading(out.Oid))
	b.WriteString("<p>" + esc(out.Subject) + "</p>\n")
	if out.Body != "" {
		b.WriteString("<pre>" + esc(out.Body) + "</pre>\n")
	}
	b.WriteString("<dl>\n<dt>Author</dt><dd>" + esc(out.Author) + " " + esc(out.AuthorEmail) + "</dd>\n")
	if len(out.Parents) > 0 {
		b.WriteString("<dt>Parents</dt><dd>" + esc(strings.Join(out.Parents, ", ")) + "</dd>\n")
	}
	if out.Change != nil {
		b.WriteString("<dt>Change</dt><dd>" + esc(*out.Change) + "</dd>\n")
	}
	b.WriteString("</dl>\n")
	if len(out.Attested) > 0 {
		b.WriteString("<h2>Attestations</h2>\n<ul>\n")
		for _, att := range out.Attested {
			b.WriteString("<li>" + esc(att.Kind) + ": " + esc(att.Result) + " by " + esc(att.Signer) + "</li>\n")
		}
		b.WriteString("</ul>\n")
	}
	b.WriteString("<p><a href=\"" + esc(base+"/commit/"+out.Oid+"/diff") + "\">Diff</a> — " +
		"<a href=\"" + esc(base+"/commit/"+out.Oid+"/patch") + "\">Patch</a></p>\n")
	_ = c
	_, err := io.WriteString(w, pageShell(out.Oid, b.String()))
	return err
}

func writeDiff(w io.Writer, c op.ViewCtx, out DiffGetOut) error {
	_ = c
	var b strings.Builder
	b.WriteString(heading("Diff " + out.Left + ".." + out.Right))
	for _, file := range out.Files {
		b.WriteString("<h2>" + esc(file.Path) + " (" + esc(file.Kind) + ")</h2>\n")
		if file.Binary {
			b.WriteString("<p>Binary file.</p>\n")
			continue
		}
		b.WriteString("<pre>" + esc(file.Patch) + "</pre>\n")
	}
	if out.Next != "" {
		b.WriteString("<p>More results are available through the Link header.</p>\n")
	}
	_, err := io.WriteString(w, pageShell("Diff", b.String()))
	return err
}

func writeBranches(w io.Writer, c op.ViewCtx, out BranchListOut) error {
	base := repoBase(c.Path)
	repo := strings.TrimSuffix(base, "/branches")
	_ = repo
	var b strings.Builder
	b.WriteString(heading("Branches"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<ul>\n")
	for _, branch := range out.Branches {
		href := base + "/" + branch.Name
		b.WriteString("<li><a href=\"" + esc(href) + "\">" + esc(branch.Name) + "</a> — " + esc(branch.Target) + "</li>\n")
	}
	b.WriteString("</ul>\n")
	if out.Next != "" {
		b.WriteString("<p>More results are available through the Link header.</p>\n")
	}
	b.WriteString("<h2>New branch</h2>\n" +
		formOpen(base+"/create", c.CSRF) +
		textField("name", "Name", c.Input.Get("name"), false) +
		textField("from", "From revision", c.Input.Get("from"), false) +
		submitField("Create branch"))
	_, err := io.WriteString(w, pageShell("Branches", b.String()))
	return err
}

func writeBranch(w io.Writer, c op.ViewCtx, out BranchGetOut) error {
	var b strings.Builder
	b.WriteString(heading("Branch " + out.Branch.Name))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<p>Target: " + esc(out.Branch.Target) + "</p>\n")
	path := c.Path
	b.WriteString("<h2>Move branch</h2>\n" +
		formOpen(path+"/update", c.CSRF) +
		hiddenField("_etag", "\""+out.Branch.Target+"\"") +
		textField("target", "Target revision", "", false) +
		checkField("force", "Force", false) +
		submitField("Move branch"))
	b.WriteString("<h2>Delete branch</h2>\n" +
		formOpen(path+"/delete", c.CSRF) +
		hiddenField("_etag", "\""+out.Branch.Target+"\"") +
		submitField("Delete branch"))
	_, err := io.WriteString(w, pageShell("Branch", b.String()))
	return err
}

func writeTags(w io.Writer, c op.ViewCtx, out TagListOut) error {
	base := repoBase(c.Path)
	var b strings.Builder
	b.WriteString(heading("Tags"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<ul>\n")
	for _, tag := range out.Tags {
		href := base + "/" + tag.Name
		b.WriteString("<li><a href=\"" + esc(href) + "\">" + esc(tag.Name) + "</a> — " + esc(tag.Target) + "</li>\n")
	}
	b.WriteString("</ul>\n")
	if out.Next != "" {
		b.WriteString("<p>More results are available through the Link header.</p>\n")
	}
	b.WriteString("<h2>New tag</h2>\n" +
		formOpen(base+"/create", c.CSRF) +
		textField("name", "Name", c.Input.Get("name"), false) +
		textField("target", "Target revision", c.Input.Get("target"), false) +
		textField("message", "Message (annotated)", c.Input.Get("message"), false) +
		submitField("Create tag"))
	_, err := io.WriteString(w, pageShell("Tags", b.String()))
	return err
}

func writeTag(w io.Writer, c op.ViewCtx, out TagGetOut) error {
	var b strings.Builder
	b.WriteString(heading("Tag " + out.Tag.Name))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<p>Target: " + esc(out.Tag.Target) + "</p>\n")
	if out.Tag.Commit != nil {
		b.WriteString("<p>Commit: " + esc(*out.Tag.Commit) + "</p>\n")
	}
	b.WriteString("<h2>Delete tag</h2>\n" +
		formOpen(c.Path+"/delete", c.CSRF) +
		hiddenField("_etag", "\""+out.Tag.Target+"\"") +
		submitField("Delete tag"))
	_, err := io.WriteString(w, pageShell("Tag", b.String()))
	return err
}
