package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage/storagetest"
	"github.com/kevinwolrath/prompt-driven-scraper/migrations"
	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

func open(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSQLiteStoreContract(t *testing.T) {
	storagetest.Run(t, func(t *testing.T) storage.Store {
		return open(t, filepath.Join(t.TempDir(), "test.db"))
	})
}

func TestSurvivesReopenAndMigratesOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "dir", "pds.db") // parent dirs are created
	now := time.Now().UTC()

	s := open(t, path)
	if err := s.CreateSession(ctx, contracts.Session{ID: "s1", Name: "Keep me", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateJob(ctx, contracts.Job{ID: "r1", SessionID: "s1", RunNumber: 1, Status: contracts.JobCompleted, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendRecord(ctx, contracts.Record{JobID: "r1", SourceID: "x", Key: "k", Values: map[string]any{"a": "b"}, SeenAt: now}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Reopening (normal startup) must neither recreate nor re-migrate.
	s2 := open(t, path)
	all, _ := migrations.All()
	if v, err := s2.SchemaVersion(ctx); err != nil || v != len(all) {
		t.Fatalf("schema version %d (%v), want %d", v, err, len(all))
	}
	sess, err := s2.GetSession(ctx, "s1")
	if err != nil || sess.Name != "Keep me" {
		t.Fatalf("session lost: %+v %v", sess, err)
	}
	runs, _ := s2.ListSessionJobs(ctx, "s1")
	recs, _ := s2.ListRecords(ctx, "r1")
	if len(runs) != 1 || len(recs) != 1 {
		t.Fatalf("runs %d records %d after reopen", len(runs), len(recs))
	}
	var applied int
	if err := s2.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil || applied != len(all) {
		t.Fatalf("applied migrations %d (%v)", applied, err)
	}
}

func TestRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pds.db")
	s := open(t, path)
	if _, err := s.db.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (999, 'future.sql', 'x')`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(path); err == nil {
		t.Fatal("opened a database with a newer schema")
	}
}

func TestRunsRequireExistingSession(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "pds.db"))
	err := s.CreateJob(context.Background(), contracts.Job{ID: "r1", SessionID: "nope", RunNumber: 1, Status: contracts.JobQueued})
	if err == nil {
		t.Fatal("run created for a missing session")
	}
}
