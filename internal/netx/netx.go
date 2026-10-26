// Package netx tells an upstream outage from an upstream answer, and retries the first.
//
// owfeed's exit codes make the distinction load-bearing: 8 means CI may retry, 7 means
// it must not. A 404 of a pinned SDK release or a checksum that does not match is an
// answer, and asking again gets the same one. A 503, a reset connection or a DNS
// failure is not an answer at all.
//
// Before this package every network failure on the paths that reach downloads.openwrt.org
// was exit 8, a 404 included, and nothing was retried in place.
package netx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"
)

// StatusError is a response that was not 200. Its message is the one the callers
// printed before this type existed, so logs and tests that quote it still match.
type StatusError struct {
	URL    string
	Code   int
	Status string
}

func (e *StatusError) Error() string {
	msg := fmt.Sprintf("GET %s: %s", e.URL, e.Status)
	if TransientStatus(e.Code) {
		msg += " (an upstream outage, still failing after retries; safe to run again later)"
	}
	return msg
}

// Status builds the error for a response that was not 200.
func Status(url string, resp *http.Response) error {
	return &StatusError{URL: url, Code: resp.StatusCode, Status: resp.Status}
}

// TransientStatus reports whether an HTTP status is one a server returns while it is
// failing rather than while it is answering: 408, 425, 429 and every 5xx.
func TransientStatus(code int) bool {
	return code == http.StatusRequestTimeout || code == http.StatusTooEarly ||
		code == http.StatusTooManyRequests || code >= 500
}

type outageError struct{ err error }

func (e *outageError) Error() string { return e.err.Error() }
func (e *outageError) Unwrap() error { return e.err }

// Outage marks err as an upstream outage when its type cannot say so itself -- a
// `docker pull` failure is text on a pipe, not a *net.OpError.
func Outage(err error) error {
	if err == nil {
		return nil
	}
	return &outageError{err: err}
}

