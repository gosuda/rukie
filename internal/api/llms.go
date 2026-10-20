package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/gosuda/rukie/internal/op"
)

type LLMSIn struct{}

type LLMSOut struct {
	Text string `json:"text"`
}

// LLMSIndex renders the short agent start-here file. It names the sign-in,
// token, MCP, CLI, and negotiation path plus every operation in specs. base
// is the instance origin, such as "http://127.0.0.1:3000". An empty base
// yields origin-relative links.
func LLMSIndex(specs []op.Spec, base string) string {
	prefix := strings.TrimSuffix(base, "/")
	var b strings.Builder
	b.WriteString("# rukie\n\n")
	b.WriteString("> rukie is a forge for agents and humans. Repositories live in Ruk. ")
	b.WriteString("One operation table serves HTML, JSON, Markdown, MCP tools, and CLI commands.\n\n")
	b.WriteString("## Start here\n\n")
	b.WriteString("1. Sign in: `rukie auth login --server " + prefix + " --username <name> --session-file <file>`.\n")
	b.WriteString("2. Mint a token: `rukie auth token create --scopes repo:read,repo:write --json`. ")
	b.WriteString("Tokens look like `rk_...`. Send yours as `Authorization: Bearer <token>`.\n")
	b.WriteString("3. Drive agents over MCP: `POST " + prefix + "/-/mcp` with the bearer token.\n")
	b.WriteString("4. Drive ops over HTTP: `METHOD " + prefix + "<path>` with `Accept: application/json`. ")
	b.WriteString("Ask for `Accept: text/markdown` for the Markdown view. Browsers get HTML.\n")
	b.WriteString("5. Drive ops over the CLI: `rukie <domain> <verb> --json`.\n")
	b.WriteString("\n## Operations\n\n")
	b.WriteString("| Operation | Method | Path | Summary |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	for _, s := range specs {
		b.WriteString("| `" + s.Name + "` | `" + s.Method + "` | `" + s.Path + "` | " + llmsCell(s.Summary) + " |\n")
	}
	b.WriteString("\n## Reference\n\n")
	b.WriteString("- Full per-op reference: " + prefix + "/llms-full.txt\n")
	b.WriteString("- Human-readable reference: " + prefix + "/-/api\n")
	b.WriteString("- OpenAPI 3.1: " + prefix + "/-/api/openapi.json\n")
	return b.String()
}

