package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/demosite"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/engine"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/profile"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/session"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage/storeconfig"
	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

// startAPI composes the API exactly as main does, against dbPath.
func startAPI(t *testing.T, dbPath, demoURL string) (*httptest.Server, func()) {
	t.Helper()
	store, closeStore, err := storeconfig.Open(storeconfig.Config{Kind: storeconfig.SQLite, SQLitePath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	profiles, _ := profile.LoadDir("../../examples/sources")
	zero := 0
	for _, p := range profiles {
		if _, err := store.GetProfile(ctx, p.ID); err == nil {
			continue
		}
		p.Request.URL = strings.Replace(p.Request.URL, "http://127.0.0.1:8089", demoURL, 1)
		p.Limits.DelayMs = &zero
		store.SaveProfile(ctx, p)
	}
	eng := engine.New(store, engine.Options{RetryBackoff: time.Millisecond})
	if _, err := eng.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	srv := &server{store: store, engine: eng, sessions: session.New(store, eng),
		info: apiInfo{Features: apiFeatures, Store: storeconfig.SQLite, Database: dbPath}}
	ts := httptest.NewServer(srv.routes())
	return ts, func() {
		eng.Shutdown(ctx)
		ts.Close()
		closeStore()
	}
}

func call(t *testing.T, method, url string, body any, out any) int {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, r)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("%s %s: %v\n%s", method, url, err, data)
		}
	}
	return resp.StatusCode
}