// Transient reports whether err is an upstream outage, as opposed to an answer.
//
// Unknown errors are not transient. Reading one as an outage would let CI retry
// something that may be a real finding, which is the one mistake exit 8 must not make.
func Transient(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var oe *outageError
	var transportOutage *OutageError
	var stalled *StalledError
	if errors.As(err, &oe) || errors.As(err, &transportOutage) || errors.As(err, &stalled) {
		return true
	}
	var se *StatusError
	if errors.As(err, &se) {
		return TransientStatus(se.Code)
	}
	// A certificate that does not verify is what interception looks like, and it does
	// not heal by asking again. Checked before the network cases below, because it
	// arrives wrapped in the same *url.Error.
	var certErr *tls.CertificateVerificationError
	var unknownAuth x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var invalid x509.CertificateInvalidError
	if errors.As(err, &certErr) || errors.As(err, &unknownAuth) ||
		errors.As(err, &hostErr) || errors.As(err, &invalid) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	var dnsErr *net.DNSError
	var opErr *net.OpError
	if errors.As(err, &dnsErr) || errors.As(err, &opErr) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// Transport retries GET and HEAD requests whose failure is Transient, with a delay
// that doubles between attempts. Anything with a body is passed through untouched:
// replaying a write is not this package's decision to make.
//
// Every attempt is abandoned as stalled when nothing arrives for Idle: neither the
// response headers nor, later, the next byte of the body.
//
// Only the response headers are retried here. A body that breaks off halfway -- the
// 285 MB SDK -- is returned to the caller as a read error, which Transient still
// classifies as an outage, and which Get picks up from where it stopped.
type Transport struct {
	Base     http.RoundTripper
	Attempts int           // default DefaultAttempts
	Delay    time.Duration // before the second attempt; default DefaultDelay
	Idle     time.Duration // default DefaultIdle
}

// Four attempts, 2 s, 4 s and 8 s apart: 14 s of waiting covers a CDN hiccup without
// holding a job for minutes on a real incident, which exit 8 then reports.
//
// A minute with nothing arriving is not a slow mirror. Before DefaultIdle, a server
// that kept the connection open and sent nothing held the command until CI killed
// the job: http.DefaultClient has no deadline.
//
// Variables so that tests can make the same number of attempts without the waiting.
var (
	DefaultAttempts = 4
	DefaultDelay    = 2 * time.Second
	DefaultIdle     = 60 * time.Second
)

// StalledError is a request that received nothing for its idle deadline.
type StalledError struct {
	Idle time.Duration
}

func (e *StalledError) Error() string {
	return fmt.Sprintf("nothing received for %s", e.Idle)
}

// OutageError is what Transport returns when a transient transport failure outlasted
// every attempt.
type OutageError struct {
	Attempts int
	Err      error
}

func (e *OutageError) Error() string {
	return fmt.Sprintf("upstream unreachable after %d attempts (an outage, not a finding; safe to run again later): %v", e.Attempts, e.Err)
}
func (e *OutageError) Unwrap() error { return e.Err }

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	if (req.Method != http.MethodGet && req.Method != http.MethodHead) ||
		(req.Body != nil && req.Body != http.NoBody) {
		return base.RoundTrip(req)
	}
	attempts, delay, idle := t.Attempts, t.Delay, t.Idle
	if attempts <= 0 {
		attempts = DefaultAttempts
	}
	if delay <= 0 {
		delay = DefaultDelay
	}
	if idle <= 0 {
		idle = DefaultIdle
	}

	for attempt := 1; ; attempt++ {
		resp, err := once(base, req, idle)
		retry := false
		switch {
		case err != nil:
			retry = Transient(err)
		case TransientStatus(resp.StatusCode):
			retry = true
		}
		if !retry {
			return resp, err
		}
		if attempt >= attempts {
			if err != nil {
				return nil, &OutageError{Attempts: attempt, Err: err}
			}
			// The last failing response goes back as it is; the caller turns it into
			// a StatusError, which says it was an outage.
			return resp, nil
		}
		if resp != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
		}
		if err := pause(req.Context(), delay); err != nil {
			return nil, err
		}
		delay *= 2
	}
}

// once makes one attempt and abandons it when nothing arrives for idle: while it
// waits for the headers, and inside each Read of the body. The clock runs only while
// a Read is waiting, so a caller that pauses between reads is not counted against it.
func once(base http.RoundTripper, req *http.Request, idle time.Duration) (*http.Response, error) {
	ctx, cancel := context.WithCancelCause(req.Context())
	stalled := &StalledError{Idle: idle}
	timer := time.AfterFunc(idle, func() { cancel(stalled) })
	resp, err := base.RoundTrip(req.WithContext(ctx))
	timer.Stop()
	if err != nil {
		cancel(nil)
		if errors.Is(context.Cause(ctx), stalled) {
			return nil, stalled
		}
		return nil, err
	}
	resp.Body = &idleBody{body: resp.Body, ctx: ctx, cancel: cancel, timer: timer, idle: idle, stalled: stalled}
	return resp, nil
}

// idleBody is a response body whose every Read has to receive something within idle.
type idleBody struct {
	body    io.ReadCloser
	ctx     context.Context
	cancel  context.CancelCauseFunc
	timer   *time.Timer
	idle    time.Duration
	stalled *StalledError
}

