package op

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"html/template"
	"io"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"
)

type Media uint8

const (
	MediaJSON Media = iota + 1
	MediaMarkdown
	MediaHTML
)

func (m Media) Type() string {
	switch m {
	case MediaMarkdown:
		return "text/markdown"
	case MediaHTML:
		return "text/html"
	case MediaJSON:
		return "application/json"
	default:
		return "application/json"
	}
}

func (m Media) String() string { return m.Type() }

func Offered(hasPage, hasForm bool, kind Kind) []Media {
	out := []Media{MediaJSON, MediaMarkdown}
	if kind == KindRead && hasPage {
		out = append(out, MediaHTML)
	}
	if kind == KindMutation && hasForm {
		out = append(out, MediaHTML)
	}
	return out
}

type acceptItem struct {
	typ   string
	sub   string
	q     float64
	order int
}

func parseAccept(accept string) []acceptItem {
	var items []acceptItem
	order := 0
	for segment := range strings.SplitSeq(strings.TrimSpace(accept), ",") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		parts := strings.Split(segment, ";")
		mt := strings.TrimSpace(strings.ToLower(parts[0]))
		typ, sub, ok := strings.Cut(mt, "/")
		if !ok || typ == "" || sub == "" {
			continue
		}
		q := 1.0
		for _, p := range parts[1:] {
			name, value, ok := strings.Cut(strings.TrimSpace(p), "=")
			if ok && strings.TrimSpace(strings.ToLower(name)) == "q" {
				if parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
					q = parsed
				}
			}
		}
		items = append(items, acceptItem{typ: typ, sub: sub, q: q, order: order})
		order++
	}
	return items
}

func mediaPriority(m Media) int {
	switch m {
	case MediaJSON:
		return 0
	case MediaMarkdown:
		return 1
	case MediaHTML:
		return 2
	default:
		return 2
	}
}

func Negotiate(accept string, offered []Media) (Media, bool) {
	items := parseAccept(accept)
	if len(items) == 0 {
		items = []acceptItem{{typ: "*", sub: "*", q: 1, order: 0}}
	}
	best := Media(0)
	bestQ := -1.0
	bestSpec := -1
	bestPrio := len(offered) + 1
	for _, m := range offered {
		mt, sub, _ := strings.Cut(m.Type(), "/")
		q := -1.0
		spec := -1
		for _, it := range items {
			match := -1
			switch {
			case it.typ == mt && it.sub == sub:
				match = 2
			case it.typ == mt && it.sub == "*":
				match = 1
			case it.typ == "*" && it.sub == "*":
				match = 0
			}
			if match >= 0 && (match > spec || (match == spec && q < 0)) {
				spec = match
				q = it.q
			}
		}
		if q < 0 {
			continue
		}
		prio := mediaPriority(m)
		if q > bestQ || (q == bestQ && (spec > bestSpec || (spec == bestSpec && prio < bestPrio))) {
			best = m
			bestQ = q
			bestSpec = spec
			bestPrio = prio
		}
	}
	if best == 0 || bestQ <= 0 {
		return 0, false
	}
	return best, true
}

type ViewCtx struct {
	Input     url.Values
	Spec      *Spec
	RequestID string
	Path      string
	CSRF      string
	Errors    []FieldError
	Principal Principal
}

type PageFunc func(w io.Writer, c ViewCtx, out any) error
type FormFunc func(w io.Writer, c ViewCtx) error
type MarkdownFunc func(w io.Writer, c ViewCtx, out any) error

type Views struct {
	pages    map[string]pageEntry
	forms    map[string]FormFunc
	markdown map[string]MarkdownFunc
}

type pageEntry struct {
	out reflect.Type
	fn  PageFunc
}

func NewViews() *Views {
	return &Views{
		pages:    map[string]pageEntry{},
		forms:    map[string]FormFunc{},
		markdown: map[string]MarkdownFunc{},
	}
}

func PageView[Out any](v *Views, opName string, f func(io.Writer, ViewCtx, Out) error) {
	v.pages[opName] = pageEntry{
		fn: func(w io.Writer, c ViewCtx, out any) error {
			typed, ok := out.(Out)
			if !ok {
				return fmt.Errorf("view %q: output type mismatch", opName)
			}
			return f(w, c, typed)
		},
		out: reflect.TypeFor[Out](),
	}
}

