package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/extract"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/output"
	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

type control int

const (
	ctrlNone control = iota
	ctrlPause
	ctrlFinalise
	ctrlCancel
)

type mode int

const (
	modeStart mode = iota
	modeResume
	modeFinalise
)

// state is the checkpointed, resumable position of a job.
type state struct {
	SourceIndex int             `json:"sourceIndex"`
	Seeded      bool            `json:"seeded"`
	Queue       []task          `json:"queue"`
	Seen        map[string]bool `json:"seen"`
	Keys        map[string]bool `json:"keys"`
	// Per-source counters used for limits.
	ListingPages int  `json:"listingPages"`
	Items        int  `json:"items"`
	Failed       bool `json:"failed"`
}

func newState() *state {
	return &state{Seen: map[string]bool{}, Keys: map[string]bool{}}
}

type taskKind string

const (
	taskListing taskKind = "listing"
	taskDetail  taskKind = "detail"
)

type task struct {
	Kind taskKind     `json:"kind"`
	URL  string       `json:"url"`
	Page int          `json:"page,omitempty"`
	Item *pendingItem `json:"item,omitempty"`
}

// pendingItem is a listing item whose detail page has not been fetched yet.
// Its listing-side candidate results are kept so detail results can be merged
// with correct precedence, or salvaged by "give me what you've got".
type pendingItem struct {
	ListingURL string                      `json:"listingUrl"`
	DetailURL  string                      `json:"detailUrl"`
	Results    map[string][]extract.Result `json:"results"`
	Notes      []string                    `json:"notes,omitempty"`
}

type run struct {
	e        *Engine
	profiles []*extract.Profile

	mu           sync.Mutex
	job          contracts.Job
	seq          int64
	ctrl         control
	cancel       context.CancelFunc
	done         chan struct{} // nil when no goroutine is active
	runningSince time.Time
	wake         chan struct{}
	lastFetch    time.Time
}

func (r *run) snapshot() contracts.Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked()
}

func (r *run) snapshotLocked() contracts.Job {
	j := r.job
	j.Progress = r.progressLocked()
	return j
}

func (r *run) progressLocked() contracts.Progress {
	p := r.job.Progress
	p.Sources = append([]contracts.SourceProgress(nil), p.Sources...)
	p.ElapsedMs = r.job.RunningMs
	if !r.runningSince.IsZero() {
		p.ElapsedMs += r.e.opts.Now().Sub(r.runningSince).Milliseconds()
	}
	return p
}

// emit applies mutate to the job, persists the job and the event, and
// publishes the event. It is the only way job state changes.
func (r *run) emit(typ contracts.EventType, sourceID, msg string, data map[string]any, mutate func(*contracts.Job)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emitLocked(typ, sourceID, msg, data, mutate)
}

func (r *run) emitLocked(typ contracts.EventType, sourceID, msg string, data map[string]any, mutate func(*contracts.Job)) {
	now := r.e.opts.Now()
	if mutate != nil {
		mutate(&r.job)
	}
	totals(&r.job.Progress)
	r.job.UpdatedAt = now
	r.seq++
	prog := r.progressLocked()
	ev := contracts.Event{
		JobID: r.job.ID, Seq: r.seq, Type: typ, SourceID: sourceID, Message: msg,
		Timestamp: now, Data: data, Status: r.job.Status, Progress: &prog,
	}
	ctx := context.Background()
	_ = r.e.store.AppendEvent(ctx, ev)
	job := r.job
	job.Progress = prog
	_ = r.e.store.UpdateJob(ctx, job)
	r.e.publish(ev)
}

// totals recomputes job-level counters from per-source counters. Warnings
// and errors are counted directly at job level because some are not tied to
// a source.
func totals(p *contracts.Progress) {
	*p = contracts.Progress{
		CurrentSource: p.CurrentSource, SourcesTotal: len(p.Sources), Sources: p.Sources,
		Warnings: p.Warnings, Errors: p.Errors,
	}
	for _, s := range p.Sources {
		switch s.Status {
		case contracts.SourceCompleted, contracts.SourceFailed, contracts.SourceStopped, contracts.SourceSkipped:
			p.SourcesComplete++
		}
		p.PagesVisited += s.PagesVisited
		p.PagesDiscovered += s.PagesDiscovered
		p.RecordsFound += s.RecordsFound
		p.RecordsAccepted += s.RecordsAccepted
		p.RecordsRejected += s.RecordsRejected
		p.Duplicates += s.Duplicates
	}
}

