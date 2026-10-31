package extract

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

// transformFunc converts a value. base is the URL of the page the value came
// from. Returning an error discards the candidate value.
type transformFunc func(v any, base *url.URL) (any, error)

type transformSpec struct {
	doc     string
	argDoc  string // empty means the transform takes no argument
	compile func(arg any) (transformFunc, error)
}

// Transforms is the closed set of named transforms the DSL supports.
var transforms = map[string]transformSpec{
	"trim":               {doc: "Trim surrounding whitespace.", compile: noArg(textFn(strings.TrimSpace))},
	"lower":              {doc: "Lower-case text.", compile: noArg(textFn(strings.ToLower))},
	"upper":              {doc: "Upper-case text.", compile: noArg(textFn(strings.ToUpper))},
	"collapseWhitespace": {doc: "Collapse runs of whitespace into single spaces.", compile: noArg(textFn(cleanText))},
	"regex": {doc: "Keep the first capture group (or whole match) of a regex; no match discards the value.",
		argDoc: "pattern string", compile: compileRegex},
	"replace": {doc: "Literal replace of all occurrences.", argDoc: "{from: string, to: string}", compile: compileReplace},
	"map": {doc: "Map exact (case-insensitive, trimmed) text to a value; unmapped values are discarded.",
		argDoc: "object of text -> value", compile: compileMap},
	"number":   {doc: "Extract the first number in the text, removing thousands separators (\"12,345 km\" -> \"12345\").", compile: noArg(numberFn)},
	"currency": {doc: "Like number, after stripping currency symbols and codes (\"$89,990 drive away\" -> \"89990\").", compile: noArg(numberFn)},
	"integer":  {doc: "Convert to an integer. Fails on non-integral values.", compile: noArg(integerFn)},
	"decimal":  {doc: "Convert to a decimal number.", compile: noArg(decimalFn)},
	"boolean":  {doc: "Convert true/false/yes/no/y/n/1/0 to a boolean.", compile: noArg(booleanFn)},
	"absoluteUrl": {doc: "Resolve a relative URL against the page it was found on.", compile: noArg(func(v any, base *url.URL) (any, error) {
		ref, err := url.Parse(strings.TrimSpace(Stringify(v)))
		if err != nil {
			return nil, err
		}
		if base == nil {
			return ref.String(), nil
		}
		return base.ResolveReference(ref).String(), nil
	})},
}

// TransformNames returns supported transform names in sorted order.
func TransformNames() []string {
	names := make([]string, 0, len(transforms))
	for n := range transforms {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func compileTransforms(ts []contracts.Transform, path string, issues *Issues) []transformFunc {
	out := make([]transformFunc, 0, len(ts))
	for i, t := range ts {
		p := fmt.Sprintf("%s[%d]", path, i)
		spec, ok := transforms[t.Name]
		if !ok {
			issues.Errorf(p, "unknown transform %q (supported: %s)", t.Name, strings.Join(TransformNames(), ", "))
			continue
		}
		if spec.argDoc == "" && t.Arg != nil {
			issues.Errorf(p, "transform %q takes no argument", t.Name)
			continue
		}
		if spec.argDoc != "" && t.Arg == nil {
			issues.Errorf(p, "transform %q requires an argument: %s", t.Name, spec.argDoc)
			continue
		}
		fn, err := spec.compile(t.Arg)
		if err != nil {
			issues.Errorf(p, "transform %q: %v", t.Name, err)
			continue
		}
		out = append(out, fn)
	}
	return out
}

func applyTransforms(fns []transformFunc, v any, base *url.URL) (any, error) {
	var err error
	for _, fn := range fns {
		if v, err = fn(v, base); err != nil {
			return nil, err
		}
	}
	return v, nil
}

func noArg(fn transformFunc) func(any) (transformFunc, error) {
	return func(any) (transformFunc, error) { return fn, nil }
}

func textFn(f func(string) string) transformFunc {
	return func(v any, _ *url.URL) (any, error) { return f(Stringify(v)), nil }
}

func compileRegex(arg any) (transformFunc, error) {
	pattern, ok := arg.(string)
	if !ok {
		return nil, errors.New("argument must be a pattern string")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	return func(v any, _ *url.URL) (any, error) {
		s := Stringify(v)
		m := re.FindStringSubmatch(s)
		if m == nil {
			return nil, fmt.Errorf("regex %q did not match %q", pattern, s)
		}
		if len(m) > 1 {
			return m[1], nil
		}
		return m[0], nil
	}, nil
}

func compileReplace(arg any) (transformFunc, error) {
	m, ok := arg.(map[string]any)
	if !ok {
		return nil, errors.New("argument must be {from, to}")
	}
	from, ok1 := m["from"].(string)
	to, ok2 := m["to"].(string)
	if !ok1 || from == "" || (m["to"] != nil && !ok2) || len(m) > 2 {
		return nil, errors.New("argument must be {from: non-empty string, to: string}")
	}
	return func(v any, _ *url.URL) (any, error) { return strings.ReplaceAll(Stringify(v), from, to), nil }, nil
}

func compileMap(arg any) (transformFunc, error) {
	m, ok := arg.(map[string]any)
	if !ok || len(m) == 0 {
		return nil, errors.New("argument must be a non-empty object of text -> value")
	}
	lookup := make(map[string]any, len(m))
	for k, val := range m {
		lookup[strings.ToLower(strings.TrimSpace(k))] = val
	}
	return func(v any, _ *url.URL) (any, error) {
		key := strings.ToLower(strings.TrimSpace(Stringify(v)))
		if out, ok := lookup[key]; ok {
			return out, nil
		}
		return nil, fmt.Errorf("value %q is not in map", Stringify(v))
	}, nil
}

var numberRe = regexp.MustCompile(`-?\d[\d,]*(?:\.\d+)?`)

func numberFn(v any, _ *url.URL) (any, error) {
	if f, ok := asFloat(v); ok {
		return strconv.FormatFloat(f, 'f', -1, 64), nil
	}
	s := Stringify(v)
	m := numberRe.FindString(s)
	if m == "" {
		return nil, fmt.Errorf("no number in %q", s)
	}
	return strings.ReplaceAll(m, ",", ""), nil
}

func integerFn(v any, _ *url.URL) (any, error) {
	if f, ok := asFloat(v); ok {
		if f != math.Trunc(f) {
			return nil, fmt.Errorf("%v is not an integer", f)
		}
		return int64(f), nil
	}
	s := strings.ReplaceAll(strings.TrimSpace(Stringify(v)), ",", "")
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f != math.Trunc(f) {
		return nil, fmt.Errorf("%q is not an integer", Stringify(v))
	}
	return int64(f), nil
}

func decimalFn(v any, _ *url.URL) (any, error) {
	if f, ok := asFloat(v); ok {
		return f, nil
	}
	s := strings.ReplaceAll(strings.TrimSpace(Stringify(v)), ",", "")
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, fmt.Errorf("%q is not a decimal", Stringify(v))
	}
	return f, nil
}

func booleanFn(v any, _ *url.URL) (any, error) {
	if b, ok := v.(bool); ok {
		return b, nil
	}
	switch strings.ToLower(strings.TrimSpace(Stringify(v))) {
	case "true", "yes", "y", "1":
		return true, nil
	case "false", "no", "n", "0":
		return false, nil
	}
	return nil, fmt.Errorf("%q is not a boolean", Stringify(v))
}
