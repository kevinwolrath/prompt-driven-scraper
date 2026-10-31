package engine

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage"
	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

type Engine struct {
	store storage.Store
	mu    sync.Mutex
}

func New(store storage.Store) *Engine {
	return &Engine{store: store}
}

func (e *Engine) Start(ctx context.Context, definition contracts.JobDefinition) (contracts.Job, error) {
	if definition.Name == "" {
		return contracts.Job{}, errors.New("job name is required")
	}
	if len(definition.SearchSources) == 0 {
		return contracts.Job{}, errors.New("at least one search source is required")
	}

	now := time.Now().UTC()
	job := contracts.Job{
		Definition: definition,
		Status:     contracts.JobQueued,
		Progress: contracts.Progress{
			SourcesTotal: len(definition.SearchSources),
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := e.store.CreateJob(ctx, job); err != nil {
		return contracts.Job{}, err
	}
	return job, nil
}

func (e *Engine) Transition(ctx context.Context, job *contracts.Job, next contracts.JobStatus) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !allowed(job.Status, next) {
		return errors.New("invalid job state transition")
	}
	job.Status = next
	job.UpdatedAt = time.Now().UTC()
	return e.store.UpdateJob(ctx, *job)
}

func allowed(current, next contracts.JobStatus) bool {
	switch current {
	case contracts.JobQueued:
		return next == contracts.JobRunning || next == contracts.JobCancelled || next == contracts.JobFailed
	case contracts.JobRunning:
		return next == contracts.JobPausing || next == contracts.JobFinalising || next == contracts.JobCompleted || next == contracts.JobCancelled || next == contracts.JobFailed
	case contracts.JobPausing:
		return next == contracts.JobPaused || next == contracts.JobCancelled || next == contracts.JobFailed
	case contracts.JobPaused:
		return next == contracts.JobRunning || next == contracts.JobFinalising || next == contracts.JobCancelled
	case contracts.JobFinalising:
		return next == contracts.JobPartial || next == contracts.JobCompleted || next == contracts.JobFailed
	default:
		return false
	}
}