// setStatus transitions the job; callers hold r.mu.
func (r *run) setStatus(next contracts.JobStatus, reason string) error {
	cur := r.job.Status
	if !allowed(cur, next) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidAction, cur, next)
	}
	now := r.e.opts.Now()
	if cur == contracts.JobRunning || cur == contracts.JobPausing || cur == contracts.JobFinalising {
		if !r.runningSince.IsZero() && (next == contracts.JobPaused || next.Terminal()) {
			r.job.RunningMs += now.Sub(r.runningSince).Milliseconds()
			r.runningSince = time.Time{}
		}
	}
	if next == contracts.JobRunning && r.runningSince.IsZero() {
		r.runningSince = now
		if r.job.StartedAt == nil {
			r.job.StartedAt = &now
		}
	}
	if next.Terminal() {
		r.job.FinishedAt = &now
		r.job.Progress.CurrentSource = ""
	}
	r.job.Status = next
	if reason != "" {
		r.job.StatusReason = reason
	}
	return nil
}

func (r *run) transition(typ contracts.EventType, next contracts.JobStatus, reason string, data map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.setStatus(next, reason); err != nil {
		return err
	}
	r.emitLocked(typ, "", reason, data, nil)
	return nil
}

func (r *run) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// ---------------------------------------------------------------------------
// Controls
// ---------------------------------------------------------------------------

func (r *run) pause() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.job.Status != contracts.JobRunning {
		return ErrInvalidAction
	}
	if err := r.setStatus(contracts.JobPausing, ""); err != nil {
		return err
	}
	r.ctrl = ctrlPause
	r.emitLocked(contracts.EventJobPausing, "", "finishing in-flight work before pausing", nil, nil)
	r.signal()
	return nil
}

func (r *run) resume(ctx context.Context) error {
	r.mu.Lock()
	switch r.job.Status {
	case contracts.JobPausing:
		defer r.mu.Unlock()
		r.ctrl = ctrlNone
		if err := r.setStatus(contracts.JobRunning, ""); err != nil {
			return err
		}
		r.emitLocked(contracts.EventJobResumed, "", "pause withdrawn before checkpoint", nil, nil)
		return nil
	case contracts.JobPaused:
		r.mu.Unlock()
		st, err := r.loadState(ctx)
		if err != nil {
			return err
		}
		r.mu.Lock()
		r.ctrl = ctrlNone
		r.mu.Unlock()
		if err := r.transition(contracts.EventJobResumed, contracts.JobRunning, "", map[string]any{"queued": len(st.Queue)}); err != nil {
			return err
		}
		r.start(st, modeResume)
		return nil
	}
	r.mu.Unlock()
	return ErrInvalidAction
}

func (r *run) finalise(ctx context.Context) error {
	r.mu.Lock()
	switch r.job.Status {
	case contracts.JobRunning, contracts.JobPausing:
		r.ctrl = ctrlFinalise
		r.mu.Unlock()
		r.signal()
		return nil
	case contracts.JobPaused:
		r.mu.Unlock()
		st, err := r.loadState(ctx)
		if err != nil {
			return err
		}
		r.mu.Lock()
		r.ctrl = ctrlNone
		r.mu.Unlock()
		if err := r.transition(contracts.EventJobFinalising, contracts.JobFinalising, "", map[string]any{"requested": true}); err != nil {
			return err
		}
		r.start(st, modeFinalise)
		return nil
	}
	r.mu.Unlock()
	return ErrInvalidAction
}

