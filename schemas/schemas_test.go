// Package schemas keeps the published JSON Schemas honest: every example
// configuration must validate against them.
package schemas

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/extract"
)

func compile(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	for _, f := range []string{"job.schema.json", "source-profile.schema.json"} {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(data)))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.AddResource("https://github.com/kevinwolrath/prompt-driven-scraper/schemas/"+f, doc); err != nil {
			t.Fatal(err)
		}
	}
	s, err := c.Compile("https://github.com/kevinwolrath/prompt-driven-scraper/schemas/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func load(t *testing.T, path string) any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	// Round-trip through JSON so values have JSON types.
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	out, err := jsonschema.UnmarshalJSON(strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestExamplesMatchSchemas(t *testing.T) {
	profileSchema := compile(t, "source-profile.schema.json")
	jobSchema := compile(t, "job.schema.json")
	profiles, _ := filepath.Glob("../examples/sources/*.yaml")
	jobs, _ := filepath.Glob("../examples/*.job.yaml")
	if len(profiles) == 0 || len(jobs) == 0 {
		t.Fatal("no examples found")
	}
	for _, p := range profiles {
		if err := profileSchema.Validate(load(t, p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	for _, j := range jobs {
		if err := jobSchema.Validate(load(t, j)); err != nil {
			t.Errorf("%s: %v", j, err)
		}
	}
}

func TestSchemaRejectsInventedFeatures(t *testing.T) {
	s := compile(t, "source-profile.schema.json")
	bad := map[string]any{
		"id": "x", "request": map[string]any{"url": "https://example.test"},
		"listing": map[string]any{"items": ".a"},
		"fields":  map[string]any{"price": map[string]any{"css": ".p", "transform": []any{"aiSummarise"}}},
	}
	b, _ := json.Marshal(bad)
	doc, _ := jsonschema.UnmarshalJSON(strings.NewReader(string(b)))
	if err := s.Validate(doc); err == nil {
		t.Fatal("schema accepted an unknown transform")
	}
}

// The schema's transform list must match the runtime's.
func TestSchemaTransformsMatchRuntime(t *testing.T) {
	data, _ := os.ReadFile("source-profile.schema.json")
	var doc struct {
		Defs struct {
			Transform struct {
				OneOf []struct {
					Enum     []string `json:"enum"`
					Required []string `json:"required"`
				} `json:"oneOf"`
			} `json:"transform"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, o := range doc.Defs.Transform.OneOf {
		names = append(names, o.Enum...)
		names = append(names, o.Required...)
	}
	sort.Strings(names)
	if got, want := strings.Join(names, ","), strings.Join(extract.TransformNames(), ","); got != want {
		t.Fatalf("schema transforms %s\nruntime transforms %s", got, want)
	}
}
