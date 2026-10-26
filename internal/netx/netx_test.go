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
	"strconv"
	"strings"
	"sync"
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

// Long enough that a loaded CI runner does not stall a healthy local transfer.
const idle = 200 * time.Millisecond

// staller returns what a handler calls to hold the connection open and send nothing
// more, until the client gives up or the test ends. Call it after the server's own
// cleanup is registered: cleanups run last-first, and the server cannot close while a
// handler still waits.
func staller(t *testing.T) func(*http.Request) {
	end := make(chan struct{})
	t.Cleanup(func() { close(end) })
	return func(r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-end:
		}
	}
}

// within fails the test when f has not returned after a few seconds, rather than
// leaving it to hang until go test's own timeout.
func within(t *testing.T, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("still waiting after 5s: nothing gave up on the stalled server")
	}
}

func newServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func getClient() *http.Client {
	return &http.Client{Transport: &Transport{Delay: time.Millisecond, Idle: idle}}
}

func TestTransportAbandonsAStalledBody(t *testing.T) {
	var stall func(*http.Request)
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "10")
		fmt.Fprint(w, "half")
		w.(http.Flusher).Flush()
		stall(r)
	})
	stall = staller(t)

	resp, err := getClient().Get(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body []byte
	within(t, func() { body, err = io.ReadAll(resp.Body) })
	var stalled *StalledError
	if !errors.As(err, &stalled) || string(body) != "half" {
		t.Fatalf("read %q, %v; want \"half\" and a StalledError", body, err)
	}
	if !Transient(err) {
		t.Fatalf("a stalled body is an outage: %v", err)
	}
}

func TestTransportAbandonsAServerThatNeverAnswers(t *testing.T) {
	var hits atomic.Int32
	var stall func(*http.Request)
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		stall(r)
	})
	stall = staller(t)

	var err error
	within(t, func() {
		var resp *http.Response
		if resp, err = getClient().Get(s.URL); err == nil {
			resp.Body.Close()
		}
	})
	var oe *OutageError
	var stalled *StalledError
	if !errors.As(err, &oe) || !errors.As(err, &stalled) || int(hits.Load()) != DefaultAttempts {
		t.Fatalf("want an OutageError over a StalledError after %d requests, got %v after %d", DefaultAttempts, err, hits.Load())
	}
	if !strings.Contains(err.Error(), "nothing received for 200ms") {
		t.Fatalf("the error should say what stalled: %v", err)
	}
}

// The deadline is on the server, not on the reader: a body that keeps trickling in, and
// a caller that stops to do something between reads, are both left alone.
func TestTransportLeavesASlowBodyAndASlowReaderAlone(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 6; i++ {
			fmt.Fprint(w, "x")
			w.(http.Flusher).Flush()
			time.Sleep(idle / 4)
		}
	})

	resp, err := getClient().Get(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	time.Sleep(3 * idle)
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "xxxxxx" {
		t.Fatalf("read %q, %v; want \"xxxxxx\" and no error", body, err)
	}
}

// payload is a body large enough that breaking it off in the middle leaves plenty of
// both halves.
var payload = strings.Repeat("0123456789abcdef", 4096)

// rangeServer serves payload and honours Range. A request for which brk returns a cut
// stops at that byte: with stalls set it then holds the connection open, otherwise the
// server drops it short of Content-Length.
func rangeServer(t *testing.T, brk func(n int) (cut int, stalls bool)) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var ranges []string
	var stall func(*http.Request)
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ranges = append(ranges, r.Header.Get("Range"))
		n := len(ranges)
		mu.Unlock()
		if ae := r.Header.Get("Accept-Encoding"); ae != "identity" {
			t.Errorf("request %d: Accept-Encoding %q, want identity", n, ae)
		}

		from := 0
		if rg := r.Header.Get("Range"); rg != "" {
			if _, err := fmt.Sscanf(rg, "bytes=%d-", &from); err != nil {
				t.Errorf("request %d: Range %q", n, rg)
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(payload)-1, len(payload)))
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)-from))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		}
		cut, stalls := brk(n)
		if cut <= 0 {
			fmt.Fprint(w, payload[from:])
			return
		}
		fmt.Fprint(w, payload[from:cut])
		if stalls {
			w.(http.Flusher).Flush()
			stall(r)
		}
	})
	stall = staller(t)
	return s, &ranges
}

func readGet(t *testing.T, url string) (got string, err error) {
	t.Helper()
	old := DefaultDelay
	DefaultDelay = time.Millisecond
	defer func() { DefaultDelay = old }()

	within(t, func() {
		var body io.ReadCloser
		if body, err = Get(context.Background(), getClient(), url); err != nil {
			return
		}
		defer body.Close()
		var b []byte
		b, err = io.ReadAll(body)
		got = string(b)
	})
	return got, err
}

func TestGetReadsAHealthyBodyInOneRequest(t *testing.T) {
	s, ranges := rangeServer(t, func(int) (int, bool) { return 0, false })
	got, err := readGet(t, s.URL)
	if err != nil || got != payload {
		t.Fatalf("read %d bytes, %v; want %d and no error", len(got), err, len(payload))
	}
	if len(*ranges) != 1 || (*ranges)[0] != "" {
		t.Fatalf("requests with Range %q, want one request without", *ranges)
	}
}

