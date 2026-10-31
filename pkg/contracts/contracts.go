package contracts

import "time"

type JobStatus string

const (
	JobQueued     JobStatus = "queued"
	JobRunning    JobStatus = "running"
	JobPausing    JobStatus = "pausing"
	JobPaused     JobStatus = "paused"
	JobFinalising JobStatus = "finalising"
	JobCompleted  JobStatus = "completed"
	JobPartial    JobStatus = "partial"
	JobCancelled  JobStatus = "cancelled"
	JobFailed     JobStatus = "failed"
)

type FieldDefinition struct {
	Name     string `json:"name" yaml:"name"`
	Type     string `json:"type" yaml:"type"`
	Required bool   `json:"required,omitempty" yaml:"required,omitempty"`
}

type OutputDefinition struct {
	Format string            `json:"format" yaml:"format"`
	Fields []FieldDefinition `json:"fields" yaml:"fields"`
}

type SourceRef struct {
	ID      string                 `json:"id" yaml:"id"`
	Profile map[string]interface{} `json:"profile,omitempty" yaml:"profile,omitempty"`
}

type JobDefinition struct {
	ID            string                 `json:"id,omitempty" yaml:"id,omitempty"`
	Name          string                 `json:"name" yaml:"name"`
	QueryInput    string                 `json:"queryInput" yaml:"queryInput"`
	Query         map[string]interface{} `json:"query,omitempty" yaml:"query,omitempty"`
	SearchSources []SourceRef            `json:"searchSources" yaml:"searchSources"`
	Output        OutputDefinition       `json:"output" yaml:"output"`
}

type Progress struct {
	SourcesTotal      int `json:"sourcesTotal"`
	SourcesComplete   int `json:"sourcesComplete"`
	PagesVisited      int `json:"pagesVisited"`
	PagesDiscovered   int `json:"pagesDiscovered,omitempty"`
	RecordsFound      int `json:"recordsFound"`
	RecordsAccepted   int `json:"recordsAccepted"`
	RecordsRejected   int `json:"recordsRejected"`
	Warnings          int `json:"warnings"`
	Errors            int `json:"errors"`
}

type Job struct {
	Definition JobDefinition `json:"definition"`
	Status     JobStatus     `json:"status"`
	Progress   Progress      `json:"progress"`
	CreatedAt  time.Time     `json:"createdAt"`
	UpdatedAt  time.Time     `json:"updatedAt"`
}

type Event struct {
	JobID     string                 `json:"jobId"`
	Type      string                 `json:"type"`
	SourceID  string                 `json:"sourceId,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
	Data      map[string]interface{} `json:"data,omitempty"`
}

type Record struct {
	JobID    string                 `json:"jobId"`
	SourceID string                 `json:"sourceId"`
	Values   map[string]interface{} `json:"values"`
	SeenAt   time.Time              `json:"seenAt"`
}
