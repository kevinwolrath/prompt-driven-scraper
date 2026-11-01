package extract

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/andybalholm/cascadia"

	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

// Context names for Candidate.From.
const (
	FromItem    = "item"
	FromListing = "listing"
	FromDetail  = "detail"
)

var builtins = map[string]bool{"pageUrl": true, "detailUrl": true, "listingUrl": true, "sourceId": true}

// Profile is a validated, compiled source profile ready for execution.
type Profile struct {
	Source       contracts.SourceProfile
	Format       string
	ignore       []cascadia.Selector
	itemsCSS     cascadia.Selector
	itemsPath    string
	skip         []*matcher
	DetailLink   *Rule
	FollowDetail bool
	Next         *Rule
	TotalResults *Rule
	TotalPages   *Rule
	// Fields in sorted name order for deterministic evaluation.
	Fields []*Rule
	// Strategies are the acquisition strategies in order; never empty.
	Strategies []contracts.AcquisitionStrategy
}

// Rule is a compiled FieldRule.
type Rule struct {
	Name       string
	candidates []*candidate
	reject     []*matcher
	prefer     []*matcher
	transforms []transformFunc
}

type candidate struct {
	src    contracts.Candidate
	from   string
	css    cascadia.Selector
	origin string
}

// Compile validates a source profile and compiles selectors, regexes and
// transforms. The returned profile is nil when there are errors.
func Compile(p contracts.SourceProfile) (*Profile, Issues) {
	var issues Issues
	c := &Profile{Source: p, Format: p.Format}
	if c.Format == "" {
		c.Format = "html"
	}
	if c.Format != "html" && c.Format != "json" {
		issues.Errorf("format", "format must be html or json, got %q", p.Format)
	}
	if strings.TrimSpace(p.ID) == "" {
		issues.Errorf("id", "profile id is required")
	}
	if u, err := url.Parse(p.Request.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		issues.Errorf("request.url", "request.url must be an absolute http(s) URL, got %q", p.Request.URL)
	}
	for _, name := range sortedKeys(p.Request.Params) {
		m := p.Request.Params[name]
		path := "request.params." + name
		if (m.From == "") == (m.Value == nil) {
			issues.Errorf(path, "set exactly one of from or value")
		}
		if m.Map != nil && m.From == "" {
			issues.Errorf(path+".map", "map requires from")
		}
		switch m.Unmapped {
		case "", "omit", "pass", "fail":
		default:
			issues.Errorf(path+".unmapped", "unmapped must be omit, pass or fail")
		}
	}
	c.Strategies = compileAcquisition(p.Acquisition, c.Format, &issues)
	if p.Limits.MaxPages < 0 || p.Limits.MaxRecords < 0 {
		issues.Errorf("limits", "limits must not be negative")
	}

	for i, s := range p.Ignore {
		sel, err := cascadia.Compile(s)
		if err != nil {
			issues.Errorf(fmt.Sprintf("ignore[%d]", i), "invalid CSS selector: %v", err)
			continue
		}
		if c.Format == "json" {
			issues.Errorf(fmt.Sprintf("ignore[%d]", i), "ignore selectors only apply to html sources")
		}
		c.ignore = append(c.ignore, sel)
	}

	// Listing
	if strings.TrimSpace(p.Listing.Items) == "" {
		issues.Errorf("listing.items", "listing.items is required (a CSS selector for html, a JSON path for json)")
	} else if c.Format == "html" {
		sel, err := cascadia.Compile(p.Listing.Items)
		if err != nil {
			issues.Errorf("listing.items", "invalid CSS selector: %v", err)
		}
		c.itemsCSS = sel
	} else {
		c.itemsPath = p.Listing.Items
	}
	for i, m := range p.Listing.Skip {
		c.skip = append(c.skip, compileMatcher(m, fmt.Sprintf("listing.skip[%d]", i), false, &issues))
	}
	c.FollowDetail = p.Listing.FollowDetail
	if p.Listing.DetailLink != nil {
		c.DetailLink = compileRule("detailLink", *p.Listing.DetailLink, "listing.detailLink", c.Format, false, &issues, FromItem)
	}
	if c.FollowDetail && c.DetailLink == nil {
		issues.Errorf("listing.followDetail", "followDetail requires listing.detailLink")
	}

	// Pagination
	if pg := p.Pagination; pg != nil {
		if (pg.Next == nil) == (pg.Param == nil) && (pg.Next != nil || pg.Param != nil) {
			issues.Errorf("pagination", "set only one of pagination.next or pagination.param")
		}
		if pg.Next != nil {
			c.Next = compileRule("next", *pg.Next, "pagination.next", c.Format, false, &issues, FromListing)
		}
		if pg.Param != nil && strings.TrimSpace(pg.Param.Name) == "" {
			issues.Errorf("pagination.param.name", "pagination.param.name is required")
		}
		if pg.TotalResults != nil {
			c.TotalResults = compileRule("totalResults", *pg.TotalResults, "pagination.totalResults", c.Format, false, &issues, FromListing)
			if pg.PageSize <= 0 {
				issues.Errorf("pagination.pageSize", "pageSize is required with totalResults")
			}
		}
		if pg.TotalPages != nil {
			c.TotalPages = compileRule("totalPages", *pg.TotalPages, "pagination.totalPages", c.Format, false, &issues, FromListing)
		}
		if pg.Param == nil && pg.Next == nil && (pg.TotalPages != nil || pg.TotalResults != nil) {
			issues.Warnf("pagination", "page totals are set but no next/param pagination is configured")
		}
	}

	// Fields
	if len(p.Fields) == 0 {
		issues.Errorf("fields", "at least one field mapping is required")
	}
	for _, name := range sortedKeys(p.Fields) {
		r := compileRule(name, p.Fields[name], "fields."+name, c.Format, c.FollowDetail, &issues, FromItem)
		c.Fields = append(c.Fields, r)
	}

	if issues.HasErrors() {
		return nil, issues
	}
	return c, issues
}

