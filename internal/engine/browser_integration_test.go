package engine

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/demosite"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/fetch/browser"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/profile"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage"
	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

// End to end with a real headless browser (skipped when none is installed):
// a source that refuses plain HTTP and renders listings with JavaScript.
func TestRealBrowserFallbackEndToEnd(t *testing.T) {
	b := browser.New(browser.OptionsFromEnv())
	defer b.Close()
	srv := httptest.NewServer(demosite.Handler(demosite.Options{}))
	defer srv.Close()

	profiles, errs := profile.LoadDir("../../examples/sources")
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	var p contracts.SourceProfile
	for _, x := range profiles {
		if x.ID == "demo-protected" {
			p = x
		}
	}
	p.Request.URL = strings.Replace(p.Request.URL, demoBase, srv.URL, 1)
	zero := 0
	p.Limits.DelayMs = &zero

	def := contracts.JobDefinition{
		Name: "protected", QueryInput: "EVs from 2024",
		Query:         contracts.Query{Params: map[string]any{"fuel": "electric"}, Filters: []contracts.Filter{{Field: "year", Op: "gte", Value: 2024}}},
		SearchSources: []contracts.SourceRef{{ID: "protected", Profile: &p}},
		Output: contracts.OutputDefinition{Format: "csv", Fields: contracts.OutputFields{
			{Name: "make", Type: "string", Required: true}, {Name: "model", Type: "string", Required: true},
			{Name: "year", Type: "integer", Required: true}, {Name: "price", Type: "decimal", Required: true},
			{Name: "url", Type: "url", Required: true},
		}},
	}
	run := func(def contracts.JobDefinition) contracts.Job {
		eng := New(storage.NewMemoryStore(), Options{Browser: b, RetryBackoff: time.Millisecond})
		job, err := eng.Submit(context.Background(), def)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		done, _ := eng.Wait(ctx, job.ID)
		return done
	}

	done := run(def)
	sp := done.Progress.Sources[0]
	if strings.Contains(sp.FailureReason, "no Chrome or Chromium found") {
		t.Skip("no Chrome/Chromium available")
	}
	if done.Status != contracts.JobCompleted {
		t.Fatalf("status %s, source %+v", done.Status, sp)
	}
	if sp.Acquisition[0].LastResult != "HTTP 403 Forbidden" || sp.Acquisition[1].Succeeded != 1 {
		t.Fatalf("acquisition %+v", sp.Acquisition)
	}
	// 11 priced EVs (one listed twice); 2022/2023 rejected by the filter.
	if sp.PagesAttempted != 1 || sp.PagesVisited != 1 || sp.RecordsFound != 12 || sp.RecordsAccepted != 9 || sp.RecordsRejected != 2 || sp.Duplicates != 1 {
		t.Fatalf("progress %+v", sp)
	}

	// A bot challenge defeats both strategies: the source fails at acquisition.
	challenge := p
	challenge.Request.URL = srv.URL + "/demo/challenge/cars"
	short := *challenge.Acquisition
	short.Strategies = append([]contracts.AcquisitionStrategy(nil), short.Strategies...)
	short.Strategies[1].TimeoutS = 3
	challenge.Acquisition = &short
	def.SearchSources = []contracts.SourceRef{{ID: "challenge", Profile: &challenge}}
	failed := run(def)
	fs := failed.Progress.Sources[0]
	if failed.Status != contracts.JobFailed || fs.FailureStage != contracts.StageAcquisition ||
		!strings.Contains(fs.FailureReason, "http: bot challenge / access denied (HTTP 403); browser: bot challenge / access denied (HTTP 403), still shown when the browser wait ended") {
		t.Fatalf("challenge: %s %+v", failed.Status, fs)
	}
}
