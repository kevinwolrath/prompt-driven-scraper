// Package examples embeds the example job and source profiles so they can be
// shown to AI assistants as a worked example.
package examples

import (
	"embed"
	"io/fs"
	"sort"
)

//go:embed *.job.yaml sources/*.yaml
var files embed.FS

// File is one embedded example.
type File struct {
	Name    string
	Content string
}

// Files returns the example job(s) first, then source profiles, each sorted by name.
func Files() []File {
	var out []File
	for _, pattern := range []string{"*.job.yaml", "sources/*.yaml"} {
		names, _ := fs.Glob(files, pattern)
		sort.Strings(names)
		for _, n := range names {
			data, _ := fs.ReadFile(files, n)
			out = append(out, File{Name: n, Content: string(data)})
		}
	}
	return out
}
