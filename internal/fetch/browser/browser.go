// Package browser is the "browser" acquisition strategy: it loads pages in
// a headless Chrome/Chromium over the DevTools protocol and returns the
// rendered DOM as HTML. It never extracts anything; the HTML goes through
// the same extraction rules as an HTTP response.
//
// One browser process is started lazily on first use and shared; each page
// load gets its own tab, closed when the load finishes.
package browser

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"github.com/kevinwolrath/prompt-driven-scraper/internal/fetch"
)

const DefaultTimeout = 45 * time.Second

type Options struct {
	// ExecPath is the Chrome/Chromium binary. Empty means auto-detect.
	ExecPath string
	// Headed shows the browser window (useful locally to watch a load).
	Headed bool
	// NoSandbox disables Chrome's sandbox; needed when running as root
	// (e.g. in containers).
	NoSandbox bool
}

// OptionsFromEnv reads PDS_BROWSER_PATH and PDS_BROWSER_HEADED.
func OptionsFromEnv() Options {
	return Options{
		ExecPath:  os.Getenv("PDS_BROWSER_PATH"),
		Headed:    os.Getenv("PDS_BROWSER_HEADED") == "1",
		NoSandbox: os.Geteuid() == 0,
	}
}

type Browser struct {
	opts Options

	mu            sync.Mutex
	browserCtx    context.Context
	cancelAlloc   context.CancelFunc
	cancelBrowser context.CancelFunc
}

var _ fetch.Browser = (*Browser)(nil)

func New(opts Options) *Browser { return &Browser{opts: opts} }

// ErrUnavailable is returned when no Chrome/Chromium can be found.
var ErrUnavailable = errors.New("browser acquisition unavailable: no Chrome or Chromium found " +
	"(install one, or set PDS_BROWSER_PATH to its executable)")

func (b *Browser) execPath() string {
	if b.opts.ExecPath != "" {
		return b.opts.ExecPath
	}
	for _, name := range []string{
		"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome",
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
	} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

func (b *Browser) start() (context.Context, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.browserCtx != nil && b.browserCtx.Err() == nil {
		return b.browserCtx, nil
	}
	path := b.execPath()
	if path == "" {
		return nil, ErrUnavailable
	}
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.ExecPath(path), chromedp.Flag("headless", !b.opts.Headed))
	if b.opts.NoSandbox {
		opts = append(opts, chromedp.NoSandbox)
	}
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx, chromedp.WithErrorf(quietErrorf))
	if err := chromedp.Run(browserCtx); err != nil { // launches the process
		cancelBrowser()
		cancelAlloc()
		return nil, fmt.Errorf("start browser %s: %w", path, err)
	}
	b.browserCtx, b.cancelAlloc, b.cancelBrowser = browserCtx, cancelAlloc, cancelBrowser
	return browserCtx, nil
}

// Close stops the browser process, if one was started.
func (b *Browser) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cancelBrowser != nil {
		b.cancelBrowser()
		b.cancelAlloc()
		b.browserCtx, b.cancelBrowser, b.cancelAlloc = nil, nil, nil
	}
	return nil
}

// quietErrorf drops chromedp's reports of protocol events it cannot decode,
// which happen whenever Chrome is newer than the bundled protocol types and
// do not affect page loads.
func quietErrorf(format string, args ...any) {
	if msg := fmt.Sprintf(format, args...); !strings.Contains(msg, "could not unmarshal event") {
		log.Print("browser: " + msg)
	}
}

// TimeoutError says which step of a browser load ran out of time.
type TimeoutError struct {
	Timeout time.Duration
	Step    string
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("browser timed out after %s while %s", e.Timeout, e.Step)
}

func (e *TimeoutError) Unwrap() error { return context.DeadlineExceeded }

