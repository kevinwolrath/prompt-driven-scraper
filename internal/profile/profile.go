// Package profile decodes and validates job configurations and source
// profiles. Decoding is strict: unknown keys are errors, so configuration
// that relies on DSL features the runtime does not have is rejected rather
// than silently ignored.
package profile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/extract"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/output"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/query"
	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

// Decode strictly decodes YAML or JSON (JSON is valid YAML) into out.
func Decode(data []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("configuration is empty")
		}
		return cleanYAMLError(err)
	}
	return nil
}

func cleanYAMLError(err error) error {
	msg := strings.ReplaceAll(err.Error(), "yaml: unmarshal errors:\n  ", "")
	msg = strings.ReplaceAll(msg, "in type contracts.", "in ")
	return errors.New(msg)
}

func DecodeJob(data []byte) (contracts.JobDefinition, error) {
	var def contracts.JobDefinition
	err := Decode(data, &def)
	return def, err
}

func DecodeProfile(data []byte) (contracts.SourceProfile, error) {
	var p contracts.SourceProfile
	err := Decode(data, &p)
	return p, err
}

// Resolver looks up stored source profiles by id.
type Resolver interface {
	GetProfile(ctx context.Context, id string) (contracts.SourceProfile, error)
}

// ValidateProfile validates a single source profile.
func ValidateProfile(p contracts.SourceProfile) extract.Issues {
	_, issues := extract.Compile(p)
	return issues
}

// Resolve fills each SourceRef.Profile from its ProfileRef where needed. The
// resolved profile is a snapshot: the job keeps the profile it ran with even
// if the stored profile changes later. def.SearchSources is replaced with a
// copy so the caller's definition is not modified.
func Resolve(ctx context.Context, def *contracts.JobDefinition, r Resolver) extract.Issues {
	var issues extract.Issues
	def.SearchSources = append([]contracts.SourceRef(nil), def.SearchSources...)
	for i := range def.SearchSources {
		ref := &def.SearchSources[i]
		path := fmt.Sprintf("searchSources[%d]", i)
		switch {
		case ref.Profile != nil && ref.ProfileRef != "":
			issues.Errorf(path, "set either profile or profileRef, not both")
		case ref.Profile == nil && ref.ProfileRef == "":
			issues.Errorf(path, "a search source needs an inline profile or a profileRef")
		case ref.Profile == nil:
			if r == nil {
				issues.Errorf(path+".profileRef", "profileRef %q cannot be resolved here", ref.ProfileRef)
				continue
			}
			p, err := r.GetProfile(ctx, ref.ProfileRef)
			if err != nil {
				issues.Errorf(path+".profileRef", "unknown source profile %q", ref.ProfileRef)
				continue
			}
			ref.Profile = &p
		}
		if ref.ID == "" && ref.Profile != nil {
			ref.ID = ref.Profile.ID
		}
	}
	return issues
}

// ValidateJob validates a resolved job definition and returns compiled
// profiles in search-source order when valid.
func ValidateJob(def contracts.JobDefinition) ([]*extract.Profile, extract.Issues) {
	var issues extract.Issues
	if strings.TrimSpace(def.Name) == "" {
		issues.Errorf("name", "name is required")
	}
	if strings.TrimSpace(def.QueryInput) == "" {
		issues.Warnf("queryInput", "queryInput is empty; record what the user asked for so the configuration is reviewable")
	}
	output.Validate(def.Output, &issues)
	query.ValidateFilters(def.Query.Filters, def.Output.Fields, &issues)

	outFields := map[string]bool{}
	for _, f := range def.Output.Fields {
		outFields[f.Name] = true
	}
	if len(def.SearchSources) == 0 {
		issues.Errorf("searchSources", "at least one search source is required")
	}
	ids := map[string]bool{}
	compiled := make([]*extract.Profile, 0, len(def.SearchSources))
	for i, ref := range def.SearchSources {
		path := fmt.Sprintf("searchSources[%d]", i)
		if ref.Profile == nil {
			issues.Errorf(path, "source profile is not resolved")
			continue
		}
		if ids[ref.ID] {
			issues.Errorf(path+".id", "duplicate source id %q", ref.ID)
		}
		ids[ref.ID] = true
		c, pIssues := extract.Compile(*ref.Profile)
		for _, is := range pIssues {
			is.Path = path + ".profile." + is.Path
			issues = append(issues, is)
		}
		for name := range ref.Profile.Fields {
			if !outFields[name] {
				issues.Warnf(path+".profile.fields."+name, "field %q is not in the output schema and will be ignored", name)
			}
		}
		for _, f := range def.Output.Fields {
			if _, ok := ref.Profile.Fields[f.Name]; !ok {
				if f.Required {
					issues.Errorf(path+".profile.fields", "required output field %q has no mapping in source %q", f.Name, ref.ID)
				} else {
					issues.Warnf(path+".profile.fields", "output field %q has no mapping in source %q; it will be empty", f.Name, ref.ID)
				}
			}
		}
		compiled = append(compiled, c)
	}
	if issues.HasErrors() {
		return nil, issues
	}
	return compiled, issues
}
