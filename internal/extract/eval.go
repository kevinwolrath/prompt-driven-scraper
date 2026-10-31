package extract

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

// Scope supplies the contexts candidates read from. Detail is nil until the
// detail page has been fetched.
type Scope struct {
	SourceID  string
	Item      *Item
	Listing   *Page
	Detail    *Page
	DetailURL string
}

// Result is one surviving candidate value. Results are serialisable so
// listing-page results can be checkpointed while a detail page is pending.
type Result struct {
	Index     int    `json:"i"`
	Value     any    `json:"v"`
	Preferred bool   `json:"p,omitempty"`
	Origin    string `json:"o"`
}

type raw struct {
	text  string
	value any
	node  *html.Node
}

// Evaluate runs every candidate whose context is in froms and returns the
// surviving values, plus human-readable notes about discarded values.
func (r *Rule) Evaluate(s Scope, froms ...string) ([]Result, []string) {
	var results []Result
	var notes []string
	for i, c := range r.candidates {
		if !contains(froms, c.from) {
			continue
		}
		raws, base := c.extract(s)
		for _, rv := range raws {
			v, err := applyTransforms(r.transforms, rv.value, base)
			if err != nil {
				notes = append(notes, fmt.Sprintf("%s [%s]: %v", r.Name, c.origin, err))
				continue
			}
			if v == nil || v == "" {
				continue
			}
			sub := subject{text: rv.text, node: rv.node, value: v, within: true}
			if m := anyMatch(r.reject, sub); m != nil {
				notes = append(notes, fmt.Sprintf("%s [%s]: rejected %q (%s)", r.Name, c.origin, truncate(rv.text, 60), m.describe()))
				continue
			}
			results = append(results, Result{Index: i, Value: v, Preferred: anyMatch(r.prefer, sub) != nil, Origin: c.origin})
		}
	}
	return results, notes
}

// Choose applies precedence: candidate order, except that a value matching a
// prefer rule beats any value that does not.
func Choose(results []Result) (Result, bool) {
	if len(results) == 0 {
		return Result{}, false
	}
	sorted := append([]Result(nil), results...)
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].Index < sorted[b].Index })
	for _, r := range sorted {
		if r.Preferred {
			return r, true
		}
	}
	return sorted[0], true
}

// First evaluates a rule and returns the chosen value.
func (r *Rule) First(s Scope, froms ...string) (any, bool) {
	res, _ := r.Evaluate(s, froms...)
	chosen, ok := Choose(res)
	return chosen.Value, ok
}

func (c *candidate) extract(s Scope) ([]raw, *url.URL) {
	var page *Page
	var sel *goquery.Selection
	var jsonCtx any
	switch c.from {
	case FromItem:
		if s.Item == nil {
			return nil, nil
		}
		page, sel, jsonCtx = s.Item.Page, s.Item.Sel, s.Item.JSON
	case FromListing:
		page = s.Listing
	case FromDetail:
		page = s.Detail
	}
	if page == nil {
		return nil, nil
	}
	if sel == nil && page.HTML != nil {
		sel = page.HTML.Selection
	}
	if jsonCtx == nil && c.from != FromItem {
		jsonCtx = page.JSON
	}
	base := page.URL
	src := c.src

	switch {
	case src.Const != nil:
		return []raw{{text: Stringify(src.Const), value: src.Const}}, base
	case src.Builtin != "":
		var v string
		switch src.Builtin {
		case "pageUrl":
			v = page.URL.String()
		case "listingUrl":
			if s.Listing != nil {
				v = s.Listing.URL.String()
			}
		case "detailUrl":
			v = s.DetailURL
		case "sourceId":
			v = s.SourceID
		}
		if v == "" {
			return nil, base
		}
		return []raw{{text: v, value: v}}, base
	case src.JSONLD != "":
		if sel == nil {
			return nil, base
		}
		var out []raw
		for _, obj := range jsonLD(sel) {
			if src.JSONLDType != "" && !ldHasType(obj, src.JSONLDType) {
				continue
			}
			out = append(out, scalars(EvalPath(obj, src.JSONLD), nil)...)
		}
		return out, base
	case src.Meta != "":
		if sel == nil {
			return nil, base
		}
		var out []raw
		q := fmt.Sprintf(`meta[name=%q], meta[property=%q], meta[itemprop=%q]`, src.Meta, src.Meta, src.Meta)
		sel.Find(q).Each(func(_ int, m *goquery.Selection) {
			if v, ok := m.Attr("content"); ok {
				out = append(out, raw{text: v, value: v, node: m.Get(0)})
			}
		})
		return out, base
	case src.Css != "" || src.Attr != "":
		if sel == nil {
			return nil, base
		}
		var nodes []*goquery.Selection
		if src.Css == "" {
			nodes = []*goquery.Selection{sel.First()}
		} else {
			sel.FindMatcher(c.css).Each(func(_ int, n *goquery.Selection) { nodes = append(nodes, n) })
		}
		var out []raw
		for _, n := range nodes {
			var text string
			if src.Attr != "" {
				v, ok := n.Attr(src.Attr)
				if !ok {
					continue
				}
				text = v
			} else {
				text = cleanText(n.Text())
			}
			if src.JSON != "" {
				var doc any
				if err := json.Unmarshal([]byte(text), &doc); err != nil {
					continue
				}
				out = append(out, scalars(EvalPath(doc, src.JSON), n.Get(0))...)
				continue
			}
			out = append(out, raw{text: text, value: text, node: n.Get(0)})
		}
		return out, base
	case src.JSON != "":
		if jsonCtx == nil {
			return nil, base
		}
		return scalars(EvalPath(jsonCtx, src.JSON), nil), base
	}
	return nil, base
}

func scalars(values []any, node *html.Node) []raw {
	var out []raw
	for _, v := range flatten(values) {
		switch v.(type) {
		case map[string]any, []any:
			continue
		}
		out = append(out, raw{text: Stringify(v), value: v, node: node})
	}
	return out
}

// Items returns the listing items on a page, dropping those matching
// listing.skip. Skipped items are returned separately with their reason.
func (p *Profile) Items(page *Page) (items []*Item, skipped []string) {
	var all []*Item
	if page.HTML != nil && p.itemsCSS != nil {
		page.HTML.FindMatcher(p.itemsCSS).Each(func(i int, s *goquery.Selection) {
			all = append(all, &Item{Page: page, Sel: s, Index: i})
		})
	} else if page.JSON != nil {
		for i, v := range flatten(EvalPath(page.JSON, p.itemsPath)) {
			all = append(all, &Item{Page: page, JSON: v, Index: i})
		}
	}
	for _, it := range all {
		sub := subject{text: it.Text()}
		if it.Sel != nil {
			sub.node = it.Sel.Get(0)
		}
		if m := anyMatch(p.skip, sub); m != nil {
			skipped = append(skipped, fmt.Sprintf("item %d skipped by listing.skip (%s)", it.Index+1, m.describe()))
			continue
		}
		items = append(items, it)
	}
	return items, skipped
}

// ParsePage parses a body using this profile's format and ignore rules.
func (p *Profile) ParsePage(rawURL string, body []byte) (*Page, error) {
	return ParsePage(p.Format, rawURL, body, p.ignore)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
