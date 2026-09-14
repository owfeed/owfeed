package smoke

import "testing"

// The strings are what Docker 29.4.0 printed, measured; see pullOutageRE.
func TestPullOutageTellsARegistryFailureFromAnAnswer(t *testing.T) {
	cases := []struct {
		out  string
		want bool
	}{
		{`Error response from daemon: Get "https://nonexistent.invalid/v2/": Bad Gateway`, true},
		{`Error response from daemon: Get "http://127.0.0.1:1/v2/": dial tcp 127.0.0.1:1: connect: connection refused`, true},
		{`Error response from daemon: toomanyrequests: You have reached your unauthenticated pull rate limit.`, true},
		{`Error response from daemon: received unexpected HTTP status: 503 Service Unavailable`, true},
		{`Error response from daemon: Get "https://registry-1.docker.io/v2/": net/http: TLS handshake timeout`, true},
		{`Error response from daemon: manifest for openwrt/rootfs:x86-64-0.0.0-nope not found: manifest unknown: manifest unknown`, false},
		{`Error response from daemon: pull access denied for owfeed/definitely-not-a-repo-xyz, repository does not exist or may require 'docker login': denied: requested access to the resource is denied`, false},
	}
	for _, c := range cases {
		if got := pullOutage(c.out); got != c.want {
			t.Errorf("pullOutage(%q) = %v, want %v", c.out, got, c.want)
		}
	}
}