func (v *Views) Form(opName string, layout FormFunc) {
	v.forms[opName] = layout
}

func (v *Views) Markdown(opName string, f MarkdownFunc) {
	v.markdown[opName] = f
}

func (v *Views) check(specs []Spec) error {
	byName := map[string]Spec{}
	for _, s := range specs {
		byName[s.Name] = s
	}
	for name, entry := range v.pages {
		s, ok := byName[name]
		if !ok {
			return fmt.Errorf("view %q: unknown operation", name)
		}
		if s.Kind != KindRead {
			return fmt.Errorf("view %q: page views bind to reads only", name)
		}
		if entry.out != s.Out {
			return fmt.Errorf("view %q: output type mismatch", name)
		}
	}
	for name := range v.forms {
		s, ok := byName[name]
		if !ok {
			return fmt.Errorf("form %q: unknown operation", name)
		}
		if s.Kind != KindMutation {
			if _, hasPage := v.pages[name]; !hasPage {
				return fmt.Errorf("form %q: forms bind to mutations or reads with a page view", name)
			}
		}
	}
	for name := range v.markdown {
		if _, ok := byName[name]; !ok {
			return fmt.Errorf("markdown %q: unknown operation", name)
		}
	}
	return nil
}

func RenderMarkdown(w io.Writer, v any) error {
	if v == nil {
		_, err := io.WriteString(w, "null\n")
		return err
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			_, err := io.WriteString(w, "null\n")
			return err
		}
		rv = rv.Elem()
	}
	kind := rv.Kind()
	if kind == reflect.Struct {
		return renderMarkdownStruct(w, rv, 0)
	}
	if kind == reflect.Slice || kind == reflect.Array {
		return renderMarkdownSlice(w, rv, 0)
	}
	if kind == reflect.Map {
		return renderMarkdownMap(w, rv, 0)
	}
	_, err := io.WriteString(w, markdownScalar(rv)+"\n")
	return err
}

func markdownFieldName(sf reflect.StructField) (string, bool) {
	if tag, ok := sf.Tag.Lookup("json"); ok {
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" {
			return "", false
		}
		if name != "" {
			return name, true
		}
	}
	return strings.ToLower(sf.Name), true
}

func isScalarKind(k reflect.Kind) bool {
	return k == reflect.Bool || k == reflect.String ||
		k == reflect.Int || k == reflect.Int8 || k == reflect.Int16 || k == reflect.Int32 || k == reflect.Int64 ||
		k == reflect.Uint || k == reflect.Uint8 || k == reflect.Uint16 || k == reflect.Uint32 || k == reflect.Uint64 ||
		k == reflect.Float32 || k == reflect.Float64
}

func derefValue(rv reflect.Value) reflect.Value {
	for (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface) && !rv.IsNil() {
		rv = rv.Elem()
	}
	return rv
}

func markdownScalar(rv reflect.Value) string {
	rv = derefValue(rv)
	if !rv.IsValid() {
		return "null"
	}
	kind := rv.Kind()
	if kind == reflect.String {
		return markdownEscape(rv.String())
	}
	if kind == reflect.Bool {
		if rv.Bool() {
			return "true"
		}
		return "false"
	}
	if isIntKind(kind) {
		return strconv.FormatInt(rv.Int(), 10)
	}
	if isUintKind(kind) {
		return strconv.FormatUint(rv.Uint(), 10)
	}
	if kind == reflect.Float32 || kind == reflect.Float64 {
		return strconv.FormatFloat(rv.Float(), 'g', -1, 64)
	}
	raw, err := jsonv2.Marshal(rv.Interface(), jsonv1.DefaultOptionsV1())
	if err != nil {
		return "null"
	}
	return string(raw)
}

func isIntKind(kind reflect.Kind) bool {
	return kind == reflect.Int || kind == reflect.Int8 || kind == reflect.Int16 || kind == reflect.Int32 || kind == reflect.Int64
}

func isUintKind(kind reflect.Kind) bool {
	return kind == reflect.Uint || kind == reflect.Uint8 || kind == reflect.Uint16 || kind == reflect.Uint32 || kind == reflect.Uint64
}

func markdownEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\', '`', '*', '_', '{', '}', '[', ']', '(', ')', '#', '+', '-', '.', '!':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func renderMarkdownStruct(w io.Writer, rv reflect.Value, depth int) error {
	pad := strings.Repeat("  ", depth)
	rt := rv.Type()
	for i := range rt.NumField() {
		sf := rt.Field(i)
		if !sf.IsExported() {
			continue
		}
		name, ok := markdownFieldName(sf)
		if !ok {
			continue
		}
		fv := derefValue(rv.Field(i))
		if !fv.IsValid() {
			if _, err := io.WriteString(w, pad+"- **"+name+"**: null\n"); err != nil {
				return err
			}
			continue
		}
		if err := renderMarkdownValue(w, pad, name, fv, depth); err != nil {
			return err
		}
	}
	return nil
}

func renderMarkdownValue(w io.Writer, pad, name string, fv reflect.Value, depth int) error {
	kind := fv.Kind()
	if kind == reflect.Struct {
		if _, err := io.WriteString(w, pad+"- **"+name+"**:\n"); err != nil {
			return err
		}
		return renderMarkdownStruct(w, fv, depth+1)
	}
	if kind == reflect.Slice || kind == reflect.Array {
		return renderMarkdownSequence(w, pad, name, fv, depth)
	}
	if kind == reflect.Map {
		if _, err := io.WriteString(w, pad+"- **"+name+"**:\n"); err != nil {
			return err
		}
		return renderMarkdownMap(w, fv, depth+1)
	}
	_, err := io.WriteString(w, pad+"- **"+name+"**: "+markdownScalar(fv)+"\n")
	return err
}

func renderMarkdownSequence(w io.Writer, pad, name string, fv reflect.Value, depth int) error {
	if fv.Len() == 0 {
		_, err := io.WriteString(w, pad+"- **"+name+"**: []\n")
		return err
	}
	if elem := derefValue(fv.Index(0)); elem.Kind() == reflect.Struct {
		if _, err := io.WriteString(w, pad+"- **"+name+"**:\n"); err != nil {
			return err
		}
		return renderMarkdownSlice(w, fv, depth+1)
	}
	parts := make([]string, 0, fv.Len())
	for j := range fv.Len() {
		parts = append(parts, markdownScalar(fv.Index(j)))
	}
	_, err := io.WriteString(w, pad+"- **"+name+"**: "+strings.Join(parts, ", ")+"\n")
	return err
}

func structScalarColumns(rt reflect.Type) []int {
	var cols []int
	for i := range rt.NumField() {
		sf := rt.Field(i)
		if !sf.IsExported() {
			continue
		}
		if _, ok := markdownFieldName(sf); !ok {
			continue
		}
		ft := sf.Type
		for ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Interface {
			ft = ft.Elem()
		}
		if isScalarKind(ft.Kind()) {
			cols = append(cols, i)
		}
		if len(cols) >= 6 {
			break
		}
	}
	return cols
}

func renderMarkdownSlice(w io.Writer, rv reflect.Value, depth int) error {
	pad := strings.Repeat("  ", depth)
	if rv.Len() == 0 {
		_, err := io.WriteString(w, pad+"_(empty)_\n")
		return err
	}
	first := derefValue(rv.Index(0))
	if first.Kind() == reflect.Struct {
		cols := structScalarColumns(first.Type())
		if len(cols) == 0 {
			for j := range rv.Len() {
				if _, err := io.WriteString(w, pad+"- item "+strconv.Itoa(j+1)+"\n"); err != nil {
					return err
				}
				if err := renderMarkdownStruct(w, derefValue(rv.Index(j)), depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		headers := make([]string, len(cols))
		for k, c := range cols {
			name, _ := markdownFieldName(first.Type().Field(c))
			headers[k] = markdownEscape(name)
		}
		if _, err := io.WriteString(w, pad+"| "+strings.Join(headers, " | ")+" |\n"); err != nil {
			return err
		}
		sep := make([]string, len(cols))
		for k := range sep {
			sep[k] = "---"
		}
		if _, err := io.WriteString(w, pad+"| "+strings.Join(sep, " | ")+" |\n"); err != nil {
			return err
		}
		for j := range rv.Len() {
			row := derefValue(rv.Index(j))
			cells := make([]string, len(cols))
			for k, c := range cols {
				cells[k] = markdownScalar(row.Field(c))
			}
			if _, err := io.WriteString(w, pad+"| "+strings.Join(cells, " | ")+" |\n"); err != nil {
				return err
			}
		}
		return nil
	}
	for j := range rv.Len() {
		if _, err := io.WriteString(w, pad+"- "+markdownScalar(rv.Index(j))+"\n"); err != nil {
			return err
		}
	}
	return nil
}

func renderMarkdownMap(w io.Writer, rv reflect.Value, depth int) error {
	pad := strings.Repeat("  ", depth)
	keys := rv.MapKeys()
	strKeys := make([]string, 0, len(keys))
	byStr := map[string]reflect.Value{}
	for _, k := range keys {
		ks := markdownScalar(k)
		strKeys = append(strKeys, ks)
		byStr[ks] = rv.MapIndex(k)
	}
	slices.Sort(strKeys)
	for _, ks := range strKeys {
		fv := derefValue(byStr[ks])
		if fv.Kind() == reflect.Struct || fv.Kind() == reflect.Map {
			if _, err := io.WriteString(w, pad+"- **"+ks+"**:\n"); err != nil {
				return err
			}
			if err := RenderMarkdownIndent(w, fv.Interface(), depth+1); err != nil {
				return err
			}
			continue
		}
		if _, err := io.WriteString(w, pad+"- **"+ks+"**: "+markdownScalar(fv)+"\n"); err != nil {
			return err
		}
	}
	return nil
}

func RenderMarkdownIndent(w io.Writer, v any, depth int) error {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			_, err := io.WriteString(w, strings.Repeat("  ", depth)+"null\n")
			return err
		}
		rv = rv.Elem()
	}
	kind := rv.Kind()
	if kind == reflect.Struct {
		return renderMarkdownStruct(w, rv, depth)
	}
	if kind == reflect.Slice || kind == reflect.Array {
		return renderMarkdownSlice(w, rv, depth)
	}
	if kind == reflect.Map {
		return renderMarkdownMap(w, rv, depth)
	}
	_, err := io.WriteString(w, strings.Repeat("  ", depth)+markdownScalar(rv)+"\n")
	return err
}

var errorPageTemplate = template.Must(template.New("error").Parse(`<!DOCTYPE html>
<html lang="en">
<head><meta charset="utf-8"><title>{{.Status}} {{.Title}}</title></head>
<body>
<main>
<h1>{{.Status}} {{.Title}}</h1>
{{if .Detail}}<p>{{.Detail}}</p>{{end}}
<p><code>{{.Code}}</code></p>
{{if .RequestID}}<p>Request: <code>{{.RequestID}}</code></p>{{end}}
{{if .RecoveryNext}}<p>Next: <code>{{.RecoveryNext}}</code></p>{{end}}
<p><a href="/-/api">Operation index</a></p>
</main>
</body>
</html>
`))

func RenderErrorPage(w io.Writer, c ViewCtx, p problemBody) error {
	data := struct {
		Title        string
		Detail       string
		Code         string
		RequestID    string
		RecoveryNext string
		Status       int
	}{
		Status:       p.Status,
		Title:        p.Title,
		Detail:       p.Detail,
		Code:         p.Code,
		RequestID:    p.RequestID,
		RecoveryNext: recoveryNext(p.Recovery),
	}
	_ = c
	return errorPageTemplate.Execute(w, data)
}

func recoveryNext(r *Recovery) string {
	if r == nil {
		return ""
	}
	return r.Next
}

func RenderErrorMarkdown(w io.Writer, c ViewCtx, p problemBody) error {
	_ = c
	var b strings.Builder
	b.WriteString("# " + strconv.Itoa(p.Status) + " " + p.Title + "\n\n")
	b.WriteString("- **code**: " + markdownEscape(p.Code) + "\n")
	if p.Detail != "" {
		b.WriteString("- **detail**: " + markdownEscape(p.Detail) + "\n")
	}
	if p.RequestID != "" {
		b.WriteString("- **request_id**: `" + p.RequestID + "`\n")
	}
	if next := recoveryNext(p.Recovery); next != "" {
		b.WriteString("- **next**: `" + next + "`\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

type ReferenceEntry struct {
	Name     string
	Method   string
	Path     string
	Scope    string
	Kind     string
	Summary  string
	Problems []string
}

func ReferenceEntries(specs []Spec) []ReferenceEntry {
	entries := make([]ReferenceEntry, 0, len(specs))
	for _, s := range specs {
		problems := make([]string, 0, len(s.Problems))
		for _, c := range s.Problems {
			problems = append(problems, c.Slug())
		}
		slices.Sort(problems)
		entries = append(entries, ReferenceEntry{
			Name:     s.Name,
			Method:   s.Method,
			Path:     s.Path,
			Scope:    s.Scope.String(),
			Kind:     s.Kind.String(),
			Summary:  s.Summary,
			Problems: problems,
		})
	}
	return entries
}

var referenceTemplate = template.Must(template.New("reference").Parse(`<!DOCTYPE html>
<html lang="en">
<head><meta charset="utf-8"><title>Operation index</title></head>
<body>
<main>
<h1>Operations</h1>
<table>
<thead><tr><th>Operation</th><th>Method</th><th>Path</th><th>Scope</th><th>Summary</th></tr></thead>
<tbody>
{{range .}}<tr><td><code>{{.Name}}</code></td><td>{{.Method}}</td><td><code>{{.Path}}</code></td><td>{{.Scope}}</td><td>{{.Summary}}</td></tr>{{end}}
</tbody>
</table>
</main>
</body>
</html>
`))

func RenderReference(w io.Writer, c ViewCtx, out any) error {
	_ = c
	entries, ok := out.([]ReferenceEntry)
	if !ok {
		return fmt.Errorf("reference view: expected []ReferenceEntry")
	}
	return referenceTemplate.Execute(w, entries)
}

func RenderProblem(w io.Writer, c ViewCtx, out any) error {
	_ = c
	p, ok := out.(Problem)
	if !ok {
		return fmt.Errorf("problem view: expected Problem")
	}
	data := struct {
		Title     string
		Code      string
		RukCodes  []string
		Status    int
		Exit      int
		Retryable bool
	}{
		Title: p.Title, Status: p.Status, Code: p.Code.Slug(),
		Retryable: p.Retryable, Exit: p.Exit, RukCodes: p.RukCodes,
	}
	return problemPageTemplate.Execute(w, data)
}

var problemPageTemplate = template.Must(template.New("problem").Parse(`<!DOCTYPE html>
<html lang="en">
<head><meta charset="utf-8"><title>{{.Code}}</title></head>
<body>
<main>
<h1>{{.Title}}</h1>
<ul>
<li>Status: {{.Status}}</li>
<li>Code: <code>{{.Code}}</code></li>
<li>Retryable: {{.Retryable}}</li>
<li>Exit: {{.Exit}}</li>
{{range .RukCodes}}<li>Ruk: <code>{{.}}</code></li>{{end}}
</ul>
</main>
</body>
</html>
`))

type CursorPayload struct {
	Sort string `json:"sort"`
	Last string `json:"last"`
}

func cursorSubkey(key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("rukie-cursor-v1"))
	return mac.Sum(nil)
}

func EncodeCursor(key []byte, payload CursorPayload) (string, error) {
	raw, err := jsonv2.Marshal(payload, jsonv1.DefaultOptionsV1())
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, cursorSubkey(key))
	mac.Write(raw)
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func DecodeCursor(key []byte, cursor string) (CursorPayload, error) {
	var zero CursorPayload
	encoded, sigEncoded, ok := strings.Cut(cursor, ".")
	if !ok {
		return zero, fmt.Errorf("invalid cursor")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return zero, fmt.Errorf("invalid cursor")
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigEncoded)
	if err != nil {
		return zero, fmt.Errorf("invalid cursor")
	}
	mac := hmac.New(sha256.New, cursorSubkey(key))
	mac.Write(raw)
	if !hmac.Equal(mac.Sum(nil), sig) {
		return zero, fmt.Errorf("invalid cursor")
	}
	var payload CursorPayload
	if err := jsonv2.Unmarshal(raw, &payload, jsonv1.DefaultOptionsV1()); err != nil {
		return zero, fmt.Errorf("invalid cursor")
	}
	return payload, nil
}
