package fetch

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestFallbackEligible(t *testing.T) {
	cases := map[error]bool{
		&StatusError{Code: 401}:                            true,
		&StatusError{Code: 403}:                            true,
		&StatusError{Code: 429}:                            true,
		&StatusError{Code: 404}:                            false,
		&StatusError{Code: 500}:                            false,
		&BlockedError{Marker: "x"}:                         true,
		errors.New("connection reset"):                     false,
		context.DeadlineExceeded:                           false,
		fmt.Errorf("wrapped: %w", &StatusError{Code: 403}): true,
	}
	for err, want := range cases {
		if got := FallbackEligible(err); got != want {
			t.Errorf("%v: got %v want %v", err, got, want)
		}
	}
}

func TestDetectBlock(t *testing.T) {
	blocked := []string{
		`<html><head><title>Just a moment...</title></head><body><div id="cf-chl-widget-x"></div></body></html>`,
		`<html><head><TITLE>Access Denied</TITLE></head><body>Reference #18</body></html>`,
		`<script src="https://geo.captcha-delivery.com/captcha/"></script>`,
	}
	for _, b := range blocked {
		if DetectBlock([]byte(b)) == "" {
			t.Errorf("not detected: %s", b)
		}
	}
	ordinary := []string{
		`<html><head><title>Cars for sale</title></head><body><article>Toyota</article></body></html>`,
		`<form><div class="g-recaptcha" data-sitekey="x"></div>Contact the dealer</form>`,
		`<p>Access denied to private listings? Sign in.</p>`,
	}
	for _, b := range ordinary {
		if m := DetectBlock([]byte(b)); m != "" {
			t.Errorf("false positive %q: %s", m, b)
		}
	}
}
