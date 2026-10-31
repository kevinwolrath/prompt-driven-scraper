// Package storagetest is a contract test suite every storage.Store
// implementation must pass.
package storagetest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage"
	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

var t0 = time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)

func at(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

func definition(query string) contracts.JobDefinition {
	return contracts.JobDefinition{
		Name:       "EVs",
		QueryInput: query,
		Query:      contracts.Query{Params: map[string]any{"fuel": "electric"}},
		SearchSources: []contracts.SourceRef{{ID: "s1", ProfileRef: "s1", Profile: &contracts.SourceProfile{
			ID: "s1", Request: contracts.RequestSpec{URL: "https://example.test"}, Listing: contracts.Listing{Items: ".a"},
			Fields: map[string]contracts.FieldRule{"make": {Candidate: contracts.Candidate{Css: ".make"}}},
		}}},
		Output: contracts.OutputDefinition{Format: "csv", Fields: contracts.OutputFields{{Name: "make", Type: "string", Required: true}}},
	}
}

func session(id string, min int) contracts.Session {
	return contracts.Session{ID: id, Name: "Session " + id, Definition: definition("query " + id), CreatedAt: at(min), UpdatedAt: at(min)}
}

func job(id, sessionID string, number, min int) contracts.Job {
	return contracts.Job{
		ID: id, SessionID: sessionID, RunNumber: number, Definition: definition("snapshot " + id),
		Status: contracts.JobQueued, CreatedAt: at(min), UpdatedAt: at(min),
		Progress: contracts.Progress{SourcesTotal: 1, Sources: []contracts.SourceProgress{{ID: "s1", Status: contracts.SourcePending}}},
	}
}

// Run executes the contract suite. newStore must return an empty store.
func Run(t *testing.T, newStore func(t *testing.T) storage.Store) {
	ctx := context.Background()

	t.Run("sessions", func(t *testing.T) {
		s := newStore(t)
		must(t, s.CreateSession(ctx, session("a", 0)))
		must(t, s.CreateSession(ctx, session("b", 1)))
		if err := s.CreateSession(ctx, session("a", 2)); err == nil {
			t.Fatal("duplicate session id accepted")
		}
		// Creating a new session never touches existing ones.
		must(t, s.CreateSession(ctx, session("c", 2)))
		a, err := s.GetSession(ctx, "a")
		must(t, err)
		if !reflect.DeepEqual(a.Definition, session("a", 0).Definition) || a.Name != "Session a" {
			t.Fatalf("session a changed: %+v", a)
		}
		// Update only changes the target session.
		upd := a
		upd.Name, upd.Definition.QueryInput, upd.UpdatedAt = "Renamed", "edited", at(10)
		must(t, s.UpdateSession(ctx, upd))
		a, _ = s.GetSession(ctx, "a")
		b, _ := s.GetSession(ctx, "b")
		if a.Name != "Renamed" || a.Definition.QueryInput != "edited" || !a.CreatedAt.Equal(at(0)) {
			t.Fatalf("update not applied: %+v", a)
		}
		if b.Definition.QueryInput != "query b" {
			t.Fatalf("other session changed: %+v", b)
		}
		list, err := s.ListSessions(ctx)
		must(t, err)
		if ids := sessionIDs(list); !reflect.DeepEqual(ids, []string{"a", "c", "b"}) {
			t.Fatalf("list order %v, want most recently updated first", ids)
		}
		if _, err := s.GetSession(ctx, "missing"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("missing session: %v", err)
		}
		if err := s.UpdateSession(ctx, session("missing", 0)); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("update missing session: %v", err)
		}
	})

	t.Run("runs", func(t *testing.T) {
		s := newStore(t)
		must(t, s.CreateSession(ctx, session("a", 0)))
		must(t, s.CreateSession(ctx, session("b", 0)))
		must(t, s.CreateJob(ctx, job("r1", "a", 1, 1)))
		must(t, s.CreateJob(ctx, job("r2", "a", 2, 2)))
		must(t, s.CreateJob(ctx, job("r3", "b", 1, 3)))
		must(t, s.CreateJob(ctx, job("adhoc", "", 0, 4)))
		if err := s.CreateJob(ctx, job("r4", "a", 2, 5)); err == nil {
			t.Fatal("duplicate run number in a session accepted")
		}
		if err := s.CreateJob(ctx, job("r1", "a", 3, 5)); err == nil {
			t.Fatal("duplicate run id accepted (would overwrite a run)")
		}
		runs, err := s.ListSessionJobs(ctx, "a")
		must(t, err)
		if len(runs) != 2 || runs[0].ID != "r2" || runs[1].ID != "r1" {
			t.Fatalf("session runs %v", jobIDs(runs))
		}
		all, _ := s.ListJobs(ctx)
		if len(all) != 4 || all[0].ID != "adhoc" {
			t.Fatalf("all runs %v", jobIDs(all))
		}

		// The configuration snapshot and identity are fixed at creation.
		r1, _ := s.GetJob(ctx, "r1")
		changed := r1
		changed.Definition = definition("tampered")
		changed.SessionID, changed.RunNumber = "b", 9
		changed.Status, changed.Progress.RecordsAccepted = contracts.JobRunning, 5
		started := at(6)
		changed.StartedAt, changed.UpdatedAt = &started, at(6)
		must(t, s.UpdateJob(ctx, changed))
		r1, _ = s.GetJob(ctx, "r1")
		if r1.Definition.QueryInput != "snapshot r1" || r1.SessionID != "a" || r1.RunNumber != 1 {
			t.Fatalf("snapshot/identity changed: %+v", r1)
		}
		if r1.Status != contracts.JobRunning || r1.Progress.RecordsAccepted != 5 || r1.StartedAt == nil || !r1.StartedAt.Equal(started) {
			t.Fatalf("update not applied: %+v", r1)
		}

		// Finished runs are immutable.
		r1.Status = contracts.JobCompleted
		finished := at(7)
		r1.FinishedAt = &finished
		must(t, s.UpdateJob(ctx, r1))
		r1.Status, r1.Progress.RecordsAccepted = contracts.JobRunning, 99
		if err := s.UpdateJob(ctx, r1); !errors.Is(err, storage.ErrImmutable) {
			t.Fatalf("update of finished run: %v", err)
		}
		r1, _ = s.GetJob(ctx, "r1")
		if r1.Status != contracts.JobCompleted || r1.Progress.RecordsAccepted != 5 {
			t.Fatalf("finished run changed: %+v", r1)
		}
		if err := s.UpdateJob(ctx, job("missing", "", 0, 0)); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("update missing run: %v", err)
		}
		if _, err := s.GetJob(ctx, "missing"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("missing run: %v", err)
		}
	})

	t.Run("events records checkpoints outputs", func(t *testing.T) {
		s := newStore(t)
		must(t, s.CreateJob(ctx, job("r1", "", 0, 0)))
		must(t, s.CreateJob(ctx, job("r2", "", 0, 0)))
		for i := 1; i <= 3; i++ {
			prog := &contracts.Progress{RecordsAccepted: i}
			must(t, s.AppendEvent(ctx, contracts.Event{JobID: "r1", Seq: int64(i), Type: contracts.EventRecordAccepted,
				SourceID: "s1", Message: fmt.Sprint(i), Timestamp: at(i), Status: contracts.JobRunning,
				Data: map[string]any{"n": float64(i)}, Progress: prog}))
		}
		must(t, s.AppendEvent(ctx, contracts.Event{JobID: "r2", Seq: 1, Type: contracts.EventJobStarted, Timestamp: at(0), Status: contracts.JobRunning}))
		evs, err := s.ListEvents(ctx, "r1", 1)
		must(t, err)
		if len(evs) != 2 || evs[0].Seq != 2 || evs[1].Message != "3" || evs[1].Data["n"] != float64(3) ||
			evs[1].Progress == nil || evs[1].Progress.RecordsAccepted != 3 || !evs[1].Timestamp.Equal(at(3)) {
			t.Fatalf("events %+v", evs)
		}

		for i, v := range []string{"Tesla", "BYD"} {
			must(t, s.AppendRecord(ctx, contracts.Record{JobID: "r1", SourceID: "s1", Key: v, Values: map[string]any{"make": v},
				Provenance: map[string]string{"make": "css .make"}, Partial: i == 1, SeenAt: at(i)}))
		}
		recs, err := s.ListRecords(ctx, "r1")
		must(t, err)
		if len(recs) != 2 || recs[0].Values["make"] != "Tesla" || recs[1].Key != "BYD" || !recs[1].Partial || recs[0].Provenance["make"] != "css .make" {
			t.Fatalf("records %+v", recs)
		}
		if other, _ := s.ListRecords(ctx, "r2"); len(other) != 0 {
			t.Fatalf("records leaked between runs: %+v", other)
		}

		if _, err := s.GetCheckpoint(ctx, "r1"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("missing checkpoint: %v", err)
		}
		must(t, s.SaveCheckpoint(ctx, contracts.Checkpoint{JobID: "r1", State: []byte(`{"a":1}`), UpdatedAt: at(1)}))
		must(t, s.SaveCheckpoint(ctx, contracts.Checkpoint{JobID: "r1", State: []byte(`{"a":2}`), UpdatedAt: at(2)}))
		cp, err := s.GetCheckpoint(ctx, "r1")
		must(t, err)
		if string(cp.State) != `{"a":2}` {
			t.Fatalf("checkpoint %s", cp.State)
		}

		out := contracts.Output{JobID: "r1", Format: "csv", ContentType: "text/csv", Partial: true, RecordCount: 2, Data: []byte("make\nTesla\n"), CreatedAt: at(9)}
		must(t, s.SaveOutput(ctx, out))
		if err := s.SaveOutput(ctx, contracts.Output{JobID: "r1", Format: "json", Data: []byte("[]")}); !errors.Is(err, storage.ErrImmutable) {
			t.Fatalf("second output save: %v", err)
		}
		got, err := s.GetOutput(ctx, "r1")
		must(t, err)
		if string(got.Data) != string(out.Data) || !got.Partial || got.RecordCount != 2 || got.Format != "csv" {
			t.Fatalf("output %+v", got)
		}
		if _, err := s.GetOutput(ctx, "r2"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("missing output: %v", err)
		}
	})

	t.Run("profiles", func(t *testing.T) {
		s := newStore(t)
		p := definition("").SearchSources[0].Profile
		must(t, s.SaveProfile(ctx, *p))
		p2 := *p
		p2.Name = "Updated"
		must(t, s.SaveProfile(ctx, p2))
		got, err := s.GetProfile(ctx, "s1")
		must(t, err)
		if got.Name != "Updated" || got.Fields["make"].Css != ".make" {
			t.Fatalf("profile %+v", got)
		}
		list, _ := s.ListProfiles(ctx)
		if len(list) != 1 {
			t.Fatalf("profiles %d", len(list))
		}
		if _, err := s.GetProfile(ctx, "missing"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("missing profile: %v", err)
		}
	})
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func sessionIDs(ss []contracts.Session) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.ID
	}
	return out
}

func jobIDs(js []contracts.Job) []string {
	out := make([]string, len(js))
	for i, j := range js {
		out[i] = j.ID
	}
	return out
}
