// Package ai assembles the bootstrap prompt a user pastes into ChatGPT,
// Claude or another assistant before asking it to write configuration.
//
// The prompt is composed at build time from the repository files themselves
// (ai/BOOTSTRAP.md, ai/PROFILE_AUTHORING.md, the JSON Schemas and the example
// job), so there is exactly one copy of each to maintain.
package ai

import (
	_ "embed"
	"strings"

	"github.com/kevinwolrath/prompt-driven-scraper/examples"
	"github.com/kevinwolrath/prompt-driven-scraper/schemas"
)

//go:embed BOOTSTRAP.md
var bootstrap string

//go:embed PROFILE_AUTHORING.md
var authoring string

// AuthoringManual returns ai/PROFILE_AUTHORING.md verbatim.
func AuthoringManual() string { return authoring }

// BootstrapPrompt returns the complete prompt to paste into a new AI session.
func BootstrapPrompt() string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(bootstrap))
	b.WriteString("\n\n---\n\n")
	b.WriteString(strings.TrimSpace(authoring))
	b.WriteString("\n\n---\n\n# Appendix A: JSON Schemas\n")
	for _, f := range schemas.Files() {
		section(&b, "schemas/"+f.Name, "json", f.Content)
	}
	b.WriteString("\n---\n\n# Appendix B: worked example\n\n")
	b.WriteString("The example sources point at a demo site bundled with Prompt Driven Scraper " +
		"(http://127.0.0.1:8089). Use them as a model for structure and negative knowledge, not as real sources.\n")
	for _, f := range examples.Files() {
		section(&b, "examples/"+f.Name, "yaml", f.Content)
	}
	return b.String()
}

func section(b *strings.Builder, name, lang, content string) {
	b.WriteString("\n## `" + name + "`\n\n```" + lang + "\n")
	b.WriteString(strings.TrimSpace(content))
	b.WriteString("\n```\n")
}
