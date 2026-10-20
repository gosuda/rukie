package cli_test

import (
	"strings"
	"testing"

	"github.com/gosuda/rukie/internal/cli"
	"github.com/gosuda/rukie/internal/op"
)

func treeOf(paths ...[]string) *op.Command {
	root := &op.Command{Children: map[string]*op.Command{}}
	for _, path := range paths {
		node := root
		for _, segment := range path {
			next, ok := node.Children[segment]
			if !ok {
				next = &op.Command{Name: segment, Children: map[string]*op.Command{}}
				node.Children[segment] = next
			}
			node = next
		}
	}
	return root
}

func TestHandWrittenNamesDoNotCollide(t *testing.T) {
	server, _, _ := buildCLIServer(t)
	root, err := op.BuildCommands(server.Specs(), server.Document())
	if err != nil {
		t.Fatalf("commands: %v", err)
	}
	if err := cli.CheckReserved(root); err != nil {
		t.Fatalf("reserved names collide with the landed tree: %v", err)
	}
	for _, path := range cli.ReservedPaths {
		tree := treeOf(path)
		if err := cli.CheckReserved(tree); err == nil {
			t.Fatalf("reserved path %q must be rejected", strings.Join(path, " "))
		} else if !strings.Contains(err.Error(), strings.Join(path, " ")) {
			t.Fatalf("err = %v, want the colliding path", err)
		}
	}
	near := treeOf([]string{"admin"}, []string{"admin", "user"}, []string{"admin", "user", "list"})
	if err := cli.CheckReserved(near); err != nil {
		t.Fatalf("a derived admin subtree without reserved leaves must pass: %v", err)
	}
	prefix := treeOf([]string{"admin", "status", "extra"})
	if err := cli.CheckReserved(prefix); err == nil {
		t.Fatalf("a derived group below a reserved leaf must collide")
	}
	cousin := treeOf([]string{"admin", "statuesque"})
	if err := cli.CheckReserved(cousin); err != nil {
		t.Fatalf("a shared string prefix without a shared path must pass: %v", err)
	}
}

func TestCLIWrapperFlagNeedsValue(t *testing.T) {
	for _, argv := range [][]string{
		{"api", "index", "--session-file"},
		{"api", "index", "--config"},
	} {
		c := newIO()
		if code := runCLI(t, argv, c); code != 64 {
			t.Fatalf("%v exit %d, want 64", argv, code)
		}
		if !strings.Contains(c.stderr.String(), "needs an argument") {
			t.Fatalf("%v stderr = %q, want the missing-argument report", argv, c.stderr.String())
		}
	}
}

func TestMCPStdioArgs(t *testing.T) {
	c := newIO()
	if code := cli.RunMCP(t.Context(), c.io(), []string{}); code != 64 {
		t.Fatalf("missing server exit %d, want 64", code)
	}
	noToken := newIO()
	noToken.env["RUKIE_SERVER"] = "http://127.0.0.1:9"
	if code := cli.RunMCP(t.Context(), noToken.io(), []string{}); code != 64 {
		t.Fatalf("missing token exit %d, want 64", code)
	}
	badURL := newIO()
	badURL.env["RUKIE_SERVER"] = "://bad"
	badURL.env["RUKIE_TOKEN"] = "rk_x"
	if code := cli.RunMCP(t.Context(), badURL.io(), []string{}); code != 64 {
		t.Fatalf("bad url exit %d, want 64", code)
	}
}

func TestCLIStubsNameTheirStep(t *testing.T) {
	for path, step := range map[string]string{
		"dev": "K9", "runner": "F6", "job-exec": "F6", "backup": "K10",
		"admin status": "K10", "admin repo import": "F14",
	} {
		msg, ok := cli.StubFor(strings.Split(path, " "))
		if !ok {
			t.Fatalf("%q must be a stub", path)
		}
		if !strings.Contains(msg, step) {
			t.Fatalf("stub %q = %q, want step %s", path, msg, step)
		}
	}
	for _, live := range [][]string{{"serve"}, {"bootstrap"}, {"mcp"}, {"auth", "login"}, {"api", "index"}} {
		if _, ok := cli.StubFor(live); ok {
			t.Fatalf("%v must not be a stub", live)
		}
	}
	c := newIO()
	if code := runCLI(t, []string{"dev"}, c); code == 0 {
		t.Fatalf("stub must exit non-zero")
	}
	if !strings.Contains(c.stdout.String(), "K9") {
		t.Fatalf("stub output = %q, want the owning step", c.stdout.String())
	}
}
