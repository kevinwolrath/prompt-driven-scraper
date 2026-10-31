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
go run ./apps/api        # http://localhost:8080
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
| `POST /api/jobs` | Submit a job (YAML or JSON). `400` with `issues` if invalid. |
| `GET /api/jobs`, `GET /api/jobs/{id}` | Job documents with progress. |
| `POST /api/jobs/{id}/pause\|resume\|cancel\|finalise` | Controls. `409` if not valid in the current state. |
| `GET /api/jobs/{id}/events` | Server-sent events; honours `Last-Event-ID` / `?since=` for replay. |
| `GET /api/jobs/{id}/records` | Accepted records so far, with provenance. |
| `GET /api/jobs/{id}/output` | Final JSON/CSV output (completed or partial jobs). |
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
  storage/      document-oriented store interfaces + in-memory implementation
  demosite/     deterministic demo marketplace with real-world traps
pkg/contracts/  shared document types (jobs, profiles, events, records)
schemas/        JSON Schemas for jobs and source profiles
examples/       example job and source profiles
ai/             PROFILE_AUTHORING.md — the manual given to AI assistants
```

## Persistence

Runtime data is document-oriented — `jobs`, `source_profiles`, `job_events`, `checkpoints`,
`records`, `outputs` — behind the `storage.Store` interfaces. The engine has no database dependency;
a MongoDB implementation maps one-to-one onto those collections. This first slice ships an
in-memory store, so jobs do not survive an API restart yet.

## Current limitations

- HTTP GET only; no JavaScript rendering, POST bodies, cookies/login.
- No robots.txt handling yet; profiles set politeness via `limits.delayMs`.
- Sources run sequentially and each source fetches one page at a time (deterministic and polite).
- No date output type yet.
- In-memory storage only; no recovery of running jobs after restart.
