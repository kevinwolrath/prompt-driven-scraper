# Prompt Driven Scraper

A deterministic, configurable scraper. AI assistants (Claude, ChatGPT, …) help users **write the
configuration**; the running application never calls an AI service. The same configuration against
the same website content produces the same records.

```text
Job configuration ─► profile validation ─► HTTP fetch ─► listing discovery ─► deterministic extraction
   ─► field transforms ─► generic records ─► live events / progress ─► JSON / CSV output
```

## Quick start

Requires Go 1.23+ and (for the UI) Node 20+.

```bash
# CLI: run the example job against the bundled demo site
go run ./apps/cli run -demo-site -o ev-cars.csv examples/ev-cars.job.yaml
```

```bash
# API + UI: build the UI once, then start the API (it serves the UI, the demo site and example profiles)
(cd apps/web && npm install && npm run build)
go run ./apps/api        # http://localhost:8080, data in data/prompt-driven-scraper.db
```

For UI development run `go run ./apps/api` and `cd apps/web && npm run dev` (Vite proxies `/api`).

```bash
# Docker
docker build -t prompt-driven-scraper . && docker run -p 8080:8080 prompt-driven-scraper
```

## The model

A job has three user-facing concepts:

1. **QueryInput** — what the user wants (`"I'm only looking for EV cars after 2024"`) plus its
   deterministic form: `query.params` (generic values that profiles map to request parameters) and
   `query.filters` (applied to every record).
2. **SearchSources** — the sources to search. Each has a declarative **source profile** that owns
   *all* site-specific behaviour: URLs, parameter mapping, pagination, listing/detail discovery,
   extraction candidates, rejection rules, precedence, transforms and quirks.
3. **Output** — the generic records wanted, e.g.
   `{"make": "string", "year": "integer", "batterySizeKwh": "decimal?", "url": "url"}`.

See [`examples/ev-cars.job.yaml`](examples/ev-cars.job.yaml) and
[`examples/sources/`](examples/sources/).

## Source profile DSL (summary)

```yaml
price:
  candidates:                       # ordered = precedence
    - { from: detail, jsonld: offers.price, jsonldType: Car }
    - { from: detail, css: ".price-driveaway" }
    - { css: ".price span" }        # listing-card fallback
  reject:                           # negative knowledge
    - css: ".finance-repayment"
    - contains: "per week"
    - max: 2000
  prefer:
    - contains: "drive away"
  transform: [currency, decimal]    # closed set of named transforms
```

Candidates read CSS text/attributes, JSON-LD, embedded JSON, JSON APIs (with `[key=value]` path
predicates), meta tags, constants or built-ins (`detailUrl`, …). There is no embedded code. Configuration is
decoded strictly: unknown keys and transforms are errors, so invented features fail loudly. The full reference
for humans and AI assistants is [`ai/PROFILE_AUTHORING.md`](ai/PROFILE_AUTHORING.md); the
machine contracts are [`schemas/`](schemas/).

### Using an AI assistant

Give the assistant `ai/PROFILE_AUTHORING.md` and the schemas, then ask e.g.

> Use the Prompt Driven Scraper instructions. Find suitable Australian vehicle sales sources for EVs
> from 2024 onwards and create the scraper configuration.

Paste the result into **Load AI-generated configuration** in the UI (or save it and run
`scraper validate` / `scraper run`). Each accepted record carries field provenance, and each rejected record a
reason, so profiles can be corrected quickly.

## Sessions and runs

A **Session** is a saved scraping project: a name plus its current Query Input, Search Sources and
Output. Edit and save it as often as you like; drafts with validation errors can be saved but not run.

A **Run** is one execution of a Session. Every run gets the next run number and keeps an
**immutable snapshot** of the configuration it used, with each source profile copied in full. Editing
the Session or a stored profile later never changes what an old run shows. Running again always
creates a new run; old runs, their records, events and output are never overwritten. A finished
run cannot be updated at the storage layer.

```text
Session "Victorian EVs from 2024"      (current configuration, editable)
  ├── Run #1  completed  18 results   (snapshot of the configuration at the time)
  ├── Run #2  partial    12 results
  └── Run #3  completed  21 results
```

Internally a Run is the engine's existing job document with `sessionId` and `runNumber`.

## Job execution

Jobs run asynchronously. Each source is a FIFO queue of tasks (listing page → its detail pages →
next listing page), checkpointed after every task.

