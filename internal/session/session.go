// Package session implements Sessions: persistent scraping projects whose
// current configuration can be edited and run repeatedly. Each run is an
// engine job with its own immutable configuration snapshot; running again
// always creates a new run.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/engine"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage"
	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

var ErrNotFound = errors.New("session not found")

type Service struct {
	store  storage.Store
	engine *engine.Engine
	now    func() time.Time
	runMu  sync.Mutex // serialises run numbering
}

func New(store storage.Store, eng *engine.Engine) *Service {
	return &Service{store: store, engine: eng, now: func() time.Time { return time.Now().UTC() }}
}

// Create stores a new session. The definition may be an incomplete draft;
// validation issues are returned but do not prevent saving.
func (s *Service) Create(ctx context.Context, name string, def contracts.JobDefinition) (contracts.Session, []contracts.ValidationIssue, error) {
	now := s.now()
	sess := contracts.Session{ID: newID("ses", now), Name: cleanName(name), Definition: def, CreatedAt: now, UpdatedAt: now}
	sess.Definition.Name = sess.Name
	if err := s.store.CreateSession(ctx, sess); err != nil {
		return sess, nil, err
	}
	return sess, s.Validate(ctx, sess.Definition), nil
}

// Update replaces a session's name and current configuration. Existing runs
// keep their own snapshots and are unaffected.
func (s *Service) Update(ctx context.Context, id, name string, def contracts.JobDefinition) (contracts.Session, []contracts.ValidationIssue, error) {
	sess, err := s.Get(ctx, id)
	if err != nil {
		return sess, nil, err
	}
	sess.Name, sess.Definition, sess.UpdatedAt = cleanName(name), def, s.now()
	sess.Definition.Name = sess.Name
	if err := s.store.UpdateSession(ctx, sess); err != nil {
		return sess, nil, err
	}
	return sess, s.Validate(ctx, sess.Definition), nil
}

func (s *Service) Get(ctx context.Context, id string) (contracts.Session, error) {
	sess, err := s.store.GetSession(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		return sess, ErrNotFound
	}
	return sess, err
}

// List returns light summaries; no records or configuration snapshots.
func (s *Service) List(ctx context.Context) ([]contracts.SessionSummary, error) {
	sessions, err := s.store.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]contracts.SessionSummary, 0, len(sessions))
	for _, sess := range sessions {
		runs, err := s.Runs(ctx, sess.ID)
		if err != nil {
			return nil, err
		}
		sum := contracts.SessionSummary{
			ID: sess.ID, Name: sess.Name, QueryInput: sess.Definition.QueryInput,
			CreatedAt: sess.CreatedAt, UpdatedAt: sess.UpdatedAt, RunCount: len(runs),
		}
		if len(runs) > 0 {
			sum.LastRun = &runs[0]
		}
		out = append(out, sum)
	}
	return out, nil
}

// Runs returns summaries of a session's runs, newest first, with live
// progress for runs this process is executing.
func (s *Service) Runs(ctx context.Context, id string) ([]contracts.RunSummary, error) {
	jobs, err := s.store.ListSessionJobs(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]contracts.RunSummary, len(jobs))
	for i, j := range jobs {
		if live, err := s.engine.Get(ctx, j.ID); err == nil {
			j = live
		}
		out[i] = j.Summary()
	}
	return out, nil
}

// Validate reports issues in a definition without running it.
func (s *Service) Validate(ctx context.Context, def contracts.JobDefinition) []contracts.ValidationIssue {
	_, issues, _ := s.engine.Prepare(ctx, &def)
	if issues == nil {
		return []contracts.ValidationIssue{}
	}
	return issues
}

// Run starts a new run of the session's current configuration.
func (s *Service) Run(ctx context.Context, id string) (contracts.Job, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	sess, err := s.Get(ctx, id)
	if err != nil {
		return contracts.Job{}, err
	}
	runs, err := s.store.ListSessionJobs(ctx, id)
	if err != nil {
		return contracts.Job{}, err
	}
	next := 1
	if len(runs) > 0 {
		next = runs[0].RunNumber + 1
	}
	def := sess.Definition
	def.Name = sess.Name
	return s.engine.SubmitRun(ctx, def, engine.RunMeta{SessionID: id, RunNumber: next})
}

func cleanName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "Untitled session"
	}
	return name
}

func newID(prefix string, now time.Time) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%s-%s", prefix, now.Format("20060102-150405"), hex.EncodeToString(b))
}
