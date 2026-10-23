package mcp

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
)

type Mount struct {
	server  *op.Server
	handler http.Handler
	base    string
}

func NewMount(svc *auth.Service, server *op.Server, base string) (*Mount, error) {
	m := &Mount{server: server, base: base}
	if err := CheckPlannedList(); err != nil {
		return nil, err
	}
	full := op.Principal{Kind: op.PrincipalHuman, Grants: fullGrants()}
	if _, err := NewServer(server.Specs(), server.Document(), nilInvoker{}, op.ToolFilter{Principal: full}, ""); err != nil {
		return nil, err
	}
	streamable := msdk.NewStreamableHTTPHandler(m.getServer, &msdk.StreamableHTTPOptions{Stateless: true})
	m.handler = BearerMiddleware(svc)(streamable)
	return m, nil
}

type nilInvoker struct{}

func (nilInvoker) Invoke(_ context.Context, _ *http.Request) (*op.Response, error) {
	return &op.Response{Status: http.StatusServiceUnavailable}, nil
}

func fullGrants() op.Scopes {
	var out op.Scopes
	for _, s := range op.AllScopes() {
		out = out.Add(s)
	}
	return out
}

func (m *Mount) Authenticated() http.Handler {
	return m.handler
}

func (m *Mount) MethodNotAllowed() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
}

func (m *Mount) getServer(r *http.Request) *msdk.Server {
	p := PrincipalOf(r)
	var toolsets []string
	if raw := r.URL.Query().Get("toolsets"); raw != "" {
		for part := range strings.SplitSeq(raw, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				toolsets = append(toolsets, trimmed)
			}
		}
	}
	inv := &op.LocalInvoker{Server: m.server, Principal: p}
	srv, err := NewServer(m.server.Specs(), m.server.Document(), inv, op.ToolFilter{Principal: p, Toolsets: toolsets}, m.base)
	if err != nil {
		slog.Error("mcp build server", "err", err)
		return msdk.NewServer(&msdk.Implementation{Name: "rukie", Title: "rukie", Version: "0.0.0"}, nil)
	}
	return srv
}