```text
queued → running ⇄ pausing → paused → running …
running/pausing/paused → finalising → completed | partial
any non-terminal → cancelled ; failures → failed
```

| Control | Behaviour |
| --- | --- |
| **Pause** | Stop scheduling; finish the in-flight request; save a checkpoint → `paused`. |
| **Resume** | Continue from the checkpoint. Nothing is fetched twice. |
| **Cancel** | Abort in-flight work → `cancelled`. No output. |
| **Give me what you've got** | Stop discovering; salvage already-fetched listing items whose detail pages are pending (listing values only, marked partial); validate, dedupe and render output → `partial`. Also works from `paused`. |

A job whose sources partly failed also finishes `partial`, with the reason recorded.

### Events and progress

Every state change is an event: `JobStarted, SourceStarted, PageDiscovered, PageFetched, RecordFound,
RecordAccepted, RecordRejected, Warning, Error, SourceCompleted, JobPausing, JobPaused, JobResumed,
JobFinalising, JobCompleted, JobPartial, JobCancelled, JobFailed`. Each carries a sequence number,
the job status and a progress snapshot. Progress is factual (pages, records found/accepted/rejected,
duplicates, warnings, errors, elapsed running time); a percentage exists only when a profile points
at a real total on the site. Accepted records stream as they are found.

### HTTP API

| Method & path | |
| --- | --- |
| `GET /api/sessions` | Session summaries (name, run count, last run). No results. |
| `POST /api/sessions` | Create a session: `{ "name", "definition" }` (definition = job configuration; drafts allowed). |
| `GET /api/sessions/{id}` | The session, its run summaries and current validation issues. |
| `PUT /api/sessions/{id}` | Save the session's name and current configuration. Never touches runs. |
| `POST /api/sessions/{id}/runs` | Start a new run of the saved configuration. `400` with `issues` if invalid. |
| `GET /api/runs/{id}` | The run, including its configuration snapshot and progress. |
| `POST /api/runs/{id}/pause\|resume\|cancel\|finalise` | Controls. `409` if not valid in the current state. |
| `GET /api/runs/{id}/events` | Server-sent events; replays history, then live. Honours `Last-Event-ID` / `?since=`. |
| `GET /api/runs/{id}/records` | Accepted records, with provenance. |
| `GET /api/runs/{id}/output` | The run's JSON/CSV output (completed or partial runs), available again at any time. |
| `GET/POST /api/profiles` | List / add source profiles. |
| `POST /api/validate` | Validate a job or profile without running it. |

### CLI

```text
scraper validate [-profiles dir] <job-or-profile.yaml>
scraper run [-profiles dir] [-o file] [-demo-site] [-events] <job.yaml>
scraper demo-site [-addr 127.0.0.1:8089] [-latency 0s]
```

During `run`: Ctrl-C once = give me what you've got, twice = cancel; `p`/`r` + Enter = pause/resume.

## Layout

```text
apps/
  api/          Go HTTP API + SSE; serves the built UI and the demo site
  cli/          Go CLI: validate / run / demo-site
  web/          React + TypeScript UI (Query, Sources, Output, Run)
internal/
  engine/       async job runner, state machine, checkpoints, event bus
  profile/      strict decoding and validation of jobs and profiles
  query/        generic query -> request mapping; record filters
  extract/      deterministic extraction, matchers, transforms, precedence
  output/       type coercion, dedupe, JSON/CSV rendering
  fetch/        HTTP fetcher (an interface; replaceable)
  session/      Sessions: save configuration, number and start runs
  storage/      store interfaces, MemoryStore, sqlite/ (SQLiteStore), storeconfig/
                (selection) and storagetest/ (contract tests run against every store)
  demosite/     deterministic demo marketplace with real-world traps
pkg/contracts/  shared document types (jobs, profiles, events, records)
schemas/        JSON Schemas for jobs and source profiles
examples/       example job and source profiles
migrations/     versioned SQL schema shared by SQLite and Cloudflare D1
deploy/         Cloudflare D1 configuration
ai/             PROFILE_AUTHORING.md — the manual given to AI assistants
```

## Persistence

Persistence is behind the `storage.Store` interfaces; the engine never knows which database it is
using. The implementation is chosen when the API starts:

