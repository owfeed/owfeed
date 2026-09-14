package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"owfeed.org/owfeed/internal/netx"
)

// toServer sends every request to one local server, whatever host it names, so that
// the real downloads.openwrt.org URLs can be answered by a test.
type toServer struct{ base *url.URL }

func (r toServer) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host, req.Host = r.base.Scheme, r.base.Host, r.base.Host
	return http.DefaultTransport.RoundTrip(req)
}

// `owfeed releases` asks downloads.openwrt.org one question, so it is the smallest
// command that shows the contract end to end: an outage is 8 after retries, an answer
// is 7 at once. Before netx both were 8, and the 404 was never asked again.
func TestUpstreamExitCodeSeparatesAnOutageFromAnAnswer(t *testing.T) {
	cases := []struct {
		name   string
		status int
		code   int
		hits   int32
	}{
		{"404 is an answer", http.StatusNotFound, exitCheck, 1},
		{"503 is an outage", http.StatusServiceUnavailable, exitUpstream, int32(netx.DefaultAttempts)},
		{"429 is an outage", http.StatusTooManyRequests, exitUpstream, int32(netx.DefaultAttempts)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(c.status)
			}))
			defer srv.Close()
			u, _ := url.Parse(srv.URL)

			old := upstreamHTTP
			upstreamHTTP = &http.Client{Transport: &netx.Transport{Base: toServer{u}, Delay: time.Millisecond}}
			defer func() { upstreamHTTP = old }()

			var stderr bytes.Buffer
			got := run([]string{"-cache", t.TempDir(), "releases"}, &bytes.Buffer{}, &stderr)
			if got != c.code {
				t.Fatalf("exit %d, want %d\n%s", got, c.code, stderr.String())
			}
			if hits.Load() != c.hits {
				t.Fatalf("%d request(s), want %d", hits.Load(), c.hits)
			}
			if c.code == exitUpstream && !strings.Contains(stderr.String(), "an upstream outage") {
				t.Fatalf("an outage should say so:\n%s", stderr.String())
			}
		})
	}
}

func TestWrapUpstream(t *testing.T) {
	cases := []struct {
		err  error
		code int
	}{
		{&netx.StatusError{Code: 404}, exitCheck},
		{fmt.Errorf("sha256sums: %w", &netx.StatusError{Code: 502}), exitUpstream},
		{&netx.OutageError{Attempts: 4, Err: errors.New("connection refused")}, exitUpstream},
		{errors.New("sha256sums from x: signature does not verify"), exitCheck},
		{netx.Outage(errors.New("docker pull: Bad Gateway")), exitUpstream},
	}
	for _, c := range cases {
		if got := codeFor(wrapUpstream(c.err)); got != c.code {
			t.Errorf("wrapUpstream(%v) exits %d, want %d", c.err, got, c.code)
		}
	}
}
