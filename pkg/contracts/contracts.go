// Package contracts holds the shared, serialisable document types used by the
// engine, CLI, API and UI. Everything here is plain data: no behaviour that
// depends on a deployment model or a storage engine.
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

// Terminal reports whether no further transitions are possible.
func (s JobStatus) Terminal() bool {
	switch s {
	case JobCompleted, JobPartial, JobCancelled, JobFailed:
		return true
	}
	return false
}

// JobDefinition is the user-facing configuration: QueryInput, SearchSources
// and Output. It is what an AI assistant generates and a user loads.
type JobDefinition struct {
	ID            string           `json:"id,omitempty" yaml:"id,omitempty"`
	Name          string           `json:"name" yaml:"name"`
	QueryInput    string           `json:"queryInput" yaml:"queryInput"`
	Query         Query            `json:"query,omitempty" yaml:"query,omitempty"`
	SearchSources []SourceRef      `json:"searchSources" yaml:"searchSources"`
	Output        OutputDefinition `json:"output" yaml:"output"`
}

// Query is the deterministic representation of QueryInput.
//
// Params are generic named values ("fuel", "yearMin") that source profiles map
// onto their own request parameters. Filters are applied to every extracted
// record after type coercion, so results honour the query even when a source
// cannot filter server-side.
type Query struct {
	Params  map[string]any `json:"params,omitempty" yaml:"params,omitempty"`
	Filters []Filter       `json:"filters,omitempty" yaml:"filters,omitempty"`
}

type Filter struct {
	Field string `json:"field" yaml:"field"`
	// Op is one of eq, ne, gt, gte, lt, lte, in, contains.
	Op    string `json:"op" yaml:"op"`
	Value any    `json:"value" yaml:"value"`
	// Missing decides what happens when the record has no value for Field:
	// "reject" (default) or "pass".
	Missing string `json:"missing,omitempty" yaml:"missing,omitempty"`
}

// SourceRef names a source and supplies its profile either inline or by
// reference to a stored source profile document.
type SourceRef struct {
	ID         string         `json:"id" yaml:"id"`
	ProfileRef string         `json:"profileRef,omitempty" yaml:"profileRef,omitempty"`
	Profile    *SourceProfile `json:"profile,omitempty" yaml:"profile,omitempty"`
}

type OutputDefinition struct {
	// Format is "json" or "csv".
	Format string `json:"format" yaml:"format"`
	// Fields may be written as a list of FieldDefinition or in compact form as
	// an object of name -> type, where a trailing "?" marks the field optional.
	Fields OutputFields `json:"fields" yaml:"fields"`
	// DedupeBy lists the fields that identify a record. Defaults to ["url"]
	// when a url field exists, otherwise all fields.
	DedupeBy []string `json:"dedupeBy,omitempty" yaml:"dedupeBy,omitempty"`
}

type OutputFields []FieldDefinition

type FieldDefinition struct {
	Name string `json:"name" yaml:"name"`
	// Type is one of string, integer, decimal, boolean, url.
	Type     string `json:"type" yaml:"type"`
	Required bool   `json:"required,omitempty" yaml:"required,omitempty"`
}

// ---------------------------------------------------------------------------
// Source profile DSL
// ---------------------------------------------------------------------------

