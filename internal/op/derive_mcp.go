package op

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"
)

type Args map[string]any

func (a Args) scalar(key string) (string, bool) {
	v, ok := a[key]
	if !ok || v == nil {
		return "", false
	}
	switch t := v.(type) {
	case string:
		return t, true
	case bool:
		if t {
			return "true", true
		}
		return "false", true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	default:
		raw, err := jsonv2.Marshal(v, jsonv1.DefaultOptionsV1())
		if err != nil {
			return "", false
		}
		return string(raw), true
	}
}

func (a Args) ToRequest(s *Spec, d *Document, base *url.URL) (*http.Request, error) {
	os, ok := d.Operations[s.Name]
	if !ok {
		return nil, fmt.Errorf("op %q: missing from document", s.Name)
	}
	plain := s.Path
	escaped := s.Path
	for _, p := range os.Params {
		if p.In != "path" {
			continue
		}
		value, ok := a.scalar(p.Name)
		if !ok {
			return nil, fmt.Errorf("op %q: missing path parameter %q", s.Name, p.Name)
		}
		plain = strings.Replace(plain, "{"+p.Name+"}", value, 1)
		plain = strings.Replace(plain, "{"+p.Name+"...}", value, 1)
		enc := value
		if !strings.Contains(s.Path, "{"+p.Name+"...}") {
			enc = url.PathEscape(value)
		} else {
			parts := strings.Split(value, "/")
			for i := range parts {
				parts[i] = url.PathEscape(parts[i])
			}
			enc = strings.Join(parts, "/")
		}
		escaped = strings.Replace(escaped, "{"+p.Name+"}", enc, 1)
		escaped = strings.Replace(escaped, "{"+p.Name+"...}", enc, 1)
	}
	target := *base
	target.Path = singleJoinPath(base.Path, plain)
	target.RawPath = singleJoinPath(base.EscapedPath(), escaped)
	query := target.Query()
	for _, p := range os.Params {
		if p.In != "query" {
			continue
		}
		if value, ok := a.scalar(p.Name); ok {
			query.Set(p.Name, value)
		}
	}
	target.RawQuery = query.Encode()
	var body []byte
	if s.Kind == KindMutation {
		obj := map[string]any{}
		for name := range bodyPropertyNames(resolveRef(os.Body, d.Components)) {
			if v, ok := a[name]; ok {
				obj[name] = v
			}
		}
		raw, err := jsonv2.Marshal(obj, jsonv1.DefaultOptionsV1())
		if err != nil {
			return nil, err
		}
		body = raw
	}
	var reader *strings.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	} else {
		reader = strings.NewReader("")
	}
	req, err := http.NewRequest(s.Method, target.String(), reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if v, ok := a.scalar("if_match"); ok {
		req.Header.Set("If-Match", v)
	}
	if v, ok := a.scalar("idempotency_key"); ok {
		req.Header.Set("Idempotency-Key", v)
	}
	return req, nil
}

func singleJoinPath(base, path string) string {
	return strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(path, "/")
}

func bodyPropertyNames(body Schema) map[string]bool {
	out := map[string]bool{}
	if body == nil {
		return out
	}
	props, ok := body["properties"].(map[string]any)
	if !ok {
		return out
	}
	for name := range props {
		out[name] = true
	}
	return out
}

type Invoker interface {
	Invoke(ctx context.Context, r *http.Request) (*Response, error)
}

type LocalInvoker struct {
	Server    *Server
	Principal Principal
}

func (l *LocalInvoker) Invoke(ctx context.Context, r *http.Request) (*Response, error) {
	return l.Server.Invoke(WithTrusted(ctx, l.Principal, SurfaceMCP), r)
}

type ToolFilter struct {
	Toolsets  []string
	Principal Principal
	ReadOnly  bool
}

type ToolAnnotations struct {
	ReadOnlyHint    bool
	DestructiveHint bool
	IdempotentHint  bool
	OpenWorldHint   bool
}

type Tool struct {
	InputSchema  Schema
	OutputSchema Schema
	Name         string
	Title        string
	Description  string
	Annotations  ToolAnnotations
}

func ToolName(name string) string { return strings.ReplaceAll(name, ".", "_") }

func toolDomain(name string) string {
	domain, _, _ := strings.Cut(name, ".")
	return domain
}

