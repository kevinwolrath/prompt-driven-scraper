// Package migrations holds the versioned SQL schema shared by the SQLite
// store and Cloudflare D1. Files are named NNNN_description.sql, the layout
// `wrangler d1 migrations apply` expects, and are applied in order.
// Never edit a released migration; add a new one.
package migrations

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed *.sql
var files embed.FS

type Migration struct {
	Version int
	Name    string
	SQL     string
}

// All returns the migrations in version order.
func All() ([]Migration, error) {
	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	out := make([]Migration, 0, len(names))
	for i, n := range names {
		num, _, ok := strings.Cut(n, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil {
			return nil, fmt.Errorf("migration %s: name must start with a number and underscore", n)
		}
		if v != i+1 {
			return nil, fmt.Errorf("migration %s: expected version %d", n, i+1)
		}
		data, err := fs.ReadFile(files, n)
		if err != nil {
			return nil, err
		}
		out = append(out, Migration{Version: v, Name: n, SQL: string(data)})
	}
	return out, nil
}