// compileAcquisition validates acquisition strategies; none means plain HTTP.
func compileAcquisition(a *contracts.Acquisition, format string, issues *Issues) []contracts.AcquisitionStrategy {
	if a == nil {
		return []contracts.AcquisitionStrategy{{Type: "http"}}
	}
	if len(a.Strategies) == 0 {
		issues.Errorf("acquisition.strategies", "list at least one strategy (http or browser), or omit acquisition for plain HTTP")
	}
	seen := map[string]bool{}
	for i, st := range a.Strategies {
		path := fmt.Sprintf("acquisition.strategies[%d]", i)
		switch st.Type {
		case "http":
			if st.WaitUntil != "" || st.WaitFor != "" {
				issues.Errorf(path, "waitUntil and waitFor only apply to browser strategies")
			}
		case "browser", "user-browser":
			if format == "json" {
				issues.Errorf(path+".type", "browser acquisition returns rendered HTML; it cannot be used with format: json")
			}
			switch st.WaitUntil {
			case "", "load", "domcontentloaded":
			default:
				issues.Errorf(path+".waitUntil", "waitUntil must be load or domcontentloaded, got %q", st.WaitUntil)
			}
			if st.WaitFor != "" {
				if _, err := cascadia.Compile(st.WaitFor); err != nil {
					issues.Errorf(path+".waitFor", "invalid CSS selector: %v", err)
				}
			}
		default:
			issues.Errorf(path+".type", "type must be http, browser or user-browser, got %q", st.Type)
		}
		if seen[st.Type] {
			issues.Errorf(path+".type", "strategy %q is listed twice", st.Type)
		}
		seen[st.Type] = true
		if st.TimeoutS < 0 {
			issues.Errorf(path+".timeoutSeconds", "timeoutSeconds must not be negative")
		}
	}
	return a.Strategies
}