func (b *idleBody) Read(p []byte) (int, error) {
	b.timer.Reset(b.idle)
	n, err := b.body.Read(p)
	b.timer.Stop()
	// The transport reports the cancellation in words of its own, which differ
	// between HTTP/1 and HTTP/2; the cause is what says it was the deadline.
	if err != nil && !errors.Is(err, io.EOF) && errors.Is(context.Cause(b.ctx), b.stalled) {
		return n, b.stalled
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.timer.Stop()
	err := b.body.Close()
	b.cancel(nil)
	return err
}

// pause waits for d, or returns early with the context's error.
func pause(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Client returns a copy of hc whose transport retries outages. hc is not modified.
func Client(hc *http.Client) *http.Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	c := *hc
	c.Transport = &Transport{Base: hc.Transport}
	return &c
}

// Get returns the body of a GET of url, which has to answer 200 OK.
//
// A body that stalls or breaks off is asked for again from the byte it stopped at, so
// a connection lost 280 MB into the 285 MB SDK costs the last 5 MB rather than the
// whole download, and the reader never sees the break. Get gives up with an
// OutageError after DefaultAttempts breaks with nothing received between them.
//
// Nothing here proves that the pieces are one file. downloads.openwrt.org ignores
// If-Range (measured in docs/apk-behaviour.md), so a file replaced between two
// requests would arrive spliced; the caller hashes what it read against a signed
// list, and that is the check that catches it.
func Get(ctx context.Context, hc *http.Client, url string) (io.ReadCloser, error) {
	resp, err := get(ctx, hc, url, 0)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, Status(url, resp)
	}
	return &resumable{ctx: ctx, hc: hc, url: url, body: resp.Body, delay: DefaultDelay}, nil
}

func get(ctx context.Context, hc *http.Client, url string, from int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// Left to itself Go asks for gzip and decodes it, and a count of decoded bytes is
	// not an offset into the file that a Range header names.
	req.Header.Set("Accept-Encoding", "identity")
	if from > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", from))
	}
	return hc.Do(req)
}

// resumable is the body Get returns.
type resumable struct {
	ctx   context.Context
	hc    *http.Client
	url   string
	body  io.ReadCloser
	read  int64 // bytes handed to the reader so far
	tries int   // breaks since a byte last arrived
	delay time.Duration
}

func (r *resumable) Read(p []byte) (int, error) {
	for {
		n, err := r.body.Read(p)
		r.read += int64(n)
		if n > 0 {
			r.tries, r.delay = 0, DefaultDelay
		}
		if err == nil || errors.Is(err, io.EOF) {
			return n, err
		}
		if err := r.resume(err); err != nil {
			return n, err
		}
		if n > 0 {
			return n, nil
		}
	}
}

func (r *resumable) Close() error { return r.body.Close() }

// resume replaces a body that failed with cause by one that starts at byte r.read.
func (r *resumable) resume(cause error) error {
	for {
		if !Transient(cause) {
			return cause
		}
		r.tries++
		if r.tries >= DefaultAttempts {
			return &OutageError{Attempts: r.tries, Err: fmt.Errorf("GET %s: broke off at byte %d: %w", r.url, r.read, cause)}
		}
		r.body.Close()
		if err := pause(r.ctx, r.delay); err != nil {
			return err
		}
		r.delay *= 2

		// Through the same client, so the request for the rest is retried and
		// bounded like the first one; an error here has outlasted that already.
		resp, err := get(r.ctx, r.hc, r.url, r.read)
		if err != nil {
			// The cause as text only: it is an outage, and wrapping it would make
			// Transient call err one too when err is an answer.
			return fmt.Errorf("resuming at byte %d after %s: %w", r.read, cause.Error(), err)
		}
		switch resp.StatusCode {
		case http.StatusPartialContent:
			var start int64
			cr := resp.Header.Get("Content-Range")
			if _, err := fmt.Sscanf(cr, "bytes %d-", &start); err != nil || start != r.read {
				resp.Body.Close()
				return fmt.Errorf("GET %s from byte %d: the server sent Content-Range %q", r.url, r.read, cr)
			}
			r.body = resp.Body
			return nil
		case http.StatusOK:
			// The server ignored Range and starts from byte 0 again: what was read
			// already is read once more and dropped.
			r.body = resp.Body
			if _, err := io.CopyN(io.Discard, resp.Body, r.read); err != nil {
				cause = err
				continue
			}
			return nil
		default:
			resp.Body.Close()
			return Status(r.url, resp)
		}
	}
}
