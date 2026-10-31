# Prompt Driven Scraper

Deterministic, profile-driven scraping runtime with optional AI-assisted profile authoring outside the runtime.

## Goals

- Deterministic execution: no LLM calls are required to run a scrape.
- Portable: run as a local CLI, local API, desktop-backed service, container, or cloud service.
- Profile driven: source-specific behaviour lives in declarative source profiles rather than hard-coded site branches.
- Document oriented persistence: jobs, profiles, checkpoints, events, and results are naturally stored as JSON documents.
- Interruptible jobs: progress, pause, resume, cancel, and "give me what you've got" partial finalisation are first-class runtime behaviours.
- AI friendly authoring: repository instructions define how GPT/Claude can produce valid job and source-profile JSON/YAML.

## Monorepo layout

```text
apps/
  api/          Go HTTP API exposing the engine
  cli/          Go CLI for validate/run/test/resume
  web/          React UI that can talk to a local or remote API

internal/
  engine/       Deterministic job runtime and state machine
  profile/      Source-profile parsing and validation
  query/        Generic query model -> source-specific request mapping
  extract/      Deterministic extraction and transforms
  output/       CSV/JSON output finalisation
  storage/      Persistence interfaces

pkg/
  contracts/    Shared Go contracts for jobs, profiles, events, results

schemas/
  job.schema.json
  source-profile.schema.json

examples/
  ev-cars.job.yaml
  sources/example-cars.source.yaml

ai/
  PROFILE_AUTHORING.md

web/
  shared/       TypeScript contracts generated or mirrored from schemas
```

## Core model

A job has three visible concepts:

1. **QueryInput** — what the user wants, e.g. `EV cars after 2024`.
2. **SearchSources** — one or more deterministic source profiles.
3. **Output** — the desired generic fields and output format.

Source profiles contain both request/query mapping and result extraction mapping.

```text
Generic Query
    -> Source Profile request mapping
    -> Website/API
    -> Source Profile extraction mapping
    -> Generic Records
    -> Output formatter
```

## Runtime status

Jobs are designed around an explicit state machine:

```text
queued -> running -> pausing -> paused -> running
                   \-> finalising -> partial
                   \-> cancelled
                   \-> failed
running -> completed
```

`finalising` is used by **Give me what you've got**: stop discovering new work, finish already fetched items, validate/dedupe current records, then produce partial output.

## Storage

The initial storage abstraction is document-oriented. A MongoDB-compatible implementation is a natural cloud option, while local development can use an embedded/document implementation without changing engine code.

Suggested collections/documents:

- `jobs`
- `source_profiles`
- `job_events`
- `checkpoints`
- `records`
- `outputs`

Relational modelling is deliberately not assumed by the engine.

## Next implementation slice

- compile Go workspace and basic CLI/API binaries
- validate job/profile files against contracts
- in-memory storage implementation
- event stream + job state machine
- React job runner screen with live progress and Pause / Cancel / Give me what you've got controls
- first real HTTP + HTML source adapter
