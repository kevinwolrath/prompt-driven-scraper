package profile

import (
	"strings"
	"testing"
)

func TestDecodeIsStrict(t *testing.T) {
	_, err := DecodeProfile([]byte(`
id: x
request: { url: "https://example.test" }
listing: { items: ".a" }
fields:
  price:
    css: ".p"
    javascript: "return price * 2"
`))
	if err == nil || !strings.Contains(err.Error(), "javascript") {
		t.Fatalf("expected unknown key error, got %v", err)
	}
}

func TestCompactOutputFields(t *testing.T) {
	for _, src := range []string{
		`{"name":"n","queryInput":"q","searchSources":[],"output":{"format":"json","fields":{"make":"string","battery":"decimal?"}}}`,
		"name: n\nsearchSources: []\noutput:\n  format: json\n  fields:\n    make: string\n    battery: decimal?\n",
	} {
		def, err := DecodeJob([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		f := def.Output.Fields
		if len(f) != 2 || f[0].Name != "make" || !f[0].Required || f[1].Name != "battery" || f[1].Type != "decimal" || f[1].Required {
			t.Fatalf("fields = %+v", f)
		}
	}
}

func TestExampleProfilesAreValid(t *testing.T) {
	profiles, errs := LoadDir("../../examples/sources")
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if len(profiles) < 2 {
		t.Fatalf("expected example profiles, got %d", len(profiles))
	}
}
