package squeeze_test

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"fmt"
	"io"
	"testing"

	"owfeed.org/owfeed/internal/squeeze"
)

// Text shaped like a LuCI payload: repetitive, but not one repeated line.
func fixture() []byte {
	var b bytes.Buffer
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, ".example-%d{margin:%dpx;color:#%06x}\n", i%37, i%11, i*7919)
	}
	return b.Bytes()
}

func TestGzipIsSmallerAndReadsBack(t *testing.T) {
	in := fixture()
	z, err := squeeze.Gzip(in)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(z))
	if err != nil {
		t.Fatal(err)
	}
	back, err := io.ReadAll(zr)
	if err != nil || !bytes.Equal(back, in) {
		t.Fatalf("round trip: %v, equal=%v", err, bytes.Equal(back, in))
	}
	if zr.Name != "" || !zr.ModTime.IsZero() {
		t.Errorf("header carries name %q, mtime %v; two builds would differ", zr.Name, zr.ModTime)
	}

	var best bytes.Buffer
	w, _ := gzip.NewWriterLevel(&best, gzip.BestCompression)
	w.Write(in)
	w.Close()
	if len(z) >= best.Len() {
		t.Errorf("zopfli %d B, BestCompression %d B: nothing gained", len(z), best.Len())
	}

	again, err := squeeze.Gzip(in)
	if err != nil || !bytes.Equal(again, z) {
		t.Errorf("two runs over the same input differ")
	}
}

func TestDeflateReadsBack(t *testing.T) {
	in := fixture()
	z, err := squeeze.Deflate(in)
	if err != nil {
		t.Fatal(err)
	}
	back, err := io.ReadAll(flate.NewReader(bytes.NewReader(z)))
	if err != nil || !bytes.Equal(back, in) {
		t.Fatalf("round trip: %v, equal=%v", err, bytes.Equal(back, in))
	}
}
