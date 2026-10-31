-- Prompt Driven Scraper persistence schema, version 1.
--
-- Shared by the local SQLite store (applied by the Go migration runner) and
-- Cloudflare D1 (applied with `wrangler d1 migrations apply`). Keep it to
-- plain SQLite syntax that D1 also accepts.
--
-- Tables describe scraper concepts only. Scraped content, provenance,
-- configuration and progress are JSON; there are no domain tables.
-- Timestamps are UTC RFC 3339 text, so they sort correctly as strings.

CREATE TABLE sessions (
  id              TEXT PRIMARY KEY,
  name            TEXT NOT NULL,
  definition_json TEXT NOT NULL,   -- current (editable) configuration
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL
);

CREATE TABLE runs (
  id               TEXT PRIMARY KEY,
  session_id       TEXT REFERENCES sessions(id),  -- NULL for ad-hoc CLI runs
  run_number       INTEGER,                        -- 1, 2, 3 ... within a session
  status           TEXT NOT NULL,
  status_reason    TEXT NOT NULL DEFAULT '',
  definition_json  TEXT NOT NULL,   -- immutable configuration snapshot, profiles inline
  progress_json    TEXT NOT NULL,
  records_accepted INTEGER NOT NULL DEFAULT 0,
  running_ms       INTEGER NOT NULL DEFAULT 0,
  created_at       TEXT NOT NULL,
  updated_at       TEXT NOT NULL,
  started_at       TEXT,
  finished_at      TEXT,
  UNIQUE (session_id, run_number)
);
CREATE INDEX runs_by_session ON runs (session_id, run_number);
CREATE INDEX runs_by_status ON runs (status);

CREATE TABLE run_events (
  run_id        TEXT NOT NULL REFERENCES runs(id),
  seq           INTEGER NOT NULL,
  type          TEXT NOT NULL,
  source_id     TEXT NOT NULL DEFAULT '',
  message       TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL,
  data_json     TEXT,
  progress_json TEXT,
  created_at    TEXT NOT NULL,
  PRIMARY KEY (run_id, seq)
);

CREATE TABLE run_records (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,  -- acceptance order
  run_id          TEXT NOT NULL REFERENCES runs(id),
  source_id       TEXT NOT NULL,
  record_key      TEXT NOT NULL,  -- generic dedupe identity; enables run-to-run comparison later
  data_json       TEXT NOT NULL,
  provenance_json TEXT,
  page_url        TEXT NOT NULL DEFAULT '',
  partial         INTEGER NOT NULL DEFAULT 0,
  created_at      TEXT NOT NULL
);
CREATE INDEX run_records_by_run ON run_records (run_id, id);
CREATE INDEX run_records_by_key ON run_records (record_key);

CREATE TABLE run_checkpoints (
  run_id     TEXT PRIMARY KEY REFERENCES runs(id),
  state_json TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE run_outputs (
  run_id       TEXT PRIMARY KEY REFERENCES runs(id),
  format       TEXT NOT NULL,
  content_type TEXT NOT NULL,
  partial      INTEGER NOT NULL DEFAULT 0,
  record_count INTEGER NOT NULL DEFAULT 0,
  content      BLOB NOT NULL,   -- may move to R2 (with a key here) for large outputs
  created_at   TEXT NOT NULL
);

CREATE TABLE source_profiles (
  id           TEXT PRIMARY KEY,
  profile_json TEXT NOT NULL,
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);