// compileRule compiles a FieldRule. contextual rules (detailLink, next,
// totals) may only read from defaultFrom.
func compileRule(name string, fr contracts.FieldRule, path, format string, detail bool, issues *Issues, defaultFrom string) *Rule {
	r := &Rule{Name: name}
	cands := fr.Candidates
	inline := fr.Candidate != (contracts.Candidate{})
	switch {
	case inline && len(cands) > 0:
		issues.Errorf(path, "use either an inline candidate or candidates, not both")
	case inline:
		cands = []contracts.Candidate{fr.Candidate}
	case len(cands) == 0:
		issues.Errorf(path, "at least one candidate is required")
	}
	for i, cand := range cands {
		cp := fmt.Sprintf("%s.candidates[%d]", path, i)
		if inline {
			cp = path
		}
		if c := compileCandidate(cand, cp, format, detail, defaultFrom, issues); c != nil {
			r.candidates = append(r.candidates, c)
		}
	}
	for i, m := range fr.Reject {
		r.reject = append(r.reject, compileMatcher(m, fmt.Sprintf("%s.reject[%d]", path, i), true, issues))
	}
	for i, m := range fr.Prefer {
		r.prefer = append(r.prefer, compileMatcher(m, fmt.Sprintf("%s.prefer[%d]", path, i), true, issues))
	}
	r.transforms = compileTransforms(fr.Transform, path+".transform", issues)
	return r
}

func compileCandidate(cand contracts.Candidate, path, format string, detail bool, defaultFrom string, issues *Issues) *candidate {
	c := &candidate{src: cand, from: cand.From}
	if c.from == "" {
		c.from = defaultFrom
	}
	switch c.from {
	case FromItem, FromListing:
	case FromDetail:
		if !detail {
			issues.Errorf(path+".from", "from: detail requires listing.followDetail: true")
		}
	default:
		issues.Errorf(path+".from", "from must be item, listing or detail, got %q", cand.From)
	}
	if defaultFrom == FromListing && c.from == FromItem {
		issues.Errorf(path+".from", "this rule is evaluated per page; use from: listing")
	}

	kinds := 0
	if cand.Css != "" || cand.Attr != "" {
		kinds++
	}
	if cand.JSONLD != "" {
		kinds++
	}
	if cand.JSON != "" && cand.Css == "" && cand.Attr == "" {
		kinds++
	}
	if cand.Meta != "" {
		kinds++
	}
	if cand.Const != nil {
		kinds++
	}
	if cand.Builtin != "" {
		kinds++
	}
	if kinds != 1 {
		issues.Errorf(path, "a candidate must use exactly one of css/attr, jsonld, json, meta, const, builtin")
		return nil
	}
	if cand.JSONLDType != "" && cand.JSONLD == "" {
		issues.Errorf(path+".jsonldType", "jsonldType requires jsonld")
	}
	htmlOnly := cand.Css != "" || cand.Attr != "" || cand.JSONLD != "" || cand.Meta != ""
	if format == "json" && htmlOnly {
		issues.Errorf(path, "css/attr/jsonld/meta candidates require an html source; use json")
	}
	if cand.Css != "" {
		sel, err := cascadia.Compile(cand.Css)
		if err != nil {
			issues.Errorf(path+".css", "invalid CSS selector: %v", err)
			return nil
		}
		c.css = sel
	}
	if cand.Builtin != "" && !builtins[cand.Builtin] {
		issues.Errorf(path+".builtin", "unknown builtin %q (supported: detailUrl, listingUrl, pageUrl, sourceId)", cand.Builtin)
	}
	c.origin = describeCandidate(cand, c.from)
	return c
}

func describeCandidate(c contracts.Candidate, from string) string {
	var parts []string
	switch {
	case c.Css != "":
		parts = append(parts, "css "+c.Css)
	case c.JSONLD != "":
		parts = append(parts, "jsonld "+c.JSONLD)
	case c.Meta != "":
		parts = append(parts, "meta "+c.Meta)
	case c.Const != nil:
		parts = append(parts, fmt.Sprintf("const %v", c.Const))
	case c.Builtin != "":
		parts = append(parts, "builtin "+c.Builtin)
	}
	if c.Attr != "" {
		parts = append(parts, "@"+c.Attr)
	}
	if c.JSON != "" {
		parts = append(parts, "json "+c.JSON)
	}
	return from + ": " + strings.Join(parts, " ")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
