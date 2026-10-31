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
	sessions    map[string]contracts.Session
	jobs        map[string]contracts.Job
	events      map[string][]contracts.Event
	records     map[string][]contracts.Record
	checkpoints map[string]contracts.Checkpoint
	outputs     map[string]contracts.Output
	profiles    map[string]contracts.SourceProfile
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		sessions:    map[string]contracts.Session{},
		jobs:        map[string]contracts.Job{},
		events:      map[string][]contracts.Event{},
		records:     map[string][]contracts.Record{},
		checkpoints: map[string]contracts.Checkpoint{},
		outputs:     map[string]contracts.Output{},
		profiles:    map[string]contracts.SourceProfile{},
	}
}

var _ Store = (*MemoryStore)(nil)

func (s *MemoryStore) CreateSession(_ context.Context, sess contracts.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess.ID == "" {
		return errors.New("session id is required")
	}
	if _, exists := s.sessions[sess.ID]; exists {
		return errors.New("session already exists")
	}
	s.sessions[sess.ID] = sess
	return nil
}

func (s *MemoryStore) UpdateSession(_ context.Context, sess contracts.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, exists := s.sessions[sess.ID]
	if !exists {
		return ErrNotFound
	}
	sess.CreatedAt = old.CreatedAt
	s.sessions[sess.ID] = sess
	return nil
}

func (s *MemoryStore) GetSession(_ context.Context, id string) (contracts.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	if !ok {
		return contracts.Session{}, ErrNotFound
	}
	return sess, nil
}

func (s *MemoryStore) ListSessions(_ context.Context) ([]contracts.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, sess)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].UpdatedAt.After(out[b].UpdatedAt) })
	return out, nil
}

func (s *MemoryStore) CreateJob(_ context.Context, job contracts.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job.ID == "" {
		return errors.New("job id is required")
	}
	if _, exists := s.jobs[job.ID]; exists {
		return errors.New("job already exists")
	}
	if job.SessionID != "" {
		if _, ok := s.sessions[job.SessionID]; !ok {
			return errors.New("session does not exist")
		}
		for _, j := range s.jobs {
			if j.SessionID == job.SessionID && j.RunNumber == job.RunNumber {
				return errors.New("run number already used in this session")
			}
		}
	}
	s.jobs[job.ID] = job
	return nil
}

func (s *MemoryStore) UpdateJob(_ context.Context, job contracts.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, exists := s.jobs[job.ID]
	if !exists {
		return ErrNotFound
	}
	if old.Status.Terminal() {
		return ErrImmutable
	}
	job.Definition, job.SessionID, job.RunNumber, job.CreatedAt = old.Definition, old.SessionID, old.RunNumber, old.CreatedAt
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

func (s *MemoryStore) ListSessionJobs(_ context.Context, sessionID string) ([]contracts.Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []contracts.Job{}
	for _, j := range s.jobs {
		if j.SessionID == sessionID {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].RunNumber > out[b].RunNumber })
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
	if _, exists := s.outputs[out.JobID]; exists {
		return ErrImmutable
	}
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
