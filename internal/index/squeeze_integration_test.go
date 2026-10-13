package index_test

import (
	"bytes"
	"compress/flate"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"owfeed.org/owfeed/internal/apk"
	"owfeed.org/owfeed/internal/build"
	"owfeed.org/owfeed/internal/config"
	"owfeed.org/owfeed/internal/index"
	"owfeed.org/owfeed/internal/testapk"
)

// A signed package re-deflated with zopfli is the same package: smaller, still
// `ADBd`, inflating to the identical ADB, and still verifying against the key that
// signed it -- checked here by apk itself, not by Squeeze's own word.
func TestIntegrationSqueezeKeepsTheSignature(t *testing.T) {
	tool := testapk.Require(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	f := newFeed(t, tool)
	src := t.TempDir()
	payload := filepath.Join(src, "root", "www", "luci-static", "example")
	if err := os.MkdirAll(payload, 0o755); err != nil {
		t.Fatal(err)
	}
	var css bytes.Buffer
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&css, ".example-%d{margin:%dpx;color:#%06x}\n", i%37, i%11, i*7919)
	}
	if err := os.WriteFile(filepath.Join(payload, "x.css"), css.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := build.Build(ctx, tool, build.Request{
		Package: config.Package{
			Name: "luci-app-example", Build: config.BuildMkpkg, Arch: config.PkgArch{List: []string{config.Noarch}},
			Version: "1.0-r1", Files: "root",
		},
		Root: src, Version: "1.0-r1", OutDir: f.dir, SourceDateEpoch: time.Unix(1750000000, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Base(res.File)

	if _, err := index.Sign(ctx, tool, f.pkgDir, f.signer); err != nil {
		t.Fatal(err)
	}
	if name, err := index.PackageName(ctx, tool, f.pkgDir, file); err != nil || name != "luci-app-example" {
		t.Fatalf("PackageName = %q, %v", name, err)
	}
	old, err := os.ReadFile(res.File)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(res.File, 0o644); err != nil {
		t.Fatal(err)
	}

	before, after, err := index.Squeeze(ctx, tool, f.pkgDir, file, f.signer, f.trustDir)
	if err != nil {
		t.Fatalf("Squeeze: %v", err)
	}
	if before != len(old) || after >= before {
		t.Fatalf("Squeeze reported %d -> %d B for a %d B file", before, after, len(old))
	}
	now, err := os.ReadFile(res.File)
	if err != nil {
		t.Fatal(err)
	}
	if len(now) != after || !bytes.HasPrefix(now, []byte("ADBd")) {
		t.Fatalf("on disk: %d B starting %q", len(now), now[:4])
	}
	if st, err := os.Stat(res.File); err != nil || st.Mode().Perm() != 0o644 {
		t.Errorf("mode after Squeeze: %v, %v; want 0644 as before", st.Mode(), err)
	}
	if !bytes.Equal(inflate(t, old), inflate(t, now)) {
		t.Fatal("the re-deflated file inflates to a different ADB")
	}

	v, err := tool.Run(ctx, apk.Invocation{
		Workdir: f.pkgDir, TrustDir: f.trustDir,
		Args: []string{"--keys-dir", apk.TrustDirRef(), "verify", file},
	})
	if err != nil || v.ExitCode != 0 {
		t.Fatalf("apk verify after Squeeze: exit %d, %v: %s%s", v.ExitCode, err, v.Stdout, v.Stderr)
	}

	// The temporary file is gone; a stray .apk in the publish directory would be indexed.
	pkgs, err := index.Packages(f.pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pkgs {
		if p != file && p != "luci-app-one-1.0-r1.apk" && p != "luci-theme-two-1.0-r1.apk" {
			t.Errorf("left behind: %s", p)
		}
	}

	// Only apk's default deflate container is touched.
	other := filepath.Join(f.pkgDir, "other.apk")
	if err := os.WriteFile(other, []byte("ADBc\x01\x09rest"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := index.Squeeze(ctx, tool, f.pkgDir, "other.apk", f.signer, f.trustDir); err == nil {
		t.Error("Squeeze accepted an ADBc file")
	}
}

func inflate(t *testing.T, b []byte) []byte {
	t.Helper()
	out, err := io.ReadAll(flate.NewReader(bytes.NewReader(b[4:])))
	if err != nil {
		t.Fatal(err)
	}
	return out
}
