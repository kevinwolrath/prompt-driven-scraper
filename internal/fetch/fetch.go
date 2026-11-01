// Package fetch performs HTTP requests for the engine. It is an interface so
// tests, caches or recorded fixtures can replace the network.
package fetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Request struct {
	URL     string
	Headers map[string]string
	Timeout time.Duration
}

type Response struct {
	TabID      string // user-browser tab retained for manual interaction
	URL        string // final URL after redirects
	StatusCode int
	Body       []byte
	Title      string // browser only
}

type Fetcher interface {
	Fetch(ctx context.Context, req Request) (Response, error)
}

const (
	UserAgent    = "PromptDrivenScraper/0.1 (deterministic; +https://github.com/kevinwolrath/prompt-driven-scraper)"
	maxBodyBytes = 16 << 20
)

// HTTP is the default network fetcher.
type HTTP struct {
	Client *http.Client
}

func NewHTTP() *HTTP {
	return &HTTP{Client: &http.Client{}}
}

// StatusError is returned for non-2xx responses.
type StatusError struct {
	URL  string
	Code int
}

func (e *StatusError) Error() string { return fmt.Sprintf("GET %s: HTTP %d", e.URL, e.Code) }

// Retryable reports whether a retry might succeed.
func (e *StatusError) Retryable() bool { return e.Code == 429 || e.Code >= 500 }

func (h *HTTP) Fetch(ctx context.Context, req Request) (Response, error) {
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if err != nil {
		return Response{}, err
	}
	r.Header.Set("User-Agent", UserAgent)
	r.Header.Set("Accept", "text/html,application/json;q=0.9,*/*;q=0.8")
	for k, v := range req.Headers {
		r.Header.Set(k, v)
	}
	resp, err := h.Client.Do(r)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return Response{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if m := DetectBlock(body); m != "" {
			return Response{}, &BlockedError{URL: req.URL, Status: resp.StatusCode, Marker: m}
		}
		return Response{}, &StatusError{URL: req.URL, Code: resp.StatusCode}
	}
	return Response{URL: resp.Request.URL.String(), StatusCode: resp.StatusCode, Body: body}, nil
}
