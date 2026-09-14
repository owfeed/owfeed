package netx

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTransient(t *testing.T) {
	// A real refused dial, so the error has the shape net/http actually returns.
	_, refused := net.Dial("tcp", "127.0.0.1:1")
	if refused == nil {
		t.Skip("something listens on 127.0.0.1:1")
	}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"404", &StatusError{Code: 404}, false},
		{"403", &StatusError{Code: 403}, false},
		{"500", &StatusError{Code: 500}, true},
		{"503 wrapped", fmt.Errorf("sha256sums: %w", &StatusError{Code: 503}), true},
		{"429", &StatusError{Code: 429}, true},
		{"408", &StatusError{Code: 408}, true},
		{"refused", &url.Error{Op: "Get", URL: "https://x", Err: refused}, true},
		{"dns", &url.Error{Op: "Get", URL: "https://x", Err: &net.DNSError{Err: "no such host", Name: "x"}}, true},
		{"unexpected EOF", fmt.Errorf("reading: %w", io.ErrUnexpectedEOF), true},
		{"deadline", context.DeadlineExceeded, true},
		{"canceled", fmt.Errorf("x: %w", context.Canceled), false},
		{"unknown authority", &url.Error{Op: "Get", URL: "https://x", Err: x509.UnknownAuthorityError{}}, false},
		{"signature mismatch", errors.New("sha256sums from x: signature does not verify"), false},
		{"outage marker", Outage(errors.New("docker pull: Bad Gateway")), true},
		{"transport outage", &OutageError{Attempts: 4, Err: refused}, true},
	}
	for _, c := range cases {
		if got := Transient(c.err); got != c.want {
			t.Errorf("%s: Transient(%v) = %v, want %v", c.name, c.err, got, c.want)
		}
	}
}

// server answers each request with the next status in codes, repeating the last.
func server(t *testing.T, codes ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(hits.Add(1))
		code := codes[len(codes)-1]
		if n <= len(codes) {
			code = codes[n-1]
		}
		w.WriteHeader(code)
		fmt.Fprint(w, "body")
	}))
	t.Cleanup(s.Close)
	return s, &hits
}

func client() *http.Client {
	return &http.Client{Transport: &Transport{Delay: time.Millisecond}}
}

func TestTransportRetriesAnOutageUntilItAnswers(t *testing.T) {
	s, hits := server(t, 503, 502, 200)
	resp, err := client().Get(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || hits.Load() != 3 {
		t.Fatalf("status %d after %d requests, want 200 after 3", resp.StatusCode, hits.Load())
	}
}

func TestTransportAsksForA404Once(t *testing.T) {
	s, hits := server(t, 404)
	resp, err := client().Get(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 || hits.Load() != 1 {
		t.Fatalf("status %d after %d requests, want 404 after 1", resp.StatusCode, hits.Load())
	}
	if Transient(Status(s.URL, resp)) {
		t.Fatal("a 404 classified as an outage")
	}
}

func TestTransportGivesUpOnAPersistentOutage(t *testing.T) {
	s, hits := server(t, 503)
	resp, err := client().Get(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if int(hits.Load()) != DefaultAttempts {
		t.Fatalf("%d requests, want %d", hits.Load(), DefaultAttempts)
	}
	se := Status(s.URL, resp)
	if !Transient(se) || !strings.Contains(se.Error(), "an upstream outage") {
		t.Fatalf("a persistent 503 should read as an outage: %v", se)
	}
}

func TestTransportDoesNotReplayAWrite(t *testing.T) {
	s, hits := server(t, 503)
	resp, err := client().Post(s.URL, "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if hits.Load() != 1 {
		t.Fatalf("POST sent %d times, want 1", hits.Load())
	}
}

func TestTransportReportsAnUnreachableHostAsAnOutage(t *testing.T) {
	s := httptest.NewServer(http.NotFoundHandler())
	addr := s.URL
	s.Close() // nothing listens there any more: every dial is refused

	_, err := client().Get(addr)
	var oe *OutageError
	if !errors.As(err, &oe) || oe.Attempts != DefaultAttempts {
		t.Fatalf("want an OutageError after %d attempts, got %v", DefaultAttempts, err)
	}
	if !Transient(err) {
		t.Fatalf("an unreachable host is an outage: %v", err)
	}
}

func TestTransportStopsWhenTheContextIsCancelled(t *testing.T) {
	s, _ := server(t, 503)
	ctx, cancel := context.WithCancel(context.Background())
	c := &http.Client{Transport: &Transport{Delay: time.Hour}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := c.Do(req)
	if !errors.Is(err, context.Canceled) || time.Since(start) > 5*time.Second {
		t.Fatalf("want context.Canceled promptly, got %v after %s", err, time.Since(start))
	}
	if Transient(err) {
		t.Fatal("a cancelled run is not an outage")
	}
}
