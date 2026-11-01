// Package engine runs scrape jobs asynchronously and deterministically.
//
// A job's sources are processed in order, and each source's work is a FIFO
// queue of tasks (listing pages, then the detail pages they discover). The
// queue is checkpointed after every task, which is what makes Pause/Resume
// continue rather than restart, and what lets "give me what you've got"
// salvage already-fetched work. Nothing in this package knows about a
// specific website: all site behaviour comes from compiled source profiles.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/extract"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/fetch"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/profile"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage"
	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

var (
	ErrNotFound      = errors.New("job not found")
	ErrInvalidAction = errors.New("action is not valid in the job's current state")
)

// ValidationError is returned when a job definition is invalid.
type ValidationError struct {
	Issues []contracts.ValidationIssue
}

func (e *ValidationError) Error() string {
	var msgs []string
	for _, is := range e.Issues {
		if is.Severity == "error" {
			msgs = append(msgs, is.Path+": "+is.Message)
		}
	}
	return "invalid job: " + strings.Join(msgs, "; ")
}

type Options struct {
	Fetcher fetch.Fetcher
	// Browser serves "browser" acquisition strategies. Nil means sources
	// that need one fail with a clear error.
	Browser     fetch.Browser
	UserBrowser fetch.Browser
	// DefaultDelay between requests to one source when a profile does not set
	// limits.delayMs.
	DefaultDelay time.Duration
	// RetryBackoff is the base wait between retries.
	RetryBackoff time.Duration
	Now          func() time.Time
}

type Engine struct {
	store storage.Store
	opts  Options

	mu   sync.Mutex
	runs map[string]*run
	subs map[string]map[chan contracts.Event]struct{}
}