func InputSchema(s *Spec, d *Document) (Schema, error) {
	os, ok := d.Operations[s.Name]
	if !ok {
		return nil, fmt.Errorf("op %q: missing from document", s.Name)
	}
	props := map[string]any{}
	required := []string{}
	for _, p := range os.Params {
		var name, inw string
		switch p.In {
		case "path":
			name, inw = p.Name, "path"
		case "query":
			name, inw = p.Name, "query"
		case "header":
			switch p.Name {
			case "If-Match":
				name, inw = "if_match", "header"
			case "Idempotency-Key":
				name, inw = "idempotency_key", "header"
			default:
				continue
			}
		default:
			continue
		}
		if _, dup := props[name]; dup {
			return nil, fmt.Errorf("op %q: argument name collision on %q", s.Name, name)
		}
		prop := map[string]any{}
		if p.Schema != nil {
			local, err := d.Localize(p.Schema)
			if err != nil {
				return nil, err
			}
			maps.Copy(prop, local)
		}
		prop["x-rukie-in"] = inw
		props[name] = prop
		if p.Required {
			required = append(required, name)
		}
	}
	body := resolveRef(os.Body, d.Components)
	if bodyProps, ok := body["properties"].(map[string]any); ok {
		bodyRequired := map[string]bool{}
		if req, ok := body["required"].([]any); ok {
			for _, r := range req {
				if name, ok := r.(string); ok {
					bodyRequired[name] = true
				}
			}
		}
		for _, name := range slices.Sorted(maps.Keys(bodyProps)) {
			if _, dup := props[name]; dup {
				return nil, fmt.Errorf("op %q: argument name collision on %q", s.Name, name)
			}
			prop := map[string]any{}
			if pm, ok := bodyProps[name].(map[string]any); ok {
				local, err := d.Localize(pm)
				if err != nil {
					return nil, err
				}
				maps.Copy(prop, local)
			}
			prop["x-rukie-in"] = "body"
			props[name] = prop
			if bodyRequired[name] {
				required = append(required, name)
			}
		}
	}
	slices.Sort(required)
	return Schema{
		"type":                 "object",
		"properties":           props,
		"required":             required,
		"additionalProperties": false,
	}, nil
}

func OutputSchema(s *Spec, d *Document) (Schema, error) {
	os, ok := d.Operations[s.Name]
	if !ok {
		return nil, fmt.Errorf("op %q: missing from document", s.Name)
	}
	if os.Response == nil {
		return Schema{"type": "object"}, nil
	}
	return d.Localize(os.Response)
}

func Annotations(s *Spec) ToolAnnotations {
	return ToolAnnotations{
		ReadOnlyHint:    s.Kind == KindRead,
		DestructiveHint: s.Destructive,
		IdempotentHint:  s.Idempotent || s.Kind == KindRead,
		OpenWorldHint:   false,
	}
}

func BuildTools(specs []Spec, d *Document, f ToolFilter) ([]*Tool, error) {
	sets := map[string]bool{}
	for _, ts := range f.Toolsets {
		sets[ts] = true
	}
	var tools []*Tool
	for i := range specs {
		s := &specs[i]
		if s.Hidden || !f.Principal.Can(s.Scope) {
			continue
		}
		if len(sets) > 0 && !sets[toolDomain(s.Name)] {
			continue
		}
		if f.ReadOnly && s.Kind != KindRead {
			continue
		}
		in, err := InputSchema(s, d)
		if err != nil {
			return nil, err
		}
		out, err := OutputSchema(s, d)
		if err != nil {
			return nil, err
		}
		tools = append(tools, &Tool{
			InputSchema:  in,
			OutputSchema: out,
			Name:         ToolName(s.Name),
			Title:        s.Summary,
			Description:  "CLI: rukie " + strings.ReplaceAll(s.Name, ".", " ") + "\nErrors: " + strings.Join(problemSlugs(s.Problems), ", "),
			Annotations:  Annotations(s),
		})
	}
	slices.SortFunc(tools, func(a, b *Tool) int { return strings.Compare(a.Name, b.Name) })
	return tools, nil
}

func checkMCP(specs []Spec, d *Document) error {
	seen := map[string]string{}
	for i := range specs {
		s := &specs[i]
		name := ToolName(s.Name)
		if prev, dup := seen[name]; dup {
			return fmt.Errorf("mcp: tool name %q collides (%s, %s)", name, prev, s.Name)
		}
		seen[name] = s.Name
		if s.Hidden {
			continue
		}
		if _, err := InputSchema(s, d); err != nil {
			return err
		}
		if _, err := OutputSchema(s, d); err != nil {
			return err
		}
	}
	return nil
}