// Load navigates to req.URL, waits as configured and returns the DOM.
// Main-document HTTP errors are returned as *fetch.StatusError and challenge
// pages as *fetch.BlockedError, so the engine can report them precisely.
func (b *Browser) Load(ctx context.Context, req fetch.BrowserRequest) (fetch.Response, error) {
	browserCtx, err := b.start()
	if err != nil {
		return fetch.Response{}, err
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	tabCtx, closeTab := chromedp.NewContext(browserCtx)
	defer closeTab()
	// Open the tab with its own context: chromedp ties a tab's lifetime to
	// the context of its first Run, and the page must outlive the timeout
	// below so it can still be inspected when a wait runs out.
	if err := chromedp.Run(tabCtx); err != nil {
		return fetch.Response{}, fmt.Errorf("open browser tab: %w", err)
	}
	runCtx, cancel := context.WithTimeout(tabCtx, timeout)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel) // the run was cancelled or paused hard
	defer stop()

	var mu sync.Mutex
	status := 0
	chromedp.ListenTarget(runCtx, func(ev any) {
		if r, ok := ev.(*network.EventResponseReceived); ok && r.Type == network.ResourceTypeDocument {
			mu.Lock()
			if status == 0 {
				status = int(r.Response.Status)
			}
			mu.Unlock()
		}
	})

	step := "starting"
	at := func(s string) chromedp.Action {
		return chromedp.ActionFunc(func(context.Context) error { step = s; return nil })
	}
	actions := []chromedp.Action{network.Enable(), at("navigating to " + req.URL)}
	switch req.WaitUntil {
	case "domcontentloaded":
		actions = append(actions,
			chromedp.ActionFunc(func(ctx context.Context) error {
				_, _, errText, err := page.Navigate(req.URL).Do(ctx)
				if err == nil && errText != "" {
					err = errors.New(errText)
				}
				return err
			}),
			at("waiting for DOMContentLoaded"),
			chromedp.Poll(`document.readyState !== "loading"`, nil, chromedp.WithPollingInterval(100*time.Millisecond)),
		)
	default:
		actions = append(actions, chromedp.Navigate(req.URL))
	}
	if req.WaitFor != "" {
		actions = append(actions, at(fmt.Sprintf("waiting for %q", req.WaitFor)), chromedp.WaitReady(req.WaitFor, chromedp.ByQuery))
	}
	var html, title, location string
	actions = append(actions, at("reading the page"),
		chromedp.Location(&location), chromedp.Title(&title), chromedp.OuterHTML("html", &html, chromedp.ByQuery))

	err = chromedp.Run(runCtx, actions...)
	mu.Lock()
	code := status
	mu.Unlock()
	if err != nil {
		if ctx.Err() != nil {
			return fetch.Response{}, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) || runCtx.Err() == context.DeadlineExceeded {
			te := &TimeoutError{Timeout: timeout, Step: step}
			// Say what the page was when time ran out: a challenge or an
			// HTTP error is more useful than the timeout alone.
			readCtx, cancelRead := context.WithTimeout(tabCtx, 5*time.Second)
			var html string
			_ = chromedp.Run(readCtx, chromedp.OuterHTML("html", &html, chromedp.ByQuery))
			cancelRead()
			if m := fetch.DetectBlock([]byte(html)); m != "" {
				return fetch.Response{}, fmt.Errorf("%w: %w", &fetch.BlockedError{URL: req.URL, Status: code, Marker: m}, te)
			}
			if code >= 400 {
				return fetch.Response{}, fmt.Errorf("%w: %w", &fetch.StatusError{URL: req.URL, Code: code}, te)
			}
			return fetch.Response{}, te
		}
		// Chrome fails a navigation outright when the page is an HTTP error
		// with no body (net::ERR_HTTP_RESPONSE_CODE_FAILURE). Report the
		// status the server sent, which is what the failure actually was.
		if code >= 400 {
			return fetch.Response{}, &fetch.StatusError{URL: req.URL, Code: code}
		}
		return fetch.Response{}, fmt.Errorf("browser %s: %w", step, err)
	}
	resp := fetch.Response{URL: location, StatusCode: code, Body: []byte(html), Title: title}
	if resp.URL == "" {
		resp.URL = req.URL
	}
	if m := fetch.DetectBlock(resp.Body); m != "" {
		return resp, &fetch.BlockedError{URL: req.URL, Status: code, Marker: m}
	}
	if code >= 400 {
		return resp, &fetch.StatusError{URL: req.URL, Code: code}
	}
	return resp, nil
}
