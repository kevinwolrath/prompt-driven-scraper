// Package storage defines document-oriented persistence for the engine.
// Each method reads or writes whole documents keyed by id; nothing assumes a
// relational or transactional model, so a MongoDB (or similar) implementation
// maps one-to-one onto collections:
//
//	sessions, runs (jobs), run_events, run_checkpoints, run_records,
//	run_outputs, source_profiles
//
// Historical runs are immutable: once a job reaches a terminal status,
// UpdateJob is refused, and an output can be saved only once per job.
package storage

import (
	"context"
	"errors"

	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrImmutable is returned when writing to a finished run.
	ErrImmutable = errors.New("finished runs are immutable")
)

type Store interface {
	SessionStore
	JobStore
	EventStore
	RecordStore
	CheckpointStore
	OutputStore
	ProfileStore
}

type SessionStore interface {
	CreateSession(ctx context.Context, s contracts.Session) error
	UpdateSession(ctx context.Context, s contracts.Session) error
	GetSession(ctx context.Context, id string) (contracts.Session, error)
	// ListSessions returns sessions most recently updated first.
	ListSessions(ctx context.Context) ([]contracts.Session, error)
}

type JobStore interface {
	CreateJob(ctx context.Context, job contracts.Job) error
	// UpdateJob updates a job's status, progress and timestamps. The
	// configuration snapshot (Definition), SessionID and RunNumber are fixed
	// at creation. Updating a job whose stored status is terminal returns
	// ErrImmutable.
	UpdateJob(ctx context.Context, job contracts.Job) error
	GetJob(ctx context.Context, id string) (contracts.Job, error)
	// ListJobs returns jobs newest first.
	ListJobs(ctx context.Context) ([]contracts.Job, error)
	// ListSessionJobs returns a session's runs, newest (highest RunNumber) first.
	ListSessionJobs(ctx context.Context, sessionID string) ([]contracts.Job, error)
}

type EventStore interface {
	AppendEvent(ctx context.Context, event contracts.Event) error
	// ListEvents returns a job's events with Seq > afterSeq in order.
	ListEvents(ctx context.Context, jobID string, afterSeq int64) ([]contracts.Event, error)
}

type RecordStore interface {
	AppendRecord(ctx context.Context, record contracts.Record) error
	// ListRecords returns a job's records in acceptance order.
	ListRecords(ctx context.Context, jobID string) ([]contracts.Record, error)
}

type CheckpointStore interface {
	SaveCheckpoint(ctx context.Context, cp contracts.Checkpoint) error
	GetCheckpoint(ctx context.Context, jobID string) (contracts.Checkpoint, error)
}

type OutputStore interface {
	// SaveOutput stores a job's output once; a second save returns ErrImmutable.
	SaveOutput(ctx context.Context, out contracts.Output) error
	GetOutput(ctx context.Context, jobID string) (contracts.Output, error)
}

type ProfileStore interface {
	SaveProfile(ctx context.Context, p contracts.SourceProfile) error
	GetProfile(ctx context.Context, id string) (contracts.SourceProfile, error)
	ListProfiles(ctx context.Context) ([]contracts.SourceProfile, error)
}
