package cli

import (
	"cmp"
	"context"
	"flag"
	"net/http"
	"net/url"
	"strings"
	"time"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpsdk "github.com/gosuda/rukie/internal/mcp"
	"github.com/gosuda/rukie/internal/op"
)

func RunMCP(ctx context.Context, std IO, argv []string) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(std.Stderr)
	serverFlag := fs.String("server", "", "server base URL")
	tokenEnv := fs.String("token-env", "RUKIE_TOKEN", "environment variable holding the bearer token")
	toolsetsFlag := fs.String("toolsets", "", "comma-separated op domains to expose")
	readOnly := fs.Bool("read-only", false, "expose read ops only")
	if err := fs.Parse(argv); err != nil {
		return 64
	}
	if rest := fs.Args(); len(rest) > 0 {
		emit(std.Stderr, "rukie mcp: too many positional arguments")
		return 64
	}
	serverURL := cmp.Or(*serverFlag, std.getenv("RUKIE_SERVER"))
	if serverURL == "" {
		emit(std.Stderr, "rukie mcp: --server is required")
		return 64
	}
	token := strings.TrimSpace(std.getenv(*tokenEnv))
	if token == "" {
		emitf(std.Stderr, "rukie mcp: environment variable %s is empty\n", *tokenEnv)
		return 64
	}
	base, err := url.Parse(strings.TrimSuffix(serverURL, "/"))
	if err != nil {
		emit(std.Stderr, "rukie mcp: bad --server URL")
		return 64
	}
	server, err := buildRemote()
	if err != nil {
		emit(std.Stderr, "rukie mcp:", err.Error())
		return 1
	}
	var toolsets []string
	if *toolsetsFlag != "" {
		for part := range strings.SplitSeq(*toolsetsFlag, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				toolsets = append(toolsets, trimmed)
			}
		}
	}
	invoker := &RemoteInvoker{
		Base: base, Token: token,
		Client: &http.Client{Timeout: 60 * time.Second},
	}
	filter := op.ToolFilter{Principal: fullGrantsPrincipal(), Toolsets: toolsets, ReadOnly: *readOnly}
	srv, err := mcpsdk.NewServer(server.Specs(), server.Document(), invoker, filter, strings.TrimSuffix(serverURL, "/"))
	if err != nil {
		emit(std.Stderr, "rukie mcp:", err.Error())
		return 1
	}
	if err := srv.Run(ctx, &msdk.StdioTransport{}); err != nil {
		emit(std.Stderr, "rukie mcp:", err.Error())
		return 1
	}
	return 0
}

func fullGrantsPrincipal() op.Principal {
	var grants op.Scopes
	for _, s := range op.AllScopes() {
		grants = grants.Add(s)
	}
	return op.Principal{Kind: op.PrincipalHuman, Grants: grants}
}
