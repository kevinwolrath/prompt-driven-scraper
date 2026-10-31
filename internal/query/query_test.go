package query

import (
	"strings"
	"testing"

	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

func TestBuildURL(t *testing.T) {
	spec := contracts.RequestSpec{
		URL: "https://example.test/search?sort=new",
		Params: map[string]contracts.ParamMapping{
			"fuel":    {From: "fuel", Map: map[string]string{"electric": "EV"}},
			"minYear": {From: "yearMin"},
			"country": {Value: "AU"},
		},
	}
	got, warnings, err := BuildURL(spec, map[string]any{"fuel": "electric", "yearMin": 2024, "colour": "red"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://example.test/search?country=AU&fuel=EV&minYear=2024&sort=new" {
		t.Fatalf("url %s", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "colour") {
		t.Fatalf("warnings %v", warnings)
	}
}

func TestFilters(t *testing.T) {
	filters := []contracts.Filter{
		{Field: "year", Op: "gte", Value: 2024},
		{Field: "fuel", Op: "in", Value: []any{"electric", "hybrid"}},
		{Field: "battery", Op: "gt", Value: 50, Missing: "pass"},
	}
	cases := []struct {
		values map[string]any
		pass   bool
	}{
		{map[string]any{"year": int64(2024), "fuel": "Electric"}, true},
		{map[string]any{"year": int64(2023), "fuel": "electric"}, false},
		{map[string]any{"fuel": "electric"}, false},
		{map[string]any{"year": "2025", "fuel": "electric"}, false}, // string vs number is incomparable
		{map[string]any{"year": int64(2025), "fuel": "petrol"}, false},
		{map[string]any{"year": int64(2025), "fuel": "hybrid", "battery": 40.0}, false},
	}
	for i, c := range cases {
		if got := Apply(filters, c.values) == ""; got != c.pass {
			t.Errorf("case %d: pass=%v want %v (%s)", i, got, c.pass, Apply(filters, c.values))
		}
	}
	if Apply([]contracts.Filter{{Field: "year", Op: "lt", Value: 2024}}, map[string]any{"year": "abc"}) == "" {
		t.Error("incomparable values must not pass lt")
	}
}
