package extract

import (
	"reflect"
	"testing"

	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

func f(v float64) *float64 { return &v }

func TestEvalPath(t *testing.T) {
	doc := map[string]any{
		"offers": []any{
			map[string]any{"type": "weekly", "amount": 189.0},
			map[string]any{"type": "driveaway", "amount": 58900.0},
		},
		"items": []any{"a", "b"},
	}
	cases := map[string][]any{
		"offers.amount":                 {189.0, 58900.0},
		"offers.1.amount":               {58900.0},
		"offers[type=driveaway].amount": {58900.0},
		"offers[type='WEEKLY'].amount":  {189.0},
		"items.0":                       {"a"},
		"missing.path":                  nil,
	}
	for path, want := range cases {
		if got := EvalPath(doc, path); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v want %v", path, got, want)
		}
	}
}

func TestTransforms(t *testing.T) {
	cases := []struct {
		ts   []contracts.Transform
		in   any
		want any
		err  bool
	}{
		{[]contracts.Transform{{Name: "currency"}, {Name: "decimal"}}, "$89,990 drive away", 89990.0, false},
		{[]contracts.Transform{{Name: "number"}, {Name: "integer"}}, "12,345 km", int64(12345), false},
		{[]contracts.Transform{{Name: "regex", Arg: `([\d.]+)\s*kWh`}, {Name: "decimal"}}, "75.5 kWh", 75.5, false},
		{[]contracts.Transform{{Name: "regex", Arg: `kWh`}}, "none", nil, true},
		{[]contracts.Transform{{Name: "map", Arg: map[string]any{"EV": "electric"}}}, " ev ", "electric", false},
		{[]contracts.Transform{{Name: "map", Arg: map[string]any{"EV": "electric"}}}, "petrol", nil, true},
		{[]contracts.Transform{{Name: "replace", Arg: map[string]any{"from": "Located in ", "to": ""}}}, "Located in Perth", "Perth", false},
		{[]contracts.Transform{{Name: "integer"}}, 2024.0, int64(2024), false},
		{[]contracts.Transform{{Name: "integer"}}, "20.5", nil, true},
		{[]contracts.Transform{{Name: "boolean"}}, "Yes", true, false},
	}
	for i, c := range cases {
		var issues Issues
		fns := compileTransforms(c.ts, "t", &issues)
		if issues.HasErrors() {
			t.Fatalf("case %d: %v", i, issues)
		}
		got, err := applyTransforms(fns, c.in, nil)
		if (err != nil) != c.err || (!c.err && got != c.want) {
			t.Errorf("case %d: got %v (%T), err %v; want %v", i, got, got, err, c.want)
		}
	}
}

func TestUnknownTransformIsAnError(t *testing.T) {
	var issues Issues
	compileTransforms([]contracts.Transform{{Name: "llmExtract"}}, "fields.x.transform", &issues)
	if !issues.HasErrors() {
		t.Fatal("expected unknown transform to be rejected")
	}
}

const listingHTML = `<html><body>
<article class="card"><a class="details" href="/cars/1">Car 1</a>
  <div class="price"><span class="finance-repayment">$189 per week</span> <span class="vehicle-price">$58,900 drive away</span></div></article>
<article class="card ad"><a class="details" href="/ad">Ad</a></article>
<article class="card"><a class="details" href="/cars/2">Car 2</a>
  <div class="price"><span class="vehicle-price">$44,000</span> <span class="was">$1,500 off</span></div></article>
</body></html>`

func TestNegativeKnowledgeAndPrecedence(t *testing.T) {
	p, issues := Compile(contracts.SourceProfile{
		ID:      "t",
		Request: contracts.RequestSpec{URL: "https://example.test/cars"},
		Listing: contracts.Listing{
			Items:      "article.card",
			Skip:       []contracts.Matcher{{Css: ".ad"}},
			DetailLink: &contracts.FieldRule{Candidate: contracts.Candidate{Css: "a.details", Attr: "href"}},
		},
		Fields: map[string]contracts.FieldRule{
			"price": {
				Candidates: []contracts.Candidate{{Css: ".price span"}},
				Reject:     []contracts.Matcher{{Css: ".finance-repayment"}, {Contains: "per week"}, {Max: f(2000)}},
				Prefer:     []contracts.Matcher{{Contains: "drive away"}},
				Transform:  []contracts.Transform{{Name: "currency"}, {Name: "decimal"}},
			},
		},
	})
	if issues.HasErrors() {
		t.Fatal(issues)
	}
	page, err := p.ParsePage("https://example.test/cars", []byte(listingHTML))
	if err != nil {
		t.Fatal(err)
	}
	items, skipped := p.Items(page)
	if len(items) != 2 || len(skipped) != 1 {
		t.Fatalf("items=%d skipped=%d", len(items), len(skipped))
	}
	want := []float64{58900, 44000}
	for i, it := range items {
		v, ok := p.Fields[0].First(Scope{Item: it, Listing: page}, FromItem)
		if !ok || v != want[i] {
			t.Errorf("item %d price = %v, want %v", i, v, want[i])
		}
	}
	link, _ := p.DetailLink.First(Scope{Item: items[1], Listing: page}, FromItem)
	if link != "/cars/2" {
		t.Errorf("detail link %v", link)
	}
}

func TestChoosePreferBeatsOrder(t *testing.T) {
	got, _ := Choose([]Result{{Index: 2, Value: "b", Preferred: true}, {Index: 0, Value: "a"}})
	if got.Value != "b" {
		t.Fatalf("got %v", got.Value)
	}
	got, _ = Choose([]Result{{Index: 2, Value: "b"}, {Index: 0, Value: "a"}})
	if got.Value != "a" {
		t.Fatalf("got %v", got.Value)
	}
}

func TestCompileRejectsInvalidProfiles(t *testing.T) {
	bad := contracts.SourceProfile{
		ID:      "bad",
		Request: contracts.RequestSpec{URL: "not-a-url"},
		Listing: contracts.Listing{Items: "div[["},
		Fields: map[string]contracts.FieldRule{
			"a": {},
			"b": {Candidate: contracts.Candidate{Css: ".x", JSONLD: "y"}},
			"c": {Candidate: contracts.Candidate{From: "detail", Css: ".x"}},
		},
	}
	_, issues := Compile(bad)
	paths := map[string]bool{}
	for _, is := range issues {
		paths[is.Path] = true
	}
	for _, want := range []string{"request.url", "listing.items", "fields.a", "fields.b", "fields.c.from"} {
		if !paths[want] {
			t.Errorf("expected an issue at %s; got %v", want, issues)
		}
	}
}
