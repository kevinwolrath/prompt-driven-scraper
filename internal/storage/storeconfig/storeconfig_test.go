package storeconfig

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage/sqlite"
)

func TestOpen(t *testing.T) {
	s, closeFn, err := Open(Config{Kind: Memory})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(*storage.MemoryStore); !ok {
		t.Fatalf("memory: got %T", s)
	}
	closeFn()

	path := filepath.Join(t.TempDir(), "data", "x.db")
	s, closeFn, err = Open(Config{Kind: SQLite, SQLitePath: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(*sqlite.Store); !ok {
		t.Fatalf("sqlite: got %T", s)
	}
	closeFn()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("sqlite file not created: %v", err)
	}
}

func TestInvalidConfigurationFailsLoudly(t *testing.T) {
	if _, _, err := Open(Config{Kind: D1}); !errors.Is(err, ErrD1Unavailable) {
		t.Fatalf("d1: %v", err)
	}
	for _, kind := range []string{"", "mongo", "SQLITE"} {
		if s, _, err := Open(Config{Kind: kind}); err == nil || s != nil {
			t.Fatalf("kind %q: store %T, err %v", kind, s, err)
		}
	}
	// An unusable SQLite path is an error, not a fallback to memory.
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, []byte("x"), 0o644)
	if s, _, err := Open(Config{Kind: SQLite, SQLitePath: filepath.Join(file, "pds.db")}); err == nil || s != nil {
		t.Fatalf("bad sqlite path: store %T, err %v", s, err)
	}
}
