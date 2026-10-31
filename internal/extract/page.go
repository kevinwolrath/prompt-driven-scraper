package extract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/andybalholm/cascadia"
)

// Page is a fetched, parsed document.
type Page struct {
	URL    *url.URL
	Format string
	HTML   *goquery.Document
	JSON   any
}

// ParsePage parses a response body. ignore selectors are removed from HTML
// documents before any extraction happens.
func ParsePage(format, rawURL string, body []byte, ignore []cascadia.Selector) (*Page, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	p := &Page{URL: u, Format: format}
	if format == "json" {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("parse json: %w", err)
		}
		p.JSON = normaliseNumbers(v)
		return p, nil
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}
	for _, sel := range ignore {
		doc.FindMatcher(sel).Remove()
	}
	p.HTML = doc
	return p, nil
}

func normaliseNumbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return x.String()
		}
		return f
	case map[string]any:
		for k, val := range x {
			x[k] = normaliseNumbers(val)
		}
	case []any:
		for i := range x {
			x[i] = normaliseNumbers(x[i])
		}
	}
	return v
}

// Item is one candidate record on a listing page.
type Item struct {
	Page  *Page
	Sel   *goquery.Selection
	JSON  any
	Index int
}

func (i *Item) Text() string {
	if i.Sel != nil {
		return cleanText(i.Sel.Text())
	}
	b, _ := json.Marshal(i.JSON)
	return string(b)
}

func cleanText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// jsonLD returns schema.org objects from JSON-LD scripts within sel, with
// top-level arrays and @graph containers flattened.
func jsonLD(sel *goquery.Selection) []any {
	var out []any
	sel.Find(`script[type="application/ld+json"]`).Each(func(_ int, s *goquery.Selection) {
		var v any
		if err := json.Unmarshal([]byte(s.Text()), &v); err != nil {
			return
		}
		out = append(out, flattenLD(v)...)
	})
	return out
}

func flattenLD(v any) []any {
	switch x := v.(type) {
	case []any:
		var out []any
		for _, el := range x {
			out = append(out, flattenLD(el)...)
		}
		return out
	case map[string]any:
		out := []any{x}
		if g, ok := x["@graph"]; ok {
			out = append(out, flattenLD(g)...)
		}
		return out
	}
	return nil
}

func ldHasType(v any, typ string) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	switch t := m["@type"].(type) {
	case string:
		return strings.EqualFold(t, typ)
	case []any:
		for _, el := range t {
			if s, ok := el.(string); ok && strings.EqualFold(s, typ) {
				return true
			}
		}
	}
	return false
}
