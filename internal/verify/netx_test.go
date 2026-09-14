package verify

import (
	"time"

	"owfeed.org/owfeed/internal/netx"
)

// The live-feed client retries a 5xx before reporting it. The tests that serve one keep
// every attempt and drop the waiting between them.
func init() { netx.DefaultDelay = time.Millisecond }
