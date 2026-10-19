package htmlcheck

import (
	"errors"
	"testing"
)

func TestCheckRejects(t *testing.T) {
	cases := map[string]string{
		"script element":        `<p>hi</p><script>alert(1)</script>`,
		"script uppercase":      `<SCRIPT SRC="x.js"></SCRIPT>`,
		"event handler":         `<button onclick="run()">x</button>`,
		"event handler mixed":   `<body OnLoad="run()">`,
		"style attribute":       `<p style="color:red">x</p>`,
		"javascript href":       `<a href="javascript:alert(1)">x</a>`,
		"javascript padded":     `<a href="  JaVaScRiPt:alert(1)">x</a>`,
		"vbscript href":         `<a href="vbscript:msgbox(1)">x</a>`,
		"formaction javascript": `<button formaction="javascript:alert(1)">x</button>`,
		"data url on anchor":    `<a href="data:text/html,<p>x</p>">x</a>`,
		"data url on div src":   `<div src="data:text/plain,hi">x</div>`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if err := Check([]byte(src)); err == nil {
				t.Fatalf("Check(%q) = nil, want rejection", src)
			}
		})
	}
}

func TestCheckAccepts(t *testing.T) {
	cases := map[string]string{
		"plain paragraph":     `<p>hello <strong>world</strong></p>`,
		"escaped script text": `<p>&lt;script&gt;alert(1)&lt;/script&gt;</p>`,
		"onload in text":      `<p>write onload= here</p>`,
		"normal link":         `<a href="/o/r/blob/main/README.md">readme</a>`,
		"https link":          `<a href="https://example.com/x">x</a>`,
		"anchor link":         `<a href="#L12">line 12</a>`,
		"img data url":        `<img src="data:image/png;base64,iVBORw0KGgo=" alt="qr">`,
		"img https":           `<img src="https://example.com/a.png" alt="a">`,
		"details disclosure":  `<details><summary>diff</summary><table><tr><td>x</td></tr></table></details>`,
		"form post":           `<form method="post" action="/logout"><button type="submit">out</button></form>`,
		"meta refresh":        `<meta http-equiv="refresh" content="15">`,
		"bunny fonts link":    `<link rel="stylesheet" href="https://fonts.bunny.net/css?family=x" referrerpolicy="no-referrer">`,
		"table with align":    `<table><tr><td align="right">1</td></tr></table>`,
		"data attribute":      `<span data-line="12">x</span>`,
		"colon attr":          `<svg aria-hidden="true"><use href="#i-check"></use></svg>`,
		"onion word":          `<p>onion popover onset</p>`,
		"empty":               ``,
		"text only":           `nothing but text &amp; entities`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if err := Check([]byte(src)); err != nil {
				t.Fatalf("Check(%q) = %v, want acceptance", src, err)
			}
		})
	}
	if !errors.Is(Check([]byte(`<script>`)), ErrScriptTag) {
		t.Fatal("script rejection does not wrap ErrScriptTag")
	}
}
