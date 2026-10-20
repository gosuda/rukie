package op

import (
	"flag"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"
)

type FlagKind uint8

const (
	FlagBool FlagKind = iota + 1
	FlagInt
	FlagNumber
	FlagString
	FlagEnum
	FlagRepeated
	FlagJSON
)

type Flag struct {
	Name     string
	Property string
	Help     string
	Enum     []string
	Kind     FlagKind
	Required bool
}

type Command struct {
	Children   map[string]*Command
	Spec       *Spec
	doc        *Document
	Name       string
	Flags      []Flag
	Positional []string
}

type GlobalOpts struct {
	Server         string
	TokenFile      string
	Input          string
	IfMatch        string
	IdempotencyKey string
	Cursor         string
	Timeout        time.Duration
	All            bool
	JSON           bool
}

type Invocation struct {
	Leaf   *Command
	Args   Args
	Doc    *Document
	Global GlobalOpts
}

type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func ErrUsage(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

var reservedGlobals = map[string]bool{
	"server": true, "token-file": true, "json": true, "input": true,
	"if-match": true, "idempotency-key": true, "timeout": true, "help": true,
	"cursor": true, "all": true,
}

func kebab(name string) string {
	return strings.ReplaceAll(strings.ToLower(name), "_", "-")
}

func flagKindFor(prop map[string]any) (FlagKind, []string) {
	typ, _ := prop["type"].(string)
	switch typ {
	case "boolean":
		return FlagBool, nil
	case "integer":
		return FlagInt, nil
	case "number":
		return FlagNumber, nil
	case "array":
		items, _ := prop["items"].(map[string]any)
		itemType, _ := items["type"].(string)
		if itemType == "" || itemType == "string" || itemType == "integer" || itemType == "number" || itemType == "boolean" {
			return FlagRepeated, nil
		}
		return FlagJSON, nil
	case "object":
		return FlagJSON, nil
	default:
		if enum, ok := prop["enum"].([]any); ok && len(enum) > 0 {
			values := make([]string, 0, len(enum))
			for _, e := range enum {
				if s, ok := e.(string); ok {
					values = append(values, s)
				}
			}
			if len(values) > 0 {
				return FlagEnum, values
			}
		}
		return FlagString, nil
	}
}

func BuildCommands(specs []Spec, d *Document) (*Command, error) {
	root := &Command{Children: map[string]*Command{}, doc: d}
	for i := range specs {
		s := &specs[i]
		if s.Hidden {
			continue
		}
		path := strings.Split(s.Name, ".")
		if len(s.CLI.Path) > 0 {
			path = slices.Clone(s.CLI.Path)
		}
		node := root
		for _, segment := range path[:len(path)-1] {
			next, ok := node.Children[segment]
			if !ok {
				next = &Command{Name: segment, Children: map[string]*Command{}}
				node.Children[segment] = next
			}
			if next.Spec != nil {
				return nil, fmt.Errorf("cli: group %q collides with a leaf", strings.Join(path, " "))
			}
			node = next
		}
		verb := path[len(path)-1]
		if existing, dup := node.Children[verb]; dup {
			if existing.Spec != nil || len(existing.Children) > 0 {
				return nil, fmt.Errorf("cli: duplicate command path %q", strings.Join(path, " "))
			}
		}
		leaf := &Command{Name: verb, Spec: s, Children: map[string]*Command{}}
		node.Children[verb] = leaf
		in, err := InputSchema(s, d)
		if err != nil {
			return nil, err
		}
		props, _ := in["properties"].(map[string]any)
		positionals := pathParamOrder(s.Path)
		if len(s.CLI.Positional) > 0 {
			positionals = slices.Clone(s.CLI.Positional)
		}
		leaf.Positional = positionals
		seen := map[string]bool{}
		for name, raw := range props {
			prop, _ := raw.(map[string]any)
			in, _ := prop["x-rukie-in"].(string)
			if in == "header" || name == "cursor" {
				continue
			}
			flagName := kebab(name)
			if reservedGlobals[flagName] {
				return nil, fmt.Errorf("cli: op %q property %q collides with a global flag", s.Name, name)
			}
			if seen[flagName] {
				return nil, fmt.Errorf("cli: op %q duplicate flag %q", s.Name, flagName)
			}
			seen[flagName] = true
			kind, enum := flagKindFor(prop)
			help, _ := prop["description"].(string)
			leaf.Flags = append(leaf.Flags, Flag{Name: flagName, Property: name, Kind: kind, Enum: enum, Help: help})
		}
		slices.SortFunc(leaf.Flags, func(a, b Flag) int { return strings.Compare(a.Name, b.Name) })
	}
	return root, nil
}

func pathParamOrder(path string) []string {
	var out []string
	for _, seg := range strings.Split(path, "/") {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			inner := seg[1 : len(seg)-1]
			inner = strings.TrimSuffix(inner, "...")
			out = append(out, inner)
		}
	}
	return out
}

