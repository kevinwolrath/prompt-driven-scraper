package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/fetch"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/fetch/browser"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage"
	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

// listingHTML renders two cars per page; page 1 links to page 2.
func listingHTML(page int, next string) string {
	link := ""
	if next != "" {
		link = fmt.Sprintf(`<a class="next" href="%s">Next</a>`, next)
	}
	return fmt.Sprintf(`<html><head><title>Cars</title></head><body>
<article class="car"><span class="name">Car %d-A</span><span class="price">$8,500</span></article>
<article class="car"><span class="name">Car %d-B</span><span class="price">$14,000</span></article>%s</body></html>`, page, page, link)
}

// acqSite serves /open/* to plain HTTP and refuses /blocked/* with 403.
func acqSite(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/open/cars", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, listingHTML(1, ""))
	})
	mux.HandleFunc("/blocked/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "<html><body>Forbidden</body></html>")
	})
	mux.HandleFunc("/missing/cars", http.NotFound)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// fakeBrowser stands in for a real browser: it "renders" blocked pages.
type fakeBrowser struct {
	calls atomic.Int64
	err   error
	reqs  []fetch.BrowserRequest
}

func (b *fakeBrowser) Load(_ context.Context, req fetch.BrowserRequest) (fetch.Response, error) {
	b.calls.Add(1)
	b.reqs = append(b.reqs, req)
	if b.err != nil {
		return fetch.Response{}, b.err
	}
	next := ""
	page := 2
	if !strings.Contains(req.URL, "page=2") {
		next, page = req.URL+"?page=2", 1
	}
	return fetch.Response{URL: req.URL, StatusCode: 200, Title: "Cars", Body: []byte(listingHTML(page, next))}, nil
}

func acqJob(url string, acq *contracts.Acquisition) contracts.JobDefinition {
	zero := 0
	return contracts.JobDefinition{
		Name: "acq", QueryInput: "cars under $10k",
		Query: contracts.Query{Filters: []contracts.Filter{{Field: "price", Op: "lte", Value: 10000}}},
		SearchSources: []contracts.SourceRef{{ID: "site", Profile: &contracts.SourceProfile{
			ID: "site", Name: "Test site", Request: contracts.RequestSpec{URL: url},
			Acquisition: acq,
			Limits:      contracts.Limits{DelayMs: &zero, MaxPages: 5},
			Pagination:  &contracts.Pagination{Next: &contracts.FieldRule{Candidate: contracts.Candidate{From: "listing", Css: "a.next", Attr: "href"}}},
			Listing:     contracts.Listing{Items: "article.car"},
			Fields: map[string]contracts.FieldRule{
				"name":  {Candidate: contracts.Candidate{Css: ".name"}},
				"price": {Candidate: contracts.Candidate{Css: ".price"}, Transform: []contracts.Transform{{Name: "currency"}, {Name: "decimal"}}},
			},
		}}},
		Output: contracts.OutputDefinition{Format: "json", DedupeBy: []string{"name"},
			Fields: contracts.OutputFields{{Name: "name", Type: "string", Required: true}, {Name: "price", Type: "decimal", Required: true}}},
	}
}

var httpThenBrowser = &contracts.Acquisition{Strategies: []contracts.AcquisitionStrategy{
	{Type: "http"}, {Type: "browser", WaitUntil: "domcontentloaded", WaitFor: "article.car"},
}}

