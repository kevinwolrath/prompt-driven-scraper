package storage

import (
	"context"
	"errors"
	"sync"

	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

type MemoryStore struct {
	mu      sync.RWMutex
	jobs    map[string]contracts.Job
	events  []contracts.Event
	records []contracts.Record
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{jobs: make(map[string]contracts.Job)}
}

func (s *MemoryStore) CreateJob(_ context.Context, job contracts.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := job.Definition.ID
	if id == "" {
		id = job.Definition.Name
		job.Definition.ID = id
	}
	if _, exists := s.jobs[id]; exists {
		return errors.New("job already exists")
	}
	s.jobs[id] = job
	return nil
}

func (s *MemoryStore) UpdateJob(_ context.Context, job contracts.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := job.Definition.ID
	if id == "" {
		id = job.Definition.Name
	}
	if _, exists := s.jobs[id]; !exists {
		return errors.New("job not found")
	}
	s.jobs[id] = job
	return nil
}

func (s *MemoryStore) GetJob(_ context.Context, id string) (contracts.Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.jobs[id]
	if !ok {
		return contracts.Job{}, errors.New("job not found")
	}
	return job, nil
}

func (s *MemoryStore) AppendEvent(_ context.Context, event contracts.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return nil
}

func (s *MemoryStore) AppendRecord(_ context.Context, record contracts.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
	return nil
}

func (s *MemoryStore) ListRecords(_ context.Context, jobID string) ([]contracts.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.Record, 0)
	for _, record := range s.records {
		if record.JobID == jobID {
			out = append(out, record)
		}
	}
	return out, nil
}
