package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestLoginLimiterWindowAndReset(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	l := newLoginLimiter(3, 10*time.Minute)

	if got := l.retryAfter("1.2.3.4", now); got != 0 {
		t.Fatalf("fresh IP blocked for %v", got)
	}
	for i := 0; i < 3; i++ {
		l.fail("1.2.3.4", now)
	}
	if got := l.retryAfter("1.2.3.4", now); got <= 0 || got > 10*time.Minute {
		t.Fatalf("retryAfter = %v, want (0, 10m]", got)
	}
	// Another address is unaffected.
	if got := l.retryAfter("5.6.7.8", now); got != 0 {
		t.Fatalf("unrelated IP blocked for %v", got)
	}
	// The window slides: after it passes, the same address is allowed again.
	if got := l.retryAfter("1.2.3.4", now.Add(11*time.Minute)); got != 0 {
		t.Fatalf("stale window still blocks for %v", got)
	}
	// A success clears the record.
	l.fail("9.9.9.9", now)
	l.reset("9.9.9.9")
	if got := l.retryAfter("9.9.9.9", now); got != 0 {
		t.Fatalf("reset left a block of %v", got)
	}
}

func TestConsoleLoginThrottlesAfterFailures(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := httptest.NewServer(withConsoleAuth(ok, "correct-horse-battery-staple"))
	defer srv.Close()

	form := url.Values{"token": {"wrong"}}
	for i := 0; i < loginMaxFailures; i++ {
		res, err := http.PostForm(srv.URL+"/login", form)
		if err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i+1, res.StatusCode)
		}
	}
	res, err := http.PostForm(srv.URL+"/login", form)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusTooManyRequests || res.Header.Get("Retry-After") == "" {
		t.Fatalf("after %d failures = %d (Retry-After %q), want 429", loginMaxFailures, res.StatusCode, res.Header.Get("Retry-After"))
	}

	// The correct token is throttled too: the block is about the peer, not the
	// guess, otherwise a blocked address could keep testing.
	good, err := http.PostForm(srv.URL+"/login", url.Values{"token": {"correct-horse-battery-staple"}})
	if err != nil {
		t.Fatal(err)
	}
	defer good.Body.Close()
	if good.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("correct token during lockout = %d, want 429", good.StatusCode)
	}
}

// A request carrying no credential is not an attempt: a browser that has not
// logged in yet must not lock itself out by loading the page.
func TestConsoleGateCountsOnlyCredentialAttempts(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := httptest.NewServer(withConsoleAuth(ok, "correct-horse-battery-staple"))
	defer srv.Close()

	for i := 0; i < loginMaxFailures+5; i++ {
		res, err := http.Get(srv.URL + "/api/board")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anonymous request %d = %d, want 401", i+1, res.StatusCode)
		}
	}
}

func TestConsoleGateThrottlesBadBearer(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := httptest.NewServer(withConsoleAuth(ok, "correct-horse-battery-staple"))
	defer srv.Close()

	client := &http.Client{}
	call := func() int {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/board", nil)
		req.Header.Set("Authorization", "Bearer nope")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	for i := 0; i < loginMaxFailures; i++ {
		if got := call(); got != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i+1, got)
		}
	}
	if got := call(); got != http.StatusTooManyRequests {
		t.Fatalf("attempt after window = %d, want 429", got)
	}

	// A correct credential is still accepted while the address is parked, and it
	// clears the penalty: guessing is throttled, knowing the token is not.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/board", nil)
	req.Header.Set("Authorization", "Bearer correct-horse-battery-staple")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("valid token during lockout = %d, want 200", res.StatusCode)
	}
	if got := call(); got != http.StatusUnauthorized {
		t.Fatalf("after a successful login = %d, want 401 (limiter should be reset)", got)
	}
}

func TestConsoleSecurityHeaders(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := httptest.NewServer(withSecurityHeaders(withConsoleAuth(ok, "correct-horse-battery-staple")))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := res.Header.Get(header); got != want {
			t.Fatalf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestClientIPDropsPort(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = net.JoinHostPort("203.0.113.9", "54321")
	if got := clientIP(r); got != "203.0.113.9" {
		t.Fatalf("clientIP = %q", got)
	}
	r.RemoteAddr = "not-a-host-port"
	if got := clientIP(r); got != "not-a-host-port" {
		t.Fatalf("clientIP fallback = %q", got)
	}
}
