package extract

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"

	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

// matcher is a compiled contracts.Matcher. Every property that is set must
// hold (AND); a list of matchers matches when any one matches (OR).
type matcher struct {
	src      contracts.Matcher
	contains string
	equals   string
	regex    *regexp.Regexp
	css      cascadia.Selector
	min, max *float64
	fold     bool
}

// subject is what a matcher is evaluated against.
type subject struct {
	text  string     // raw extracted text
	node  *html.Node // element the value came from (may be nil)
	value any        // transformed value (may be nil before transforms)
	// within selects css semantics: true means the node or an ancestor must
	// match (field rules); false means the node or a descendant must match
	// (listing.skip).
	within bool
}

func compileMatcher(m contracts.Matcher, path string, allowNumeric bool, issues *Issues) *matcher {
	c := &matcher{src: m, contains: m.Contains, equals: m.Equals, min: m.Min, max: m.Max, fold: !m.CaseSensitive}
	if m.Contains == "" && m.Equals == "" && m.Regex == "" && m.Css == "" && m.Min == nil && m.Max == nil {
		issues.Errorf(path, "matcher must set at least one of contains, equals, regex, css, min, max")
		return nil
	}
	if !allowNumeric && (m.Min != nil || m.Max != nil) {
		issues.Errorf(path, "min/max are only valid in field reject/prefer rules")
	}
	if c.fold {
		c.contains = strings.ToLower(c.contains)
		c.equals = strings.ToLower(c.equals)
	}
	if m.Regex != "" {
		expr := m.Regex
		if c.fold && !strings.HasPrefix(expr, "(?") {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			issues.Errorf(path+".regex", "invalid regex: %v", err)
		}
		c.regex = re
	}
	if m.Css != "" {
		sel, err := cascadia.Compile(m.Css)
		if err != nil {
			issues.Errorf(path+".css", "invalid CSS selector: %v", err)
		}
		c.css = sel
	}
	return c
}

func (m *matcher) match(s subject) bool {
	text := s.text
	if m.fold {
		text = strings.ToLower(text)
	}
	if m.contains != "" && !strings.Contains(text, m.contains) {
		return false
	}
	if m.equals != "" && strings.TrimSpace(text) != m.equals {
		return false
	}
	if m.regex != nil && !m.regex.MatchString(s.text) {
		return false
	}
	if m.css != nil {
		if s.node == nil || !cssMatch(m.css, s.node, s.within) {
			return false
		}
	}
	if m.min != nil || m.max != nil {
		f, ok := asFloat(s.value)
		if !ok {
			return false
		}
		if m.min != nil && f < *m.min {
			return false
		}
		if m.max != nil && f > *m.max {
			return false
		}
	}
	return true
}

func cssMatch(sel cascadia.Selector, n *html.Node, within bool) bool {
	if within {
		for p := n; p != nil; p = p.Parent {
			if p.Type == html.ElementNode && sel.Match(p) {
				return true
			}
		}
		return false
	}
	return sel.Match(n) || sel.MatchFirst(n) != nil
}

func (m *matcher) describe() string {
	var parts []string
	if m.src.Contains != "" {
		parts = append(parts, fmt.Sprintf("contains %q", m.src.Contains))
	}
	if m.src.Equals != "" {
		parts = append(parts, fmt.Sprintf("equals %q", m.src.Equals))
	}
	if m.src.Regex != "" {
		parts = append(parts, fmt.Sprintf("regex %q", m.src.Regex))
	}
	if m.src.Css != "" {
		parts = append(parts, fmt.Sprintf("css %q", m.src.Css))
	}
	if m.min != nil {
		parts = append(parts, fmt.Sprintf("min %v", *m.min))
	}
	if m.max != nil {
		parts = append(parts, fmt.Sprintf("max %v", *m.max))
	}
	return strings.Join(parts, " and ")
}

func anyMatch(ms []*matcher, s subject) *matcher {
	for _, m := range ms {
		if m != nil && m.match(s) {
			return m
		}
	}
	return nil
}

func asFloat(v any) (float64, bool) {
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
