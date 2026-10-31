// Command api exposes the deterministic engine over HTTP with a server-sent
// events stream per job. It can also serve the built web UI and the bundled
// demo site, so one binary is enough for a local or containerised setup.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/demosite"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/engine"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/profile"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage"
	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

const maxBody = 2 << 20

func main() {
	addr := flag.String("addr", envOr("PDS_ADDR", ":8080"), "API listen address")
	profilesDir := flag.String("profiles", envOr("PDS_PROFILES", "examples/sources"), "directory of source profiles to preload")
	webDir := flag.String("web", envOr("PDS_WEB", "apps/web/dist"), "built web UI to serve at / (ignored if missing)")
	demoAddr := flag.String("demo-addr", envOr("PDS_DEMO_ADDR", "127.0.0.1:8089"), "serve the demo site here (empty to disable)")
	demoLatency := flag.Duration("demo-latency", 250*time.Millisecond, "artificial demo site latency, so progress is visible")
	flag.Parse()

	store := storage.NewMemoryStore()
	if *profilesDir != "" {
		profiles, errs := profile.LoadDir(*profilesDir)
		for _, e := range errs {
			log.Println("skipped profile:", e)
		}
		for _, p := range profiles {
			_ = store.SaveProfile(context.Background(), p)
		}
		log.Printf("loaded %d source profiles from %s", len(profiles), *profilesDir)
	}
	if *demoAddr != "" {
		go func() {
			log.Printf("demo site on http://%s/", *demoAddr)
			if err := http.ListenAndServe(*demoAddr, demosite.Handler(demosite.Options{Latency: *demoLatency})); err != nil {
				log.Println("demo site:", err)
			}
		}()
	}

	srv := &server{store: store, engine: engine.New(store, engine.Options{})}
	mux := srv.routes()
	if st, err := os.Stat(*webDir); err == nil && st.IsDir() {
		mux.Handle("GET /", spa(*webDir))
		log.Printf("serving web UI from %s", *webDir)
	}
	log.Printf("prompt-driven-scraper API listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, cors(mux)))
}

type server struct {
	store  storage.Store
	engine *engine.Engine
}

func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/profiles", s.listProfiles)
	mux.HandleFunc("POST /api/profiles", s.saveProfile)
	mux.HandleFunc("POST /api/validate", s.validate)
	mux.HandleFunc("GET /api/jobs", s.listJobs)
	mux.HandleFunc("POST /api/jobs", s.createJob)
	mux.HandleFunc("GET /api/jobs/{id}", s.getJob)
	mux.HandleFunc("POST /api/jobs/{id}/{action}", s.controlJob)
	mux.HandleFunc("GET /api/jobs/{id}/events", s.events)
	mux.HandleFunc("GET /api/jobs/{id}/records", s.records)
	mux.HandleFunc("GET /api/jobs/{id}/output", s.output)
	return mux
}

func (s *server) listProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.store.ListProfiles(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, profiles)
}

func (s *server) saveProfile(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	p, err := profile.DecodeProfile(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	issues := profile.ValidateProfile(p)
	if issues.HasErrors() {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid profile", "issues": issues})
		return
	}
	if err := s.store.SaveProfile(r.Context(), p); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": p, "issues": nonNil(issues)})
}

// validate accepts a job or a source profile as YAML or JSON and reports
// what it is, its decoded form, and any validation issues. Nothing runs.
func (s *server) validate(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if profile.LooksLikeJob(body) {
		def, err := profile.DecodeJob(body)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"kind": "job", "valid": false, "error": err.Error()})
			return
		}
		resolved := def
		_, issues, err := s.engine.Prepare(r.Context(), &resolved)
		writeJSON(w, http.StatusOK, map[string]any{"kind": "job", "valid": err == nil, "job": def, "issues": nonNil(issues)})
		return
	}
	p, err := profile.DecodeProfile(body)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"kind": "profile", "valid": false, "error": err.Error()})
		return
	}
	issues := profile.ValidateProfile(p)
	writeJSON(w, http.StatusOK, map[string]any{"kind": "profile", "valid": !issues.HasErrors(), "profile": p, "issues": nonNil(issues)})
}

func (s *server) listJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.engine.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (s *server) createJob(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	def, err := profile.DecodeJob(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	job, err := s.engine.Submit(r.Context(), def)
	if err != nil {
		var ve *engine.ValidationError
		if errors.As(err, &ve) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid job", "issues": ve.Issues})
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

func (s *server) getJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.engine.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *server) controlJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var job contracts.Job
	var err error
	switch r.PathValue("action") {
	case "pause":
		job, err = s.engine.Pause(r.Context(), id)
	case "resume":
		job, err = s.engine.Resume(r.Context(), id)
	case "cancel":
		job, err = s.engine.Cancel(r.Context(), id)
	case "finalise", "finalize", "give-me-what-youve-got":
		job, err = s.engine.Finalise(r.Context(), id)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown action"})
		return
	}
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// events streams job events as SSE. Each event's id is its sequence number,
// so EventSource reconnects resume exactly where they left off.
func (s *server) events(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}
	var last int64
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		fmt.Sscan(v, &last)
	} else if v := r.URL.Query().Get("since"); v != "" {
		fmt.Sscan(v, &last)
	}
	ctx := r.Context()
	backlog, ch, unsubscribe, err := s.engine.Subscribe(ctx, id, last)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	send := func(ev contracts.Event) bool {
		if ev.Seq <= last {
			return false
		}
		last = ev.Seq
		b, _ := json.Marshal(ev)
		fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Type, b)
		return ev.Status.Terminal()
	}
	for _, ev := range backlog {
		if send(ev) {
			flusher.Flush()
			unsubscribe()
			return
		}
	}
	flusher.Flush()
	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	defer func() { unsubscribe() }()
	for {
		select {
		case <-ctx.Done():
			return
		case <-keepAlive.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				// Consumer fell behind: replay from the store and continue.
				backlog, ch, unsubscribe, err = s.engine.Subscribe(ctx, id, last)
				if err != nil {
					return
				}
				for _, ev := range backlog {
					if send(ev) {
						flusher.Flush()
						return
					}
				}
				flusher.Flush()
				continue
			}
			done := send(ev)
			flusher.Flush()
			if done {
				return
			}
		}
	}
}

func (s *server) records(w http.ResponseWriter, r *http.Request) {
	if _, err := s.engine.Get(r.Context(), r.PathValue("id")); err != nil {
		writeEngineError(w, err)
		return
	}
	recs, err := s.engine.Records(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNilRecords(recs))
}

func (s *server) output(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	out, err := s.engine.Output(r.Context(), id)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	ext := out.Format
	w.Header().Set("Content-Type", out.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.%s"`, id, ext))
	_, _ = w.Write(out.Data)
}

func readBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, maxBody))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeEngineError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, engine.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, engine.ErrInvalidAction):
		writeError(w, http.StatusConflict, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

func nonNil(issues []contracts.ValidationIssue) []contracts.ValidationIssue {
	if issues == nil {
		return []contracts.ValidationIssue{}
	}
	return issues
}

func nonNilRecords(r []contracts.Record) []contracts.Record {
	if r == nil {
		return []contracts.Record{}
	}
	return r
}

// cors allows a separately served UI (e.g. the Vite dev server) to call the API.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Last-Event-ID")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// spa serves a built single-page app, falling back to index.html.
func spa(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := os.Stat(filepath.Join(dir, filepath.Clean(r.URL.Path))); err != nil {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		files.ServeHTTP(w, r)
	})
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
