package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/fetch"
)

// These tests drive a real headless Chrome/Chromium and are skipped when
// none is installed.
func newBrowser(t *testing.T) *Browser {
	t.Helper()
	b := New(OptionsFromEnv())
	if b.execPath() == "" {
		t.Skip("no Chrome/Chromium available")
	}
	t.Cleanup(func() { b.Close() })
	return b
}

func site() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/js", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><title>JS listings</title></head><body><main id="list"></main>
<script>setTimeout(() => { document.getElementById("list").innerHTML =
  '<article class="card"><span class="name">Rendered by JavaScript</span></article>'; }, 300);</script></body></html>`)
	})
	mux.HandleFunc("/forbidden", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `<html><head><title>Forbidden</title></head><body>No.</body></html>`)
	})
	mux.HandleFunc("/challenge", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><title>Just a moment...</title></head><body><div id="cf-chl-widget"></div></body></html>`)
	})
	return httptest.NewServer(mux)
}

func TestLoadReturnsRenderedDOM(t *testing.T) {
	b := newBrowser(t)
	srv := site()
	defer srv.Close()
	for _, waitUntil := range []string{"load", "domcontentloaded"} {
		resp, err := b.Load(context.Background(), fetch.BrowserRequest{URL: srv.URL + "/js", WaitUntil: waitUntil, WaitFor: "article.card", Timeout: 20 * time.Second})
		if err != nil {
			t.Fatalf("%s: %v", waitUntil, err)
		}
		if !strings.Contains(string(resp.Body), "Rendered by JavaScript") || resp.Title != "JS listings" || resp.StatusCode != 200 {
			t.Fatalf("%s: status %d title %q body %s", waitUntil, resp.StatusCode, resp.Title, resp.Body)
		}
	}
}

func TestLoadReportsHTTPErrorsAndChallenges(t *testing.T) {
	b := newBrowser(t)
	srv := site()
	defer srv.Close()
	_, err := b.Load(context.Background(), fetch.BrowserRequest{URL: srv.URL + "/forbidden", Timeout: 20 * time.Second})
	var se *fetch.StatusError
	if !errors.As(err, &se) || se.Code != 403 {
		t.Fatalf("forbidden: %v", err)
	}
	_, err = b.Load(context.Background(), fetch.BrowserRequest{URL: srv.URL + "/challenge", Timeout: 20 * time.Second})
	var be *fetch.BlockedError
	if !errors.As(err, &be) {
		t.Fatalf("challenge: %v", err)
	}
}

func TestLoadTimeoutNamesTheStep(t *testing.T) {
	b := newBrowser(t)
	srv := site()
	defer srv.Close()
	start := time.Now()
	_, err := b.Load(context.Background(), fetch.BrowserRequest{URL: srv.URL + "/js", WaitFor: ".never-appears", Timeout: 1500 * time.Millisecond})
	var te *TimeoutError
	if !errors.As(err, &te) || !strings.Contains(err.Error(), `waiting for ".never-appears"`) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("timeout took %s", time.Since(start))
	}
	// The shared browser is still usable after a timed-out tab.
	if _, err := b.Load(context.Background(), fetch.BrowserRequest{URL: srv.URL + "/js", WaitFor: "article.card", Timeout: 20 * time.Second}); err != nil {
		t.Fatalf("after timeout: %v", err)
	}
}

func TestUnavailableBrowser(t *testing.T) {
	b := New(Options{ExecPath: ""})
	b.opts.ExecPath = "/nonexistent/chrome"
	if _, err := b.Load(context.Background(), fetch.BrowserRequest{URL: "http://127.0.0.1:1/"}); err == nil {
		t.Fatal("expected an error without a browser")
	}
}
