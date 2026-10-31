package storage

import (
	"context"

	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

type Store interface {
	CreateJob(ctx context.Context, job contracts.Job) error
	UpdateJob(ctx context.Context, job contracts.Job) error
	GetJob(ctx context.Context, id string) (contracts.Job, error)
	AppendEvent(ctx context.Context, event contracts.Event) error
	AppendRecord(ctx context.Context, record contracts.Record) error
	ListRecords(ctx context.Context, jobID string) ([]contracts.Record, error)
}
