// Package query maps the generic deterministic query onto source requests
// and applies query filters to extracted records.
package query

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/extract"
	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

// BuildURL produces a source's first request URL from the generic query
// params. It returns warnings for query params the source cannot use, so a
// user can see which constraints rely on record filters instead.
func BuildURL(spec contracts.RequestSpec, params map[string]any) (string, []string, error) {
	u, err := url.Parse(spec.URL)
	if err != nil {
		return "", nil, err
	}
	q := u.Query()
	var warnings []string
	used := map[string]bool{}
	for _, name := range sortedKeys(spec.Params) {
		m := spec.Params[name]
		if m.Value != nil {
			q.Set(name, extract.Stringify(m.Value))
			continue
		}
		used[m.From] = true
		v, ok := params[m.From]
		if !ok || v == nil {
			continue
		}
		text := extract.Stringify(v)
		if m.Map != nil {
			mapped, ok := lookup(m.Map, text)
			if !ok {
				switch m.Unmapped {
				case "pass":
					mapped = text
				case "fail":
					return "", warnings, fmt.Errorf("query param %s=%q has no mapping for request param %s", m.From, text, name)
				default:
					warnings = append(warnings, fmt.Sprintf("query param %s=%q has no mapping for request param %s; omitted", m.From, text, name))
					continue
				}
			}
			text = mapped
		}
		q.Set(name, text)
	}
	for _, name := range sortedKeys(params) {
		if !used[name] {
			warnings = append(warnings, fmt.Sprintf("query param %q is not supported by this source's request mapping; relying on record filters", name))
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), warnings, nil
}

// WithParam returns rawURL with one query parameter set.
func WithParam(rawURL, name string, value int) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set(name, strconv.Itoa(value))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func lookup(m map[string]string, key string) (string, bool) {
	if v, ok := m[key]; ok {
		return v, true
	}
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return "", false
}

var ops = map[string]bool{"eq": true, "ne": true, "gt": true, "gte": true, "lt": true, "lte": true, "in": true, "contains": true}

// ValidateFilters checks filters against the output fields.
func ValidateFilters(filters []contracts.Filter, fields contracts.OutputFields, issues *extract.Issues) {
	known := map[string]string{}
	for _, f := range fields {
		known[f.Name] = f.Type
	}
	for i, f := range filters {
		path := fmt.Sprintf("query.filters[%d]", i)
		if _, ok := known[f.Field]; !ok {
			issues.Errorf(path+".field", "filter field %q is not an output field", f.Field)
		}
		if !ops[f.Op] {
			issues.Errorf(path+".op", "unknown op %q (supported: eq, ne, gt, gte, lt, lte, in, contains)", f.Op)
		}
		if f.Op == "in" {
			if _, ok := f.Value.([]any); !ok {
				issues.Errorf(path+".value", "op in requires a list value")
			}
		}
		if f.Missing != "" && f.Missing != "reject" && f.Missing != "pass" {
			issues.Errorf(path+".missing", "missing must be reject or pass")
		}
	}
}

// Apply returns a rejection reason, or "" when the record passes all filters.
func Apply(filters []contracts.Filter, values map[string]any) string {
	for _, f := range filters {
		v, ok := values[f.Field]
		if !ok || v == nil {
			if f.Missing == "pass" {
				continue
			}
			return fmt.Sprintf("filter %s %s %v: value missing", f.Field, f.Op, f.Value)
		}
		if !test(f, v) {
			return fmt.Sprintf("filter %s %s %v: got %v", f.Field, f.Op, f.Value, v)
		}
	}
	return ""
}

func test(f contracts.Filter, v any) bool {
	switch f.Op {
	case "eq":
		return compare(v, f.Value) == 0
	case "ne":
		return compare(v, f.Value) != 0
	case "gt":
		return compare(v, f.Value) > 0
	case "gte":
		return compare(v, f.Value) >= 0
	case "lt":
		return compare(v, f.Value) == -1
	case "lte":
		c := compare(v, f.Value)
		return c == -1 || c == 0
	case "in":
		list, _ := f.Value.([]any)
		for _, el := range list {
			if compare(v, el) == 0 {
				return true
			}
		}
		return false
	case "contains":
		return strings.Contains(strings.ToLower(extract.Stringify(v)), strings.ToLower(extract.Stringify(f.Value)))
	}
	return false
}

const incomparable = -2

// compare returns -1, 0, 1, or incomparable (-2, which is never > 0 or == 0).
func compare(a, b any) int {
	fa, aok := num(a)
	fb, bok := num(b)
	if aok && bok {
		switch {
		case fa < fb:
			return -1
		case fa > fb:
			return 1
		}
		return 0
	}
	if aok != bok {
		return incomparable
	}
	sa := strings.ToLower(extract.Stringify(a))
	sb := strings.ToLower(extract.Stringify(b))
	return strings.Compare(sa, sb)
}

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	}
	return 0, false
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
