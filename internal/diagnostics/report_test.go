package diagnostics

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/demosite"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/engine"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/profile"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage/sqlite"
	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

func TestReportExplainsEachStage(t *testing.T) {
	srv := httptest.NewServer(demosite.Handler(demosite.Options{}))
	defer srv.Close()
	profiles, errs := profile.LoadDir("../../examples/sources")
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	byID := map[string]contracts.SourceProfile{}
	zero := 0
	for _, p := range profiles {
		p.Request.URL = strings.Replace(p.Request.URL, "http://127.0.0.1:8089", srv.URL, 1)
		p.Limits.DelayMs = &zero
		byID[p.ID] = p
	}
	// Extraction failure: the page loads but the item selector matches nothing.
	wrong := byID["demo-motors"]
	wrong.ID, wrong.Name = "wrong-selector", "Wrong selector"
	wrong.Listing.Items = "div.vehicle-tile"
	wrong.Listing.FollowDetail = false
	fields := map[string]contracts.FieldRule{} // own map: profile copies share it
	for name, f := range wrong.Fields {
		var keep []contracts.Candidate
		for _, c := range append(f.Candidates, f.Candidate) {
			if c.From != "detail" && c != (contracts.Candidate{}) {
				keep = append(keep, c)
			}
		}
		if len(keep) > 0 {
			f.Candidate, f.Candidates = contracts.Candidate{}, keep
			fields[name] = f
		}
	}
	wrong.Fields = fields
	// Acquisition failure: a challenge page, HTTP only.
	blocked := byID["demo-motors"]
	blocked.ID, blocked.Name = "blocked", "Blocked site"
	blocked.Request = contracts.RequestSpec{URL: srv.URL + "/demo/challenge/cars"}

	data, _ := os.ReadFile("../../examples/ev-cars.job.yaml")
	def, err := profile.DecodeJob(data)
	if err != nil {
		t.Fatal(err)
	}
	def.Name = "Diagnostics check"
	ok := byID["demo-motors"]
	def.SearchSources = []contracts.SourceRef{{ID: "blocked", Profile: &blocked}, {ID: "wrong-selector", Profile: &wrong}, {ID: "demo-motors", Profile: &ok}}

	path := filepath.Join(t.TempDir(), "pds.db")
	store, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(store, engine.Options{RetryBackoff: time.Millisecond})
	job, err := eng.Submit(context.Background(), def)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := eng.Wait(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	store.Close()

	// Report from storage, as after a restart.
	store, _ = sqlite.Open(path)
	defer store.Close()
	saved, _ := store.GetJob(context.Background(), job.ID)
	events, _ := store.ListEvents(context.Background(), job.ID, 0)
	report := Report(saved, events)

	for _, want := range []string{
		"Prompt Driven Scraper — Run Diagnostics",
		"SESSION\nDiagnostics check",
		"Status: Partial",
		"Pages loaded/attempted: ",
		// acquisition failure
		"SOURCE: Blocked site",
		"- HTTP: bot challenge / access denied (HTTP 403) (0 of 1 attempts succeeded)",
		"- Pages loaded: 0/1",
		"Extraction:\n- Not reached (no page acquired)",
		"Filtering:\n- Not reached",
		"Source failed at acquisition: could not acquire listing page 1",
		// extraction failure, with page evidence
		"SOURCE: Wrong selector",
		"- HTTP: page loaded",
		"- 0 items on 1 loaded page(s): check listing.items and the field rules",
		`listing page 1 has no items matching "div.vehicle-tile"`,
		`Title: "Cars for sale | Demo Motors"`,
		"article.listing-card ×5",
		// a working source, with grouped rejections
		"SOURCE: Demo Motors (HTML)",
		"- 9 accepted, 6 rejected, 1 duplicates",
		"filter year gte 2024 (×2)",
		`query param "yearMin"`,
		// configuration snapshot and event log
		"CONFIGURATION (snapshot used by this run",
		"items: div.vehicle-tile",
		"EVENT LOG",
		"[blocked] AcquisitionAttempt: HTTP: bot challenge / access denied (HTTP 403)",
		"JobPartial",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report is missing %q", want)
		}
	}
	if strings.Contains(report, "- could not acquire listing page: ") {
		t.Error("the acquisition failure is listed twice")
	}
	if t.Failed() || testing.Verbose() {
		t.Log(report)
	}
}
