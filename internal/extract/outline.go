package extract

import (
	"fmt"
	"sort"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// PageOutline is a compact, generic description of a page's structure. It
// is recorded when a loaded listing page yields no items, so a person (or an
// AI assistant revising the profile) can see what the page did contain.
type PageOutline struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
	Bytes int    `json:"bytes"`
	// TextChars is the length of visible text (scripts and styles excluded).
	TextChars int `json:"textChars"`
	Scripts   int `json:"scripts"`
	// Repeated lists the most frequent element signatures, most common first.
	Repeated     []RepeatedElement `json:"repeated,omitempty"`
	JSONLDTypes  []string          `json:"jsonldTypes,omitempty"`
	EmbeddedJSON []string          `json:"embeddedJson,omitempty"`
	// JSONKeys describes a JSON page's top level.
	JSONKeys []string `json:"jsonKeys,omitempty"`
	Hints    []string `json:"hints,omitempty"`
}

type RepeatedElement struct {
	Selector string `json:"selector"`
	Count    int    `json:"count"`
	Sample   string `json:"sample,omitempty"`
}

const (
	minRepeats  = 3
	maxRepeated = 12
)

// Outline describes a parsed page.
func Outline(p *Page, size int) PageOutline {
	o := PageOutline{URL: p.URL.String(), Bytes: size}
	if p.HTML == nil {
		if m, ok := p.JSON.(map[string]any); ok {
			for _, k := range sortedKeys(m) {
				if arr, ok := m[k].([]any); ok {
					o.JSONKeys = append(o.JSONKeys, fmt.Sprintf("%s (array of %d)", k, len(arr)))
				} else {
					o.JSONKeys = append(o.JSONKeys, k)
				}
			}
		}
		return o
	}
	doc := p.HTML
	o.Title = cleanText(doc.Find("title").First().Text())
	o.Scripts = doc.Find("script").Length()

	body := doc.Find("body").Clone()
	body.Find("script, style, noscript, template").Remove()
	o.TextChars = len(cleanText(body.Text()))

	counts := map[string]int{}
	samples := map[string]string{}
	doc.Find("body *").Each(func(_ int, s *goquery.Selection) {
		tag := goquery.NodeName(s)
		if tag == "script" || tag == "style" || tag == "svg" || tag == "path" {
			return
		}
		var sigs []string
		if id, ok := s.Attr("data-testid"); ok && id != "" {
			sigs = append(sigs, fmt.Sprintf("%s[data-testid=%q]", tag, id))
		}
		if cls, ok := s.Attr("class"); ok {
			for _, c := range strings.Fields(cls) {
				sigs = append(sigs, tag+"."+c)
			}
		}
		if len(sigs) == 0 && (tag == "article" || tag == "li") {
			sigs = append(sigs, tag)
		}
		for _, sig := range sigs {
			counts[sig]++
			if _, ok := samples[sig]; !ok {
				samples[sig] = truncate(cleanText(s.Text()), 80)
			}
		}
	})
	for sig, n := range counts {
		if n >= minRepeats {
			o.Repeated = append(o.Repeated, RepeatedElement{Selector: sig, Count: n, Sample: samples[sig]})
		}
	}
	sort.Slice(o.Repeated, func(a, b int) bool {
		if o.Repeated[a].Count != o.Repeated[b].Count {
			return o.Repeated[a].Count > o.Repeated[b].Count
		}
		return o.Repeated[a].Selector < o.Repeated[b].Selector
	})
	if len(o.Repeated) > maxRepeated {
		o.Repeated = o.Repeated[:maxRepeated]
	}

	types := map[string]bool{}
	for _, obj := range jsonLD(doc.Selection) {
		if m, ok := obj.(map[string]any); ok {
			switch t := m["@type"].(type) {
			case string:
				types[t] = true
			case []any:
				for _, x := range t {
					if s, ok := x.(string); ok {
						types[s] = true
					}
				}
			}
		}
	}
	for t := range types {
		o.JSONLDTypes = append(o.JSONLDTypes, t)
	}
	sort.Strings(o.JSONLDTypes)

	doc.Find(`script[type="application/json"], script#__NEXT_DATA__, script[type="application/ld+json"]`).Each(func(_ int, s *goquery.Selection) {
		typ, _ := s.Attr("type")
		if typ == "application/ld+json" {
			return
		}
		sel := "script"
		if id, ok := s.Attr("id"); ok && id != "" {
			sel += "#" + id
		} else {
			sel += fmt.Sprintf("[type=%q]", typ)
		}
		o.EmbeddedJSON = append(o.EmbeddedJSON, fmt.Sprintf("%s (%d bytes)", sel, len(s.Text())))
	})

	if o.TextChars < 500 && o.Scripts >= 5 {
		o.Hints = append(o.Hints, "very little visible text and many scripts: the listings are probably rendered by JavaScript; add a browser acquisition strategy with waitFor")
	}
	if len(o.EmbeddedJSON) > 0 {
		o.Hints = append(o.Hints, "the page embeds JSON data; listing values may be readable with css + json candidates")
	}
	if len(o.JSONLDTypes) > 0 {
		o.Hints = append(o.Hints, "the page has JSON-LD ("+strings.Join(o.JSONLDTypes, ", ")+")")
	}
	return o
}
