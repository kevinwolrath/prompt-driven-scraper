// Package schemas publishes the JSON Schemas for jobs and source profiles.
package schemas

import (
	"embed"
	"io/fs"
	"sort"
)

//go:embed *.schema.json
var files embed.FS

// File is one embedded schema.
type File struct {
	Name    string
	Content string
}

// Files returns the schemas sorted by name.
func Files() []File {
	names, _ := fs.Glob(files, "*.schema.json")
	sort.Strings(names)
	out := make([]File, 0, len(names))
	for _, n := range names {
		data, _ := fs.ReadFile(files, n)
		out = append(out, File{Name: n, Content: string(data)})
	}
	return out
}
