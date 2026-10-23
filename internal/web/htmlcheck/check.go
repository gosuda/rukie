package htmlcheck

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/net/html"
)

var (
	ErrScriptTag = errors.New("htmlcheck: script element is forbidden")
	ErrEventAttr = errors.New("htmlcheck: event handler attribute is forbidden")
	ErrStyleAttr = errors.New("htmlcheck: style attribute is forbidden")
	ErrScriptURL = errors.New("htmlcheck: script URL is forbidden")
	ErrDataURL   = errors.New("htmlcheck: data URL is forbidden outside img")
)

var urlAttrs = map[string]bool{
	"href":       true,
	"src":        true,
	"action":     true,
	"formaction": true,
	"xlink:href": true,
}

func isScriptURL(v string) bool {
	t := strings.TrimSpace(strings.ToLower(v))
	return strings.HasPrefix(t, "javascript:") || strings.HasPrefix(t, "vbscript:")
}

func isDataURL(v string) bool {
	return strings.HasPrefix(strings.TrimSpace(strings.ToLower(v)), "data:")
}

func Check(src []byte) error {
	z := html.NewTokenizer(bytes.NewReader(src))
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			if errors.Is(z.Err(), io.EOF) {
				return nil
			}
			return z.Err()
		case html.TextToken, html.CommentToken, html.DoctypeToken:
		case html.StartTagToken, html.SelfClosingTagToken, html.EndTagToken:
			name, hasAttr := z.TagName()
			tag := string(name)
			if tag == "script" {
				return fmt.Errorf("%w: <%s>", ErrScriptTag, tag)
			}
			if !hasAttr {
				continue
			}
			for {
				key, val, more := z.TagAttr()
				attr := string(key)
				if len(attr) > 2 && strings.HasPrefix(attr, "on") {
					return fmt.Errorf("%w: %s", ErrEventAttr, attr)
				}
				if attr == "style" {
					return fmt.Errorf("%w", ErrStyleAttr)
				}
				if urlAttrs[attr] {
					v := string(val)
					if isScriptURL(v) {
						return fmt.Errorf("%w: %s", ErrScriptURL, attr)
					}
					if isDataURL(v) && (tag != "img" || attr != "src") {
						return fmt.Errorf("%w: %s on <%s>", ErrDataURL, attr, tag)
					}
				}
				if !more {
					break
				}
			}
		}
	}
}
