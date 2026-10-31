package engine

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/fetch"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage/sqlite"
	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

func openSQLite(t *testing.T, path string) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// restart simulates a new process: a fresh store connection and engine on
// the same database file, sharing only the network-side fetch counter.
func (h *harness) restart(t *testing.T, path string, fetcher fetch.Fetcher) *Engine {
	t.Helper()
	store := openSQLite(t, path)
	eng := New(store, Options{Fetcher: fetcher, RetryBackoff: time.Millisecond})
	h.engine = eng
	return eng
}

func TestFinishedRunSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pds.db")
	store := openSQLite(t, path)
	h := newHarnessWithStore(t, 0, store)
	job, out := h.runToCompletion()
	events, _, cancel, _ := h.engine.Subscribe(context.Background(), job.ID, 0)
	cancel()
	recs, _ := h.engine.Records(context.Background(), job.ID)
	store.Close()

	eng := h.restart(t, path, h.fetcher)
	ctx := context.Background()
	got, err := eng.Get(ctx, job.ID)
	if err != nil || got.Status != contracts.JobCompleted || got.Progress.RecordsAccepted != 18 {
		t.Fatalf("run after restart: %+v %v", got.Status, err)
	}
	if len(got.Definition.SearchSources) != 2 || got.Definition.SearchSources[0].Profile == nil {
		t.Fatal("configuration snapshot (with resolved profiles) lost")
	}
	gotOut, err := eng.Output(ctx, job.ID)
	if err != nil || string(gotOut.Data) != string(out.Data) {
		t.Fatalf("output after restart differs (%v)", err)
	}
	gotRecs, _ := eng.Records(ctx, job.ID)
	gotEvents, _, cancel2, _ := eng.Subscribe(ctx, job.ID, 0)
	cancel2()
	if len(gotRecs) != len(recs) || len(gotEvents) != len(events) {
		t.Fatalf("records %d/%d events %d/%d after restart", len(gotRecs), len(recs), len(gotEvents), len(events))
	}
	if gotEvents[len(gotEvents)-1].Type != contracts.EventJobCompleted {
		t.Fatalf("last event %s", gotEvents[len(gotEvents)-1].Type)
	}
	if _, err := eng.Resume(ctx, job.ID); err == nil {
		t.Fatal("a finished run must not be resumable")
	}
}

func TestPausedRunResumesAfterRestart(t *testing.T) {
	ref := newHarness(t, 0)
	_, want := ref.runToCompletion()
	totalFetches := ref.fetcher.n.Load()

	path := filepath.Join(t.TempDir(), "pds.db")
	store := openSQLite(t, path)
	h := newHarnessWithStore(t, 20*time.Millisecond, store)
	job, err := h.engine.Submit(context.Background(), h.def)
	if err != nil {
		t.Fatal(err)
	}
	h.waitFor(job.ID, func(j contracts.Job) bool { return j.Progress.RecordsAccepted >= 3 })
	// Graceful shutdown pauses at a checkpoint.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.engine.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if j, _ := h.engine.Get(ctx, job.ID); j.Status != contracts.JobPaused {
		t.Fatalf("after shutdown: %s", j.Status)
	}
	store.Close()

	eng := h.restart(t, path, h.fetcher)
	if changed, err := eng.Recover(ctx); err != nil || len(changed) != 0 {
		t.Fatalf("recover changed %v (%v); a cleanly paused run needs no recovery", changed, err)
	}
	if _, err := eng.Resume(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	done := h.wait(job.ID)
	if done.Status != contracts.JobCompleted {
		t.Fatalf("status %s (%s)", done.Status, done.StatusReason)
	}
	if got := h.normalised(h.output(job.ID)); got != ref.normalised(want) {
		t.Fatalf("output after restart+resume differs:\n%s\n---\n%s", got, ref.normalised(want))
	}
	if h.fetcher.n.Load() != totalFetches {
		t.Fatalf("restart+resume made %d fetches, uninterrupted made %d", h.fetcher.n.Load(), totalFetches)
	}
}

