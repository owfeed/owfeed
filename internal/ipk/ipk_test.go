package ipk_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

// zopfli changes how the streams are compressed and nothing else: the same members,
// inflating to the same bytes, in a smaller file.
func TestBuildZopfliIsSmallerWithTheSameContent(t *testing.T) {
	payload := t.TempDir()
	var css bytes.Buffer
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&css, ".example-%d{margin:%dpx;color:#%06x}\n", i%37, i%11, i*7919)
	}
	if err := os.WriteFile(filepath.Join(payload, "x.css"), css.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg := ipk.Package{Name: "luci-app-example", Version: "1.0.0-r1", Arch: ipk.ArchAll}

	members := map[bool]map[string][]byte{}
	size := map[bool]int{}
	for _, z := range []bool{false, true} {
		file, err := ipk.Build(pkg, ipk.Options{Payload: payload, OutDir: t.TempDir(), Zopfli: z})
		if err != nil {
			t.Fatalf("Build(zopfli=%v): %v", z, err)
		}
		outer, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		size[z] = len(outer)
		members[z] = map[string][]byte{}
		tr := tar.NewReader(gunzip(t, outer))
		for {
			hdr, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(hdr.Name, ".gz") {
				if body, err = io.ReadAll(gunzip(t, body)); err != nil {
					t.Fatal(err)
				}
			}
			members[z][hdr.Name] = body
		}
	}

	if size[true] >= size[false] {
		t.Errorf("zopfli %d B, best level %d B: nothing gained", size[true], size[false])
	}
	if len(members[true]) != len(members[false]) {
		t.Fatalf("zopfli build has %d members, standard %d", len(members[true]), len(members[false]))
	}
	for name, want := range members[false] {
		if !bytes.Equal(members[true][name], want) {
			t.Errorf("%s differs once inflated", name)
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
