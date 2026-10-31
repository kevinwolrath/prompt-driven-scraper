package ai

import (
	"os"
	"strings"
	"testing"
)

// The prompt must contain the repository files verbatim, so editing
// PROFILE_AUTHORING.md (or a schema/example) is all it takes to update it.
func TestBootstrapPromptIsBuiltFromRepositoryFiles(t *testing.T) {
	prompt := BootstrapPrompt()
	for _, path := range []string{
		"BOOTSTRAP.md",
		"PROFILE_AUTHORING.md",
		"../schemas/job.schema.json",
		"../schemas/source-profile.schema.json",
		"../examples/ev-cars.job.yaml",
		"../examples/sources/demo-motors.source.yaml",
		"../examples/sources/demo-ev-api.source.yaml",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(prompt, strings.TrimSpace(string(data))) {
			t.Errorf("prompt does not contain %s verbatim", path)
		}
	}
	if !strings.HasPrefix(prompt, "# Prompt Driven Scraper — configuration authoring session") {
		t.Errorf("prompt should start with the bootstrap preamble")
	}
}
