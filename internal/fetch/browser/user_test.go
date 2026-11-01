package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/fetch"
)

func testUserChrome(t *testing.T) string {
	t.Helper()
	path := New(Options{}).execPath()
	if path == "" {
		t.Skip("Chrome not installed")
	}
	dir := t.TempDir()
	cmd := exec.Command(path, "--headless=new", "--remote-debugging-port=0", "--user-data-dir="+dir, "--no-first-run", "--no-default-browser-check", "about:blank")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(filepath.Join(dir, "DevToolsActivePort"))
		parts := strings.Fields(string(data))
		if len(parts) == 2 {
			return "http://127.0.0.1:" + parts[0]
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("Chrome debugger did not start")
	return ""
}

func TestUserBrowserContinuesSameTabAcrossReconnect(t *testing.T) {
	endpoint := testUserChrome(t)
	var loaded atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/blocked" {
			w.WriteHeader(403)
			fmt.Fprint(w, `<title>Access denied</title><p>Test-only interaction fixture</p>`)
			return
		}
		if r.URL.Path != "/cars" {
			http.NotFound(w, r)
			return
		}
		loaded.Add(1)
		fmt.Fprint(w, `<title>Public listings</title><article class="car">real DOM fixture</article>`)
	}))
	defer srv.Close()
	b := NewUser(UserOptions{Endpoint: endpoint})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	resp, err := b.Load(ctx, fetch.BrowserRequest{URL: srv.URL + "/blocked", Key: "run", WaitFor: "article.car", Timeout: 5 * time.Second})
	var interaction *fetch.InteractionRequired
	if !errors.As(err, &interaction) || resp.StatusCode != 403 || resp.Title != "Access denied" || interaction.TabID == "" {
		t.Fatalf("expected handoff with evidence: %+v %v", resp, err)
	}
	// On a local test fixture only, perform the action that a human would take.
	// Never used against a real site's challenge.
	if err := chromedp.Run(b.tabs["run"].ctx, chromedp.Evaluate(`localStorage.setItem('persistent-state','retained')`, nil), chromedp.Navigate(srv.URL+"/cars")); err != nil {
		t.Fatal(err)
	}
	again := NewUser(UserOptions{Endpoint: endpoint})
	resp, err = again.Load(ctx, fetch.BrowserRequest{URL: srv.URL + "/blocked", Key: "run", Continue: true, TabID: interaction.TabID, WaitFor: "article.car", Timeout: 5 * time.Second})
	if err != nil || resp.TabID != interaction.TabID || !strings.Contains(string(resp.Body), "real DOM fixture") || loaded.Load() != 1 {
		t.Fatalf("Continue navigated or lost tab: loaded=%d response=%+v err=%v", loaded.Load(), resp, err)
	}
	// New page, same persistent browser storage; no disposable identity.
	resp, err = again.Load(ctx, fetch.BrowserRequest{URL: srv.URL + "/blocked", Key: "other", Timeout: 5 * time.Second})
	if !errors.As(err, &interaction) {
		t.Fatal(err)
	}
	var value string
	if err = chromedp.Run(again.tabs["other"].ctx, chromedp.Evaluate(`localStorage.getItem('persistent-state')`, &value)); err != nil || value != "retained" {
		t.Fatalf("persistent storage: %q %v", value, err)
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "unrelated private content") }))
	defer other.Close()
	if err := chromedp.Run(again.tabs["other"].ctx, chromedp.Navigate(other.URL)); err != nil {
		t.Fatal(err)
	}
	foreign, foreignErr := again.Load(ctx, fetch.BrowserRequest{URL: srv.URL + "/blocked", Key: "other", Continue: true, TabID: interaction.TabID, Timeout: 5 * time.Second})
	if !errors.As(foreignErr, &interaction) || len(foreign.Body) != 0 {
		t.Fatalf("read unrelated origin: body=%q err=%v", foreign.Body, foreignErr)
	}
	b.tabs["run"].cancel()
	again.tabs["other"].cancel()
}

func TestUserBrowserRejectsRemoteOrCredentialEndpoints(t *testing.T) {
	for _, raw := range []string{"https://example.com", "http://192.168.1.2:9222", "http://user:password@127.0.0.1:9222", "ws://example.com/devtools/browser/x"} {
		if localEndpoint(raw) == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
func TestUserBrowserCanceledWhileQueued(t *testing.T) {
	b := NewUser(UserOptions{})
	b.gate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := b.Load(ctx, fetch.BrowserRequest{URL: "https://public.example/"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("queued cancellation: %v", err)
	}
}