// SourceProfile declaratively describes how the deterministic engine talks to
// one source and how that source's content maps to generic output fields.
type SourceProfile struct {
	ID          string `json:"id" yaml:"id"`
	Name        string `json:"name,omitempty" yaml:"name,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	// Format of fetched documents: "html" (default) or "json".
	Format  string      `json:"format,omitempty" yaml:"format,omitempty"`
	Request RequestSpec `json:"request" yaml:"request"`
	// Acquisition says how pages are obtained. Omitted means plain HTTP.
	Acquisition *Acquisition         `json:"acquisition,omitempty" yaml:"acquisition,omitempty"`
	Limits      Limits               `json:"limits,omitempty" yaml:"limits,omitempty"`
	Pagination  *Pagination          `json:"pagination,omitempty" yaml:"pagination,omitempty"`
	Listing     Listing              `json:"listing" yaml:"listing"`
	Ignore      []string             `json:"ignore,omitempty" yaml:"ignore,omitempty"`
	Fields      map[string]FieldRule `json:"fields" yaml:"fields"`
	Notes       []string             `json:"notes,omitempty" yaml:"notes,omitempty"`
}

type RequestSpec struct {
	URL     string                  `json:"url" yaml:"url"`
	Params  map[string]ParamMapping `json:"params,omitempty" yaml:"params,omitempty"`
	Headers map[string]string       `json:"headers,omitempty" yaml:"headers,omitempty"`
}

// ParamMapping produces one request parameter from either a generic query
// param (From) or a constant (Value).
type ParamMapping struct {
	From  string            `json:"from,omitempty" yaml:"from,omitempty"`
	Value any               `json:"value,omitempty" yaml:"value,omitempty"`
	Map   map[string]string `json:"map,omitempty" yaml:"map,omitempty"`
	// Unmapped controls what happens when From has a value missing from Map:
	// "omit" (default, a warning is emitted), "pass" (send as-is) or "fail".
	Unmapped string `json:"unmapped,omitempty" yaml:"unmapped,omitempty"`
}

// Acquisition lists the strategies used to obtain a page, in order. Each
// later strategy is a fallback, used only when the previous one failed in a
// way a different strategy might fix: HTTP 401, 403 or 429, or a bot
// challenge page. Whatever strategy succeeds, the page goes through the same
// extraction rules.
type Acquisition struct {
	Strategies []AcquisitionStrategy `json:"strategies" yaml:"strategies"`
}

type AcquisitionStrategy struct {
	// Type is "http" (plain GET) or "browser" (headless Chrome/Chromium).
	Type string `json:"type" yaml:"type"`
	// WaitUntil (browser): "load" (default) or "domcontentloaded".
	WaitUntil string `json:"waitUntil,omitempty" yaml:"waitUntil,omitempty"`
	// WaitFor (browser): CSS selector to wait for on listing pages before
	// reading the DOM, typically the listing.items selector.
	WaitFor  string `json:"waitFor,omitempty" yaml:"waitFor,omitempty"`
	TimeoutS int    `json:"timeoutSeconds,omitempty" yaml:"timeoutSeconds,omitempty"`
}

type Limits struct {
	MaxPages   int `json:"maxPages,omitempty" yaml:"maxPages,omitempty"`
	MaxRecords int `json:"maxRecords,omitempty" yaml:"maxRecords,omitempty"`
	// DelayMs between requests to this source. Defaults to 500 when unset.
	DelayMs  *int `json:"delayMs,omitempty" yaml:"delayMs,omitempty"`
	Retries  *int `json:"retries,omitempty" yaml:"retries,omitempty"`
	TimeoutS int  `json:"timeoutSeconds,omitempty" yaml:"timeoutSeconds,omitempty"`
}

type Pagination struct {
	// Next follows a link/value on the listing page to the next page.
	Next *FieldRule `json:"next,omitempty" yaml:"next,omitempty"`
	// Param increments a request parameter instead.
	Param *PageParam `json:"param,omitempty" yaml:"param,omitempty"`
	// TotalResults and PageSize, or TotalPages, let the engine report a real
	// page total. Leave both unset when the source does not expose one.
	TotalResults *FieldRule `json:"totalResults,omitempty" yaml:"totalResults,omitempty"`
	PageSize     int        `json:"pageSize,omitempty" yaml:"pageSize,omitempty"`
	TotalPages   *FieldRule `json:"totalPages,omitempty" yaml:"totalPages,omitempty"`
}

type PageParam struct {
	Name  string `json:"name" yaml:"name"`
	Start int    `json:"start,omitempty" yaml:"start,omitempty"`
	Step  int    `json:"step,omitempty" yaml:"step,omitempty"`
}

type Listing struct {
	// Items selects one element (html: CSS selector) or array (json: path)
	// per candidate record on a listing page.
	Items string `json:"items" yaml:"items"`
	// Skip drops listing items matching any rule (ads, sponsored tiles).
	Skip []Matcher `json:"skip,omitempty" yaml:"skip,omitempty"`
	// DetailLink locates each item's detail page URL.
	DetailLink *FieldRule `json:"detailLink,omitempty" yaml:"detailLink,omitempty"`
	// FollowDetail fetches each item's detail page so candidates with
	// from: detail can be evaluated.
	FollowDetail bool `json:"followDetail,omitempty" yaml:"followDetail,omitempty"`
}

// FieldRule extracts one value. Either give a single candidate inline
// (css/attr/jsonld/...) or an ordered Candidates list.
type FieldRule struct {
	Candidate  `yaml:",inline"`
	Candidates []Candidate `json:"candidates,omitempty" yaml:"candidates,omitempty"`
	Reject     []Matcher   `json:"reject,omitempty" yaml:"reject,omitempty"`
	Prefer     []Matcher   `json:"prefer,omitempty" yaml:"prefer,omitempty"`
	Transform  []Transform `json:"transform,omitempty" yaml:"transform,omitempty"`
}

// Candidate is one place a value may come from. Exactly one of Css, Attr
// (on the context element itself), JSONLD, JSON, Meta, Const or Builtin is set.
type Candidate struct {
	// From selects the context: "item" (default; the listing item),
	// "listing" (the whole listing page) or "detail" (the detail page).
	From string `json:"from,omitempty" yaml:"from,omitempty"`
	Css  string `json:"css,omitempty" yaml:"css,omitempty"`
	// Attr reads an attribute instead of text. With no Css it reads the
	// context element's own attribute.
	Attr string `json:"attr,omitempty" yaml:"attr,omitempty"`
	// JSONLD is a dot path evaluated against schema.org JSON-LD blocks.
	JSONLD     string `json:"jsonld,omitempty" yaml:"jsonld,omitempty"`
	JSONLDType string `json:"jsonldType,omitempty" yaml:"jsonldType,omitempty"`
	// JSON is a dot path into a JSON document or JSON listing item.
	JSON string `json:"json,omitempty" yaml:"json,omitempty"`
	// Meta reads <meta name|property=...> content.
	Meta  string `json:"meta,omitempty" yaml:"meta,omitempty"`
	Const any    `json:"const,omitempty" yaml:"const,omitempty"`
	// Builtin is one of pageUrl, detailUrl, listingUrl, sourceId.
	Builtin string `json:"builtin,omitempty" yaml:"builtin,omitempty"`
}

// Matcher is a deterministic predicate. Text predicates are case-insensitive
// unless CaseSensitive is set. Min/Max apply to the transformed numeric value.
type Matcher struct {
	Contains      string   `json:"contains,omitempty" yaml:"contains,omitempty"`
	Equals        string   `json:"equals,omitempty" yaml:"equals,omitempty"`
	Regex         string   `json:"regex,omitempty" yaml:"regex,omitempty"`
	Css           string   `json:"css,omitempty" yaml:"css,omitempty"`
	Min           *float64 `json:"min,omitempty" yaml:"min,omitempty"`
	Max           *float64 `json:"max,omitempty" yaml:"max,omitempty"`
	CaseSensitive bool     `json:"caseSensitive,omitempty" yaml:"caseSensitive,omitempty"`
}

// Transform is a named deterministic operation. In configuration it is
// written either as a bare name ("decimal") or a single-key object
// ({"regex": "(\\d+) kWh"}).
type Transform struct {
	Name string `json:"name"`
	Arg  any    `json:"arg,omitempty"`
}

// ---------------------------------------------------------------------------
// Runtime documents
// ---------------------------------------------------------------------------

type SourceStatus string

const (
	SourcePending   SourceStatus = "pending"
	SourceRunning   SourceStatus = "running"
	SourceCompleted SourceStatus = "completed"
	SourceStopped   SourceStatus = "stopped"
	SourceSkipped   SourceStatus = "skipped"
	SourceFailed    SourceStatus = "failed"
)

// AcquisitionStats counts attempts of one acquisition strategy for a source.
type AcquisitionStats struct {
	Strategy  string `json:"strategy"`
	Attempts  int    `json:"attempts"`
	Succeeded int    `json:"succeeded"`
	Failed    int    `json:"failed"`
	// LastResult is a short description of the latest attempt, e.g.
	// "HTTP 403 Forbidden", "page loaded", "bot challenge detected".
	LastResult string `json:"lastResult,omitempty"`
}

// Failure stages for SourceProgress.FailureStage.
const (
	StageAcquisition = "acquisition"
	StageExtraction  = "extraction"
)

type SourceProgress struct {
	ID     string       `json:"id"`
	Name   string       `json:"name,omitempty"`
	Status SourceStatus `json:"status"`
	// PagesAttempted counts logical pages (URLs) the source tried to obtain;
	// PagesVisited counts those that were obtained, by any strategy. Several
	// acquisition attempts for one URL count as one page.
	PagesAttempted  int `json:"pagesAttempted"`
	PagesVisited    int `json:"pagesVisited"`
	PagesFailed     int `json:"pagesFailed"`
	PagesDiscovered int `json:"pagesDiscovered"`
	// ListingPagesTotal is only set when the source exposes a real total.
	ListingPagesTotal   int `json:"listingPagesTotal,omitempty"`
	ListingPagesVisited int `json:"listingPagesVisited"`
	RecordsFound        int `json:"recordsFound"`
	RecordsAccepted     int `json:"recordsAccepted"`
	RecordsRejected     int `json:"recordsRejected"`
	Duplicates          int `json:"duplicates"`
	Warnings            int `json:"warnings"`
	Errors              int `json:"errors"`
	// Acquisition has one entry per configured strategy, in order.
	Acquisition []AcquisitionStats `json:"acquisition,omitempty"`
	// FailureStage and FailureReason explain a failed source.
	FailureStage  string `json:"failureStage,omitempty"`
	FailureReason string `json:"failureReason,omitempty"`
}

type Progress struct {
	CurrentSource   string           `json:"currentSource,omitempty"`
	SourcesTotal    int              `json:"sourcesTotal"`
	SourcesComplete int              `json:"sourcesComplete"`
	PagesAttempted  int              `json:"pagesAttempted"`
	PagesVisited    int              `json:"pagesVisited"`
	PagesDiscovered int              `json:"pagesDiscovered"`
	RecordsFound    int              `json:"recordsFound"`
	RecordsAccepted int              `json:"recordsAccepted"`
	RecordsRejected int              `json:"recordsRejected"`
	Duplicates      int              `json:"duplicates"`
	Warnings        int              `json:"warnings"`
	Errors          int              `json:"errors"`
	ElapsedMs       int64            `json:"elapsedMs"`
	Sources         []SourceProgress `json:"sources"`
}

// Job is one execution. In product terms it is a Run: when SessionID is set
// it is run number RunNumber of that Session. Definition is the immutable
// configuration snapshot the run used, with every source profile resolved
// inline, so editing the Session or a stored profile later never changes
// what a historical run reports.
type Job struct {
	ID         string        `json:"id"`
	SessionID  string        `json:"sessionId,omitempty"`
	RunNumber  int           `json:"runNumber,omitempty"`
	Definition JobDefinition `json:"definition"`
	Status     JobStatus     `json:"status"`
	// StatusReason explains partial/failed/cancelled outcomes.
	StatusReason string     `json:"statusReason,omitempty"`
	Progress     Progress   `json:"progress"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
	// RunningMs accumulates time spent running, excluding paused time.
	RunningMs int64 `json:"runningMs"`
}

