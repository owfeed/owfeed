package main

import (
	"net/http"

	"owfeed.org/owfeed/internal/netx"
)

// upstreamHTTP is the client for every request to downloads.openwrt.org. It retries a
// 5xx, a 429 or a dropped connection a few times before anything reaches wrapUpstream.
// A variable so that tests can point it at a local server.
var upstreamHTTP = netx.Client(http.DefaultClient)

// wrapUpstream gives an error from a step that talked to upstream its exit code:
// exitUpstream (8) when upstream was failing -- CI may retry that -- and exitCheck (7)
// when upstream answered and the answer is the problem: a 404 of a pinned release, a
// signature or checksum that does not match, a listing that does not parse.
//
// Before this, every one of those was 8, so CI that honours the contract would retry a
// release that does not exist until it gave up, and read it as an outage.
func wrapUpstream(err error) error {
	if err == nil {
		return nil
	}
	if netx.Transient(err) {
		return wrap(exitUpstream, err)
	}
	return wrap(exitCheck, err)
}
