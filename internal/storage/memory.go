package storage

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

// MemoryStore is an in-process Store for the CLI, tests and single-node
// local use. Documents are copied by value on the way in and out.
type MemoryStore struct {
	mu          sync.RWMutex
	jobs        map[string]contracts.Job
	events      map[string][]contracts.Event
	records     map[string][]contracts.Record
	checkpoints map[string]contracts.Checkpoint
	outputs     map[string]contracts.Output
	profiles    map[string]contracts.SourceProfile
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		jobs:        map[string]contracts.Job{},
		events:      map[string][]contracts.Event{},
		records:     map[string][]contracts.Record{},
		checkpoints: map[string]contracts.Checkpoint{},
		outputs:     map[string]contracts.Output{},
		profiles:    map[string]contracts.SourceProfile{},
	}
}

var _ Store = (*MemoryStore)(nil)

func (s *MemoryStore) CreateJob(_ context.Context, job contracts.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job.ID == "" {
		return errors.New("job id is required")
	}
	if _, exists := s.jobs[job.ID]; exists {
		return errors.New("job already exists")
	}
	s.jobs[job.ID] = job
	return nil
}

func (s *MemoryStore) UpdateJob(_ context.Context, job contracts.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[job.ID]; !exists {
		return ErrNotFound
	}
	s.jobs[job.ID] = job
	return nil
}

func (s *MemoryStore) GetJob(_ context.Context, id string) (contracts.Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.jobs[id]
	if !ok {
		return contracts.Job{}, ErrNotFound
	}
	return job, nil
}

func (s *MemoryStore) ListJobs(_ context.Context) ([]contracts.Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, j)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].CreatedAt.After(out[b].CreatedAt) })
	return out, nil
}

func (s *MemoryStore) AppendEvent(_ context.Context, event contracts.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events[event.JobID] = append(s.events[event.JobID], event)
	return nil
}

func (s *MemoryStore) ListEvents(_ context.Context, jobID string, afterSeq int64) ([]contracts.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	all := s.events[jobID]
	i := sort.Search(len(all), func(i int) bool { return all[i].Seq > afterSeq })
	return append([]contracts.Event(nil), all[i:]...), nil
}

func (s *MemoryStore) AppendRecord(_ context.Context, record contracts.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[record.JobID] = append(s.records[record.JobID], record)
	return nil
}

func (s *MemoryStore) ListRecords(_ context.Context, jobID string) ([]contracts.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]contracts.Record(nil), s.records[jobID]...), nil
}

func (s *MemoryStore) SaveCheckpoint(_ context.Context, cp contracts.Checkpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpoints[cp.JobID] = cp
	return nil
}

func (s *MemoryStore) GetCheckpoint(_ context.Context, jobID string) (contracts.Checkpoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp, ok := s.checkpoints[jobID]
	if !ok {
		return contracts.Checkpoint{}, ErrNotFound
	}
	return cp, nil
}

func (s *MemoryStore) SaveOutput(_ context.Context, out contracts.Output) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outputs[out.JobID] = out
	return nil
}

func (s *MemoryStore) GetOutput(_ context.Context, jobID string) (contracts.Output, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out, ok := s.outputs[jobID]
	if !ok {
		return contracts.Output{}, ErrNotFound
	}
	return out, nil
}

func (s *MemoryStore) SaveProfile(_ context.Context, p contracts.SourceProfile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ID == "" {
		return errors.New("profile id is required")
	}
	s.profiles[p.ID] = p
	return nil
}

func (s *MemoryStore) GetProfile(_ context.Context, id string) (contracts.SourceProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.profiles[id]
	if !ok {
		return contracts.SourceProfile{}, ErrNotFound
	}
	return p, nil
}

func (s *MemoryStore) ListProfiles(_ context.Context) ([]contracts.SourceProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.SourceProfile, 0, len(s.profiles))
	for _, p := range s.profiles {
		out = append(out, p)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out, nil
}
