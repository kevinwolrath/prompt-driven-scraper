package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/kevinwolrath/prompt-driven-scraper/internal/fetch"
)

// UserOptions are local operator settings, never downloaded profile settings.
// Endpoint attaches only to a deliberately enabled loopback CDP endpoint.
type UserOptions struct{ ExecPath, ProfileDir, Endpoint string }

func UserOptionsFromEnv() UserOptions {
	return UserOptions{ExecPath: os.Getenv("PDS_BROWSER_PATH"), ProfileDir: envDefault("PDS_USER_BROWSER_PROFILE", "data/user-browser"), Endpoint: os.Getenv("PDS_USER_BROWSER_URL")}
}
func envDefault(key, fallback string) string {
	if s := os.Getenv(key); s != "" {
		return s
	}
	return fallback
}

type userTab struct {
	ctx    context.Context
	cancel context.CancelFunc
	status int
	frame  string
	mu     sync.Mutex
}
type UserBrowser struct {
	opts UserOptions
	gate chan struct{}
	tabs map[string]*userTab
}

func NewUser(opts UserOptions) *UserBrowser {
	return &UserBrowser{opts: opts, tabs: map[string]*userTab{}, gate: make(chan struct{}, 1)}
}

// Close leaves visible Chrome and interaction tabs with the user. Connections
// end when the hosting process exits. Cancelling remote chromedp contexts here
// would close tabs that are checkpointed for Continue after an API restart.
func (b *UserBrowser) Close() error { return nil }

func localEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("invalid user browser endpoint")
	}
	if u.Scheme != "http" && u.Scheme != "ws" {
		return errors.New("user browser endpoint must use http or ws on loopback")
	}
	ip := net.ParseIP(u.Hostname())
	if u.User != nil || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
		return errors.New("user browser endpoint must be loopback without credentials")
	}
	return nil
}
func debuggerURL(ctx context.Context, endpoint string) (string, error) {
	if err := localEndpoint(endpoint); err != nil {
		return "", err
	}
	if strings.HasPrefix(endpoint, "ws://") {
		return endpoint, nil
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(endpoint, "/")+"/json/version", nil)
	client := &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("cannot connect to user browser debugging endpoint")
	}
	defer resp.Body.Close()
	var v struct {
		URL string `json:"webSocketDebuggerUrl"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&v) != nil || v.URL == "" {
		return "", errors.New("user browser did not expose a debugging websocket")
	}
	if err := localEndpoint(v.URL); err != nil {
		return "", err
	}
	return v.URL, nil
}

func (b *UserBrowser) connect(ctx context.Context) (string, error) {
	if b.opts.Endpoint != "" {
		return debuggerURL(ctx, b.opts.Endpoint)
	}
	dir, err := filepath.Abs(b.opts.ProfileDir)
	if err != nil {
		return "", err
	}
	if b.opts.ProfileDir == "" {
		return "", errors.New("user browser profile directory is required")
	}
	// Never open an everyday Chrome profile with remote debugging.
	for _, root := range []string{filepath.Join(os.Getenv("LOCALAPPDATA"), "Google", "Chrome", "User Data"), filepath.Join(os.Getenv("HOME"), ".config", "google-chrome")} {
		rel, e := filepath.Rel(root, dir)
		if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return "", errors.New("use a dedicated user browser profile, not Chrome's everyday User Data directory")
		}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	active := filepath.Join(dir, "DevToolsActivePort")
	readEndpoint := func() string {
		data, e := os.ReadFile(active)
		if e != nil {
			return ""
		}
		lines := strings.Fields(string(data))
		if len(lines) < 2 {
			return ""
		}
		port, e := strconv.Atoi(lines[0])
		if e != nil || port < 1 || port > 65535 || !strings.HasPrefix(lines[1], "/devtools/browser/") {
			return ""
		}
		return "ws://127.0.0.1:" + lines[0] + lines[1]
	}
	// Reuse the persistent browser after restarting the API, if still running.
	if ep := readEndpoint(); ep != "" {
		httpEP := "http://" + strings.Split(strings.TrimPrefix(ep, "ws://"), "/")[0]
		if ws, e := debuggerURL(ctx, httpEP); e == nil && ws == ep {
			return ws, nil
		}
	}
	path := New(Options{ExecPath: b.opts.ExecPath}).execPath()
	if path == "" {
		return "", ErrUnavailable
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	cmd := exec.Command(path, "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0", "--user-data-dir="+dir, "--no-first-run", "--no-default-browser-check", "--new-window", "about:blank")
	// Ordinary visible Chrome: no automation identity modifications or stealth.
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start user browser: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("waiting for user browser debugger: %w", ctx.Err())
		case <-ticker.C:
			if ep := readEndpoint(); ep != "" {
				httpEP := "http://" + strings.Split(strings.TrimPrefix(ep, "ws://"), "/")[0]
				if ws, e := debuggerURL(ctx, httpEP); e == nil && ws == ep {
					return ws, nil
				}
			}
		}
	}
}

// Load opens only scraper-owned tabs. Continue reattaches the checkpoint's tab
// and reads it without navigating, reloading, clicking or solving a challenge.
func (b *UserBrowser) Load(ctx context.Context, req fetch.BrowserRequest) (fetch.Response, error) {
	select {
	case b.gate <- struct{}{}:
		defer func() { <-b.gate }()
	case <-ctx.Done():
		return fetch.Response{}, ctx.Err()
	}
	if ctx.Err() != nil {
		return fetch.Response{}, ctx.Err()
	}
	if req.Continue && req.TabID == "" {
		return fetch.Response{}, errors.New("Continue requires the checkpointed Chrome tab ID")
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	setup, cancelSetup := context.WithTimeout(ctx, timeout)
	defer cancelSetup()
	ep, err := b.connect(setup)
	if err != nil {
		return fetch.Response{}, err
	}
	tab := b.tabs[req.Key]
	if tab == nil || tab.ctx.Err() != nil {
		alloc, cancelAlloc := chromedp.NewRemoteAllocator(context.Background(), ep, chromedp.NoModifyURL)
		opts := []chromedp.ContextOption{chromedp.WithErrorf(quietErrorf)}
		if req.Continue && req.TabID != "" {
			opts = append(opts, chromedp.WithTargetID(target.ID(req.TabID)))
		}
		tc, cc := chromedp.NewContext(alloc, opts...)
		tab = &userTab{ctx: tc, cancel: func() { cc(); cancelAlloc() }}
		b.tabs[req.Key] = tab
		chromedp.ListenTarget(tc, func(ev any) {
			tab.mu.Lock()
			defer tab.mu.Unlock()
			switch e := ev.(type) {
			case *page.EventFrameNavigated:
				if e.Frame.ParentID == "" {
					tab.frame = string(e.Frame.ID)
				}
			case *network.EventResponseReceived:
				if e.Type == network.ResourceTypeDocument && (tab.frame == "" || string(e.FrameID) == tab.frame) {
					tab.status = int(e.Response.Status)
				}
			}
		})
	}
	// First Run must use the tab's lifetime context, not the per-load timeout.
	// Bound attach/creation with cancellation, keeping a successfully opened tab.
	attached := make(chan error, 1)
	go func() { attached <- chromedp.Run(tab.ctx) }()
	select {
	case err = <-attached:
	case <-setup.Done():
		tab.cancel()
		delete(b.tabs, req.Key)
		return fetch.Response{}, setup.Err()
	}
	if err != nil {
		tab.cancel()
		delete(b.tabs, req.Key)
		return fetch.Response{}, fmt.Errorf("attach user browser tab (it may have been closed): %w", err)
	}
	runCtx, cancel := context.WithTimeout(tab.ctx, timeout)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	if !req.Continue {
		tab.mu.Lock()
		tab.status = 0
		tab.frame = ""
		tab.mu.Unlock()
		if req.WaitUntil == "domcontentloaded" {
			err = chromedp.Run(runCtx, network.Enable(), chromedp.ActionFunc(func(ctx context.Context) error {
				_, _, errText, err := page.Navigate(req.URL).Do(ctx)
				if err != nil {
					return err
				}
				if errText != "" {
					return errors.New(errText)
				}
				return nil
			}), chromedp.Poll(`document.readyState !== "loading"`, nil))
		} else {
			err = chromedp.Run(runCtx, network.Enable(), chromedp.Navigate(req.URL))
		}
	}
	var html, title, location string
	// An error status can still have an inspectable page. Read on the live tab.
	readCtx, cancelRead := context.WithTimeout(tab.ctx, 5*time.Second)
	stopRead := context.AfterFunc(ctx, cancelRead)
	readErr := chromedp.Run(readCtx, chromedp.Location(&location), chromedp.ActionFunc(func(context.Context) error {
		wanted, _ := url.Parse(req.URL)
		got, _ := url.Parse(location)
		if got != nil && (got.Scheme == "http" || got.Scheme == "https") && (wanted == nil || !strings.EqualFold(got.Host, wanted.Host) || got.Scheme != wanted.Scheme) {
			return &fetch.InteractionRequired{TabID: string(chromedp.FromContext(tab.ctx).Target.TargetID), Reason: "Chrome is on a different origin; return to the requested source before continuing"}
		}
		return nil
	}), chromedp.Title(&title), chromedp.OuterHTML("html", &html, chromedp.ByQuery))
	stopRead()
	cancelRead()
	tab.mu.Lock()
	status := tab.status
	tab.mu.Unlock()
	id := string(chromedp.FromContext(tab.ctx).Target.TargetID)
	resp := fetch.Response{URL: location, Title: title, Body: []byte(html), StatusCode: status, TabID: id}
	if ctx.Err() != nil {
		return resp, ctx.Err()
	}
	if readErr != nil {
		var interaction *fetch.InteractionRequired
		if errors.As(readErr, &interaction) {
			return resp, interaction
		}
		return resp, fmt.Errorf("read user browser tab: %w", readErr)
	}
	reason := fetch.DetectBlock(resp.Body)
	if reason == "" && status >= 400 {
		reason = fmt.Sprintf("HTTP %d", status)
	}
	if reason == "" && strings.HasPrefix(location, "chrome-error:") {
		reason = "browser navigation error"
	}
	if reason != "" {
		return resp, &fetch.InteractionRequired{TabID: id, Reason: reason}
	}
	if err != nil {
		return resp, fmt.Errorf("user browser navigation: %w", err)
	}
	if req.WaitFor != "" {
		err = chromedp.Run(runCtx, chromedp.WaitReady(req.WaitFor, chromedp.ByQuery))
		if err != nil {
			if ctx.Err() != nil {
				return resp, ctx.Err()
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return resp, &fetch.InteractionRequired{TabID: id, Reason: fmt.Sprintf("expected page state %q did not appear; inspect Chrome or correct the profile selector", req.WaitFor)}
			}
			return resp, fmt.Errorf("user browser waiting for %q: %w", req.WaitFor, err)
		}
		err = chromedp.Run(runCtx, chromedp.OuterHTML("html", &html, chromedp.ByQuery))
		resp.Body = []byte(html)
		if err != nil {
			return resp, err
		}
	}
	// Restrict extraction after manual navigation to the requested origin.
	wanted, _ := url.Parse(req.URL)
	got, _ := url.Parse(resp.URL)
	if got == nil || wanted == nil || !strings.EqualFold(got.Host, wanted.Host) || got.Scheme != wanted.Scheme {
		return resp, &fetch.InteractionRequired{TabID: id, Reason: "Chrome is on a different origin; return to the requested source before continuing"}
	}
	tab.cancel()
	delete(b.tabs, req.Key)
	return resp, nil
}