func checkCLI(specs []Spec, d *Document) error {
	root, err := BuildCommands(specs, d)
	if err != nil {
		return err
	}
	var walk func(c *Command, path []string) error
	walk = func(c *Command, path []string) error {
		if c.Spec != nil && len(c.Children) > 0 {
			return fmt.Errorf("cli: leaf %q has children", strings.Join(path, " "))
		}
		if c.Spec != nil {
			params := map[string]bool{}
			os := d.Operations[c.Spec.Name]
			for _, p := range os.Params {
				if p.In == "path" {
					params[p.Name] = true
				}
			}
			for _, pos := range c.Positional {
				if !params[pos] {
					return fmt.Errorf("cli: op %q positional %q is not a path parameter", c.Spec.Name, pos)
				}
			}
		}
		names := make([]string, 0, len(c.Children))
		for name := range c.Children {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			if err := walk(c.Children[name], append(path, name)); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(root, nil)
}

func Parse(root *Command, argv []string) (*Invocation, error) {
	node := root
	var path []string
	rest := argv
	for len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		next, ok := node.Children[rest[0]]
		if !ok {
			return nil, ErrUsage("unknown command %q", strings.Join(append(path, rest[0]), " "))
		}
		node = next
		path = append(path, rest[0])
		rest = rest[1:]
		if node.Spec != nil {
			break
		}
	}
	if node.Spec == nil {
		return nil, ErrUsage("incomplete command %q", strings.Join(path, " "))
	}
	leaf := node
	inv := &Invocation{Leaf: leaf, Args: Args{}, Doc: root.doc}
	fs := flag.NewFlagSet(strings.Join(path, " "), flag.ContinueOnError)
	strValues := map[string]*string{}
	boolValues := map[string]*bool{}
	listValues := map[string]*stringList{}
	for _, f := range leaf.Flags {
		switch f.Kind {
		case FlagBool:
			set := false
			boolValues[f.Name] = &set
			fs.BoolVar(boolValues[f.Name], f.Name, false, f.Help)
		case FlagRepeated:
			set := &stringList{}
			listValues[f.Name] = set
			fs.Var(set, f.Name, f.Help)
		case FlagInt, FlagNumber, FlagString, FlagEnum, FlagJSON:
			set := ""
			strValues[f.Name] = &set
			fs.StringVar(strValues[f.Name], f.Name, "", f.Help)
		}
	}
	global := &inv.Global
	fs.StringVar(&global.Server, "server", "", "server base URL")
	fs.StringVar(&global.TokenFile, "token-file", "", "file holding the bearer token")
	fs.BoolVar(&global.JSON, "json", false, "machine-readable JSON output")
	fs.StringVar(&global.Input, "input", "", "JSON args object from file or stdin (-)")
	fs.StringVar(&global.IfMatch, "if-match", "", "If-Match validator")
	fs.StringVar(&global.IdempotencyKey, "idempotency-key", "", "idempotency key")
	fs.StringVar(&global.Cursor, "cursor", "", "pagination cursor")
	fs.BoolVar(&global.All, "all", false, "fetch all pages")
	fs.DurationVar(&global.Timeout, "timeout", 30*time.Second, "request timeout")
	if err := fs.Parse(rest); err != nil {
		return nil, ErrUsage("%v", err)
	}
	positional := fs.Args()
	if len(positional) > len(leaf.Positional) {
		return nil, ErrUsage("too many positional arguments")
	}
	for i, value := range positional {
		if _, set := inv.Args[leaf.Positional[i]]; set {
			continue
		}
		inv.Args[leaf.Positional[i]] = value
	}
	for _, f := range leaf.Flags {
		switch f.Kind {
		case FlagBool:
			inv.Args[f.Property] = *boolValues[f.Name]
		case FlagRepeated:
			items := make([]any, 0, len(*listValues[f.Name]))
			for _, v := range *listValues[f.Name] {
				items = append(items, v)
			}
			if len(items) > 0 {
				inv.Args[f.Property] = items
			}
		case FlagInt, FlagNumber, FlagString, FlagEnum, FlagJSON:
			raw := *strValues[f.Name]
			if raw == "" {
				continue
			}
			if err := parseStringFlag(f, raw, inv); err != nil {
				return nil, err
			}
		}
	}
	if global.IfMatch != "" {
		inv.Args["if_match"] = global.IfMatch
	}
	if global.IdempotencyKey != "" {
		inv.Args["idempotency_key"] = global.IdempotencyKey
	}
	if global.Cursor != "" {
		inv.Args["cursor"] = global.Cursor
	}
	return inv, nil
}

func GenerateIdempotencyKey() string { return newClaimID() }

func parseStringFlag(f Flag, raw string, inv *Invocation) error {
	if f.Kind == FlagInt {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return ErrUsage("flag --%s: %v", f.Name, err)
		}
		inv.Args[f.Property] = parsed
		return nil
	}
	if f.Kind == FlagNumber {
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return ErrUsage("flag --%s: %v", f.Name, err)
		}
		inv.Args[f.Property] = parsed
		return nil
	}
	if f.Kind == FlagEnum {
		if !slices.Contains(f.Enum, raw) {
			return ErrUsage("flag --%s: must be one of %s", f.Name, strings.Join(f.Enum, ", "))
		}
		inv.Args[f.Property] = raw
		return nil
	}
	if f.Kind == FlagJSON {
		var decoded any
		if err := jsonv2.Unmarshal([]byte(raw), &decoded, jsonv1.DefaultOptionsV1()); err != nil {
			return ErrUsage("flag --%s: invalid JSON", f.Name)
		}
		inv.Args[f.Property] = decoded
		return nil
	}
	inv.Args[f.Property] = raw
	return nil
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}