func (r *run) cancelJob() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.job.Status.Terminal() {
		return ErrInvalidAction
	}
	if r.done != nil {
		// The runner observes the cancelled context and records the outcome.
		r.ctrl = ctrlCancel
		r.cancel()
		return nil
	}
	if err := r.setStatus(contracts.JobCancelled, "cancelled by user"); err != nil {
		return err
	}
	r.markSourcesLocked(contracts.SourceStopped)
	r.emitLocked(contracts.EventJobCancelled, "", "cancelled by user", nil, nil)
	return nil
}

func (r *run) takeControl() control {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ctrl
}

// ---------------------------------------------------------------------------
// Checkpoints
// ---------------------------------------------------------------------------

func (r *run) saveState(st *state) {
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	_ = r.e.store.SaveCheckpoint(context.Background(), contracts.Checkpoint{JobID: r.job.ID, State: b, UpdatedAt: r.e.opts.Now()})
}

func (r *run) loadState(ctx context.Context) (*state, error) {
	cp, err := r.e.store.GetCheckpoint(ctx, r.job.ID)
	if err != nil {
		return nil, fmt.Errorf("load checkpoint: %w", err)
	}
	st := newState()
	if err := json.Unmarshal(cp.State, st); err != nil {
		return nil, fmt.Errorf("decode checkpoint: %w", err)
	}
	return st, nil
}

// ---------------------------------------------------------------------------
// Runner
// ---------------------------------------------------------------------------

func (r *run) start(st *state, m mode) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r.mu.Lock()
	r.cancel = cancel
	r.done = done
	r.mu.Unlock()

	go func() {
		defer func() {
			r.mu.Lock()
			if r.done == done {
				r.done = nil
			}
			r.mu.Unlock()
			cancel()
			close(done)
		}()
		r.execute(ctx, st, m)
	}()
}

func (r *run) execute(ctx context.Context, st *state, m mode) {
	switch m {
	case modeStart:
		if err := r.transition(contracts.EventJobStarted, contracts.JobRunning, "", map[string]any{"queryInput": r.job.Definition.QueryInput}); err != nil {
			return
		}
	case modeResume:
		// resume() has already transitioned to running.
	case modeFinalise:
		r.finish(ctx, st, true)
		return
	}

	for st.SourceIndex < len(r.profiles) {
		idx := st.SourceIndex
		p := r.profiles[idx]
		srcID := r.job.Definition.SearchSources[idx].ID
		if !st.Seeded {
			r.seed(idx, p, st)
			r.saveState(st)
		} else {
			r.emit(contracts.EventSourceStarted, srcID, "continuing from checkpoint", nil, func(j *contracts.Job) {
				j.Progress.CurrentSource = srcID
				j.Progress.Sources[idx].Status = contracts.SourceRunning
			})
		}
		delay := r.delay(p)
		for len(st.Queue) > 0 {
			switch r.takeControl() {
			case ctrlPause:
				r.saveState(st)
				r.mu.Lock()
				if r.setStatus(contracts.JobPaused, "") == nil {
					r.emitLocked(contracts.EventJobPaused, srcID, "checkpoint saved", map[string]any{"queued": len(st.Queue)}, nil)
				}
				r.mu.Unlock()
				return
			case ctrlFinalise:
				r.saveState(st)
				r.finish(ctx, st, true)
				return
			}
			if ctx.Err() != nil {
				r.cancelled(st)
				return
			}
			if !r.wait(ctx, delay) {
				continue // woken by a control request or cancellation; re-check
			}
			t := st.Queue[0]
			st.Queue = st.Queue[1:]
			r.process(ctx, idx, p, st, t)
			if ctx.Err() != nil {
				// Cancelled mid-task: put the task back so the checkpoint is exact.
				st.Queue = append([]task{t}, st.Queue...)
				r.cancelled(st)
				return
			}
			r.saveState(st)
		}
		status := contracts.SourceCompleted
		if st.Failed {
			status = contracts.SourceFailed
		}
		r.emit(contracts.EventSourceCompleted, srcID, string(status), nil, func(j *contracts.Job) {
			j.Progress.Sources[idx].Status = status
		})
		st.SourceIndex++
		st.Seeded, st.ListingPages, st.Items, st.Failed = false, 0, 0, false
		r.saveState(st)
	}
	r.finish(ctx, st, false)
}