// LLMSFull renders the full per-operation reference. Each section names the
// route, scope, CLI and MCP spellings, inputs, and errors. A nil doc yields
// sections without input rows.
func LLMSFull(specs []op.Spec, doc *op.Document, base string) string {
	prefix := strings.TrimSuffix(base, "/")
	var b strings.Builder
	b.WriteString("# rukie operation reference\n\n")
	b.WriteString("> Every operation in the table. This file is generated. Index: " + prefix + "/llms.txt.\n")
	for _, s := range specs {
		b.WriteString("\n## `" + s.Name + "`\n\n")
		b.WriteString(s.Summary + "\n")
		if strings.TrimSpace(s.Description) != "" {
			b.WriteString("\n" + strings.TrimSpace(s.Description) + "\n")
		}
		b.WriteString("\n- Method and path: `" + s.Method + " " + s.Path + "`\n")
		b.WriteString("- Scope: `" + s.Scope.String() + "`\n")
		b.WriteString("- Kind: `" + s.Kind.String() + "`\n")
		b.WriteString("- CLI: `" + llmsCLI(s) + "`\n")
		b.WriteString("- MCP: `" + op.ToolName(s.Name) + "`\n")
		if s.Kind == op.KindMutation {
			b.WriteString("- Idempotency-Key: required on this mutation\n")
		}
		if s.HasETag {
			b.WriteString("- ETag: served; send it back as If-Match to write safely\n")
		}
		if rows := llmsInputs(s, doc); len(rows) > 0 {
			b.WriteString("\n### Inputs\n\n")
			b.WriteString("| Name | In | Required | Type |\n")
			b.WriteString("| --- | --- | --- | --- |\n")
			for _, r := range rows {
				b.WriteString("| `" + r[0] + "` | " + r[1] + " | " + r[2] + " | `" + r[3] + "` |\n")
			}
		}
		b.WriteString("\n### Errors\n\n")
		slugs := make([]string, 0, len(s.Problems))
		for _, c := range s.Problems {
			slugs = append(slugs, "`"+c.Slug()+"`")
		}
		slices.Sort(slugs)
		line := "Framework problems only."
		if len(slugs) > 0 {
			line = strings.Join(slugs, ", ") + ", plus the framework problems."
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func llmsCell(s string) string {
	out := strings.ReplaceAll(s, "|", "\\|")
	return strings.ReplaceAll(out, "\n", " ")
}

func llmsCLI(s op.Spec) string {
	path := strings.Split(s.Name, ".")
	if len(s.CLI.Path) > 0 {
		path = s.CLI.Path
	}
	return "rukie " + strings.Join(path, " ")
}

func llmsInputs(s op.Spec, doc *op.Document) [][4]string {
	if doc == nil {
		return nil
	}
	os, ok := doc.Operations[s.Name]
	if !ok {
		return nil
	}
	var rows [][4]string
	for _, p := range os.Params {
		required := "no"
		if p.Required {
			required = "yes"
		}
		rows = append(rows, [4]string{p.Name, p.In, required, llmsType(p.Schema)})
	}
	props, required := llmsBody(doc, os.Body)
	for _, name := range slices.Sorted(maps.Keys(props)) {
		need := "no"
		if required[name] {
			need = "yes"
		}
		pm, _ := props[name].(map[string]any)
		rows = append(rows, [4]string{name, "body", need, llmsType(pm)})
	}
	slices.SortFunc(rows, func(a, b [4]string) int {
		if c := strings.Compare(a[0], b[0]); c != 0 {
			return c
		}
		return strings.Compare(a[1], b[1])
	})
	return rows
}

func llmsBody(doc *op.Document, body op.Schema) (map[string]any, map[string]bool) {
	if ref, ok := body["$ref"].(string); ok {
		if name, found := strings.CutPrefix(ref, "#/components/schemas/"); found {
			if resolved, ok := doc.Components[name]; ok {
				body = resolved
			}
		}
	}
	props, _ := body["properties"].(map[string]any)
	required := map[string]bool{}
	if req, ok := body["required"].([]any); ok {
		for _, r := range req {
			if name, ok := r.(string); ok {
				required[name] = true
			}
		}
	}
	return props, required
}

func llmsType(s op.Schema) string {
	if len(s) == 0 {
		return "value"
	}
	if typ, ok := s["type"].(string); ok && typ != "" {
		return typ
	}
	if ref, ok := s["$ref"].(string); ok && ref != "" {
		if _, name, found := strings.CutLast(ref, "/"); found && name != "" {
			return name
		}
		return ref
	}
	return "value"
}

func llmsETag(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func llmsTextOf(out any) (string, bool) {
	switch v := out.(type) {
	case LLMSOut:
		return v.Text, true
	case map[string]any:
		text, ok := v["text"].(string)
		return text, ok
	default:
		return "", false
	}
}

// RegisterLLMS adds the agent reference ops to the table. base is the
// instance origin printed into the files. The index handler reads the table
// on each request. The full handler also needs the mounted document, which
// only exists after Mount, so RegisterLLMS returns a fill function the owner
// calls once with server.Document(). A nil document yields sections without
// input rows.
func RegisterLLMS(t *op.Table, base string) (func(*op.Document), error) {
	var doc *op.Document
	index := op.Op[LLMSIn, LLMSOut]{
		Name:        "api.llms.get",
		Kind:        op.KindRead,
		Method:      "GET",
		Path:        "/llms.txt",
		Scope:       op.ScopePublicRead,
		Summary:     "Serve the agent start-here file",
		Description: "Short agent index: sign-in, tokens, MCP, CLI, and every operation with its route.",
		ETag:        func(out LLMSOut) string { return llmsETag(out.Text) },
		Handler: func(_ context.Context, _ op.Principal, _ LLMSIn) (LLMSOut, error) {
			return LLMSOut{Text: LLMSIndex(t.Specs(), base)}, nil
		},
	}
	if err := op.Register(t, index); err != nil {
		return nil, fmt.Errorf("RegisterLLMS: %w", err)
	}
	full := op.Op[LLMSIn, LLMSOut]{
		Name:        "api.llmsfull.get",
		Kind:        op.KindRead,
		Method:      "GET",
		Path:        "/llms-full.txt",
		Scope:       op.ScopePublicRead,
		Summary:     "Serve the full per-operation reference",
		Description: "Every operation with its route, scope, CLI and MCP names, inputs, and errors.",
		ETag:        func(out LLMSOut) string { return llmsETag(out.Text) },
		Handler: func(_ context.Context, _ op.Principal, _ LLMSIn) (LLMSOut, error) {
			return LLMSOut{Text: LLMSFull(t.Specs(), doc, base)}, nil
		},
	}
	if err := op.Register(t, full); err != nil {
		return nil, fmt.Errorf("RegisterLLMS: %w", err)
	}
	return func(d *op.Document) { doc = d }, nil
}

// RegisterLLMSViews binds the Markdown and HTML projections of the agent
// reference ops. Markdown carries the raw file text. HTML wraps it in a
// zero-JS pre block.
func RegisterLLMSViews(v *op.Views) {
	for _, name := range []string{"api.llms.get", "api.llmsfull.get"} {
		title := "llms.txt"
		if name == "api.llmsfull.get" {
			title = "llms-full.txt"
		}
		v.Markdown(name, func(w io.Writer, _ op.ViewCtx, out any) error {
			text, ok := llmsTextOf(out)
			if !ok {
				return fmt.Errorf("llms markdown view: unexpected output type")
			}
			_, err := io.WriteString(w, text)
			return err
		})
		op.PageView(v, name, llmsPage(title))
	}
}

func llmsPage(title string) func(io.Writer, op.ViewCtx, LLMSOut) error {
	return func(w io.Writer, _ op.ViewCtx, out LLMSOut) error {
		body := "<h1>" + html.EscapeString(title) + "</h1>\n<pre>" + html.EscapeString(out.Text) + "</pre>\n"
		_, err := io.WriteString(w, pageShell(title, body))
		return err
	}
}
