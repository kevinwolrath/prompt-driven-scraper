package engine

import (
	"context"
	"errors"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/fetch"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage/sqlite"
	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
	"path/filepath"
	"testing"
	"time"
)

type interactionBrowser struct {
	blocked bool
	calls   int
	last    fetch.BrowserRequest
}

func (b *interactionBrowser) Load(_ context.Context, req fetch.BrowserRequest) (fetch.Response, error) {
	b.calls++
	b.last = req
	resp := fetch.Response{URL: req.URL, StatusCode: 403, Title: "Test interaction fixture", TabID: "test-owned-tab"}
	if b.blocked {
		return resp, &fetch.InteractionRequired{TabID: resp.TabID, Reason: "manual interaction required"}
	}
	if !req.Continue || req.TabID != "test-owned-tab" {
		return resp, errors.New("lost continuation state")
	}
	resp.StatusCode = 200
	resp.Body = []byte(listingHTML(1, ""))
	return resp, nil
}
func waitPausedInteraction(t *testing.T, e *Engine, id string) contracts.Job {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		j, _ := e.Get(context.Background(), id)
		if j.Status == contracts.JobPaused && j.Progress.Interaction != nil {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("did not request interaction")
	return contracts.Job{}
}
func TestInteractionCheckpointSurvivesRestartAndContinue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.db")
	s, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	b := &interactionBrowser{blocked: true}
	e := New(s, Options{UserBrowser: b})
	def := acqJob("https://public.example/cars", nil)
	def.BrowserMode = "user-browser"
	def.SearchSources[0].Profile.Pagination = nil
	j, err := e.Submit(context.Background(), def)
	if err != nil {
		t.Fatal(err)
	}
	paused := waitPausedInteraction(t, e, j.ID)
	if paused.Progress.PagesAttempted != 1 || paused.Progress.PagesVisited != 0 || paused.Progress.Errors != 0 || paused.Progress.Sources[0].Acquisition[0].Interactions != 1 || paused.Progress.Sources[0].Acquisition[0].Failed != 0 {
		t.Fatalf("wrong handoff counts: %+v", paused.Progress)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = e.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	persisted, _ := s.GetJob(ctx, j.ID)
	if persisted.Progress.Interaction == nil {
		t.Fatal("interaction was not persisted")
	}
	// Simulate a crash between the interaction checkpoint and its UI update.
	persisted.Status = contracts.JobRunning
	persisted.Progress.Interaction = nil
	if err := s.UpdateJob(ctx, persisted); err != nil {
		t.Fatal(err)
	}
	e = New(s, Options{UserBrowser: b})
	if _, err = e.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, _ := e.Get(ctx, j.ID)
	if recovered.Progress.Interaction == nil {
		t.Fatal("recovery lost the checkpointed interaction prompt")
	}
	b.blocked = false
	if _, err = e.Resume(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	done, err := e.Wait(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != contracts.JobCompleted || done.Progress.PagesAttempted != 1 || done.Progress.PagesVisited != 1 || done.Progress.RecordsAccepted != 1 || done.Progress.RecordsRejected != 1 || done.Progress.Interaction != nil {
		t.Fatalf("bad continued result: %+v", done)
	}
	events, _ := s.ListEvents(ctx, j.ID, 0)
	requested, resumed := false, false
	for _, ev := range events {
		requested = requested || ev.Type == contracts.EventBrowserInteractionRequired
		resumed = resumed || ev.Type == contracts.EventBrowserInteractionResumed
	}
	if !requested || !resumed {
		t.Fatal("missing interaction history")
	}
}

func TestInteractionCancelAndFinalise(t *testing.T) {
	for _, action := range []string{"cancel", "finalise"} {
		t.Run(action, func(t *testing.T) {
			s, err := sqlite.Open(filepath.Join(t.TempDir(), "run.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			b := &interactionBrowser{blocked: true}
			e := New(s, Options{UserBrowser: b})
			def := acqJob("https://public.example/cars", nil)
			def.BrowserMode = "user-browser"
			j, err := e.Submit(context.Background(), def)
			if err != nil {
				t.Fatal(err)
			}
			waitPausedInteraction(t, e, j.ID)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			if action == "cancel" {
				_, err = e.Cancel(ctx, j.ID)
			} else {
				_, err = e.Finalise(ctx, j.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			done, err := e.Wait(ctx, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := contracts.JobCancelled
			if action == "finalise" {
				want = contracts.JobPartial
			}
			if done.Status != want || b.calls != 1 || done.Progress.Interaction != nil {
				t.Fatalf("unexpected outcome: %s calls %d", done.Status, b.calls)
			}
		})
	}
}
func TestBrowserModeOverridesWithoutMutatingProfile(t *testing.T) {
	for _, mode := range []string{"automatic", "http", "browser", "user-browser"} {
		t.Run(mode, func(t *testing.T) {
			def := acqJob("https://public.example/cars", httpThenBrowser)
			def.BrowserMode = mode
			compiled, _, err := New(storage.NewMemoryStore(), Options{}).Prepare(context.Background(), &def)
			if err != nil {
				t.Fatal(err)
			}
			want := mode
			if want == "automatic" {
				want = "http"
			}
			if compiled[0].Strategies[0].Type != want {
				t.Fatalf("wrong effective strategy: %+v", compiled[0].Strategies)
			}
			if def.SearchSources[0].Profile.Acquisition.Strategies[0].Type != "http" || len(def.SearchSources[0].Profile.Acquisition.Strategies) != 2 {
				t.Fatal("mutated immutable source configuration")
			}
		})
	}
}
func TestAutomaticHTTPToUserBrowserHandoffKeepsOneLogicalPage(t *testing.T) {
	srv := acqSite(t)
	b := &interactionBrowser{blocked: true}
	s := storage.NewMemoryStore()
	e := New(s, Options{UserBrowser: b})
	def := acqJob(srv.URL+"/blocked/cars", &contracts.Acquisition{Strategies: []contracts.AcquisitionStrategy{{Type: "http"}, {Type: "user-browser"}}})
	def.SearchSources[0].Profile.Pagination = nil
	j, err := e.Submit(context.Background(), def)
	if err != nil {
		t.Fatal(err)
	}
	paused := waitPausedInteraction(t, e, j.ID)
	if paused.Progress.PagesAttempted != 1 || len(paused.Progress.Sources[0].Acquisition) != 2 {
		t.Fatalf("bad logical counts: %+v", paused.Progress)
	}
	b.blocked = false
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if _, err = e.Resume(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	done, err := e.Wait(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != contracts.JobCompleted || done.Progress.PagesAttempted != 1 || done.Progress.RecordsAccepted != 1 {
		t.Fatalf("bad fallback continuation: %+v", done.Progress)
	}
}