// hangingFetcher stops answering after limit fetches, like a process that
// died mid-request.
type hangingFetcher struct {
	inner fetch.Fetcher
	limit int64
	n     atomic.Int64
}

func (f *hangingFetcher) Fetch(ctx context.Context, r fetch.Request) (fetch.Response, error) {
	if f.n.Add(1) > f.limit {
		select {} // never returns; this "process" is gone
	}
	return f.inner.Fetch(ctx, r)
}

func TestCrashedRunIsRecoveredAndResumable(t *testing.T) {
	ref := newHarness(t, 0)
	_, want := ref.runToCompletion()

	path := filepath.Join(t.TempDir(), "pds.db")
	store := openSQLite(t, path)
	h := newHarnessWithStore(t, 0, store)
	h.engine = New(store, Options{Fetcher: &hangingFetcher{inner: fetch.NewHTTP(), limit: 7}, RetryBackoff: time.Millisecond})
	job, err := h.engine.Submit(context.Background(), h.def)
	if err != nil {
		t.Fatal(err)
	}
	h.waitFor(job.ID, func(j contracts.Job) bool { return j.Progress.PagesVisited >= 7 })
	time.Sleep(50 * time.Millisecond) // the 8th fetch is now hanging

	// A new process opens the same database without the old one shutting down.
	eng := h.restart(t, path, fetch.NewHTTP())
	ctx := context.Background()
	stuck, _ := eng.Get(ctx, job.ID)
	if stuck.Status != contracts.JobRunning {
		t.Fatalf("expected the crashed run to look running, got %s", stuck.Status)
	}
	changed, err := eng.Recover(ctx)
	if err != nil || len(changed) != 1 || changed[0] != job.ID {
		t.Fatalf("recover: %v %v", changed, err)
	}
	paused, _ := eng.Get(ctx, job.ID)
	if paused.Status != contracts.JobPaused || !strings.Contains(paused.StatusReason, "interrupted") {
		t.Fatalf("recovered run: %s %q", paused.Status, paused.StatusReason)
	}
	if _, err := eng.Resume(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	done := h.wait(job.ID)
	if done.Status != contracts.JobCompleted || done.StatusReason != "" {
		t.Fatalf("status %s (%s)", done.Status, done.StatusReason)
	}
	if got := h.normalised(h.output(job.ID)); got != ref.normalised(want) {
		t.Fatalf("output after crash recovery differs:\n%s\n---\n%s", got, ref.normalised(want))
	}
	recs, _ := eng.Records(ctx, job.ID)
	seen := map[string]bool{}
	for _, r := range recs {
		if seen[r.Key] {
			t.Fatalf("record %s accepted twice after recovery", r.Key)
		}
		seen[r.Key] = true
	}
}

func TestRunMetaAndSnapshotArePersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pds.db")
	store := openSQLite(t, path)
	h := newHarnessWithStore(t, 0, store)
	ctx := context.Background()
	now := time.Now()
	if err := store.CreateSession(ctx, contracts.Session{ID: "s1", Name: "EVs", Definition: h.def, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	job, err := h.engine.SubmitRun(ctx, h.def, RunMeta{SessionID: "s1", RunNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	h.wait(job.ID)
	// Editing a stored profile later does not change the run's snapshot.
	p, _ := store.GetProfile(ctx, "demo-motors")
	p.Name = "Edited after the run"
	store.SaveProfile(ctx, p)
	got, _ := store.GetJob(ctx, job.ID)
	if got.SessionID != "s1" || got.RunNumber != 1 || got.Definition.SearchSources[0].Profile.Name != "Demo Motors (HTML)" {
		t.Fatalf("run %s #%d snapshot profile %q", got.SessionID, got.RunNumber, got.Definition.SearchSources[0].Profile.Name)
	}
}
