package ipk_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"owfeed.org/owfeed/internal/ipk"
)

// Every gzip stream in the container is written at the best level. The XFL byte of a
// gzip header is 2 for BestCompression and 0 for the default, so a stream that drifts
// back to gzip.NewWriter fails here rather than quietly costing every router ~5%.
func TestBuildCompressesEveryStreamAtBestLevel(t *testing.T) {
	payload := t.TempDir()
	if err := os.MkdirAll(filepath.Join(payload, "www"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(payload, "www", "x.css"), bytes.Repeat([]byte("body{margin:0}\n"), 200), 0o644); err != nil {
		t.Fatal(err)
	}

	file, err := ipk.Build(ipk.Package{Name: "luci-app-example", Version: "1.0.0-r1", Arch: ipk.ArchAll},
		ipk.Options{Payload: payload, OutDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	outer, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	streams := map[string][]byte{"outer": outer}
	tr := tar.NewReader(gunzip(t, outer))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == "./data.tar.gz" || hdr.Name == "./control.tar.gz" {
			b, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			streams[hdr.Name] = b
		}
	}
	if len(streams) != 3 {
		t.Fatalf("found %d of the 3 gzip streams", len(streams))
	}
	for name, b := range streams {
		if xfl := b[8]; xfl != 2 {
			t.Errorf("%s: gzip XFL = %d, want 2 (BestCompression)", name, xfl)
		}
	}
}

func gunzip(t *testing.T, b []byte) io.Reader {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return zr
}