func New(store storage.Store, opts Options) *Engine {
	if opts.Fetcher == nil {
		opts.Fetcher = fetch.NewHTTP()
	}
	if opts.DefaultDelay == 0 {
		opts.DefaultDelay = 500 * time.Millisecond
	}
	if opts.RetryBackoff == 0 {
		opts.RetryBackoff = time.Second
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Engine{store: store, opts: opts, runs: map[string]*run{}, subs: map[string]map[chan contracts.Event]struct{}{}}
}

// Prepare resolves profile references and validates a job definition without
// running it.
func (e *Engine) Prepare(ctx context.Context, def *contracts.JobDefinition) ([]*extract.Profile, []contracts.ValidationIssue, error) {
	issues := profile.Resolve(ctx, def, e.store)
	if issues.HasErrors() {
		return nil, issues, &ValidationError{Issues: issues}
	}
	compiled, more := profile.ValidateJob(*def)
	issues = append(issues, more...)
	if issues.HasErrors() {
		return nil, issues, &ValidationError{Issues: issues}
	}
	return compiled, issues, nil
}

// RunMeta places a run within a Session. The zero value is an ad-hoc run.
type RunMeta struct {
	SessionID string
	RunNumber int
}

// Submit validates a job, stores it as queued and starts it in the background.
func (e *Engine) Submit(ctx context.Context, def contracts.JobDefinition) (contracts.Job, error) {
	return e.SubmitRun(ctx, def, RunMeta{})
}

// SubmitRun is Submit for run number meta.RunNumber of a Session. The stored
// definition is the resolved configuration snapshot for this run.
func (e *Engine) SubmitRun(ctx context.Context, def contracts.JobDefinition, meta RunMeta) (contracts.Job, error) {
	compiled, issues, err := e.Prepare(ctx, &def)
	if err != nil {
		return contracts.Job{}, err
	}
	now := e.opts.Now()
	job := contracts.Job{
		ID:         newID(now),
		SessionID:  meta.SessionID,
		RunNumber:  meta.RunNumber,
		Definition: def,
		Status:     contracts.JobQueued,
		CreatedAt:  now,
		UpdatedAt:  now,
		Progress:   contracts.Progress{SourcesTotal: len(def.SearchSources)},
	}
	for i, ref := range def.SearchSources {
		sp := contracts.SourceProgress{ID: ref.ID, Name: compiled[i].Source.Name, Status: contracts.SourcePending}
		for _, st := range compiled[i].Strategies {
			sp.Acquisition = append(sp.Acquisition, contracts.AcquisitionStats{Strategy: st.Type})
		}
		job.Progress.Sources = append(job.Progress.Sources, sp)
	}
	if err := e.store.CreateJob(ctx, job); err != nil {
		return contracts.Job{}, err
	}
	r := &run{e: e, job: job, profiles: compiled, wake: make(chan struct{}, 1)}
	e.mu.Lock()
	e.runs[job.ID] = r
	e.mu.Unlock()

	for _, is := range issues {
		if is.Severity == "warning" {
			r.emit(contracts.EventWarning, "", is.Path+": "+is.Message, nil, func(j *contracts.Job) { j.Progress.Warnings++ })
		}
	}
	r.start(newState(), modeStart)
	return r.snapshot(), nil
}

func (e *Engine) Get(ctx context.Context, id string) (contracts.Job, error) {
	if r := e.run(id); r != nil {
		return r.snapshot(), nil
	}
	job, err := e.store.GetJob(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		return job, ErrNotFound
	}
	return job, err
}

func (e *Engine) List(ctx context.Context) ([]contracts.Job, error) {
	jobs, err := e.store.ListJobs(ctx)
	if err != nil {
		return nil, err
	}
	for i := range jobs {
		if r := e.run(jobs[i].ID); r != nil {
			jobs[i] = r.snapshot()
		}
	}
	return jobs, nil
}

// Pause stops scheduling new work; the job becomes paused once in-flight work
// reaches a checkpoint.
func (e *Engine) Pause(_ context.Context, id string) (contracts.Job, error) {
	return e.control(id, func(r *run) error { return r.pause() })
}

// Resume continues a paused job from its checkpoint.
func (e *Engine) Resume(ctx context.Context, id string) (contracts.Job, error) {
	return e.control(id, func(r *run) error { return r.resume(ctx) })
}

// Cancel stops the job immediately and marks it cancelled. No output is
// produced.
func (e *Engine) Cancel(_ context.Context, id string) (contracts.Job, error) {
	return e.control(id, func(r *run) error { return r.cancelJob() })
}

// Finalise is "give me what you've got": stop discovering work, salvage what
// was already fetched, validate, dedupe, render output and mark the job
// partial.
func (e *Engine) Finalise(ctx context.Context, id string) (contracts.Job, error) {
	return e.control(id, func(r *run) error { return r.finalise(ctx) })
}

func (e *Engine) control(id string, fn func(*run) error) (contracts.Job, error) {
	r, err := e.load(context.Background(), id)
	if err != nil {
		return contracts.Job{}, err
	}
	if err := fn(r); err != nil {
		return r.snapshot(), err
	}
	return r.snapshot(), nil
}

// load returns the in-memory run for id, rebuilding it from the store when
// this process did not start it (e.g. a run paused before a restart).
func (e *Engine) load(ctx context.Context, id string) (*run, error) {
	if r := e.run(id); r != nil {
		return r, nil
	}
	job, err := e.store.GetJob(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if job.Status.Terminal() {
		return nil, ErrInvalidAction
	}
	compiled, issues := profile.ValidateJob(job.Definition)
	if issues.HasErrors() {
		return nil, &ValidationError{Issues: issues}
	}
	events, err := e.store.ListEvents(ctx, id, 0)
	if err != nil {
		return nil, err
	}
	r := &run{e: e, job: job, profiles: compiled, wake: make(chan struct{}, 1)}
	if len(events) > 0 {
		r.seq = events[len(events)-1].Seq
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if existing := e.runs[id]; existing != nil {
		return existing, nil
	}
	e.runs[id] = r
	return r, nil
}

// Recover is called once at startup. Runs left running, pausing or
// finalising by a process that stopped without a clean shutdown become
// paused, so they can be resumed from their last checkpoint (or finalised
// with "give me what you've got"). Runs that never started fail. It returns
// the ids of the runs it changed.
func (e *Engine) Recover(ctx context.Context) ([]string, error) {
	jobs, err := e.store.ListJobs(ctx)
	if err != nil {
		return nil, err
	}
	var changed []string
	for _, job := range jobs {
		if job.Status.Terminal() || job.Status == contracts.JobPaused || e.run(job.ID) != nil {
			continue
		}
		r, err := e.load(ctx, job.ID)
		if err != nil {
			return changed, fmt.Errorf("recover run %s: %w", job.ID, err)
		}
		_, cpErr := e.store.GetCheckpoint(ctx, job.ID)
		var interaction *contracts.BrowserInteraction
		if cpErr == nil {
			if st, err := r.loadState(ctx); err == nil {
				interaction = st.Interaction
			}
		}
		r.mu.Lock()
		was := r.job.Status
		if was == contracts.JobQueued || cpErr != nil {
			if r.setStatus(contracts.JobFailed, "interrupted before it saved a checkpoint") == nil {
				r.markSourcesLocked(contracts.SourceStopped)
				r.emitLocked(contracts.EventJobFailed, "", r.job.StatusReason, nil, nil)
			}
		} else {
			// Not a normal transition: the process that was running it is gone.
			r.job.Status = contracts.JobPaused
			r.job.Progress.Interaction = interaction
			r.job.StatusReason = fmt.Sprintf("interrupted while %s: the application stopped; resume continues from the last checkpoint", was)
			r.emitLocked(contracts.EventJobPaused, "", r.job.StatusReason, map[string]any{"interrupted": true, "was": was}, nil)
		}
		r.mu.Unlock()
		changed = append(changed, job.ID)
	}
	return changed, nil
}

// Shutdown pauses every running job at its next checkpoint and waits for
// in-flight work to stop, so a restart can resume them. Finalising jobs are
// allowed to finish.
func (e *Engine) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	runs := make([]*run, 0, len(e.runs))
	for _, r := range e.runs {
		runs = append(runs, r)
	}
	e.mu.Unlock()
	for _, r := range runs {
		_ = r.pause() // only valid while running
	}
	for _, r := range runs {
		r.mu.Lock()
		done := r.done
		r.mu.Unlock()
		if done == nil {
			continue
		}
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (e *Engine) run(id string) *run {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.runs[id]
}

// Records returns a job's accepted records so far.
func (e *Engine) Records(ctx context.Context, id string) ([]contracts.Record, error) {
	return e.store.ListRecords(ctx, id)
}

// Output returns the finalised output for a completed or partial job.
func (e *Engine) Output(ctx context.Context, id string) (contracts.Output, error) {
	out, err := e.store.GetOutput(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		return out, ErrNotFound
	}
	return out, err
}

// Wait blocks until the job reaches a terminal state.
func (e *Engine) Wait(ctx context.Context, id string) (contracts.Job, error) {
	r := e.run(id)
	if r == nil {
		return e.Get(ctx, id)
	}
	for {
		r.mu.Lock()
		done, status := r.done, r.job.Status
		r.mu.Unlock()
		if status.Terminal() {
			return r.snapshot(), nil
		}
		if done == nil {
			// Paused: nothing is running; poll for a resume/finalise/cancel.
			select {
			case <-ctx.Done():
				return r.snapshot(), ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		select {
		case <-ctx.Done():
			return r.snapshot(), ctx.Err()
		case <-done:
		}
	}
}

// Subscribe returns stored events after afterSeq plus a channel of live
// events. Consumers must ignore events with Seq they have already seen. The
// channel is closed if the consumer falls too far behind; re-subscribe from
// the last seen Seq to catch up.
func (e *Engine) Subscribe(ctx context.Context, id string, afterSeq int64) ([]contracts.Event, <-chan contracts.Event, func(), error) {
	if _, err := e.Get(ctx, id); err != nil {
		return nil, nil, nil, err
	}
	ch := make(chan contracts.Event, 4096)
	e.mu.Lock()
	backlog, err := e.store.ListEvents(ctx, id, afterSeq)
	if err != nil {
		e.mu.Unlock()
		return nil, nil, nil, err
	}
	if e.subs[id] == nil {
		e.subs[id] = map[chan contracts.Event]struct{}{}
	}
	e.subs[id][ch] = struct{}{}
	e.mu.Unlock()
	cancel := func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if _, ok := e.subs[id][ch]; ok {
			delete(e.subs[id], ch)
			close(ch)
		}
	}
	return backlog, ch, cancel, nil
}

func (e *Engine) publish(ev contracts.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for ch := range e.subs[ev.JobID] {
		select {
		case ch <- ev:
		default:
			delete(e.subs[ev.JobID], ch)
			close(ch)
		}
	}
}

func newID(now time.Time) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("job-%s-%s", now.Format("20060102-150405"), hex.EncodeToString(b))
}

// allowed is the job state machine.
func allowed(current, next contracts.JobStatus) bool {
	switch current {
	case contracts.JobQueued:
		return next == contracts.JobRunning || next == contracts.JobCancelled || next == contracts.JobFailed
	case contracts.JobRunning:
		return next == contracts.JobPausing || next == contracts.JobFinalising || next == contracts.JobCancelled || next == contracts.JobFailed
	case contracts.JobPausing:
		return next == contracts.JobPaused || next == contracts.JobRunning || next == contracts.JobFinalising || next == contracts.JobCancelled || next == contracts.JobFailed
	case contracts.JobPaused:
		return next == contracts.JobRunning || next == contracts.JobFinalising || next == contracts.JobCancelled
	case contracts.JobFinalising:
		return next == contracts.JobPartial || next == contracts.JobCompleted || next == contracts.JobFailed || next == contracts.JobCancelled
	}
	return false
}
