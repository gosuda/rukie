package view

import (
	"io"
	"net/http"
	"testing"

	"github.com/gosuda/rukie/internal/web/vm"
)

type principalShim struct {
	Name   string
	Scopes []string
}

type specShim struct {
	Out string
}

type viewCtxShim struct {
	Spec      specShim
	RequestID string
	Path      string
	CSRF      string
	Principal principalShim
}

type pageFuncShim func(w io.Writer, c viewCtxShim, out any) error

func adaptAPIIndex(w io.Writer, c viewCtxShim, out any) error {
	idx, ok := out.(vm.APIIndex)
	if !ok {
		idx = sampleOps()
	}
	return WriteAPIIndex(http.Header{}, w, PageCtx{
		RequestID: c.RequestID,
		Path:      c.Path,
		CSRF:      c.CSRF,
		Build:     "shim",
	}, idx)
}

func TestShimBindingShape(t *testing.T) {
	var fn pageFuncShim = adaptAPIIndex
	c := viewCtxShim{
		Spec:      specShim{Out: "APIIndex"},
		Principal: principalShim{Name: "ada"},
		RequestID: "req-shim-01",
		Path:      "/-/api",
		CSRF:      "csrf-shim",
	}
	if err := fn(io.Discard, c, sampleOps()); err != nil {
		t.Fatalf("shim page func = %v", err)
	}
	if err := fn(io.Discard, c, nil); err != nil {
		t.Fatalf("shim page func with nil out = %v", err)
	}
}
