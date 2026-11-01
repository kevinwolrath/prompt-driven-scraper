// Package diagnostics renders a run as a plain-text report: what was
// configured, what each source's acquisition, extraction and filtering
// stages did, and the relevant events. It is meant to be read by a person
// or pasted into an AI assistant to revise the configuration, and is built
// only from the stored run document and its events.
package diagnostics

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/extract"
	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

const (
	maxLogLines = 250
	maxGroups   = 10
)

// Report renders the diagnostics for one run.
func Report(job contracts.Job, events []contracts.Event) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	p := job.Progress

	w("Prompt Driven Scraper — Run Diagnostics")
	w("")
	w("SESSION")
	w("%s", nonEmpty(job.Definition.Name, "(ad-hoc run)"))
	if job.SessionID != "" {
		w("id: %s", job.SessionID)
	}
	w("")
	w("RUN")
	if job.RunNumber > 0 {
		w("Run #%d (%s)", job.RunNumber, job.ID)
	} else {
		w("Run %s", job.ID)
	}
	w("Status: %s", title(string(job.Status)))
	if job.StatusReason != "" {
		w("Reason: %s", job.StatusReason)
	}
	w("Duration: %s", (time.Duration(job.RunningMs) * time.Millisecond).Round(time.Second))
	if job.StartedAt != nil {
		w("Started: %s", job.StartedAt.UTC().Format(time.RFC3339))
	}
	if job.FinishedAt != nil {
		w("Finished: %s", job.FinishedAt.UTC().Format(time.RFC3339))
	}
	w("Query input: %q", job.Definition.QueryInput)
	w("")
	w("SUMMARY")
	w("Sources: %d/%d", p.SourcesComplete, p.SourcesTotal)
	w("Pages loaded/attempted: %d/%d", p.PagesVisited, p.PagesAttempted)
	w("Records found: %d", p.RecordsFound)
	w("Accepted: %d", p.RecordsAccepted)
	w("Rejected: %d", p.RecordsRejected)
	w("Duplicates: %d", p.Duplicates)
	w("Warnings: %d", p.Warnings)
	w("Errors: %d", p.Errors)
	w("")

	bySource := map[string][]contracts.Event{}
	for _, ev := range events {
		bySource[ev.SourceID] = append(bySource[ev.SourceID], ev)
	}
	for _, s := range p.Sources {
		writeSource(&b, s, bySource[s.ID])
	}

	if jobLevel := bySource[""]; len(jobLevel) > 0 {
		var warns []string
		for _, ev := range jobLevel {
			if ev.Type == contracts.EventWarning || ev.Type == contracts.EventError {
				warns = append(warns, string(ev.Type)+": "+ev.Message)
			}
		}
		if len(warns) > 0 {
			w("JOB-LEVEL WARNINGS AND ERRORS")
			for _, line := range group(warns) {
				w("- %s", line)
			}
			w("")
		}
	}

	w("CONFIGURATION (snapshot used by this run; source profiles inline)")
	cfg, err := yaml.Marshal(job.Definition)
	if err != nil {
		w("(could not render: %v)", err)
	} else {
		b.Write(cfg)
	}
	w("")

	w("EVENT LOG (page discovery and per-record found/accepted events omitted)")
	n := 0
	for _, ev := range events {
		switch ev.Type {
		case contracts.EventPageDiscovered, contracts.EventRecordFound, contracts.EventRecordAccepted:
			continue
		}
		if n == maxLogLines {
			w("… %d more events not shown", countRemaining(events, ev.Seq))
			break
		}
		src := ""
		if ev.SourceID != "" {
			src = "[" + ev.SourceID + "] "
		}
		msg := ev.Message
		if msg != "" {
			msg = ": " + msg
		}
		w("%s %s%s%s", ev.Timestamp.UTC().Format("15:04:05.000"), src, ev.Type, msg)
		n++
	}
	return b.String()
}

