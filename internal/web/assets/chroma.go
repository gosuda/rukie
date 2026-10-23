package assets

import (
	"bytes"
	"cmp"
	"strings"

	"github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/styles"
)

func GenerateChromaCSS() (string, error) {
	var out bytes.Buffer
	light := cmp.Or(styles.Get("github"), styles.Fallback)
	dark := cmp.Or(styles.Get("github-dark"), styles.Fallback)
	formatter := html.New(html.WithClasses(true), html.WithCSSComments(false))
	if err := formatter.WriteCSS(&out, light); err != nil {
		return "", err
	}
	var darkBuf bytes.Buffer
	if err := formatter.WriteCSS(&darkBuf, dark); err != nil {
		return "", err
	}
	out.WriteString("@media (prefers-color-scheme: dark) {\n")
	for line := range strings.SplitSeq(strings.TrimRight(darkBuf.String(), "\n"), "\n") {
		out.WriteString(line)
		out.WriteByte('\n')
	}
	out.WriteString("}\n")
	return out.String(), nil
}
