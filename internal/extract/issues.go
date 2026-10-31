package extract

import (
	"fmt"

	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

// Issues collects validation problems with their configuration paths.
type Issues []contracts.ValidationIssue

func (is *Issues) Errorf(path, format string, args ...any) {
	*is = append(*is, contracts.ValidationIssue{Path: path, Message: fmt.Sprintf(format, args...), Severity: "error"})
}

func (is *Issues) Warnf(path, format string, args ...any) {
	*is = append(*is, contracts.ValidationIssue{Path: path, Message: fmt.Sprintf(format, args...), Severity: "warning"})
}

func (is Issues) HasErrors() bool {
	for _, i := range is {
		if i.Severity == "error" {
			return true
		}
	}
	return false
}
