package api

import (
	"io"
	"strconv"
	"strings"

	"github.com/gosuda/rukie/internal/op"
)

func RegisterReleasesViews(v *op.Views) {
	op.PageView(v, "release.list", writeReleases)
	op.PageView(v, "release.get", writeRelease)
	op.PageView(v, "release.asset.list", writeReleaseAssets)
	op.PageView(v, "code.archive.get", writeArchive)
	v.Form("release.create", func(w io.Writer, c op.ViewCtx) error {
		return writeReleases(w, c, ReleaseListOut{Releases: []ReleaseJSON{}})
	})
	v.Form("release.update", func(w io.Writer, c op.ViewCtx) error {
		return writeRelease(w, c, ReleaseGetOut{})
	})
	v.Form("release.delete", func(w io.Writer, c op.ViewCtx) error {
		return writeRelease(w, c, ReleaseGetOut{})
	})
	v.Form("release.asset.create", func(w io.Writer, c op.ViewCtx) error {
		return writeRelease(w, c, ReleaseGetOut{})
	})
	v.Form("release.asset.delete", func(w io.Writer, c op.ViewCtx) error {
		return writeRelease(w, c, ReleaseGetOut{})
	})
}

func releaseListBase(requestPath string) string {
	if strings.HasSuffix(requestPath, "/releases") {
		return requestPath
	}
	if base, _, found := strings.Cut(requestPath, "/releases/"); found {
		return base + "/releases"
	}
	return requestPath
}

func writeReleaseState(row ReleaseJSON) string {
	if row.Draft {
		return "draft"
	}
	return "published"
}

func writeReleases(w io.Writer, c op.ViewCtx, out ReleaseListOut) error {
	base := releaseListBase(c.Path)
	var b strings.Builder
	b.WriteString(heading("Releases"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<ul>\n")
	for _, item := range out.Releases {
		href := base + "/tag/" + item.Tag
		b.WriteString("<li><a href=\"" + esc(href) + "\">" + esc(item.Tag) + "</a> — " + esc(item.Title) + " (" + esc(writeReleaseState(item)) + ")</li>\n")
	}
	b.WriteString("</ul>\n")
	if out.Next != "" {
		b.WriteString("<p>More results are available through the Link header.</p>\n")
	}
	b.WriteString("<h2>New release</h2>\n" +
		formOpen(base+"/new", c.CSRF) +
		textField("tag", "Tag", c.Input.Get("tag"), false) +
		textField("title", "Title", c.Input.Get("title"), false) +
		textField("notes", "Notes", c.Input.Get("notes"), false) +
		checkField("draft", "Draft", true) +
		checkField("prerelease", "Prerelease", false) +
		submitField("Create release"))
	_, err := io.WriteString(w, pageShell("Releases", b.String()))
	return err
}

func writeRelease(w io.Writer, c op.ViewCtx, out ReleaseGetOut) error {
	var b strings.Builder
	b.WriteString(errorList(c.Errors))
	if out.Release.Tag == "" {
		b.WriteString(heading("Release"))
		_, err := io.WriteString(w, pageShell("Release", b.String()))
		return err
	}
	item := out.Release
	b.WriteString(heading("Release " + item.Tag))
	b.WriteString("<p>State: " + esc(writeReleaseState(item)) + "</p>\n")
	b.WriteString("<p>Title: " + esc(item.Title) + "</p>\n")
	if item.Commit != "" {
		b.WriteString("<p>Commit: " + esc(item.Commit) + "</p>\n")
	}
	if item.PublishedAt != 0 {
		b.WriteString("<p>Published: " + esc(strconv.FormatInt(item.PublishedAt, 10)) + "</p>\n")
	}
	b.WriteString("<div>" + item.NotesHTML + "</div>\n")
	b.WriteString("<h2>Assets</h2>\n<ul>\n")
	for _, asset := range item.Assets {
		b.WriteString("<li><a href=\"" + esc(asset.URL) + "\">" + esc(asset.Name) + "</a> (" + esc(strconv.FormatInt(asset.Size, 10)) + " bytes)</li>\n")
	}
	b.WriteString("</ul>\n")
	base, _, _ := strings.Cut(c.Path, "/tag/")
	if item.Draft {
		b.WriteString("<h2>Edit draft</h2>\n" +
			formOpen(base+"/edit/"+item.Tag, c.CSRF) +
			hiddenField("_etag", c.Input.Get("_etag")) +
			textField("title", "Title", item.Title, false) +
			textField("notes", "Notes", item.Notes, false) +
			submitField("Save draft"))
		b.WriteString("<h2>Publish draft</h2>\n" +
			formOpen(base+"/edit/"+item.Tag, c.CSRF) +
			hiddenField("_etag", c.Input.Get("_etag")) +
			hiddenField("draft", "false") +
			submitField("Publish release"))
		b.WriteString("<h2>Upload asset</h2>\n<p>Upload asset bytes with a scoped API token: PUT " + esc(base+"/assets/"+item.Tag+"/{name}") + " with an If-None-Match or If-Match precondition.</p>\n")
	}
	b.WriteString("<h2>Delete release</h2>\n" +
		formOpen(base+"/delete/"+item.Tag, c.CSRF) +
		hiddenField("_etag", c.Input.Get("_etag")) +
		submitField("Delete release "+item.Tag))
	_, err := io.WriteString(w, pageShell("Release "+item.Tag, b.String()))
	return err
}

func writeReleaseAssets(w io.Writer, c op.ViewCtx, out ReleaseAssetListOut) error {
	var b strings.Builder
	b.WriteString(heading("Release assets"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<ul>\n")
	for _, asset := range out.Assets {
		b.WriteString("<li><a href=\"" + esc(asset.URL) + "\">" + esc(asset.Name) + "</a> (" + esc(strconv.FormatInt(asset.Size, 10)) + " bytes, " + esc(asset.ContentType) + ")</li>\n")
	}
	b.WriteString("</ul>\n")
	if out.Next != "" {
		b.WriteString("<p>More results are available through the Link header.</p>\n")
	}
	_, err := io.WriteString(w, pageShell("Release assets", b.String()))
	return err
}

func writeArchive(w io.Writer, c op.ViewCtx, out ArchiveGetOut) error {
	var b strings.Builder
	b.WriteString(heading("Archive"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<p>Commit: " + esc(out.Commit) + "</p>\n")
	b.WriteString("<p>Format: " + esc(out.Format) + "</p>\n")
	b.WriteString("<p><a href=\"" + esc(c.Path+"?format="+out.Format+"&download=1") + "\">Download archive</a></p>\n")
	_, err := io.WriteString(w, pageShell("Archive", b.String()))
	return err
}
