package highlight

import (
	"bytes"
	"cmp"
	"errors"
	"strings"

	"github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"

	"github.com/gosuda/rukie/internal/web/trusted"
)

const MaxHighlightBytes = 1 << 18

var ErrTooLarge = errors.New("highlight: source exceeds the highlight cap")

var formatter = html.New(html.WithClasses(true))

func Code(lang, src string) (trusted.HTML, error) {
	if len(src) > MaxHighlightBytes {
		return "", ErrTooLarge
	}
	lexer := cmp.Or(lexers.Get(lang), lexers.Fallback)
	it, err := lexer.Tokenise(nil, src)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err = formatter.Format(&buf, styles.Fallback, it); err != nil {
		return "", err
	}
	out := buf.String()
	if !strings.HasPrefix(out, "<pre") {
		return "", errors.New("highlight: unexpected formatter output")
	}
	checked, err := trusted.Raw(out)
	if err != nil {
		return "", err
	}
	return checked, nil
}