func waitRun(t *testing.T, base, id string) contracts.Job {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var job contracts.Job
		call(t, "GET", base+"/api/runs/"+id, nil, &job)
		if job.Status.Terminal() {
			return job
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run did not finish")
	return contracts.Job{}
}

func TestSessionRunFlowSurvivesRestart(t *testing.T) {
	demo := httptest.NewServer(demosite.Handler(demosite.Options{}))
	defer demo.Close()
	dbPath := filepath.Join(t.TempDir(), "data", "pds.db")
	ts, stop := startAPI(t, dbPath, demo.URL)

	var info apiInfo
	if code := call(t, "GET", ts.URL+"/api/info", nil, &info); code != 200 || info.Store != "sqlite" || len(info.Features) == 0 {
		t.Fatalf("info: %d %+v", code, info)
	}
	resp, _ := http.Get(ts.URL + "/api/ai/authoring-prompt")
	prompt, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(prompt), "Prompt Driven Scraper — instructions for AI assistants") {
		t.Fatal("authoring prompt does not contain ai/PROFILE_AUTHORING.md")
	}

	data, _ := os.ReadFile("../../examples/ev-cars.job.yaml")
	def, _ := profile.DecodeJob(data)
	var created struct {
		Session contracts.Session           `json:"session"`
		Issues  []contracts.ValidationIssue `json:"issues"`
	}
	if code := call(t, "POST", ts.URL+"/api/sessions", map[string]any{"name": "EVs", "definition": def}, &created); code != 201 {
		t.Fatalf("create session: %d", code)
	}
	id := created.Session.ID
	// A second, untouched session.
	call(t, "POST", ts.URL+"/api/sessions", map[string]any{"name": "Other"}, nil)

	var run1 contracts.Job
	if code := call(t, "POST", ts.URL+"/api/sessions/"+id+"/runs", nil, &run1); code != 201 {
		t.Fatalf("run: %d", code)
	}
	run1 = waitRun(t, ts.URL, run1.ID)
	var run2 contracts.Job
	call(t, "POST", ts.URL+"/api/sessions/"+id+"/runs", nil, &run2)
	run2 = waitRun(t, ts.URL, run2.ID)
	resp, _ = http.Get(ts.URL + "/api/runs/" + run1.ID + "/output")
	out1, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// Invalid edits are saved as drafts but cannot run.
	broken := def
	broken.Output.Fields = nil
	var upd map[string]any
	call(t, "PUT", ts.URL+"/api/sessions/"+id, map[string]any{"name": "EVs (draft)", "definition": broken}, &upd)
	if code := call(t, "POST", ts.URL+"/api/sessions/"+id+"/runs", nil, nil); code != 400 {
		t.Fatalf("running an invalid session: %d", code)
	}
	stop()

	// Restart on the same database.
	ts, stop = startAPI(t, dbPath, demo.URL)
	defer stop()
	var list []contracts.SessionSummary
	call(t, "GET", ts.URL+"/api/sessions", nil, &list)
	if len(list) != 2 || list[0].ID != id || list[0].RunCount != 2 || list[0].LastRun == nil || list[0].LastRun.ID != run2.ID {
		t.Fatalf("sessions after restart: %+v", list)
	}
	var detail struct {
		Session contracts.Session      `json:"session"`
		Runs    []contracts.RunSummary `json:"runs"`
	}
	call(t, "GET", ts.URL+"/api/sessions/"+id, nil, &detail)
	if detail.Session.Name != "EVs (draft)" || len(detail.Runs) != 2 || detail.Runs[1].RunNumber != 1 || detail.Runs[1].RecordsAccepted != 18 {
		t.Fatalf("session after restart: %+v", detail)
	}
	var old contracts.Job
	call(t, "GET", ts.URL+"/api/runs/"+run1.ID, nil, &old)
	if len(old.Definition.Output.Fields) == 0 || old.Status != contracts.JobCompleted {
		t.Fatal("run 1 snapshot was changed by the session edit")
	}
	resp, _ = http.Get(ts.URL + "/api/runs/" + run1.ID + "/output")
	again, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Equal(again, out1) {
		t.Fatalf("output after restart: %d", resp.StatusCode)
	}
	var recs []contracts.Record
	call(t, "GET", ts.URL+"/api/runs/"+run1.ID+"/records", nil, &recs)
	if len(recs) != 18 {
		t.Fatalf("records after restart: %d", len(recs))
	}
	// The event stream replays a finished run and ends.
	resp, _ = http.Get(ts.URL + "/api/runs/" + run1.ID + "/events")
	stream, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(stream), "event: RecordAccepted") || !strings.Contains(string(stream), "event: JobCompleted") {
		t.Fatal("event replay incomplete")
	}
	resp, _ = http.Get(ts.URL + "/api/runs/" + run1.ID + "/diagnostics")
	diag, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(diag), "Run #1") || !strings.Contains(string(diag), "CONFIGURATION") ||
		!strings.Contains(string(diag), "SOURCE: Demo Motors (HTML)") {
		t.Fatalf("diagnostics: %d\n%s", resp.StatusCode, diag)
	}
	if code := call(t, "POST", ts.URL+"/api/runs/"+run1.ID+"/resume", nil, nil); code != 409 {
		t.Fatalf("resuming a finished run: %d", code)
	}
}
func TestUserBrowserRejectsForeignOriginAndAllowsLocalUI(t *testing.T) {
	req := httptest.NewRequest("POST", "http://localhost:8080/api/sessions/x/runs", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Origin", "https://unrelated.example")
	if localOperator(req) {
		t.Fatal("foreign website was allowed to operate User Browser")
	}
	req.Header.Set("Origin", "http://localhost:5173")
	if !localOperator(req) {
		t.Fatal("local development UI was refused")
	}
}

func TestLocalOperatorRejectsRemoteClientsAndReboundHost(t *testing.T) {
	for _, tc := range []struct {
		host, remote string
		want         bool
	}{{"localhost:8080", "127.0.0.1:1234", true}, {"127.0.0.1:8080", "127.0.0.1:1234", true}, {"localhost:8080", "192.0.2.1:1234", false}, {"unrelated.example:8080", "127.0.0.1:1234", false}} {
		r := httptest.NewRequest("POST", "http://"+tc.host+"/api/sessions/x/runs", nil)
		r.RemoteAddr = tc.remote
		if got := localOperator(r); got != tc.want {
			t.Errorf("host=%s remote=%s local=%v", tc.host, tc.remote, got)
		}
	}
}
