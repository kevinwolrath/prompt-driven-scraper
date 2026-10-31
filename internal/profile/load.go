package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

// LoadDir reads every *.yaml, *.yml and *.json source profile in dir.
// Invalid files are reported as errors and skipped.
func LoadDir(dir string) ([]contracts.SourceProfile, []error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []error{err}
	}
	var names []string
	for _, e := range entries {
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if !e.IsDir() && (ext == ".yaml" || ext == ".yml" || ext == ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var out []contracts.SourceProfile
	var errs []error
	for _, n := range names {
		path := filepath.Join(dir, n)
		data, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		p, err := DecodeProfile(data)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		if issues := ValidateProfile(p); issues.HasErrors() {
			for _, is := range issues {
				if is.Severity == "error" {
					errs = append(errs, fmt.Errorf("%s: %s: %s", path, is.Path, is.Message))
				}
			}
			continue
		}
		out = append(out, p)
	}
	return out, errs
}

// LooksLikeJob reports whether a configuration document is a job rather than
// a single source profile.
func LooksLikeJob(data []byte) bool {
	var probe map[string]any
	if err := Decode(data, &probe); err != nil {
		return false
	}
	_, ok := probe["searchSources"]
	return ok
}
