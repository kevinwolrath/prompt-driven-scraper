package engine

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Site knowledge belongs in source profiles. Runtime code must not name
// specific websites.
func TestNoSiteSpecificRuntimeCode(t *testing.T) {
	for _, root := range []string{"../../apps", "../../internal", "../../pkg"} {
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() && (d.Name() == "node_modules" || d.Name() == "dist") {
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return err
			}
			ext := filepath.Ext(path)
			if (ext != ".go" && ext != ".ts" && ext != ".tsx") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, _ := os.ReadFile(path)
			for _, site := range []string{"carsales", "carsguide", "gumtree", "autotrader", "drive.com.au"} {
				if strings.Contains(strings.ToLower(string(data)), site) {
					t.Errorf("%s mentions %q: site-specific behaviour belongs in a source profile", path, site)
				}
			}
			return nil
		})
	}
}