func (r *run) delay(p *extract.Profile) time.Duration {
	if p.Source.Limits.DelayMs != nil {
		return time.Duration(*p.Source.Limits.DelayMs) * time.Millisecond
	}
	return r.e.opts.DefaultDelay
}

// wait enforces the politeness delay since the last fetch. It returns false
// if interrupted by a control request or cancellation.
func (r *run) wait(ctx context.Context, delay time.Duration) bool {
	r.mu.Lock()
	remaining := delay - r.e.opts.Now().Sub(r.lastFetch)
	r.mu.Unlock()
	if remaining <= 0 {
		return true
	}
	t := time.NewTimer(remaining)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-r.wake:
		return false
	case <-t.C:
		return true
	}
}

func (r *run) cancelled(st *state) {
	r.saveState(st)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.setStatus(contracts.JobCancelled, "cancelled by user") == nil {
		r.markSourcesLocked(contracts.SourceStopped)
		r.emitLocked(contracts.EventJobCancelled, "", "cancelled by user", nil, nil)
	}
}

func (r *run) markSourcesLocked(status contracts.SourceStatus) {
	for i := range r.job.Progress.Sources {
		s := &r.job.Progress.Sources[i]
		switch s.Status {
		case contracts.SourcePending:
			s.Status = contracts.SourceSkipped
		case contracts.SourceRunning:
			s.Status = status
		}
	}
}

// finish validates, deduplicates and renders output. requested marks a
// "give me what you've got" stop.
func (r *run) finish(ctx context.Context, st *state, requested bool) {
	r.mu.Lock()
	already := r.job.Status == contracts.JobFinalising
	r.mu.Unlock()
	if !already && r.transition(contracts.EventJobFinalising, contracts.JobFinalising, "", map[string]any{"requested": requested}) != nil {
		return
	}
	if requested {
		r.salvage(st)
		r.mu.Lock()
		r.markSourcesLocked(contracts.SourceStopped)
		r.mu.Unlock()
		st.Queue = nil
		r.saveState(st)
	}

	records, err := r.e.store.ListRecords(ctx, r.job.ID)
	if err != nil {
		r.fail(fmt.Errorf("list records: %w", err))
		return
	}
	records = output.Dedupe(records)
	def := r.job.Definition.Output
	data, ctype, err := output.Render(def, records)
	if err != nil {
		r.fail(fmt.Errorf("render output: %w", err))
		return
	}

	r.mu.Lock()
	sourcesFailed := 0
	for _, s := range r.job.Progress.Sources {
		if s.Status == contracts.SourceFailed {
			sourcesFailed++
		}
	}
	r.mu.Unlock()
	if sourcesFailed == len(r.profiles) && len(records) == 0 {
		r.fail(errors.New("every source failed"))
		return
	}

	partial := requested || sourcesFailed > 0
	_ = r.e.store.SaveOutput(ctx, contracts.Output{
		JobID: r.job.ID, Format: def.Format, ContentType: ctype, Partial: partial,
		RecordCount: len(records), Data: data, CreatedAt: r.e.opts.Now(),
	})
	info := map[string]any{"records": len(records), "format": def.Format}
	switch {
	case requested:
		_ = r.transition(contracts.EventJobPartial, contracts.JobPartial, "stopped early: give me what you've got", info)
	case sourcesFailed > 0:
		_ = r.transition(contracts.EventJobPartial, contracts.JobPartial, fmt.Sprintf("%d of %d sources failed", sourcesFailed, len(r.profiles)), info)
	default:
		_ = r.transition(contracts.EventJobCompleted, contracts.JobCompleted, "", info)
	}
}

func (r *run) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.setStatus(contracts.JobFailed, err.Error()) == nil {
		r.markSourcesLocked(contracts.SourceStopped)
		r.job.Progress.Errors++
		r.emitLocked(contracts.EventJobFailed, "", err.Error(), nil, nil)
	}
}
