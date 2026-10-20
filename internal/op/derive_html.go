package op

import (
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

type FieldKind uint8

const (
	FieldText FieldKind = iota + 1
	FieldPassword
	FieldNumber
	FieldCheckbox
	FieldSelect
	FieldTextarea
	FieldHidden
)

type Field struct {
	Min      *float64
	Max      *float64
	Pattern  string
	Label    string
	Help     string
	Value    string
	Name     string
	Options  []string
	Errors   []string
	MinLen   int
	MaxLen   int
	Kind     FieldKind
	Required bool
	Multiple bool
	Checked  bool
}

type Reserved struct {
	CSRF           string
	ETag           string
	IdempotencyKey string
}

func FormFields(s *Spec, d *Document, values url.Values, errs []FieldError) ([]Field, error) {
	schema, err := InputSchema(s, d)
	if err != nil {
		return nil, err
	}
	props, _ := schema["properties"].(map[string]any)
	byField := map[string][]string{}
	for _, e := range errs {
		field, _, _ := strings.Cut(e.Location, ".")
		field, _, _ = strings.Cut(field, "[")
		byField[field] = append(byField[field], e.Message)
	}
	required := map[string]bool{}
	if req, ok := schema["required"].([]string); ok {
		for _, r := range req {
			required[r] = true
		}
	}
	var fields []Field
	for _, name := range slices.Sorted(maps.Keys(props)) {
		raw, _ := props[name].(map[string]any)
		placement, _ := raw["x-rukie-in"].(string)
		if placement == "path" || placement == "header" {
			continue
		}
		f := Field{Name: name, Label: name, Value: values.Get(name)}
		f.Errors = byField[name]
		if help, ok := raw["description"].(string); ok {
			f.Help = help
		}
		f.Required = required[name]
		typ, _ := raw["type"].(string)
		format, _ := raw["format"].(string)
		switch {
		case typ == "boolean":
			f.Kind = FieldCheckbox
			f.Checked = values.Get(name) == "true" || values.Get(name) == "on" || values.Get(name) == "1"
		case typ == "integer" || typ == "number":
			f.Kind = FieldNumber
			if minimum, ok := raw["minimum"].(float64); ok {
				f.Min = &minimum
			}
			if maximum, ok := raw["maximum"].(float64); ok {
				f.Max = &maximum
			}
		case raw["enum"] != nil:
			f.Kind = FieldSelect
			if enum, ok := raw["enum"].([]any); ok {
				for _, e := range enum {
					if str, ok := e.(string); ok {
						f.Options = append(f.Options, str)
					}
				}
			}
		case typ == "array":
			f.Kind = FieldTextarea
			f.Multiple = true
			f.Value = strings.Join(values[name], "\n")
		default:
			f.Kind = FieldText
			if format == "password" {
				f.Kind = FieldPassword
			}
		}
		if minLen, ok := raw["minLength"].(float64); ok {
			f.MinLen = int(minLen)
		}
		if maxLen, ok := raw["maxLength"].(float64); ok {
			f.MaxLen = int(maxLen)
		}
		if pattern, ok := raw["pattern"].(string); ok {
			f.Pattern = pattern
		}
		fields = append(fields, f)
	}
	fields = append(fields,
		Field{Name: "_csrf", Kind: FieldHidden},
		Field{Name: "_etag", Kind: FieldHidden},
		Field{Name: "_idem", Kind: FieldHidden},
	)
	return fields, nil
}

func DecodeForm(s *Spec, d *Document, body []byte) (Args, Reserved, url.Values, error) {
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, Reserved{}, nil, &Error{Code: CodeValidation, Detail: "malformed form body"}
	}
	in, err := InputSchema(s, d)
	if err != nil {
		return nil, Reserved{}, nil, err
	}
	props, _ := in["properties"].(map[string]any)
	known := map[string]map[string]any{}
	for name, raw := range props {
		if pm, ok := raw.(map[string]any); ok {
			known[name] = pm
		}
	}
	reserved := Reserved{CSRF: values.Get("_csrf"), ETag: values.Get("_etag"), IdempotencyKey: values.Get("_idem")}
	args := Args{}
	var fields []FieldError
	for _, name := range slices.Sorted(maps.Keys(values)) {
		if name == "_csrf" || name == "_etag" || name == "_idem" {
			continue
		}
		prop, ok := known[name]
		if !ok {
			fields = append(fields, FieldError{Location: "body." + name, Message: "unknown field"})
			continue
		}
		in, _ := prop["x-rukie-in"].(string)
		if in != "body" && in != "query" {
			continue
		}
		typ, _ := prop["type"].(string)
		vals := values[name]
		switch typ {
		case "boolean":
			args[name] = len(vals) > 0 && vals[0] != "" && vals[0] != "false" && vals[0] != "0"
		case "integer":
			parsed, perr := strconv.Atoi(vals[0])
			if perr != nil {
				fields = append(fields, FieldError{Location: "body." + name, Message: "must be an integer"})
				continue
			}
			args[name] = parsed
		case "number":
			parsed, perr := strconv.ParseFloat(vals[0], 64)
			if perr != nil {
				fields = append(fields, FieldError{Location: "body." + name, Message: "must be a number"})
				continue
			}
			args[name] = parsed
		case "array":
			items := make([]any, 0, len(vals))
			for _, v := range vals {
				for line := range strings.SplitSeq(v, "\n") {
					if line != "" {
						items = append(items, line)
					}
				}
			}
			args[name] = items
		default:
			if len(vals) > 1 {
				fields = append(fields, FieldError{Location: "body." + name, Message: "repeated field"})
				continue
			}
			args[name] = vals[0]
		}
	}
	for name, prop := range known {
		typ, _ := prop["type"].(string)
		if typ == "boolean" {
			if _, ok := args[name]; !ok {
				args[name] = false
			}
		}
	}
	if len(fields) > 0 {
		return nil, Reserved{}, values, &Error{Code: CodeValidation, Detail: "invalid form submission", Fields: fields}
	}
	return args, reserved, values, nil
}

func checkForms(specs []Spec, d *Document, v *Views) error {
	for i := range specs {
		s := &specs[i]
		if _, hasForm := v.forms[s.Name]; !hasForm {
			if _, hasPage := v.pages[s.Name]; !hasPage {
				continue
			}
		}
		in, err := InputSchema(s, d)
		if err != nil {
			return err
		}
		props, _ := in["properties"].(map[string]any)
		for _, name := range slices.Sorted(maps.Keys(props)) {
			raw, _ := props[name].(map[string]any)
			in, _ := raw["x-rukie-in"].(string)
			if in != "body" {
				continue
			}
			typ, _ := raw["type"].(string)
			if typ != "" && typ != "string" && typ != "boolean" && typ != "integer" && typ != "number" && typ != "array" {
				return fmt.Errorf("form %q: nested objects are API-only", s.Name)
			}
		}
		if s.Kind == KindMutation && !s.HasLocation {
			return fmt.Errorf("form %q: mutations with a form require a Location", s.Name)
		}
	}
	return nil
}
