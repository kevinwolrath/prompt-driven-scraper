package diagnostics

import (
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
)

var sensitiveKey = regexp.MustCompile(`(?i)authorization|cookie|token|secret|password|credential|api[-_]?key|signature|sessionid`)
var reportURL = regexp.MustCompile(`https?://[^\s"<>]+`)

// Inspect a detached copy, never mutate the immutable stored snapshot. Header values
// are always private; named credentials and URL credentials are also removed.
func sanitise(job contracts.Job, events []contracts.Event) (contracts.Job, []contracts.Event, func(string) string) {
	var secrets []string
	var collect func(any, bool)
	collect = func(v any, private bool) {
		switch x := v.(type) {
		case string:
			if private && x != "" {
				secrets = append(secrets, x)
			}
		case map[string]any:
			for k, val := range x {
				collect(val, private || strings.EqualFold(k, "headers") || sensitiveKey.MatchString(k) && !(k == "sessionId" && x["definition"] != nil))
			}
		case []any:
			for _, val := range x {
				collect(val, private)
			}
		}
	}
	b, _ := json.Marshal(struct {
		Job    contracts.Job
		Events []contracts.Event
	}{job, events})
	var raw any
	_ = json.Unmarshal(b, &raw)
	collect(raw, false)
	// Longest first so a credential that is a substring of another cannot
	// prevent the complete credential from being redacted.
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	scrub := func(s string) string {
		for _, secret := range secrets {
			s = strings.ReplaceAll(s, secret, "[REDACTED]")
		}
		return reportURL.ReplaceAllStringFunc(s, func(raw string) string {
			u, err := url.Parse(raw)
			if err != nil {
				return raw
			}
			if u.User != nil {
				u.User = url.User("REDACTED")
			}
			q := u.Query()
			changed := false
			for k := range q {
				if sensitiveKey.MatchString(k) {
					q.Set(k, "REDACTED")
					changed = true
				}
			}
			if changed {
				u.RawQuery = q.Encode()
			}
			return u.String()
		})
	}
	return job, events, scrub
}
