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
	if errors.As(err, &oe) || errors.As(err, &transportOutage) {
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
// Only the response headers are covered. A body that breaks off halfway -- the
// 285 MB SDK -- is returned to the caller as a read error, which Transient still
// classifies as an outage.
type Transport struct {
	Base     http.RoundTripper
	Attempts int           // default DefaultAttempts
	Delay    time.Duration // before the second attempt; default DefaultDelay
}

// Four attempts, 2 s, 4 s and 8 s apart: 14 s of waiting covers a CDN hiccup without
// holding a job for minutes on a real incident, which exit 8 then reports.
//
// Variables so that tests can make the same number of attempts without the waiting.
var (
	DefaultAttempts = 4
	DefaultDelay    = 2 * time.Second
)

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
	attempts, delay := t.Attempts, t.Delay
	if attempts <= 0 {
		attempts = DefaultAttempts
	}
	if delay <= 0 {
		delay = DefaultDelay
	}

	for attempt := 1; ; attempt++ {
		resp, err := base.RoundTrip(req)
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
		timer := time.NewTimer(delay)
		select {
		case <-req.Context().Done():
			timer.Stop()
			return nil, req.Context().Err()
		case <-timer.C:
		}
		delay *= 2
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
