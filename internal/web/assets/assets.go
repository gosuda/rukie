package assets

import (
	"embed"
	"net/http"
	"strings"

	"github.com/gosuda/rukie/internal/web/asset"
	"github.com/gosuda/rukie/internal/web/trusted"
)

//go:embed app.js app.css chroma.css favicon.svg
var FS embed.FS

func mustRead(name string) []byte {
	b, err := FS.ReadFile(name)
	if err != nil {
		panic(err.Error())
	}
	return b
}

func Registry() []asset.Asset {
	return []asset.Asset{
		{Name: "app.css", ContentType: "text/css; charset=utf-8", Bytes: mustRead("app.css")},
		{Name: "chroma.css", ContentType: "text/css; charset=utf-8", Bytes: mustRead("chroma.css")},
		{Name: "favicon.svg", ContentType: "image/svg+xml", Bytes: mustRead("favicon.svg")},
	}
}

func LinkTags() trusted.HTML {
	var out strings.Builder
	for _, a := range Registry() {
		if !strings.HasSuffix(a.Name, ".css") {
			continue
		}
		out.WriteString(`<link rel="stylesheet" href="`)
		out.WriteString(a.URLPath())
		out.WriteString(`">`)
	}
	return trusted.HTML(out.String())
}

func IconURL() string {
	for _, a := range Registry() {
		if a.Name == "favicon.svg" {
			return a.URLPath()
		}
	}
	return "/static/favicon.svg"
}

func Handler() *http.ServeMux {
	mux := http.NewServeMux()
	for _, a := range Registry() {
		mux.Handle("GET "+a.URLPath(), a)
	}
	b := Bundle()
	mux.Handle("GET "+BundleURL(), b)
	return mux
}
