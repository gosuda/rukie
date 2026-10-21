package main

import (
	"net/http"
	"testing"

	"github.com/gosuda/rukie/internal/config"
	"github.com/gosuda/rukie/internal/op"
)

func TestDevServesFullHandler(t *testing.T) {
	full := http.NewServeMux()
	a := &app{server: &op.Server{}, handler: full}
	got := devHTTPServer(a, config.Config{})
	if got.Handler != http.Handler(full) {
		t.Fatalf("dev serves %T, want the full app handler", got.Handler)
	}
	if got.Handler == http.Handler(a.server) {
		t.Fatalf("dev serves the bare op server without git and raw routes")
	}
}
