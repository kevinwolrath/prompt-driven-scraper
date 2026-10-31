package session

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
	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage/sqlite"
	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

type env struct {
	svc   *Service
	eng   *engine.Engine
	store storage.Store
	def   contracts.JobDefinition
}

func setup(t *testing.T, store storage.Store) env {
	t.Helper()
	srv := httptest.NewServer(demosite.Handler(demosite.Options{}))
	t.Cleanup(srv.Close)
	profiles, errs := profile.LoadDir("../../examples/sources")
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	zero := 0
	for _, p := range profiles {
		p.Request.URL = strings.Replace(p.Request.URL, "http://127.0.0.1:8089", srv.URL, 1)
		p.Limits.DelayMs = &zero
		store.SaveProfile(context.Background(), p)
	}
	data, _ := os.ReadFile("../../examples/ev-cars.job.yaml")
	def, err := profile.DecodeJob(data)
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(store, engine.Options{RetryBackoff: time.Millisecond})
	return env{svc: New(store, eng), eng: eng, store: store, def: def}
}

func (e env) runAndWait(t *testing.T, sessionID string) contracts.Job {
	t.Helper()
	job, err := e.svc.Run(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done, err := e.eng.Wait(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	return done
}

func TestSessionsAndRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pds.db")
	store, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	e := setup(t, store)
	ctx := context.Background()

	// Drafts can be saved; issues are reported, not enforced.
	draft, issues, err := e.svc.Create(ctx, "  ", contracts.JobDefinition{})
	if err != nil || draft.Name != "Untitled session" || len(issues) == 0 {
		t.Fatalf("draft: %+v %v %v", draft, issues, err)
	}
	if _, err := e.svc.Run(ctx, draft.ID); err == nil {
		t.Fatal("an invalid draft must not run")
	}

	sess, issues, err := e.svc.Create(ctx, "EVs from 2024", e.def)
	if err != nil {
		t.Fatal(err)
	}
	for _, is := range issues {
		if is.Severity == "error" {
			t.Fatalf("unexpected issue %+v", is)
		}
	}
	run1 := e.runAndWait(t, sess.ID)
	out1, _ := e.eng.Output(ctx, run1.ID)

	// Edit the session: only EVs with a big battery from one source.
	edited := e.def
	edited.QueryInput = "Big-battery EVs"
	edited.SearchSources = edited.SearchSources[:1]
	edited.Query.Filters = append(edited.Query.Filters, contracts.Filter{Field: "batterySizeKwh", Op: "gte", Value: 75})
	if _, _, err := e.svc.Update(ctx, sess.ID, "Big batteries", edited); err != nil {
		t.Fatal(err)
	}
	run2 := e.runAndWait(t, sess.ID)

	if run1.RunNumber != 1 || run2.RunNumber != 2 || run1.ID == run2.ID {
		t.Fatalf("run numbers %d, %d", run1.RunNumber, run2.RunNumber)
	}
	if run2.Progress.RecordsAccepted >= run1.Progress.RecordsAccepted {
		t.Fatalf("edited session should accept fewer records: %d vs %d", run2.Progress.RecordsAccepted, run1.Progress.RecordsAccepted)
	}

	// Run 1 still reports the configuration and results it had.
	old, _ := store.GetJob(ctx, run1.ID)
	if old.Definition.QueryInput != e.def.QueryInput || len(old.Definition.SearchSources) != 2 || old.Definition.Name != "EVs from 2024" {
		t.Fatalf("run 1 snapshot changed: %q, %d sources, %q", old.Definition.QueryInput, len(old.Definition.SearchSources), old.Definition.Name)
	}
	if out, _ := e.eng.Output(ctx, run1.ID); string(out.Data) != string(out1.Data) {
		t.Fatal("run 1 output changed after run 2")
	}
	if recs, _ := store.ListRecords(ctx, run1.ID); len(recs) != run1.Progress.RecordsAccepted {
		t.Fatalf("run 1 records %d, accepted %d", len(recs), run1.Progress.RecordsAccepted)
	}

	// Sessions list: light summaries, newest activity first, drafts untouched.
	list, err := e.svc.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list %+v %v", list, err)
	}
	if list[0].ID != sess.ID || list[0].RunCount != 2 || list[0].LastRun.RunNumber != 2 || list[0].Name != "Big batteries" {
		t.Fatalf("summary %+v", list[0])
	}
	if list[1].ID != draft.ID || list[1].RunCount != 0 {
		t.Fatalf("draft summary %+v", list[1])
	}

	// Everything survives a restart.
	store.Close()
	store2, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	svc2 := New(store2, engine.New(store2, engine.Options{}))
	runs, err := svc2.Runs(ctx, sess.ID)
	if err != nil || len(runs) != 2 || runs[0].ID != run2.ID || runs[1].Status != contracts.JobCompleted {
		t.Fatalf("runs after restart %+v %v", runs, err)
	}
	reopened, _ := svc2.Get(ctx, sess.ID)
	if reopened.Definition.QueryInput != "Big-battery EVs" {
		t.Fatalf("session config after restart %q", reopened.Definition.QueryInput)
	}
}