type EventType string

const (
	EventJobStarted     EventType = "JobStarted"
	EventSourceStarted  EventType = "SourceStarted"
	EventPageDiscovered EventType = "PageDiscovered"
	EventPageFetched    EventType = "PageFetched"
	// EventAcquisitionAttempt records one strategy's attempt to obtain a page.
	EventAcquisitionAttempt EventType = "AcquisitionAttempt"
	EventRecordFound        EventType = "RecordFound"
	EventRecordAccepted     EventType = "RecordAccepted"
	EventRecordRejected     EventType = "RecordRejected"
	EventWarning            EventType = "Warning"
	EventError              EventType = "Error"
	EventSourceCompleted    EventType = "SourceCompleted"
	EventJobPausing         EventType = "JobPausing"
	EventJobPaused          EventType = "JobPaused"
	EventJobResumed         EventType = "JobResumed"
	EventJobFinalising      EventType = "JobFinalising"
	EventJobCompleted       EventType = "JobCompleted"
	EventJobPartial         EventType = "JobPartial"
	EventJobCancelled       EventType = "JobCancelled"
	EventJobFailed          EventType = "JobFailed"
)

// Event is an append-only job event document. Every event carries the job
// status and a progress snapshot so consumers never have to recompute them.
type Event struct {
	JobID     string         `json:"jobId"`
	Seq       int64          `json:"seq"`
	Type      EventType      `json:"type"`
	SourceID  string         `json:"sourceId,omitempty"`
	Message   string         `json:"message,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
	Data      map[string]any `json:"data,omitempty"`
	Status    JobStatus      `json:"status"`
	Progress  *Progress      `json:"progress,omitempty"`
}

// Record is an accepted generic record.
type Record struct {
	JobID    string         `json:"jobId"`
	SourceID string         `json:"sourceId"`
	Key      string         `json:"key"`
	Values   map[string]any `json:"values"`
	// Provenance records which candidate produced each field.
	Provenance map[string]string `json:"provenance,omitempty"`
	PageURL    string            `json:"pageUrl,omitempty"`
	Partial    bool              `json:"partial,omitempty"`
	SeenAt     time.Time         `json:"seenAt"`
}

// Output is a finalised, formatted result document.
type Output struct {
	JobID       string    `json:"jobId"`
	Format      string    `json:"format"`
	ContentType string    `json:"contentType"`
	Partial     bool      `json:"partial"`
	RecordCount int       `json:"recordCount"`
	Data        []byte    `json:"data"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Session is a persistent scraping/search project: the user's current
// configuration. Each execution of it is a Job (Run) referencing SessionID.
// Definition may be an incomplete draft; it is validated when run.
type Session struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Definition JobDefinition `json:"definition"`
	CreatedAt  time.Time     `json:"createdAt"`
	UpdatedAt  time.Time     `json:"updatedAt"`
}

