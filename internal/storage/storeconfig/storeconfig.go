// Package storeconfig selects a storage implementation at the application
// composition boundary. The engine only ever sees storage.Store.
package storeconfig

import (
	"errors"
	"fmt"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/storage/sqlite"
)

const (
	Memory = "memory"
	SQLite = "sqlite"
	D1     = "d1"

	DefaultKind       = SQLite
	DefaultSQLitePath = "data/prompt-driven-scraper.db"
)

// ErrD1Unavailable is returned for kind "d1": the shared schema and
// migrations exist, but the Go runtime has no D1 adapter yet (see README).
var ErrD1Unavailable = errors.New(`storage "d1" is not available to the Go runtime yet: ` +
	`D1 is the hosted Cloudflare persistence plane (schema in /migrations, setup in deploy/cloudflare); ` +
	`use "sqlite" or "memory"`)

type Config struct {
	Kind       string // memory | sqlite | d1
	SQLitePath string
}

// Open returns the configured store and a function that closes it. Unknown
// or unusable configurations are errors: there is no silent fallback.
func Open(cfg Config) (storage.Store, func() error, error) {
	switch cfg.Kind {
	case Memory:
		return storage.NewMemoryStore(), func() error { return nil }, nil
	case SQLite:
		path := cfg.SQLitePath
		if path == "" {
			path = DefaultSQLitePath
		}
		s, err := sqlite.Open(path)
		if err != nil {
			return nil, nil, err
		}
		return s, s.Close, nil
	case D1:
		return nil, nil, ErrD1Unavailable
	case "":
		return nil, nil, errors.New("storage kind is empty (use memory, sqlite or d1)")
	}
	return nil, nil, fmt.Errorf("unknown storage kind %q (use memory, sqlite or d1)", cfg.Kind)
}
