package trusted

import (
	"errors"
	"testing"

	"github.com/gosuda/rukie/internal/web/htmlcheck"
)

func TestRawConfined(t *testing.T) {
	ok, err := Raw(`<p>plain <a href="/x">link</a></p>`)
	if err != nil {
		t.Fatalf("Raw accepted markup: %v", err)
	}
	if ok.String() != `<p>plain <a href="/x">link</a></p>` {
		t.Fatalf("Raw altered markup: %q", ok.String())
	}
	confined := map[string]string{
		"script":        `<script>alert(1)</script>`,
		"event handler": `<img src="/x.png" onerror="alert(1)">`,
		"style":         `<p style="x:y">hi</p>`,
		"js url":        `<a href="javascript:alert(1)">x</a>`,
		"data on link":  `<a href="data:text/html,hi">x</a>`,
	}
	for name, src := range confined {
		t.Run(name, func(t *testing.T) {
			if _, inErr := Raw(src); inErr == nil {
				t.Fatalf("Raw(%q) accepted, want confinement error", src)
			}
		})
	}
	if _, scriptErr := Raw(`<script>`); !errors.Is(scriptErr, htmlcheck.ErrScriptTag) {
		t.Fatalf("Raw script error = %v, want htmlcheck sentinel", scriptErr)
	}
	md, err := Markdown(`<p><em>hi</em></p>`)
	if err != nil {
		t.Fatalf("Markdown accepted markup: %v", err)
	}
	if md.String() == "" {
		t.Fatal("Markdown returned empty HTML")
	}
	if _, err := Markdown(`<script>alert(1)</script>`); err == nil {
		t.Fatal("Markdown(script) accepted, want fail-closed error")
	}
}