// RunSummary is a light view of a run for lists; it omits the
// configuration snapshot and per-source detail.
type RunSummary struct {
	ID              string     `json:"id"`
	SessionID       string     `json:"sessionId,omitempty"`
	RunNumber       int        `json:"runNumber"`
	Status          JobStatus  `json:"status"`
	StatusReason    string     `json:"statusReason,omitempty"`
	RecordsAccepted int        `json:"recordsAccepted"`
	Warnings        int        `json:"warnings"`
	Errors          int        `json:"errors"`
	CreatedAt       time.Time  `json:"createdAt"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	RunningMs       int64      `json:"runningMs"`
}

// Summary returns the list view of a job.
func (j Job) Summary() RunSummary {
	return RunSummary{
		ID: j.ID, SessionID: j.SessionID, RunNumber: j.RunNumber, Status: j.Status, StatusReason: j.StatusReason,
		RecordsAccepted: j.Progress.RecordsAccepted, Warnings: j.Progress.Warnings, Errors: j.Progress.Errors,
		CreatedAt: j.CreatedAt, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt, RunningMs: j.RunningMs,
	}
}

// SessionSummary is a light view of a session for the session list.
type SessionSummary struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	QueryInput string      `json:"queryInput,omitempty"`
	CreatedAt  time.Time   `json:"createdAt"`
	UpdatedAt  time.Time   `json:"updatedAt"`
	RunCount   int         `json:"runCount"`
	LastRun    *RunSummary `json:"lastRun,omitempty"`
}

// Checkpoint is the engine's resumable state for a job. State is opaque JSON
// owned by the engine so storage implementations never need to understand it.
type Checkpoint struct {
	JobID     string    `json:"jobId"`
	State     []byte    `json:"state"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ValidationIssue is reported for invalid jobs and profiles.
type ValidationIssue struct {
	Path     string `json:"path"`
	Message  string `json:"message"`
	Severity string `json:"severity"` // "error" or "warning"
}
