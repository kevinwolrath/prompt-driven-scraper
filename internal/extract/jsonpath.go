package extract

import (
	"strconv"
	"strings"
)

// EvalPath evaluates a dot path ("offers.price", "items.0.url") against
// decoded JSON. Numeric segments index arrays; a non-numeric segment applied
// to an array is applied to every element, so results are flattened in
// document order. A segment may end in a predicate, "pricing[type=driveaway]",
// keeping only objects whose key equals the value (case-insensitive). An
// empty path or "$" returns the value itself.
func EvalPath(v any, path string) []any {
	path = strings.TrimSpace(path)
	if path == "" || path == "$" {
		if v == nil {
			return nil
		}
		return []any{v}
	}
	current := []any{v}
	for _, seg := range strings.Split(strings.TrimPrefix(path, "$."), ".") {
		key, pk, pv, hasPred := splitPredicate(seg)
		var next []any
		for _, c := range current {
			if key == "" {
				next = append(next, c)
				continue
			}
			next = append(next, step(c, key)...)
		}
		if hasPred {
			next = filterPredicate(next, pk, pv)
		}
		current = next
		if len(current) == 0 {
			return nil
		}
	}
	return current
}

func splitPredicate(seg string) (key, pk, pv string, ok bool) {
	open := strings.IndexByte(seg, '[')
	if open < 0 || !strings.HasSuffix(seg, "]") {
		return seg, "", "", false
	}
	pred := seg[open+1 : len(seg)-1]
	eq := strings.IndexByte(pred, '=')
	if eq < 0 {
		return seg, "", "", false
	}
	return seg[:open], strings.TrimSpace(pred[:eq]), strings.Trim(strings.TrimSpace(pred[eq+1:]), `"'`), true
}

func filterPredicate(values []any, key, want string) []any {
	var out []any
	for _, v := range flatten(values) {
		if m, ok := v.(map[string]any); ok && strings.EqualFold(Stringify(m[key]), want) {
			out = append(out, m)
		}
	}
	return out
}

func step(v any, seg string) []any {
	switch x := v.(type) {
	case map[string]any:
		if val, ok := x[seg]; ok && val != nil {
			return []any{val}
		}
	case []any:
		if idx, err := strconv.Atoi(seg); err == nil {
			if idx >= 0 && idx < len(x) && x[idx] != nil {
				return []any{x[idx]}
			}
			return nil
		}
		var out []any
		for _, el := range x {
			out = append(out, step(el, seg)...)
		}
		return out
	}
	return nil
}

// flatten expands a single array result into its elements.
func flatten(values []any) []any {
	var out []any
	for _, v := range values {
		if arr, ok := v.([]any); ok {
			for _, el := range arr {
				if el != nil {
					out = append(out, el)
				}
			}
			continue
		}
		out = append(out, v)
	}
	return out
}

// Stringify renders a scalar JSON value as text for matching and transforms.
func Stringify(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}
