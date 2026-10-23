package markdown

import (
	"bytes"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"

	"github.com/gosuda/rukie/internal/web/trusted"
)

var converter = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(html.WithXHTML()),
)

func Render(src []byte) (trusted.HTML, error) {
	var buf bytes.Buffer
	if err := converter.Convert(src, &buf); err != nil {
		return "", err
	}
	return trusted.Markdown(buf.String())
}
