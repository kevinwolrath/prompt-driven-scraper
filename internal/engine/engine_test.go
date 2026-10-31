package engine

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/demosite"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/fetch"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/profile"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage"
	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

const demoBase = "http://127.0.0.1:8089"

type countingFetcher struct {
	inner fetch.Fetcher
	n     atomic.Int64
}

func (c *countingFetcher) Fetch(ctx context.Context, r fetch.Request) (fetch.Response, error) {
	c.n.Add(1)
	return c.inner.Fetch(ctx, r)
}

type harness struct {
	t       *testing.T
	engine  *Engine
	fetcher *countingFetcher
	def     contracts.JobDefinition
	base    string
}

// normalised replaces the test server's address so outputs from different
// harnesses can be compared.
func (h *harness) normalised(out contracts.Output) string {
	return strings.ReplaceAll(string(out.Data), h.base, demoBase)
}

// newHarness loads the example job and profiles, pointing them at an
// in-process demo site.
func newHarness(t *testing.T, latency time.Duration) *harness {
	t.Helper()
	srv := httptest.NewServer(demosite.Handler(demosite.Options{Latency: latency}))
	t.Cleanup(srv.Close)

	store := storage.NewMemoryStore()
	profiles, errs := profile.LoadDir("../../examples/sources")
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	zero := 0
	for _, p := range profiles {
		p.Request.URL = strings.Replace(p.Request.URL, demoBase, srv.URL, 1)
		p.Limits.DelayMs = &zero
		if err := store.SaveProfile(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile("../../examples/ev-cars.job.yaml")
	if err != nil {
		t.Fatal(err)
	}
	def, err := profile.DecodeJob(data)
	if err != nil {
		t.Fatal(err)
	}
	cf := &countingFetcher{inner: fetch.NewHTTP()}
	eng := New(store, Options{Fetcher: cf, RetryBackoff: time.Millisecond})
	return &harness{t: t, engine: eng, fetcher: cf, def: def, base: srv.URL}
}

func (h *harness) wait(id string) contracts.Job {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	job, err := h.engine.Wait(ctx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return job
}

func (h *harness) waitFor(id string, cond func(contracts.Job) bool) contracts.Job {
	h.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		job, _ := h.engine.Get(context.Background(), id)
		if cond(job) {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatal("condition not reached")
	return contracts.Job{}
}

func (h *harness) output(id string) contracts.Output {
	h.t.Helper()
	out, err := h.engine.Output(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return out
}

func (h *harness) runToCompletion() (contracts.Job, contracts.Output) {
	job, err := h.engine.Submit(context.Background(), h.def)
	if err != nil {
		h.t.Fatal(err)
	}
	job = h.wait(job.ID)
	return job, h.output(job.ID)
}

func TestEndToEndDemo(t *testing.T) {
	h := newHarness(t, 0)
	job, out := h.runToCompletion()
	if job.Status != contracts.JobCompleted {
		t.Fatalf("status %s (%s)", job.Status, job.StatusReason)
	}
	csv := string(out.Data)
	lines := strings.Split(strings.TrimSpace(csv), "\n")
	if lines[0] != "make,model,year,price,kilometres,batterySizeKwh,fuelType,location,url" {
		t.Fatalf("header %q", lines[0])
	}
	// 9 EVs from 2024+ with a price on each source.
	if out.RecordCount != 18 || len(lines) != 19 {
		t.Fatalf("records %d, lines %d\n%s", out.RecordCount, len(lines), csv)
	}
	// Weekly finance repayments must never be read as the price.
	if !strings.Contains(csv, "Tesla,Model 3,2024,58900,12345,60,electric,Sydney NSW,") {
		t.Errorf("Tesla Model 3 row wrong:\n%s", csv)
	}
	for _, bad := range []string{",189,", ",2023,", ",2022,", "Polestar", "Sponsored", "Toyota"} {
		if strings.Contains(csv, bad) {
			t.Errorf("output should not contain %q:\n%s", bad, csv)
		}
	}
	p := job.Progress
	if p.Duplicates != 1 {
		t.Errorf("duplicates = %d, want 1", p.Duplicates)
	}
	if p.Sources[0].ListingPagesTotal != 4 || p.Sources[0].ListingPagesVisited != 4 {
		t.Errorf("listing pages = %d/%d, want 4/4", p.Sources[0].ListingPagesVisited, p.Sources[0].ListingPagesTotal)
	}
	if p.RecordsAccepted != 18 || p.Sources[0].Status != contracts.SourceCompleted {
		t.Errorf("progress %+v", p)
	}
	recs, _ := h.engine.Records(context.Background(), job.ID)
	for _, r := range recs {
		if strings.Contains(r.PageURL, demosite.BrokenDetailID) && r.SourceID == "demo-motors" && !r.Partial {
			t.Errorf("record from failed detail page should be marked partial")
		}
	}
}

func TestDeterministicOutput(t *testing.T) {
	h := newHarness(t, 0)
	_, a := h.runToCompletion()
	_, b := h.runToCompletion()
	if !bytes.Equal(a.Data, b.Data) {
		t.Fatalf("outputs differ:\n%s\n---\n%s", a.Data, b.Data)
	}
}

func TestPauseResumeContinuesFromCheckpoint(t *testing.T) {
	ref := newHarness(t, 0)
	_, want := ref.runToCompletion()
	totalFetches := ref.fetcher.n.Load()

	h := newHarness(t, 20*time.Millisecond)
	job, err := h.engine.Submit(context.Background(), h.def)
	if err != nil {
		t.Fatal(err)
	}
	h.waitFor(job.ID, func(j contracts.Job) bool { return j.Progress.RecordsAccepted >= 3 })
	if _, err := h.engine.Pause(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	paused := h.waitFor(job.ID, func(j contracts.Job) bool { return j.Status == contracts.JobPaused })
	before := h.fetcher.n.Load()
	time.Sleep(100 * time.Millisecond)
	if h.fetcher.n.Load() != before {
		t.Fatal("fetches continued while paused")
	}
	if _, err := h.engine.Resume(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	done := h.wait(job.ID)
	if done.Status != contracts.JobCompleted {
		t.Fatalf("status %s", done.Status)
	}
	if got := h.normalised(h.output(job.ID)); got != ref.normalised(want) {
		t.Fatalf("resumed output differs from uninterrupted run:\n%s\n---\n%s", got, ref.normalised(want))
	}
	if h.fetcher.n.Load() != totalFetches {
		t.Fatalf("resumed run made %d fetches, uninterrupted made %d (work was repeated)", h.fetcher.n.Load(), totalFetches)
	}
	if paused.Progress.RecordsAccepted == 0 || done.Progress.ElapsedMs <= 0 {
		t.Fatalf("unexpected progress: paused=%+v done=%+v", paused.Progress, done.Progress)
	}
}

func TestGiveMeWhatYouveGot(t *testing.T) {
	h := newHarness(t, 30*time.Millisecond)
	job, err := h.engine.Submit(context.Background(), h.def)
	if err != nil {
		t.Fatal(err)
	}
	h.waitFor(job.ID, func(j contracts.Job) bool { return j.Progress.RecordsAccepted >= 2 })
	if _, err := h.engine.Finalise(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	done := h.wait(job.ID)
	if done.Status != contracts.JobPartial {
		t.Fatalf("status %s", done.Status)
	}
	out := h.output(job.ID)
	if !out.Partial || out.RecordCount < 2 || out.RecordCount >= 18 {
		t.Fatalf("partial output: %+v", out)
	}
	// Listing items already fetched but whose detail pages were not are salvaged.
	if done.Progress.RecordsAccepted != out.RecordCount {
		t.Fatalf("accepted %d, output %d", done.Progress.RecordsAccepted, out.RecordCount)
	}
	if done.Progress.Sources[1].Status != contracts.SourceSkipped {
		t.Fatalf("second source should be skipped, got %s", done.Progress.Sources[1].Status)
	}
}

func TestGiveMeWhatYouveGotWhilePaused(t *testing.T) {
	h := newHarness(t, 20*time.Millisecond)
	job, _ := h.engine.Submit(context.Background(), h.def)
	h.waitFor(job.ID, func(j contracts.Job) bool { return j.Progress.RecordsAccepted >= 1 })
	_, _ = h.engine.Pause(context.Background(), job.ID)
	h.waitFor(job.ID, func(j contracts.Job) bool { return j.Status == contracts.JobPaused })
	if _, err := h.engine.Finalise(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if done := h.wait(job.ID); done.Status != contracts.JobPartial {
		t.Fatalf("status %s", done.Status)
	}
	h.output(job.ID)
}

func TestCancel(t *testing.T) {
	h := newHarness(t, 30*time.Millisecond)
	job, _ := h.engine.Submit(context.Background(), h.def)
	h.waitFor(job.ID, func(j contracts.Job) bool { return j.Progress.PagesVisited >= 1 })
	if _, err := h.engine.Cancel(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	done := h.wait(job.ID)
	if done.Status != contracts.JobCancelled {
		t.Fatalf("status %s", done.Status)
	}
	if _, err := h.engine.Output(context.Background(), job.ID); err == nil {
		t.Fatal("cancelled jobs produce no output")
	}
}

func TestEventsAreOrderedAndReplayable(t *testing.T) {
	h := newHarness(t, 0)
	job, _ := h.runToCompletion()
	backlog, _, cancel, err := h.engine.Subscribe(context.Background(), job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	seen := map[contracts.EventType]int{}
	for i, ev := range backlog {
		if ev.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d", i, ev.Seq)
		}
		if ev.Progress == nil {
			t.Fatalf("event %d has no progress snapshot", i)
		}
		seen[ev.Type]++
	}
	for _, typ := range []contracts.EventType{
		contracts.EventJobStarted, contracts.EventSourceStarted, contracts.EventPageDiscovered, contracts.EventPageFetched,
		contracts.EventRecordFound, contracts.EventRecordAccepted, contracts.EventRecordRejected, contracts.EventWarning,
		contracts.EventSourceCompleted, contracts.EventJobFinalising, contracts.EventJobCompleted,
	} {
		if seen[typ] == 0 {
			t.Errorf("no %s event", typ)
		}
	}
	last := backlog[len(backlog)-1]
	if last.Type != contracts.EventJobCompleted {
		t.Fatalf("last event %s", last.Type)
	}
	tail, _, cancel2, _ := h.engine.Subscribe(context.Background(), job.ID, last.Seq-1)
	defer cancel2()
	if len(tail) != 1 {
		t.Fatalf("replay from seq gave %d events", len(tail))
	}
}

func TestInvalidJobIsRejectedBeforeRunning(t *testing.T) {
	h := newHarness(t, 0)
	def := h.def
	def.SearchSources = []contracts.SourceRef{{ID: "x", ProfileRef: "does-not-exist"}}
	_, err := h.engine.Submit(context.Background(), def)
	if _, ok := err.(*ValidationError); !ok {
		t.Fatalf("expected ValidationError, got %v", err)
	}
}
