// Package sqlite is the local persistent storage.Store. It uses a pure-Go
// SQLite driver (no CGO, no database server) and applies the versioned
// migrations in /migrations on open. The same schema runs on Cloudflare D1.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage"
	"github.com/kevinwolrath/prompt-driven-scraper/migrations"
	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

type Store struct {
	db *sql.DB
}

var _ storage.Store = (*Store)(nil)

// Open creates (if needed), opens and migrates the database at path.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("sqlite: database path is required")
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("sqlite: create %s: %w", dir, err)
		}
	}
	// WAL lets readers proceed while a run writes; NORMAL sync is durable
	// across application crashes in WAL mode; busy_timeout absorbs brief
	// lock contention; foreign keys keep runs attached to sessions.
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)" +
		"&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %s: %w", path, err)
	}
	// One connection serialises writes: simplest correct choice for a
	// single-user workload, and it avoids SQLITE_BUSY entirely.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// SchemaVersion returns the highest applied migration version.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v)
	return int(v.Int64), err
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("sqlite: migrations table: %w", err)
	}
	current, err := s.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	all, err := migrations.All()
	if err != nil {
		return err
	}
	if len(all) < current {
		return fmt.Errorf("sqlite: database schema version %d is newer than this build (%d); refusing to start", current, len(all))
	}
	for _, m := range all[current:] {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			tx.Rollback()
			return fmt.Errorf("sqlite: apply migration %s: %w", m.Name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
			m.Version, m.Name, ts(time.Now())); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("sqlite: commit migration %s: %w", m.Name, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

func (s *Store) CreateSession(ctx context.Context, sess contracts.Session) error {
	if sess.ID == "" {
		return errors.New("session id is required")
	}
	def, err := json.Marshal(sess.Definition)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO sessions (id, name, definition_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		sess.ID, sess.Name, string(def), ts(sess.CreatedAt), ts(sess.UpdatedAt))
	return err
}

func (s *Store) UpdateSession(ctx context.Context, sess contracts.Session) error {
	def, err := json.Marshal(sess.Definition)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET name = ?, definition_json = ?, updated_at = ? WHERE id = ?`,
		sess.Name, string(def), ts(sess.UpdatedAt), sess.ID)
	return affected(res, err, storage.ErrNotFound)
}

func (s *Store) GetSession(ctx context.Context, id string) (contracts.Session, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, definition_json, created_at, updated_at FROM sessions WHERE id = ?`, id)
	sess, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return sess, storage.ErrNotFound
	}
	return sess, err
}

func (s *Store) ListSessions(ctx context.Context) ([]contracts.Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, definition_json, created_at, updated_at FROM sessions ORDER BY updated_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contracts.Session{}
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func scanSession(r scanner) (contracts.Session, error) {
	var sess contracts.Session
	var def, created, updated string
	if err := r.Scan(&sess.ID, &sess.Name, &def, &created, &updated); err != nil {
		return sess, err
	}
	sess.CreatedAt, sess.UpdatedAt = parseTS(created), parseTS(updated)
	return sess, json.Unmarshal([]byte(def), &sess.Definition)
}

// ---------------------------------------------------------------------------
// Runs (jobs)
// ---------------------------------------------------------------------------

const jobColumns = `id, session_id, run_number, status, status_reason, definition_json, progress_json,
	running_ms, created_at, updated_at, started_at, finished_at`

func (s *Store) CreateJob(ctx context.Context, job contracts.Job) error {
	if job.ID == "" {
		return errors.New("job id is required")
	}
	def, err := json.Marshal(job.Definition)
	if err != nil {
		return err
	}
	prog, err := json.Marshal(job.Progress)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO runs (`+jobColumns+`, records_accepted) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID, nullStr(job.SessionID), nullInt(job.RunNumber), string(job.Status), job.StatusReason, string(def), string(prog),
		job.RunningMs, ts(job.CreatedAt), ts(job.UpdatedAt), tsPtr(job.StartedAt), tsPtr(job.FinishedAt), job.Progress.RecordsAccepted)
	return err
}

