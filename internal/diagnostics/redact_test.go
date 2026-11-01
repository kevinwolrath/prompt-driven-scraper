package diagnostics

import (
	"github.com/kevinwolrath/prompt-driven-scraper/pkg/contracts"
	"strings"
	"testing"
)

func TestReportRedactsCredentialsAndKeepsAttemptEvidence(t *testing.T) {
	p := contracts.SourceProfile{ID: "public", Request: contracts.RequestSpec{URL: "https://example.test/?api_key=url-secret", Headers: map[string]string{"Authorization": "Bearer header-secret", "Cookie": "cookie-secret", "X-Custom": "custom-secret"}}}
	j := contracts.Job{Definition: contracts.JobDefinition{Name: "safe", SearchSources: []contracts.SourceRef{{ID: "public", Profile: &p}}}}
	events := []contracts.Event{{Type: contracts.EventAcquisitionAttempt, Message: "failed Bearer header-secret", Data: map[string]any{"strategy": "browser", "url": "https://example.test/?token=event-secret", "status": 403, "durationMs": 125, "title": "Access denied"}}}
	r := Report(j, events)
	for _, secret := range []string{"url-secret", "header-secret", "cookie-secret", "custom-secret", "event-secret"} {
		if strings.Contains(r, secret) {
			t.Errorf("leaked %s", secret)
		}
	}
	for _, want := range []string{`"status":403`, `"durationMs":125`, `"title":"Access denied"`, "CONFIGURATION"} {
		if !strings.Contains(r, want) {
			t.Errorf("missing %s", want)
		}
	}
	if p.Request.Headers["Cookie"] != "cookie-secret" {
		t.Fatal("mutated snapshot")
	}
}
func TestReportKeepsInternalSessionIDButRedactsQueryCredentials(t *testing.T) {
	j := contracts.Job{SessionID: "ses-public-id", Definition: contracts.JobDefinition{Name: "query", Query: contracts.Query{Params: map[string]any{"sessionId": "private-session-token", "apiKey": map[string]any{"value": "nested-secret"}}}}}
	r := Report(j, nil)
	if !strings.Contains(r, "ses-public-id") {
		t.Fatal("lost internal session id")
	}
	for _, secret := range []string{"private-session-token", "nested-secret"} {
		if strings.Contains(r, secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
}