| `-store` / `PDS_STORE` | Use | |
| --- | --- | --- |
| `sqlite` (default) | Local | File at `-db` / `PDS_DB`, default `data/prompt-driven-scraper.db` (created if missing). |
| `memory` | Tests, throwaway runs | Nothing survives the process. |
| `d1` | Hosted | Not available to the Go runtime yet: startup fails with an explanation. |

Any other value, or a database that cannot be opened, stops the API with an error. It never
falls back to memory.

```bash
go run ./apps/api -db ~/scrapes/pds.db       # or PDS_DB=~/scrapes/pds.db
go run ./apps/api -store memory              # nothing is saved
```

The schema is relational for scraper concepts (`sessions`, `runs`, `run_events`, `run_records`,
`run_checkpoints`, `run_outputs`, `source_profiles`). Scraped values, provenance, configuration and
progress are stored as JSON, so there are no domain tables. Records, events and checkpoints are written
as the run goes, not at the end.

Migrations live in [`migrations/`](migrations/) as numbered SQL files. On startup the API applies any
that are pending, inside a transaction, recording them in `schema_migrations`. It never recreates the
database, and it refuses to open a database whose schema is newer than the binary. To change the
schema, add a new file; never edit a released one.

SQLite runs in WAL mode with `synchronous=NORMAL`, a 5 s busy timeout, foreign keys on and one
connection (single user; writes are serialised). The driver is pure Go (`modernc.org/sqlite`), so no
CGO and no database server are needed.

### Backup

The database is one file. With the API stopped, copy `data/prompt-driven-scraper.db`. While it is
running, use SQLite's online backup so the copy is consistent:

```bash
sqlite3 data/prompt-driven-scraper.db ".backup 'pds-backup.db'"
```

In Docker, sessions live in the `/data` volume (`docker run -v pds-data:/data …`).

### Restart behaviour (tested)

- Finished runs, with their records, events, configuration snapshot and output, are read back after a
  restart (`TestFinishedRunSurvivesRestart`, `TestSessionRunFlowSurvivesRestart`).
- On Ctrl-C/SIGTERM the API pauses running jobs at a checkpoint before exiting. After the restart
  they show as paused and **Resume** continues where they stopped, without re-fetching completed pages
  (`TestPausedRunResumesAfterRestart`).
- If the process dies without shutting down, the next start marks those runs as paused with an
  "interrupted" reason. Resume continues from the last checkpoint: at most the one page that was in
  flight is fetched again, and records it had already accepted are not accepted twice
  (`TestCrashedRunIsRecoveredAndResumable`). Its found/duplicate counters may count that page twice.
  Runs interrupted before their first checkpoint are marked failed.

### Hosted (Cloudflare D1) — status

The hosted design keeps Cloudflare as the control and persistence plane and keeps scraper execution
portable. The Go engine is not squeezed into a Worker; it runs wherever Go runs (locally, in Docker,
or on hosted compute).

```text
React UI (Cloudflare Pages) ─► Cloudflare control plane (Worker) ─► D1   sessions, runs, records, events…
                                        ▲                             R2   later: large outputs
                                        │ HTTP
                          Go runner (local / Docker / hosted compute): deterministic engine
```

Implemented now:
- one versioned schema in [`migrations/`](migrations/), written in plain SQLite that D1 accepts, and
  used unchanged by the local SQLite store;
- [`deploy/cloudflare/wrangler.toml`](deploy/cloudflare/wrangler.toml), which creates the D1
  database and applies those migrations:
  ```bash
  cd deploy/cloudflare
  npx wrangler d1 create prompt-driven-scraper   # put the id in wrangler.toml
  npx wrangler d1 migrations apply prompt-driven-scraper --remote
  ```
  These commands follow Wrangler's documented workflow but have not been run against a Cloudflare
  account from this repository yet.

Not implemented yet:
- the Worker control-plane API over D1;
- a Go `storage.Store` that talks to that control plane (or the D1 HTTP API), and a runner mode
  that claims runs from it;
- R2 for large outputs (`run_outputs.content` is inline; D1 rows are limited to about 2 MB).

Until then `-store d1` fails at startup instead of pretending.

## Current limitations

- HTTP GET only; no JavaScript rendering, POST bodies, cookies/login.
- No robots.txt handling yet; profiles set politeness via `limits.delayMs`.
- Sources run sequentially and each source fetches one page at a time (deterministic and polite).
- No date output type yet.
- One process per database: two API instances must not share a SQLite file.
- No housekeeping (deleting sessions or runs) yet.