func TestGetResumesWhereTheBodyBrokeOff(t *testing.T) {
	for name, stalls := range map[string]bool{"stalled": true, "connection dropped": false} {
		t.Run(name, func(t *testing.T) {
			s, ranges := rangeServer(t, func(n int) (int, bool) {
				if n == 1 {
					return 10000, stalls
				}
				return 0, false
			})
			got, err := readGet(t, s.URL)
			if err != nil || got != payload {
				t.Fatalf("read %d bytes, %v; want %d and no error", len(got), err, len(payload))
			}
			if want := []string{"", "bytes=10000-"}; fmt.Sprint(*ranges) != fmt.Sprint(want) {
				t.Fatalf("requests with Range %q, want %q", *ranges, want)
			}
		})
	}
}

// Every resume makes progress here, so the count of breaks never reaches the limit
// although there are more breaks than attempts.
func TestGetKeepsGoingWhileEachAttemptMakesProgress(t *testing.T) {
	step := len(payload) / (DefaultAttempts * 2)
	s, ranges := rangeServer(t, func(n int) (int, bool) {
		if cut := n * step; cut < len(payload) {
			return cut, false
		}
		return 0, false
	})
	got, err := readGet(t, s.URL)
	if err != nil || got != payload {
		t.Fatalf("read %d bytes, %v; want %d and no error", len(got), err, len(payload))
	}
	if len(*ranges) <= DefaultAttempts {
		t.Fatalf("%d requests; the test needs more breaks than attempts", len(*ranges))
	}
}

func TestGetReadsPastAServerThatIgnoresRange(t *testing.T) {
	var hits atomic.Int32
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		if hits.Add(1) == 1 {
			fmt.Fprint(w, payload[:10000])
			return
		}
		fmt.Fprint(w, payload)
	})
	got, err := readGet(t, s.URL)
	if err != nil || got != payload || hits.Load() != 2 {
		t.Fatalf("read %d bytes, %v in %d requests; want %d, no error, 2 requests", len(got), err, hits.Load(), len(payload))
	}
}

func TestGetGivesUpWhenNothingMoreArrives(t *testing.T) {
	s, ranges := rangeServer(t, func(int) (int, bool) { return 10000, true })
	got, err := readGet(t, s.URL)
	var oe *OutageError
	if !errors.As(err, &oe) || oe.Attempts != DefaultAttempts || !Transient(err) {
		t.Fatalf("want an outage after %d attempts, got %v", DefaultAttempts, err)
	}
	if len(got) != 10000 || len(*ranges) != DefaultAttempts {
		t.Fatalf("read %d bytes in %d requests, want 10000 in %d", len(got), len(*ranges), DefaultAttempts)
	}
	for _, want := range []string{s.URL, "broke off at byte 10000", "nothing received for", "safe to run again later"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q: %v", want, err)
		}
	}
}

// The first request breaks off; every request for the rest has its connection dropped
// before an answer. That is an outage, and the error says how far the download got.
func TestGetSaysWhereItStoppedWhenTheRestCannotBeFetched(t *testing.T) {
	var hits atomic.Int32
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			fmt.Fprint(w, payload[:10000])
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	})
	got, err := readGet(t, s.URL)
	if len(got) != 10000 || !Transient(err) {
		t.Fatalf("read %d bytes, %v; want 10000 and an outage", len(got), err)
	}
	if !strings.Contains(fmt.Sprint(err), "resuming at byte 10000") {
		t.Fatalf("the error does not say where the download stopped: %v", err)
	}
	if want := int32(1 + DefaultAttempts); hits.Load() != want {
		t.Fatalf("%d requests, want %d: the first, then one retried request for the rest", hits.Load(), want)
	}
}

func TestGetDoesNotRetryAnAnswer(t *testing.T) {
	s, hits := server(t, 404)
	_, err := readGet(t, s.URL)
	if Transient(err) || hits.Load() != 1 || !strings.Contains(fmt.Sprint(err), "404") {
		t.Fatalf("a 404 is an answer: %v after %d requests", err, hits.Load())
	}
}

func TestGetStopsWhenTheContextIsCancelled(t *testing.T) {
	s, _ := rangeServer(t, func(int) (int, bool) { return 10000, true })
	ctx, cancel := context.WithCancel(context.Background())
	body, err := Get(ctx, &http.Client{Transport: &Transport{Idle: time.Hour}}, s.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	within(t, func() { _, err = io.ReadAll(body) })
	if !errors.Is(err, context.Canceled) || Transient(err) {
		t.Fatalf("want context.Canceled, not an outage; got %v", err)
	}
}

// A body can break after its last byte: chunked, with the closing chunk never sent.
// The request for the rest then starts at the end of the file, and the server answers
// 416 with the file's size, which is all of it. A 416 that names another size is a
// file that changed, and still an error.
func TestGetFinishesABodyThatBrokeAfterItsLastByte(t *testing.T) {
	for name, c := range map[string]struct {
		size    int
		wantErr bool
	}{
		"the size that was read": {len(payload), false},
		"another size":           {len(payload) + 1, true},
	} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var ranges []string
			s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				ranges = append(ranges, r.Header.Get("Range"))
				n := len(ranges)
				mu.Unlock()
				if n == 1 {
					fmt.Fprint(w, payload)
					w.(http.Flusher).Flush()
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					conn.Close()
					return
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", c.size))
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			})
			got, err := readGet(t, s.URL)
			if want := []string{"", fmt.Sprintf("bytes=%d-", len(payload))}; fmt.Sprint(ranges) != fmt.Sprint(want) {
				t.Fatalf("requests with Range %q, want %q", ranges, want)
			}
			if c.wantErr {
				if err == nil || Transient(err) || !strings.Contains(err.Error(), "416") {
					t.Fatalf("read %d bytes, %v; want a 416 that is an answer", len(got), err)
				}
				return
			}
			if err != nil || got != payload {
				t.Fatalf("read %d bytes, %v; want %d and no error", len(got), err, len(payload))
			}
		})
	}
}
