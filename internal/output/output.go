// Package output validates generic records against the configured output
// schema, deduplicates them and renders JSON or CSV.
package output

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/extract"
	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

var Types = []string{"string", "integer", "decimal", "boolean", "url"}

// Validate checks an output definition.
func Validate(def contracts.OutputDefinition, issues *extract.Issues) {
	switch def.Format {
	case "json", "csv":
	default:
		issues.Errorf("output.format", "format must be json or csv, got %q", def.Format)
	}
	if len(def.Fields) == 0 {
		issues.Errorf("output.fields", "at least one output field is required")
	}
	seen := map[string]bool{}
	for i, f := range def.Fields {
		path := fmt.Sprintf("output.fields[%d]", i)
		if strings.TrimSpace(f.Name) == "" {
			issues.Errorf(path+".name", "field name is required")
		}
		if seen[f.Name] {
			issues.Errorf(path+".name", "duplicate field %q", f.Name)
		}
		seen[f.Name] = true
		if !validType(f.Type) {
			issues.Errorf(path+".type", "unknown type %q (supported: %s)", f.Type, strings.Join(Types, ", "))
		}
	}
	for i, k := range def.DedupeBy {
		if !seen[k] {
			issues.Errorf(fmt.Sprintf("output.dedupeBy[%d]", i), "dedupe field %q is not an output field", k)
		}
	}
}

func validType(t string) bool {
	for _, x := range Types {
		if x == t {
			return true
		}
	}
	return false
}

// Coerce converts extracted values to the output field types. A non-empty
// reason means the record must be rejected. Values for fields not in the
// output definition are dropped.
func Coerce(fields contracts.OutputFields, values map[string]any) (map[string]any, string, []string) {
	out := make(map[string]any, len(fields))
	var warnings []string
	for _, f := range fields {
		v, ok := values[f.Name]
		if !ok || v == nil || v == "" {
			if f.Required {
				return nil, fmt.Sprintf("required field %q is missing", f.Name), warnings
			}
			out[f.Name] = nil
			continue
		}
		cv, err := coerce(f.Type, v)
		if err != nil {
			if f.Required {
				return nil, fmt.Sprintf("required field %q: %v", f.Name, err), warnings
			}
			warnings = append(warnings, fmt.Sprintf("optional field %q dropped: %v", f.Name, err))
			out[f.Name] = nil
			continue
		}
		out[f.Name] = cv
	}
	return out, "", warnings
}

func coerce(typ string, v any) (any, error) {
	s := strings.TrimSpace(extract.Stringify(v))
	switch typ {
	case "string":
		if s == "" {
			return nil, fmt.Errorf("value %v is not a string", v)
		}
		return s, nil
	case "integer":
		switch x := v.(type) {
		case int64:
			return x, nil
		case int:
			return int64(x), nil
		case float64:
			if x == math.Trunc(x) {
				return int64(x), nil
			}
		case string:
			if i, err := strconv.ParseInt(s, 10, 64); err == nil {
				return i, nil
			}
		}
		return nil, fmt.Errorf("%q is not an integer (add an integer transform)", s)
	case "decimal":
		switch x := v.(type) {
		case float64:
			return x, nil
		case int64:
			return float64(x), nil
		case int:
			return float64(x), nil
		case string:
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				return f, nil
			}
		}
		return nil, fmt.Errorf("%q is not a decimal (add a decimal transform)", s)
	case "boolean":
		if b, ok := v.(bool); ok {
			return b, nil
		}
		return nil, fmt.Errorf("%q is not a boolean (add a boolean transform)", s)
	case "url":
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("%q is not an absolute URL (add an absoluteUrl transform)", s)
		}
		return u.String(), nil
	}
	return nil, fmt.Errorf("unknown type %q", typ)
}

// DedupeFields returns the effective dedupe key fields.
func DedupeFields(def contracts.OutputDefinition) []string {
	if len(def.DedupeBy) > 0 {
		return def.DedupeBy
	}
	for _, f := range def.Fields {
		if f.Name == "url" {
			return []string{"url"}
		}
	}
	names := make([]string, len(def.Fields))
	for i, f := range def.Fields {
		names[i] = f.Name
	}
	return names
}

// Key is a stable identity for a record.
func Key(fields []string, values map[string]any) string {
	h := sha256.New()
	for _, f := range fields {
		h.Write([]byte(f))
		h.Write([]byte{0})
		h.Write([]byte(strings.ToLower(extract.Stringify(values[f]))))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Dedupe keeps the first record for each key, preserving order.
func Dedupe(records []contracts.Record) []contracts.Record {
	seen := map[string]bool{}
	out := make([]contracts.Record, 0, len(records))
	for _, r := range records {
		if seen[r.Key] {
			continue
		}
		seen[r.Key] = true
		out = append(out, r)
	}
	return out
}

// Render formats records in field order.
func Render(def contracts.OutputDefinition, records []contracts.Record) ([]byte, string, error) {
	switch def.Format {
	case "csv":
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		header := make([]string, len(def.Fields))
		for i, f := range def.Fields {
			header[i] = f.Name
		}
		_ = w.Write(header)
		for _, r := range records {
			row := make([]string, len(def.Fields))
			for i, f := range def.Fields {
				row[i] = extract.Stringify(r.Values[f.Name])
			}
			_ = w.Write(row)
		}
		w.Flush()
		return buf.Bytes(), "text/csv; charset=utf-8", w.Error()
	default:
		var buf bytes.Buffer
		buf.WriteString("[")
		for i, r := range records {
			if i > 0 {
				buf.WriteString(",")
			}
			buf.WriteString("\n  {")
			for j, f := range def.Fields {
				if j > 0 {
					buf.WriteString(", ")
				}
				k, _ := json.Marshal(f.Name)
				v, err := json.Marshal(r.Values[f.Name])
				if err != nil {
					return nil, "", err
				}
				buf.Write(k)
				buf.WriteString(": ")
				buf.Write(v)
			}
			buf.WriteString("}")
		}
		if len(records) > 0 {
			buf.WriteString("\n")
		}
		buf.WriteString("]\n")
		return buf.Bytes(), "application/json", nil
	}
}
