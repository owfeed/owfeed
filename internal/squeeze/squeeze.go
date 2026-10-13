// Package squeeze writes deflate streams with zopfli: a few percent smaller than
// the best standard level, read unchanged by every inflater, and 44 times slower to
// produce (32 s for a 10.6 MB tar, measured in docs/apk-behaviour.md).
// That trade is the package's to make, so nothing calls this unless a package's
// config says `compression: zopfli`.
package squeeze

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"fmt"
	"io"

	"github.com/foobaz/go-zopfli/zopfli"
)

// Gzip compresses b as one gzip member with no name and a zero mtime, so the same
// input always produces the same bytes.
func Gzip(b []byte) ([]byte, error) {
	return run(zopfli.FORMAT_GZIP, b, func(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) })
}

// Deflate compresses b as a raw deflate stream, the shape apk stores after `ADBd`.
func Deflate(b []byte) ([]byte, error) {
	return run(zopfli.FORMAT_DEFLATE, b, func(r io.Reader) (io.Reader, error) { return flate.NewReader(r), nil })
}

func run(format int, in []byte, inflater func(io.Reader) (io.Reader, error)) (out []byte, err error) {
	// The port reports a broken internal invariant by panicking; a build should
	// stop with an error naming the package, not with a goroutine dump.
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, fmt.Errorf("zopfli: %v", r)
		}
	}()
	opts := zopfli.DefaultOptions()
	var buf bytes.Buffer
	if err := zopfli.Compress(&opts, format, in, &buf); err != nil {
		return nil, fmt.Errorf("zopfli: %w", err)
	}

	// A compressor bug here ships a package no router can unpack, and the standard
	// library's inflater is the independent reader that would notice.
	r, err := inflater(bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, fmt.Errorf("zopfli wrote a stream that does not open: %w", err)
	}
	back, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("zopfli wrote a stream that does not inflate: %w", err)
	}
	if !bytes.Equal(back, in) {
		return nil, fmt.Errorf("zopfli wrote a stream that inflates to %d B, not the %d B it was given", len(back), len(in))
	}
	return buf.Bytes(), nil
}
