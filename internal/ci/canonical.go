package ci

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"math"
	"slices"
	"strconv"
	"unicode/utf16"
)

func CanonicalJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("CanonicalJSON: %w", err)
	}
	var instance any
	if err = json.Unmarshal(data, &instance); err != nil {
		return nil, fmt.Errorf("CanonicalJSON: %w", err)
	}
	var out bytes.Buffer
	if err = encodeCanonical(&out, instance); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func encodeCanonical(out *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		out.WriteString(strconv.FormatBool(v))
	case float64:
		if math.Trunc(v) != v || math.Abs(v) > 9007199254740991 {
			return fmt.Errorf("CanonicalJSON: non-exact integer")
		}
		out.WriteString(strconv.FormatFloat(v, 'f', 0, 64))
	case string:
		out.WriteByte('"')
		for _, r := range v {
			switch r {
			case '"', '\\':
				out.WriteByte('\\')
				out.WriteRune(r)
			case '\b':
				out.WriteString(`\b`)
			case '\f':
				out.WriteString(`\f`)
			case '\n':
				out.WriteString(`\n`)
			case '\r':
				out.WriteString(`\r`)
			case '\t':
				out.WriteString(`\t`)
			default:
				if r < 32 {
					fmt.Fprintf(out, `\u%04x`, r)
				} else {
					out.WriteRune(r)
				}
			}
		}
		out.WriteByte('"')
	case []any:
		out.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := encodeCanonical(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		slices.SortFunc(keys, func(a, b string) int { return slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b))) })
		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := encodeCanonical(out, key); err != nil {
				return err
			}
			out.WriteByte(':')
			if err := encodeCanonical(out, v[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("CanonicalJSON: unsupported JSON value %T", value)
	}
	return nil
}

func ExpandMatrix(spec Spec) ([]JobSpec, error) {
	expansions := make(map[string][]JobSpec, len(spec.Jobs))
	total := 0
	for _, job := range spec.Jobs {
		keys := make([]string, 0, len(job.Matrix))
		for key := range job.Matrix {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		jobs := []JobSpec{job}
		for _, key := range keys {
			var next []JobSpec
			for _, current := range jobs {
				for index, value := range job.Matrix[key] {
					copyJob := current
					copyJob.Env = make(map[string]string, len(current.Env)+1)
					for name, entry := range current.Env {
						copyJob.Env[name] = entry
					}
					copyJob.Env[key] = value
					copyJob.Name = current.Name + "-" + strconv.Itoa(index+1)
					copyJob.Matrix = nil
					next = append(next, copyJob)
					if len(next) > 256 {
						return nil, fmt.Errorf("ExpandMatrix: expanded job limit exceeded")
					}
				}
			}
			jobs = next
		}
		total += len(jobs)
		if total > 256 {
			return nil, fmt.Errorf("ExpandMatrix: expanded job limit exceeded")
		}
		expansions[job.Name] = jobs
	}
	result := make([]JobSpec, 0, total)
	for _, job := range spec.Jobs {
		for _, expanded := range expansions[job.Name] {
			expanded.Needs = nil
			for _, name := range job.Needs {
				for _, dep := range expansions[name] {
					expanded.Needs = append(expanded.Needs, dep.Name)
				}
			}
			result = append(result, expanded)
		}
	}
	return result, nil
}
