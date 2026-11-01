// Command scraper is the Prompt Driven Scraper CLI. It runs the same
// deterministic engine as the API, with no UI and no network services
// required beyond the sources being scraped.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/kevinwolrath/prompt-driven-scraper/ai"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/demosite"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/engine"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/fetch/browser"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/profile"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage"
	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

const usage = `Prompt Driven Scraper - deterministic, profile-driven scraping

Usage:
  scraper validate [-profiles dir] <job-or-profile.yaml>
  scraper run [-profiles dir] [-o file] [-demo-site] [-events] <job.yaml>
  scraper demo-site [-addr 127.0.0.1:8089] [-latency 0s]
  scraper prompt      print the AI authoring prompt to paste into ChatGPT/Claude

Profiles referenced by profileRef are loaded from -profiles (default: the
"sources" directory next to the job file).

While "run" is in progress:
  Ctrl-C once   give me what you've got (stop, salvage, output, mark partial)
  Ctrl-C twice  cancel
  p / r + Enter pause / resume
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "validate":
		err = validate(os.Args[2:])
	case "run":
		err = run(os.Args[2:])
	case "demo-site":
		err = demoSite(os.Args[2:])
	case "prompt":
		fmt.Print(ai.BootstrapPrompt())
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func loadStore(profilesDir, jobPath string) (*storage.MemoryStore, error) {
	store := storage.NewMemoryStore()
	if profilesDir == "" {
		candidate := filepath.Join(filepath.Dir(jobPath), "sources")
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			profilesDir = candidate
		}
	}
	if profilesDir == "" {
		return store, nil
	}
	profiles, errs := profile.LoadDir(profilesDir)
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, "warning: skipped profile:", e)
	}
	for _, p := range profiles {
		if err := store.SaveProfile(context.Background(), p); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func validate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	profilesDir := fs.String("profiles", "", "directory of source profiles for profileRef")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("validate needs one file")
	}
	path := fs.Arg(0)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var issues []contracts.ValidationIssue
	if profile.LooksLikeJob(data) {
		def, err := profile.DecodeJob(data)
		if err != nil {
			return err
		}
		store, err := loadStore(*profilesDir, path)
		if err != nil {
			return err
		}
		_, issues, _ = engine.New(store, engine.Options{}).Prepare(context.Background(), &def)
	} else {
		p, err := profile.DecodeProfile(data)
		if err != nil {
			return err
		}
		issues = profile.ValidateProfile(p)
	}
	failed := false
	for _, is := range issues {
		fmt.Printf("%-7s %s: %s\n", is.Severity, is.Path, is.Message)
		failed = failed || is.Severity == "error"
	}
	if failed {
		return errors.New("validation failed")
	}
	fmt.Println("ok:", path)
	return nil
}

func run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	profilesDir := fs.String("profiles", "", "directory of source profiles for profileRef")
	outPath := fs.String("o", "", "output file (default: stdout)")
	withDemo := fs.Bool("demo-site", false, "serve the demo site on 127.0.0.1:8089 during the run")
	showEvents := fs.Bool("events", false, "print every event as JSON lines to stderr")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("run needs one job file")
	}
	path := fs.Arg(0)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	def, err := profile.DecodeJob(data)
	if err != nil {
		return err
	}
	store, err := loadStore(*profilesDir, path)
	if err != nil {
		return err
	}
	if *withDemo {
		srv := &http.Server{Addr: "127.0.0.1:8089", Handler: demosite.Handler(demosite.Options{})}
		go func() { _ = srv.ListenAndServe() }()
		defer srv.Close()
		time.Sleep(50 * time.Millisecond)
	}

	ctx := context.Background()
	br := browser.New(browser.OptionsFromEnv())
	defer br.Close()
	userBrowser := browser.NewUser(browser.UserOptionsFromEnv())
	defer userBrowser.Close()
	eng := engine.New(store, engine.Options{Browser: br, UserBrowser: userBrowser})
	job, err := eng.Submit(ctx, def)
	if err != nil {
		var ve *engine.ValidationError
		if errors.As(err, &ve) {
			for _, is := range ve.Issues {
				fmt.Fprintf(os.Stderr, "%-7s %s: %s\n", is.Severity, is.Path, is.Message)
			}
		}
		return err
	}
	fmt.Fprintf(os.Stderr, "job %s started (%d sources)\n", job.ID, len(def.SearchSources))

	backlog, events, unsubscribe, err := eng.Subscribe(ctx, job.ID, 0)
	if err != nil {
		return err
	}
	defer unsubscribe()

	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt)
	go func() {
		<-sigs
		fmt.Fprintln(os.Stderr, "\n> give me what you've got (Ctrl-C again to cancel)")
		_, _ = eng.Finalise(ctx, job.ID)
		<-sigs
		fmt.Fprintln(os.Stderr, "\n> cancelling")
		_, _ = eng.Cancel(ctx, job.ID)
	}()
	go func() {
		in := bufio.NewScanner(os.Stdin)
		for in.Scan() {
			switch strings.TrimSpace(in.Text()) {
			case "p":
				_, err = eng.Pause(ctx, job.ID)
			case "r":
				_, err = eng.Resume(ctx, job.ID)
			case "g":
				_, err = eng.Finalise(ctx, job.ID)
			case "c":
				_, err = eng.Cancel(ctx, job.ID)
			default:
				continue
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, ">", err)
			}
		}
	}()

	var last int64
	handle := func(ev contracts.Event) bool {
		if ev.Seq <= last {
			return false
		}
		last = ev.Seq
		printEvent(ev, *showEvents)
		return ev.Status.Terminal()
	}
	done := false
	for _, ev := range backlog {
		done = handle(ev) || done
	}
	for !done {
		ev, ok := <-events
		if !ok {
			// Fell behind; catch up from the store.
			backlog, events, unsubscribe, err = eng.Subscribe(ctx, job.ID, last)
			if err != nil {
				return err
			}
			defer unsubscribe()
			for _, ev := range backlog {
				done = handle(ev) || done
			}
			continue
		}
		done = handle(ev)
	}

	final, _ := eng.Get(ctx, job.ID)
	p := final.Progress
	fmt.Fprintf(os.Stderr, "\n%s: %d accepted, %d rejected, %d duplicates, %d pages, %d warnings, %d errors in %s\n",
		final.Status, p.RecordsAccepted, p.RecordsRejected, p.Duplicates, p.PagesVisited, p.Warnings, p.Errors,
		(time.Duration(p.ElapsedMs) * time.Millisecond).Round(time.Millisecond))
	if final.StatusReason != "" {
		fmt.Fprintln(os.Stderr, "reason:", final.StatusReason)
	}
	out, err := eng.Output(ctx, job.ID)
	if err != nil {
		if final.Status == contracts.JobCancelled {
			return nil
		}
		return fmt.Errorf("no output: %w", err)
	}
	if *outPath == "" {
		_, err = os.Stdout.Write(out.Data)
		return err
	}
	if err := os.WriteFile(*outPath, out.Data, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %d records to %s\n", out.RecordCount, *outPath)
	return nil
}

func printEvent(ev contracts.Event, asJSON bool) {
	if asJSON {
		ev.Progress = nil
		b, _ := json.Marshal(ev)
		fmt.Fprintln(os.Stderr, string(b))
		return
	}
	src := ""
	if ev.SourceID != "" {
		src = "[" + ev.SourceID + "] "
	}
	switch ev.Type {
	case contracts.EventPageDiscovered, contracts.EventRecordFound:
		return // too chatty for the terminal; use -events
	case contracts.EventRecordAccepted:
		if rec, ok := ev.Data["record"].(contracts.Record); ok {
			fmt.Fprintf(os.Stderr, "  + %s%s\n", src, summarise(rec.Values))
			return
		}
	case contracts.EventRecordRejected:
		fmt.Fprintf(os.Stderr, "  - %s%s\n", src, ev.Message)
		return
	case contracts.EventPageFetched:
		fmt.Fprintf(os.Stderr, "  . %sfetched %s\n", src, ev.Message)
		return
	}
	msg := ev.Message
	if msg != "" {
		msg = ": " + msg
	}
	fmt.Fprintf(os.Stderr, "%s %s%s%s\n", ev.Timestamp.Format("15:04:05"), src, ev.Type, msg)
}

func summarise(values map[string]any) string {
	var parts []string
	for _, k := range []string{"year", "make", "model", "price"} {
		if v, ok := values[k]; ok && v != nil {
			parts = append(parts, fmt.Sprint(v))
		}
	}
	if len(parts) == 0 {
		b, _ := json.Marshal(values)
		return string(b)
	}
	return strings.Join(parts, " ")
}

func demoSite(args []string) error {
	fs := flag.NewFlagSet("demo-site", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8089", "listen address")
	latency := fs.Duration("latency", 0, "artificial latency per response")
	_ = fs.Parse(args)
	fmt.Fprintf(os.Stderr, "demo site on http://%s/\n", *addr)
	return http.ListenAndServe(*addr, demosite.Handler(demosite.Options{Latency: *latency}))
}