func runAcq(t *testing.T, store storage.Store, b fetch.Browser, def contracts.JobDefinition) (contracts.Job, *Engine) {
	t.Helper()
	opts := Options{RetryBackoff: time.Millisecond}
	if b != nil {
		opts.Browser = b
	}
	eng := New(store, opts)
	job, err := eng.Submit(context.Background(), def)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done, err := eng.Wait(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	return done, eng
}

func acquisitionEvents(t *testing.T, store storage.Store, id string) []contracts.Event {
	t.Helper()
	evs, err := store.ListEvents(context.Background(), id, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []contracts.Event
	for _, ev := range evs {
		if ev.Type == contracts.EventAcquisitionAttempt {
			out = append(out, ev)
		}
	}
	return out
}

func TestHTTPSuccessDoesNotUseBrowser(t *testing.T) {
	srv := acqSite(t)
	b := &fakeBrowser{}
	job, _ := runAcq(t, storage.NewMemoryStore(), b, acqJob(srv.URL+"/open/cars", httpThenBrowser))
	if job.Status != contracts.JobCompleted || b.calls.Load() != 0 {
		t.Fatalf("status %s, browser calls %d", job.Status, b.calls.Load())
	}
	sp := job.Progress.Sources[0]
	if sp.PagesAttempted != 1 || sp.PagesVisited != 1 || sp.Acquisition[0].Succeeded != 1 || sp.Acquisition[1].Attempts != 0 {
		t.Fatalf("progress %+v", sp)
	}
}

func TestForbiddenFallsBackToBrowserAndExtracts(t *testing.T) {
	srv := acqSite(t)
	store := storage.NewMemoryStore()
	b := &fakeBrowser{}
	job, eng := runAcq(t, store, b, acqJob(srv.URL+"/blocked/cars", httpThenBrowser))
	if job.Status != contracts.JobCompleted {
		t.Fatalf("status %s (%s)", job.Status, job.StatusReason)
	}
	// Two listing pages; the second goes straight to the browser.
	if b.calls.Load() != 2 {
		t.Fatalf("browser calls %d", b.calls.Load())
	}
	if b.reqs[0].WaitUntil != "domcontentloaded" || b.reqs[0].WaitFor != "article.car" {
		t.Fatalf("browser request %+v", b.reqs[0])
	}
	sp := job.Progress.Sources[0]
	if sp.PagesAttempted != 2 || sp.PagesVisited != 2 || sp.PagesFailed != 0 {
		t.Fatalf("pages attempted %d loaded %d failed %d", sp.PagesAttempted, sp.PagesVisited, sp.PagesFailed)
	}
	httpStats, browserStats := sp.Acquisition[0], sp.Acquisition[1]
	if httpStats.Attempts != 1 || httpStats.Failed != 1 || httpStats.LastResult != "HTTP 403 Forbidden" {
		t.Fatalf("http stats %+v", httpStats)
	}
	if browserStats.Attempts != 2 || browserStats.Succeeded != 2 || browserStats.LastResult != "page loaded" {
		t.Fatalf("browser stats %+v", browserStats)
	}
	// Generic extraction and filtering ran on the browser's HTML.
	recs, _ := eng.Records(context.Background(), job.ID)
	if sp.RecordsFound != 4 || len(recs) != 2 || sp.RecordsRejected != 2 || recs[0].Values["name"] != "Car 1-A" {
		t.Fatalf("found %d accepted %d rejected %d: %+v", sp.RecordsFound, len(recs), sp.RecordsRejected, recs)
	}
	evs := acquisitionEvents(t, store, job.ID)
	if len(evs) != 3 || evs[0].Data["ok"] != false || evs[0].Data["status"] != 403 ||
		evs[1].Data["strategy"] != "browser" || evs[1].Data["fallback"] != true || evs[1].Data["ok"] != true {
		t.Fatalf("attempt events %+v", evs)
	}
}

func TestHTTPAndBrowserFailureFailsSourceAtAcquisition(t *testing.T) {
	srv := acqSite(t)
	b := &fakeBrowser{err: &fetch.BlockedError{URL: "x", Status: 403, Marker: "cf-chl-"}}
	job, _ := runAcq(t, storage.NewMemoryStore(), b, acqJob(srv.URL+"/blocked/cars", httpThenBrowser))
	if job.Status != contracts.JobFailed {
		t.Fatalf("job status %s", job.Status)
	}
	sp := job.Progress.Sources[0]
	if sp.Status != contracts.SourceFailed || sp.FailureStage != contracts.StageAcquisition ||
		!strings.Contains(sp.FailureReason, "http: HTTP 403 Forbidden; browser: bot challenge / access denied") {
		t.Fatalf("source %+v", sp)
	}
	if sp.PagesAttempted != 1 || sp.PagesVisited != 0 || sp.PagesFailed != 1 || sp.RecordsFound != 0 {
		t.Fatalf("pages attempted %d loaded %d failed %d", sp.PagesAttempted, sp.PagesVisited, sp.PagesFailed)
	}
}

func TestBrowserTimeoutIsDiagnosed(t *testing.T) {
	srv := acqSite(t)
	b := &fakeBrowser{err: &browser.TimeoutError{Timeout: 45 * time.Second, Step: `waiting for "article.car"`}}
	store := storage.NewMemoryStore()
	job, _ := runAcq(t, store, b, acqJob(srv.URL+"/blocked/cars", httpThenBrowser))
	sp := job.Progress.Sources[0]
	want := `browser: browser timed out after 45s while waiting for "article.car"`
	if sp.Status != contracts.SourceFailed || !strings.Contains(sp.FailureReason, want) {
		t.Fatalf("reason %q", sp.FailureReason)
	}
	if evs := acquisitionEvents(t, store, job.ID); len(evs) != 2 || !strings.Contains(evs[1].Message, "timed out after 45s") {
		t.Fatalf("events %+v", evs)
	}
}

func TestNotFoundIsNotEligibleForFallback(t *testing.T) {
	srv := acqSite(t)
	b := &fakeBrowser{}
	job, _ := runAcq(t, storage.NewMemoryStore(), b, acqJob(srv.URL+"/missing/cars", httpThenBrowser))
	if b.calls.Load() != 0 || job.Progress.Sources[0].Status != contracts.SourceFailed {
		t.Fatalf("browser calls %d, source %s", b.calls.Load(), job.Progress.Sources[0].Status)
	}
}

func TestBrowserOnlyStrategyWithoutBrowserFailsClearly(t *testing.T) {
	srv := acqSite(t)
	acq := &contracts.Acquisition{Strategies: []contracts.AcquisitionStrategy{{Type: "browser"}}}
	job, _ := runAcq(t, storage.NewMemoryStore(), nil, acqJob(srv.URL+"/open/cars", acq))
	if !strings.Contains(job.Progress.Sources[0].FailureReason, "browser acquisition is not configured") {
		t.Fatalf("reason %q", job.Progress.Sources[0].FailureReason)
	}
}

func TestAcquisitionAttemptsArePersisted(t *testing.T) {
	srv := acqSite(t)
	path := filepath.Join(t.TempDir(), "pds.db")
	store := openSQLite(t, path)
	job, _ := runAcq(t, store, &fakeBrowser{}, acqJob(srv.URL+"/blocked/cars", httpThenBrowser))
	store.Close()

	reopened := openSQLite(t, path)
	evs := acquisitionEvents(t, reopened, job.ID)
	if len(evs) != 3 || evs[0].Data["strategy"] != "http" || evs[0].Data["status"] != float64(403) ||
		evs[0].Data["result"] != "HTTP 403 Forbidden" || evs[1].Data["title"] != "Cars" {
		t.Fatalf("persisted attempts %+v", evs)
	}
	saved, _ := reopened.GetJob(context.Background(), job.ID)
	sp := saved.Progress.Sources[0]
	if sp.PagesAttempted != 2 || sp.PagesVisited != 2 || len(sp.Acquisition) != 2 || sp.Acquisition[1].Succeeded != 2 {
		t.Fatalf("persisted progress %+v", sp)
	}
}

// Two sources may start from the same URL (e.g. two profiles for one site);
// the second must not be skipped because the first already fetched it.
func TestSourcesWithTheSameURLBothRun(t *testing.T) {
	srv := acqSite(t)
	def := acqJob(srv.URL+"/open/cars", nil)
	second := *def.SearchSources[0].Profile
	second.ID = "site-2"
	def.SearchSources = append(def.SearchSources, contracts.SourceRef{ID: "site-2", Profile: &second})
	job, _ := runAcq(t, storage.NewMemoryStore(), nil, def)
	for _, sp := range job.Progress.Sources {
		if sp.PagesAttempted != 1 || sp.PagesVisited != 1 || sp.RecordsFound != 2 {
			t.Fatalf("source %s: %+v", sp.ID, sp)
		}
	}
	if job.Progress.Duplicates != 1 { // cross-source record dedupe still applies
		t.Fatalf("duplicates %d", job.Progress.Duplicates)
	}
}