func writeSource(b *strings.Builder, s contracts.SourceProgress, events []contracts.Event) {
	w := func(format string, args ...any) { fmt.Fprintf(b, format+"\n", args...) }
	w("SOURCE: %s", nonEmpty(s.Name, s.ID))
	w("id: %s", s.ID)
	w("Status: %s", title(string(s.Status)))
	w("")

	w("Acquisition:")
	used := false
	for i, a := range s.Acquisition {
		if a.Attempts == 0 {
			continue
		}
		used = true
		label := map[string]string{"http": "HTTP", "browser": "Browser"}[a.Strategy]
		if label == "" {
			label = a.Strategy
		}
		if i > 0 {
			label += " fallback"
		}
		result := a.LastResult
		if a.Failed == 0 {
			result = "page loaded"
		}
		w("- %s: %s (%d of %d attempts succeeded)", label, result, a.Succeeded, a.Attempts)
	}
	if !used {
		w("- not started")
	}
	w("- Pages loaded: %d/%d", s.PagesVisited, s.PagesAttempted)
	w("")

	w("Extraction:")
	switch {
	case s.PagesVisited == 0:
		w("- Not reached (no page acquired)")
	case s.RecordsFound == 0:
		w("- %d items on %d loaded page(s): check listing.items and the field rules", s.RecordsFound, s.PagesVisited)
	default:
		w("- %d items found on %d loaded page(s)", s.RecordsFound, s.PagesVisited)
	}
	for _, ev := range events {
		if o, ok := outline(ev); ok {
			writeOutline(b, ev.Message, o)
		}
	}
	w("")

	w("Filtering:")
	if s.RecordsFound == 0 {
		w("- Not reached")
	} else {
		w("- %d accepted, %d rejected, %d duplicates", s.RecordsAccepted, s.RecordsRejected, s.Duplicates)
		var reasons []string
		for _, ev := range events {
			if ev.Type == contracts.EventRecordRejected {
				reasons = append(reasons, ev.Message)
			}
		}
		if len(reasons) > 0 {
			w("- Rejection reasons:")
			for _, line := range group(normaliseReasons(reasons)) {
				w("  - %s", line)
			}
		}
	}
	w("")

	var warns, errs []string
	for _, ev := range events {
		switch ev.Type {
		case contracts.EventWarning:
			if _, ok := outline(ev); !ok {
				warns = append(warns, ev.Message)
			}
		case contracts.EventError:
			errs = append(errs, ev.Message)
		}
	}
	if s.Status == contracts.SourceFailed {
		errs = append([]string{fmt.Sprintf("Source failed at %s: %s", nonEmpty(s.FailureStage, "unknown stage"), s.FailureReason)}, errs...)
	}
	if len(errs) > 0 {
		w("Errors:")
		for _, line := range group(errs) {
			w("- %s", line)
		}
		w("")
	}
	if len(warns) > 0 {
		w("Warnings:")
		for _, line := range group(warns) {
			w("- %s", line)
		}
		w("")
	}
	w("")
}

func writeOutline(b *strings.Builder, msg string, o extract.PageOutline) {
	w := func(format string, args ...any) { fmt.Fprintf(b, format+"\n", args...) }
	w("- %s", msg)
	w("  Page: %s", o.URL)
	if o.Title != "" {
		w("  Title: %q", o.Title)
	}
	w("  Size: %d bytes, %d characters of visible text, %d scripts", o.Bytes, o.TextChars, o.Scripts)
	if len(o.Repeated) > 0 {
		w("  Most repeated elements (candidate listing.items selectors):")
		for _, r := range o.Repeated {
			if r.Sample != "" {
				w("    %s ×%d — %q", r.Selector, r.Count, r.Sample)
			} else {
				w("    %s ×%d", r.Selector, r.Count)
			}
		}
	}
	if len(o.JSONLDTypes) > 0 {
		w("  JSON-LD types: %s", strings.Join(o.JSONLDTypes, ", "))
	}
	if len(o.EmbeddedJSON) > 0 {
		w("  Embedded JSON: %s", strings.Join(o.EmbeddedJSON, ", "))
	}
	if len(o.JSONKeys) > 0 {
		w("  JSON top-level keys: %s", strings.Join(o.JSONKeys, ", "))
	}
	for _, h := range o.Hints {
		w("  Hint: %s", h)
	}
}

// outline extracts a page outline from an event, whether it is held in
// memory as a struct or was decoded from storage as a map.
func outline(ev contracts.Event) (extract.PageOutline, bool) {
	raw, ok := ev.Data["outline"]
	if !ok {
		return extract.PageOutline{}, false
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return extract.PageOutline{}, false
	}
	var o extract.PageOutline
	return o, json.Unmarshal(data, &o) == nil
}

var gotValue = regexp.MustCompile(`: got .*$`)

// normaliseReasons groups "filter price lte 10000: got 12500" style reasons.
func normaliseReasons(in []string) []string {
	out := make([]string, len(in))
	for i, r := range in {
		out[i] = gotValue.ReplaceAllString(r, "")
	}
	return out
}

// group collapses repeated lines into "line (×n)", most frequent first.
func group(lines []string) []string {
	counts := map[string]int{}
	var order []string
	for _, l := range lines {
		if counts[l] == 0 {
			order = append(order, l)
		}
		counts[l]++
	}
	sort.SliceStable(order, func(a, b int) bool { return counts[order[a]] > counts[order[b]] })
	var out []string
	for i, l := range order {
		if i == maxGroups {
			out = append(out, fmt.Sprintf("… and %d more distinct messages", len(order)-maxGroups))
			break
		}
		if counts[l] > 1 {
			l = fmt.Sprintf("%s (×%d)", l, counts[l])
		}
		out = append(out, l)
	}
	return out
}

func countRemaining(events []contracts.Event, fromSeq int64) int {
	n := 0
	for _, ev := range events {
		switch ev.Type {
		case contracts.EventPageDiscovered, contracts.EventRecordFound, contracts.EventRecordAccepted:
			continue
		}
		if ev.Seq >= fromSeq {
			n++
		}
	}
	return n
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func title(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
