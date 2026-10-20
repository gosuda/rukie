package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gosuda/rukie/internal/op"
)

var ReservedPaths = [][]string{
	{"serve"},
	{"runner"},
	{"job-exec"},
	{"bootstrap"},
	{"dev"},
	{"backup"},
	{"restore"},
	{"mcp"},
	{"admin", "secret-gen"},
	{"admin", "ssh-keygen"},
	{"admin", "migrate"},
	{"admin", "status"},
	{"admin", "reconcile"},
	{"admin", "gc"},
	{"admin", "repo", "import"},
}

var StubSteps = map[string]string{
	"runner":            "F6",
	"job-exec":          "F6",
	"dev":               "K9",
	"backup":            "K10",
	"restore":           "K10",
	"admin secret-gen":  "K10",
	"admin ssh-keygen":  "K10",
	"admin migrate":     "K10",
	"admin status":      "K10",
	"admin reconcile":   "K10",
	"admin gc":          "K10",
	"admin repo import": "F14",
}

func CheckReserved(root *op.Command) error {
	for _, path := range ReservedPaths {
		node := root
		found := true
		for _, segment := range path {
			next, ok := node.Children[segment]
			if !ok {
				found = false
				break
			}
			node = next
		}
		if found {
			return fmt.Errorf("cli: hand-written command %q collides with the derived tree", strings.Join(path, " "))
		}
	}
	return nil
}

func StubFor(argv []string) (string, bool) {
	for _, path := range ReservedPaths {
		if len(argv) < len(path) {
			continue
		}
		match := true
		for i, segment := range path {
			if argv[i] != segment {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		key := strings.Join(path, " ")
		if key == "serve" || key == "bootstrap" || key == "mcp" {
			return "", false
		}
		step, ok := StubSteps[key]
		if !ok {
			return "", false
		}
		extra := ""
		if len(argv) > len(path) {
			extra = " " + strings.Join(argv[len(path):], " ")
		}
		return fmt.Sprintf("rukie %s%s: lands in %s (not implemented yet)", key, extra, step), true
	}
	return "", false
}

type IO struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
}

func (io IO) getenv(key string) string {
	if io.Getenv != nil {
		return io.Getenv(key)
	}
	return os.Getenv(key)
}

func DefaultSessionFile(getenv func(string) string) string {
	get := getenv
	if get == nil {
		get = os.Getenv
	}
	if dir := get("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "rukie", "session")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".rukie", "session")
	}
	return filepath.Join(home, ".local", "state", "rukie", "session")
}

func emit(w io.Writer, args ...any) bool {
	_, err := fmt.Fprintln(w, args...)
	return err == nil
}

func emitf(w io.Writer, format string, args ...any) bool {
	_, err := fmt.Fprintf(w, format, args...)
	return err == nil
}

func Run(ctx context.Context, std IO, argv []string) int {
	if len(argv) == 0 {
		emit(std.Stderr, "usage: rukie <command> [flags]")
		return 64
	}
	if msg, ok := StubFor(argv); ok {
		emit(std.Stdout, msg)
		return 1
	}
	server, err := buildRemote()
	if err != nil {
		emit(std.Stderr, "rukie:", err.Error())
		return 1
	}
	root, err := op.BuildCommands(server.Specs(), server.Document())
	if err != nil {
		emit(std.Stderr, "rukie:", err.Error())
		return 1
	}
	if cerr := CheckReserved(root); cerr != nil {
		emit(std.Stderr, "rukie:", cerr.Error())
		return 1
	}
	if len(argv) >= 2 && argv[0] == "auth" && argv[1] == "login" {
		return runLogin(ctx, std, argv[2:])
	}
	cmdArgs, wrapper, werr := splitWrapperArgs(root, argv)
	if werr != nil {
		emit(std.Stderr, "rukie:", werr.Error())
		return 64
	}
	inv, err := op.Parse(root, cmdArgs)
	if err != nil {
		emit(std.Stderr, "rukie:", err.Error())
		return 64
	}
	return dispatch(ctx, std, server, inv, wrapper)
}

type wrapperOpts struct {
	sessionFile string
	config      string
	dryRun      bool
}

func splitWrapperArgs(root *op.Command, argv []string) ([]string, wrapperOpts, error) {
	var w wrapperOpts
	node := root
	i := 0
	for i < len(argv) && !strings.HasPrefix(argv[i], "-") {
		next, ok := node.Children[argv[i]]
		if !ok {
			break
		}
		node = next
		i++
		if node.Spec != nil {
			break
		}
	}
	head := argv[:i]
	var tail []string
	for j := i; j < len(argv); j++ {
		arg := argv[j]
		if name, value, ok := cutFlag(arg); ok {
			switch name {
			case "session-file", "config":
				got, ok := flagValue(value, argv, &j)
				if !ok {
					return nil, w, op.ErrUsage("flag --%s needs an argument", name)
				}
				if name == "session-file" {
					w.sessionFile = got
				} else {
					w.config = got
				}
				continue
			case "dry-run":
				w.dryRun = flagBool(value)
				continue
			}
		}
		tail = append(tail, arg)
	}
	if node.Spec != nil {
		tail = orderFlagsFirst(node, tail)
	}
	return append(head, tail...), w, nil
}

func orderFlagsFirst(leaf *op.Command, tail []string) []string {
	valued := map[string]bool{
		"server": true, "token-file": true, "input": true, "if-match": true,
		"idempotency-key": true, "cursor": true, "timeout": true,
		"session-file": true, "config": true,
	}
	for _, f := range leaf.Flags {
		if f.Kind != op.FlagBool {
			valued[f.Name] = true
		}
	}
	var flags, positionals []string
	i := 0
	for i < len(tail) {
		arg := tail[i]
		i++
		if arg == "--" {
			positionals = append(positionals, tail[i:]...)
			break
		}
		name, inline, isFlag := cutFlag(arg)
		if !isFlag {
			positionals = append(positionals, arg)
			continue
		}
		if inline != "" || !valued[name] {
			flags = append(flags, arg)
			continue
		}
		flags = append(flags, arg)
		if i < len(tail) {
			flags = append(flags, tail[i])
			i++
		}
	}
	return append(flags, positionals...)
}

func cutFlag(arg string) (string, string, bool) {
	if !strings.HasPrefix(arg, "--") {
		return "", "", false
	}
	name, value, _ := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
	return name, value, true
}

func flagValue(inline string, argv []string, j *int) (string, bool) {
	if inline != "" {
		return inline, true
	}
	if *j+1 < len(argv) {
		*j++
		return argv[*j], true
	}
	return "", false
}

func flagBool(inline string) bool {
	if inline == "" {
		return true
	}
	return inline != "false" && inline != "0"
}
