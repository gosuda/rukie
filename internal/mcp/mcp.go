package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	authsdk "github.com/modelcontextprotocol/go-sdk/auth"
	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
)

var Domains = []string{
	"auth", "user", "org", "team", "repo", "code", "branch", "tag",
	"search", "release", "webhook", "mirror", "star", "explore", "inbox", "admin",
	"change", "intent", "ask", "view", "land", "proof", "ci",
}

var DefaultToolNames = []string{
	"code.rev.resolve",
	"code.tree.get",
	"code.blob.get",
	"code.blame.get",
	"code.log.list",
	"code.commit.get",
	"code.diff.get",
	"code.compare.get",
	"branch.list",
	"branch.get",
	"repo.get",
	"change.list",
	"change.get",
	"change.diff.get",
	"change.stack.list",
	"change.comment.list",
	"change.comment.create",
	"change.review.create",
	"branch.change.list",
	"intent.list",
	"intent.get",
	"intent.create",
	"intent.update",
	"intent.state.set",
	"intent.lease.create",
	"intent.lease.update",
	"intent.lease.delete",
	"intent.change.add",
	"ask.create",
	"ask.list",
	"view.list",
	"view.result.list",
	"land.plan.create",
	"land.run.create",
	"proof.list",
	"search.code",
}

const MaxDefaultTools = 36

func defaultSet() map[string]bool {
	set := make(map[string]bool, len(DefaultToolNames))
	for _, name := range DefaultToolNames {
		set[name] = true
	}
	return set
}

func CheckBudget(names []string) error {
	if len(names) > MaxDefaultTools {
		return fmt.Errorf("mcp: default toolset holds %d tools, at most %d", len(names), MaxDefaultTools)
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			return fmt.Errorf("mcp: duplicate default tool %q", name)
		}
		seen[name] = true
	}
	return nil
}

func CheckPlannedList() error {
	if len(DefaultToolNames) != MaxDefaultTools {
		return fmt.Errorf("mcp: planned default list holds %d tools, want %d", len(DefaultToolNames), MaxDefaultTools)
	}
	return CheckBudget(DefaultToolNames)
}

func DefaultSpecs(specs []op.Spec) []op.Spec {
	set := defaultSet()
	out := make([]op.Spec, 0, len(specs))
	for _, s := range specs {
		if set[s.Name] {
			out = append(out, s)
		}
	}
	return out
}

func VerifyToken(svc *auth.Service) authsdk.TokenVerifier {
	auther := auth.NewAuthenticator(svc)
	return func(ctx context.Context, token string, r *http.Request) (*authsdk.TokenInfo, error) {
		p, err := auther.Bearer(ctx, token)
		if err != nil {
			return nil, fmt.Errorf("mcp: bearer verification failed: %w", authsdk.ErrInvalidToken)
		}
		tok, _, err := svc.LookupToken(ctx, token, clientIP(r))
		if err != nil {
			return nil, fmt.Errorf("mcp: bearer verification failed: %w", authsdk.ErrInvalidToken)
		}
		if tok.ExpiresAt <= 0 {
			return nil, fmt.Errorf("mcp: bearer verification failed: %w", authsdk.ErrInvalidToken)
		}
		return &authsdk.TokenInfo{
			Scopes:     auth.ScopeNames(p.Grants),
			Expiration: time.UnixMilli(tok.ExpiresAt),
			UserID:     strconv.FormatInt(p.UserID, 10),
			Extra:      map[string]any{"principal": p},
		}, nil
	}
}

func clientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func BearerMiddleware(svc *auth.Service) func(http.Handler) http.Handler {
	return authsdk.RequireBearerToken(VerifyToken(svc), &authsdk.RequireBearerTokenOptions{
		Scopes: []string{op.ScopePublicRead.String()},
	})
}

func PrincipalOf(r *http.Request) op.Principal {
	if ti := authsdk.TokenInfoFromContext(r.Context()); ti != nil {
		if p, ok := ti.Extra["principal"].(op.Principal); ok {
			return p
		}
	}
	return op.AnonymousPrincipal()
}

func ToolResult(s *op.Spec, resp *op.Response) *msdk.CallToolResult {
	if resp == nil {
		return &msdk.CallToolResult{
			IsError: true,
			Content: []msdk.Content{&msdk.TextContent{Text: `{"code":"internal","detail":"empty response"}`}},
		}
	}
	if resp.Status >= 200 && resp.Status < 300 {
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/octet-stream") {
			size := int64(len(resp.Body))
			return &msdk.CallToolResult{
				Content: []msdk.Content{&msdk.ResourceLink{
					URI:      "rukie://op/" + s.Name,
					Name:     op.ToolName(s.Name),
					Title:    s.Summary,
					MIMEType: "application/octet-stream",
					Size:     &size,
				}},
			}
		}
		return okResult(resp.Body)
	}
	var problem any
	if err := jsonv2.Unmarshal(resp.Body, &problem, jsonv1.DefaultOptionsV1()); err != nil || problem == nil {
		problem = map[string]any{"code": "internal", "detail": "unreadable error body", "status": resp.Status}
	}
	text, err := jsonv2.Marshal(problem, jsonv1.DefaultOptionsV1())
	if err != nil {
		text = resp.Body
	}
	return &msdk.CallToolResult{
		IsError:           true,
		StructuredContent: problem,
		Content:           []msdk.Content{&msdk.TextContent{Text: string(text)}},
	}
}