// UpdateJob never touches the configuration snapshot, and refuses to change
// a run that has already finished.
func (s *Store) UpdateJob(ctx context.Context, job contracts.Job) error {
	prog, err := json.Marshal(job.Progress)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE runs SET status = ?, status_reason = ?, progress_json = ?, records_accepted = ?,
		running_ms = ?, updated_at = ?, started_at = ?, finished_at = ?
		WHERE id = ? AND status NOT IN ('completed', 'partial', 'cancelled', 'failed')`,
		string(job.Status), job.StatusReason, string(prog), job.Progress.RecordsAccepted,
		job.RunningMs, ts(job.UpdatedAt), tsPtr(job.StartedAt), tsPtr(job.FinishedAt), job.ID)
	if n, _ := rowsAffected(res, err); err == nil && n == 0 {
		if _, gerr := s.GetJob(ctx, job.ID); gerr != nil {
			return gerr
		}
		return storage.ErrImmutable
	}
	return err
}

func (s *Store) GetJob(ctx context.Context, id string) (contracts.Job, error) {
	job, err := scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return job, storage.ErrNotFound
	}
	return job, err
}

func (s *Store) ListJobs(ctx context.Context) ([]contracts.Job, error) {
	return s.listJobs(ctx, `SELECT `+jobColumns+` FROM runs ORDER BY created_at DESC, id DESC`)
}

func (s *Store) ListSessionJobs(ctx context.Context, sessionID string) ([]contracts.Job, error) {
	return s.listJobs(ctx, `SELECT `+jobColumns+` FROM runs WHERE session_id = ? ORDER BY run_number DESC`, sessionID)
}

func (s *Store) listJobs(ctx context.Context, q string, args ...any) ([]contracts.Job, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contracts.Job{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

func scanJob(r scanner) (contracts.Job, error) {
	var job contracts.Job
	var session sql.NullString
	var number sql.NullInt64
	var status, def, prog, created, updated string
	var started, finished sql.NullString
	if err := r.Scan(&job.ID, &session, &number, &status, &job.StatusReason, &def, &prog,
		&job.RunningMs, &created, &updated, &started, &finished); err != nil {
		return job, err
	}
	job.SessionID, job.RunNumber, job.Status = session.String, int(number.Int64), contracts.JobStatus(status)
	job.CreatedAt, job.UpdatedAt = parseTS(created), parseTS(updated)
	job.StartedAt, job.FinishedAt = parseTSPtr(started), parseTSPtr(finished)
	if err := json.Unmarshal([]byte(def), &job.Definition); err != nil {
		return job, fmt.Errorf("run %s definition: %w", job.ID, err)
	}
	if err := json.Unmarshal([]byte(prog), &job.Progress); err != nil {
		return job, fmt.Errorf("run %s progress: %w", job.ID, err)
	}
	return job, nil
}

// ---------------------------------------------------------------------------
// Events, records, checkpoints, outputs
// ---------------------------------------------------------------------------

func (s *Store) AppendEvent(ctx context.Context, ev contracts.Event) error {
	data, err := json.Marshal(ev.Data)
	if err != nil {
		return err
	}
	var prog []byte
	if ev.Progress != nil {
		if prog, err = json.Marshal(ev.Progress); err != nil {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO run_events (run_id, seq, type, source_id, message, status, data_json, progress_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.JobID, ev.Seq, string(ev.Type), ev.SourceID, ev.Message, string(ev.Status), string(data), nullBytes(prog), ts(ev.Timestamp))
	return err
}

func (s *Store) ListEvents(ctx context.Context, jobID string, afterSeq int64) ([]contracts.Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq, type, source_id, message, status, data_json, progress_json, created_at
		FROM run_events WHERE run_id = ? AND seq > ? ORDER BY seq`, jobID, afterSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contracts.Event{}
	for rows.Next() {
		ev := contracts.Event{JobID: jobID}
		var typ, status, created string
		var data, prog sql.NullString
		if err := rows.Scan(&ev.Seq, &typ, &ev.SourceID, &ev.Message, &status, &data, &prog, &created); err != nil {
			return nil, err
		}
		ev.Type, ev.Status, ev.Timestamp = contracts.EventType(typ), contracts.JobStatus(status), parseTS(created)
		if data.Valid && data.String != "null" {
			if err := json.Unmarshal([]byte(data.String), &ev.Data); err != nil {
				return nil, err
			}
		}
		if prog.Valid {
			ev.Progress = &contracts.Progress{}
			if err := json.Unmarshal([]byte(prog.String), ev.Progress); err != nil {
				return nil, err
			}
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *Store) AppendRecord(ctx context.Context, rec contracts.Record) error {
	values, err := json.Marshal(rec.Values)
	if err != nil {
		return err
	}
	prov, err := json.Marshal(rec.Provenance)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO run_records (run_id, source_id, record_key, data_json, provenance_json, page_url, partial, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.JobID, rec.SourceID, rec.Key, string(values), string(prov), rec.PageURL, boolInt(rec.Partial), ts(rec.SeenAt))
	return err
}

func (s *Store) ListRecords(ctx context.Context, jobID string) ([]contracts.Record, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source_id, record_key, data_json, provenance_json, page_url, partial, created_at
		FROM run_records WHERE run_id = ? ORDER BY id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contracts.Record{}
	for rows.Next() {
		rec := contracts.Record{JobID: jobID}
		var values, created string
		var prov sql.NullString
		var partial int
		if err := rows.Scan(&rec.SourceID, &rec.Key, &values, &prov, &rec.PageURL, &partial, &created); err != nil {
			return nil, err
		}
		rec.Partial, rec.SeenAt = partial != 0, parseTS(created)
		if err := json.Unmarshal([]byte(values), &rec.Values); err != nil {
			return nil, err
		}
		if prov.Valid && prov.String != "null" {
			if err := json.Unmarshal([]byte(prov.String), &rec.Provenance); err != nil {
				return nil, err
			}
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *Store) SaveCheckpoint(ctx context.Context, cp contracts.Checkpoint) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO run_checkpoints (run_id, state_json, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (run_id) DO UPDATE SET state_json = excluded.state_json, updated_at = excluded.updated_at`,
		cp.JobID, string(cp.State), ts(cp.UpdatedAt))
	return err
}

func (s *Store) GetCheckpoint(ctx context.Context, jobID string) (contracts.Checkpoint, error) {
	cp := contracts.Checkpoint{JobID: jobID}
	var state, updated string
	err := s.db.QueryRowContext(ctx, `SELECT state_json, updated_at FROM run_checkpoints WHERE run_id = ?`, jobID).Scan(&state, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return cp, storage.ErrNotFound
	}
	cp.State, cp.UpdatedAt = []byte(state), parseTS(updated)
	return cp, err
}

func (s *Store) SaveOutput(ctx context.Context, out contracts.Output) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO run_outputs (run_id, format, content_type, partial, record_count, content, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		out.JobID, out.Format, out.ContentType, boolInt(out.Partial), out.RecordCount, out.Data, ts(out.CreatedAt))
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return storage.ErrImmutable
	}
	return err
}

func (s *Store) GetOutput(ctx context.Context, jobID string) (contracts.Output, error) {
	out := contracts.Output{JobID: jobID}
	var partial int
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT format, content_type, partial, record_count, content, created_at FROM run_outputs WHERE run_id = ?`, jobID).
		Scan(&out.Format, &out.ContentType, &partial, &out.RecordCount, &out.Data, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return out, storage.ErrNotFound
	}
	out.Partial, out.CreatedAt = partial != 0, parseTS(created)
	return out, err
}

// ---------------------------------------------------------------------------
// Source profiles
// ---------------------------------------------------------------------------

func (s *Store) SaveProfile(ctx context.Context, p contracts.SourceProfile) error {
	if p.ID == "" {
		return errors.New("profile id is required")
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	now := ts(time.Now())
	_, err = s.db.ExecContext(ctx, `INSERT INTO source_profiles (id, profile_json, created_at, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET profile_json = excluded.profile_json, updated_at = excluded.updated_at`,
		p.ID, string(data), now, now)
	return err
}

func (s *Store) GetProfile(ctx context.Context, id string) (contracts.SourceProfile, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT profile_json FROM source_profiles WHERE id = ?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return contracts.SourceProfile{}, storage.ErrNotFound
	}
	if err != nil {
		return contracts.SourceProfile{}, err
	}
	return decodeProfile(data)
}

func (s *Store) ListProfiles(ctx context.Context) ([]contracts.SourceProfile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT profile_json FROM source_profiles ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contracts.SourceProfile{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		p, err := decodeProfile(data)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func decodeProfile(data string) (contracts.SourceProfile, error) {
	var p contracts.SourceProfile
	err := json.Unmarshal([]byte(data), &p)
	return p, err
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

type scanner interface {
	Scan(dest ...any) error
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func tsPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func parseTSPtr(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t := parseTS(s.String)
	return &t
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(i int) any {
	if i == 0 {
		return nil
	}
	return i
}

func nullBytes(b []byte) any {
	if b == nil {
		return nil
	}
	return string(b)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func rowsAffected(res sql.Result, err error) (int64, error) {
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func affected(res sql.Result, err error, none error) error {
	n, err := rowsAffected(res, err)
	if err != nil {
		return err
	}
	if n == 0 {
		return none
	}
	return nil
}
