package fetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Browser loads a page in a real browser and returns the resulting DOM as
// HTML. It only acquires content; extraction happens elsewhere.
type Browser interface {
	Load(ctx context.Context, req BrowserRequest) (Response, error)
}

type BrowserRequest struct {
	URL string
	// WaitUntil is "load" (default) or "domcontentloaded".
	WaitUntil string
	// WaitFor is an optional CSS selector to wait for before reading the DOM.
	WaitFor string
	Timeout time.Duration
}

// BlockedError reports content that is a bot challenge or access-denied
// page rather than the requested page.
type BlockedError struct {
	URL    string
	Status int
	Marker string
}

func (e *BlockedError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("bot challenge or access-denied page detected (HTTP %d, %q)", e.Status, e.Marker)
	}
	return fmt.Sprintf("bot challenge or access-denied page detected (%q)", e.Marker)
}

// blockMarkers are generic, vendor-level signatures of challenge or
// access-denied interstitials. They are deliberately specific, so ordinary
// pages (for example ones with a reCAPTCHA contact form) do not match.
var blockMarkers = [][]byte{
	[]byte("cf-chl-"),                 // Cloudflare challenge
	[]byte("challenge-platform"),      // Cloudflare challenge script
	[]byte("<title>just a moment..."), // Cloudflare interstitial
	[]byte("<title>attention required! | cloudflare"),
	[]byte("<title>access denied</title>"), // Akamai and others
	[]byte("_incapsula_resource"),          // Imperva
	[]byte("captcha-delivery.com"),         // DataDome
	[]byte("px-captcha"),                   // PerimeterX / HUMAN
	[]byte("pardon our interruption"),      // Distil / Imperva
}

// DetectBlock returns the matching marker if body looks like a challenge or
// access-denied page, or "" for ordinary content.
func DetectBlock(body []byte) string {
	head := body
	if len(head) > 64<<10 {
		head = head[:64<<10]
	}
	lower := bytes.ToLower(head)
	for _, m := range blockMarkers {
		if bytes.Contains(lower, m) {
			return string(m)
		}
	}
	return ""
}

// FallbackEligible reports whether an acquisition failure is the kind a
// different strategy (e.g. a real browser) might overcome: authentication or
// bot refusals and rate limits, and challenge pages. Network errors, 404s
// and server errors are not: another strategy would fail the same way.
func FallbackEligible(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden || se.Code == http.StatusTooManyRequests
	}
	var be *BlockedError
	return errors.As(err, &be)
}

// Describe gives a short human description of an acquisition result.
func Describe(err error) string {
	if err == nil {
		return "page loaded"
	}
	var se *StatusError
	if errors.As(err, &se) {
		return fmt.Sprintf("HTTP %d %s", se.Code, http.StatusText(se.Code))
	}
	var be *BlockedError
	if errors.As(err, &be) {
		d := "bot challenge / access denied"
		if be.Status != 0 {
			d += fmt.Sprintf(" (HTTP %d)", be.Status)
		}
		var te interface{ Timeout() bool }
		if errors.As(err, &te) || errors.Is(err, context.DeadlineExceeded) {
			d += ", still shown when the browser wait ended"
		}
		return d
	}
	if errors.Is(err, context.DeadlineExceeded) {
		if err == context.DeadlineExceeded {
			return "timed out"
		}
		return err.Error() // e.g. "browser timed out after 45s while waiting for ..."
	}
	return err.Error()
}