func okResult(body []byte) *msdk.CallToolResult {
	raw := body
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	var structured any
	if err := jsonv2.Unmarshal(raw, &structured, jsonv1.DefaultOptionsV1()); err != nil {
		structured = map[string]any{"raw": string(body)}
	}
	text, err := jsonv2.Marshal(structured, jsonv1.DefaultOptionsV1())
	if err != nil {
		text = raw
	}
	return &msdk.CallToolResult{
		StructuredContent: structured,
		Content:           []msdk.Content{&msdk.TextContent{Text: string(text)}},
	}
}

func callHandler(s *op.Spec, d *op.Document, inv op.Invoker, base string) msdk.ToolHandler {
	return func(ctx context.Context, req *msdk.CallToolRequest) (*msdk.CallToolResult, error) {
		args := op.Args{}
		if len(req.Params.Arguments) > 0 {
			var decoded map[string]any
			if err := jsonv2.Unmarshal(req.Params.Arguments, &decoded, jsonv1.DefaultOptionsV1()); err != nil {
				return nil, fmt.Errorf("mcp: tool %q: malformed arguments: %w", op.ToolName(s.Name), err)
			}
			args = op.Args(decoded)
		}
		httpReq, err := args.ToRequest(s, d, mustParseBase(base))
		if err != nil {
			return nil, fmt.Errorf("mcp: tool %q: %w", op.ToolName(s.Name), err)
		}
		httpReq.Header.Set("Accept", "application/json")
		resp, err := inv.Invoke(ctx, httpReq)
		if err != nil {
			return nil, err
		}
		return ToolResult(s, resp), nil
	}
}

func mustParseBase(base string) *url.URL {
	if base == "" {
		return &url.URL{Scheme: "http", Host: "internal"}
	}
	if u, err := url.Parse(base); err == nil {
		return u
	}
	return &url.URL{Scheme: "http", Host: "internal"}
}

func NewServer(specs []op.Spec, d *op.Document, inv op.Invoker, filter op.ToolFilter, base string) (*msdk.Server, error) {
	if err := CheckPlannedList(); err != nil {
		return nil, err
	}
	effective := specs
	if len(filter.Toolsets) == 0 {
		effective = DefaultSpecs(specs)
	}
	seen := map[string]string{}
	for i := range effective {
		name := op.ToolName(effective[i].Name)
		if prev, dup := seen[name]; dup {
			return nil, fmt.Errorf("mcp: tool name %q collides (%s, %s)", name, prev, effective[i].Name)
		}
		seen[name] = effective[i].Name
	}
	tools, err := op.BuildTools(effective, d, filter)
	if err != nil {
		return nil, err
	}
	if len(filter.Toolsets) == 0 {
		names := make([]string, 0, len(tools))
		for _, t := range tools {
			names = append(names, t.Name)
		}
		if err := CheckBudget(names); err != nil {
			return nil, err
		}
	}
	byName := make(map[string]*op.Spec, len(tools))
	for i := range specs {
		byName[op.ToolName(specs[i].Name)] = &specs[i]
	}
	for _, t := range tools {
		if _, dup := byName[t.Name]; !dup {
			return nil, fmt.Errorf("mcp: tool %q has no op", t.Name)
		}
	}
	slices.SortFunc(tools, func(a, b *op.Tool) int { return strings.Compare(a.Name, b.Name) })
	srv := msdk.NewServer(&msdk.Implementation{Name: "rukie", Title: "rukie", Version: "0.0.0"}, nil)
	for _, t := range tools {
		srv.AddTool(sdkTool(t), callHandler(byName[t.Name], d, inv, base))
	}
	return srv, nil
}

func sdkTool(t *op.Tool) *msdk.Tool {
	destructive := t.Annotations.DestructiveHint
	openWorld := t.Annotations.OpenWorldHint
	return &msdk.Tool{
		Name:         t.Name,
		Title:        t.Title,
		Description:  t.Description,
		InputSchema:  map[string]any(t.InputSchema),
		OutputSchema: map[string]any(t.OutputSchema),
		Annotations: &msdk.ToolAnnotations{
			ReadOnlyHint:    t.Annotations.ReadOnlyHint,
			DestructiveHint: &destructive,
			IdempotentHint:  t.Annotations.IdempotentHint,
			OpenWorldHint:   &openWorld,
		},
	}
}
