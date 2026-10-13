package index_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owfeed.org/owfeed/internal/apk"
	"owfeed.org/owfeed/internal/build"
	"owfeed.org/owfeed/internal/config"
	"owfeed.org/owfeed/internal/index"
	"owfeed.org/owfeed/internal/keys"
	"owfeed.org/owfeed/internal/testapk"
)

// feed builds a small published tree: two packages, a private key in its own
// directory and the matching public key in a trust directory.
type feed struct {
	dir      string
	pkgDir   string
	keyDir   string
	trustDir string
	signer   index.Signer
}

func newFeed(t *testing.T, tool *apk.Tool) feed {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := feed{dir: t.TempDir(), keyDir: t.TempDir(), trustDir: t.TempDir()}
	// build writes into a subdirectory named for the architecture.
	f.pkgDir = filepath.Join(f.dir, config.Noarch)

	key, err := keys.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.WritePrivate(filepath.Join(f.keyDir, "feed.pem"), key); err != nil {
		t.Fatal(err)
	}
	pub, err := keys.MarshalPublic(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.trustDir, "demo.pem"), pub, 0o644); err != nil {
		t.Fatal(err)
	}
	if f.signer, err = index.NewSigner(f.keyDir, "feed.pem", key); err != nil {
		t.Fatal(err)
	}

	src := t.TempDir()
	for _, name := range []string{"luci-app-one", "luci-theme-two"} {
		payload := filepath.Join(src, name, "root", "usr", "lib", "lua", "luci")
		if err := os.MkdirAll(payload, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(payload, name+".lua"), []byte("return {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := build.Build(ctx, tool, build.Request{
			Package: config.Package{
				Name: name, Build: config.BuildMkpkg, Arch: config.PkgArch{List: []string{config.Noarch}},
				Version: "1.0-r1", Files: filepath.Join(name, "root"),
			},
			Root: src, Version: "1.0-r1", OutDir: f.dir,
			SourceDateEpoch: time.Unix(1750000000, 0),
		})
		if err != nil {
			t.Fatalf("building %s: %v", name, err)
		}
	}
	return f
}

func TestIntegrationSignAndIndex(t *testing.T) {
	tool := testapk.Require(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	f := newFeed(t, tool)

	signed, err := index.Sign(ctx, tool, f.pkgDir, f.signer)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if len(signed) != 2 {
		t.Fatalf("signed %d packages, want 2", len(signed))
	}

	res, err := index.Build(ctx, tool, index.Options{
		Dir: f.pkgDir, TrustDir: f.trustDir, Signer: f.signer,
		Description: "owfeed test feed",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(res.Signers) != 1 || res.Signers[0] != f.signer.Identity.String() {
		t.Errorf("index signers = %v, want just %s", res.Signers, f.signer.Identity)
	}

	// Deflate, never zstd: OpenWrt builds apk with -Dzstd=disabled, so a zstd index
	// reads fine on the build host and fails on every router.
	adb, err := os.ReadFile(filepath.Join(f.pkgDir, index.IndexFile))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(adb[:4]); got != "ADBd" {
		t.Errorf("index magic = %q, want ADBd", got)
	}

	// The index records each package's size, which is what makes the sign-then-index
	// order mandatory. Checking it here is also check 404 in miniature.
	var idx struct {
		Packages []struct {
			Name     string `json:"name"`
			Version  string `json:"version"`
			Arch     string `json:"arch"`
			FileSize int64  `json:"file-size"`
		} `json:"packages"`
	}
	raw, err := os.ReadFile(filepath.Join(f.pkgDir, index.JSONFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatalf("index.json is not JSON: %v", err)
	}
	if len(idx.Packages) != 2 {
		t.Fatalf("index.json lists %d packages, want 2", len(idx.Packages))
	}
	for _, p := range idx.Packages {
		if p.Arch != "noarch" {
			t.Errorf("%s arch = %q, want noarch", p.Name, p.Arch)
		}
		// apk derives the download name from the package name and version, so the
		// file has to be sitting flat beside the index under exactly that name.
		file := filepath.Join(f.pkgDir, build.PackageFileName(p.Name, p.Version))
		st, err := os.Stat(file)
		if err != nil {
			t.Errorf("index names %s-%s but %s: %v", p.Name, p.Version, filepath.Base(file), err)
			continue
		}
		if st.Size() != p.FileSize {
			t.Errorf("%s: index says %d bytes, file is %d — the index was built before the package was signed",
				filepath.Base(file), p.FileSize, st.Size())
		}
	}

	sums, err := os.ReadFile(filepath.Join(f.pkgDir, index.SumsFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{index.IndexFile, index.JSONFile, "luci-app-one-1.0-r1.apk"} {
		if !strings.Contains(string(sums), want) {
			t.Errorf("sha256sums does not cover %s:\n%s", want, sums)
		}
	}
}

// Building the index verifies the package signatures rather than ignoring them,
// which is what turns adbsign's meaningless exit status into a caught error: if the
// signing step had quietly done nothing, this is where it would surface.
func TestIntegrationIndexRejectsUnsignedPackages(t *testing.T) {
	tool := testapk.Require(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	f := newFeed(t, tool)

	_, err := index.Build(ctx, tool, index.Options{Dir: f.pkgDir, TrustDir: f.trustDir, Signer: f.signer})
	if err == nil {
		t.Fatal("Build indexed unsigned packages")
	}
	if !strings.Contains(err.Error(), "UNTRUSTED") {
		t.Errorf("error does not come from apk's own verification: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(f.pkgDir, index.IndexFile)); statErr == nil {
		t.Error("a failed index build left packages.adb behind")
	}
}

// A key the feed does not publish must not pass, or the trust directory would be
// decoration rather than a check.
func TestIntegrationIndexRejectsForeignKey(t *testing.T) {
	tool := testapk.Require(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	f := newFeed(t, tool)

	other, err := keys.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.WritePrivate(filepath.Join(f.keyDir, "other.pem"), other); err != nil {
		t.Fatal(err)
	}
	foreign, err := index.NewSigner(f.keyDir, "other.pem", other)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := index.Sign(ctx, tool, f.pkgDir, foreign); err != nil {
		t.Fatalf("Sign with the foreign key: %v", err)
	}
	if _, err := index.Build(ctx, tool, index.Options{Dir: f.pkgDir, TrustDir: f.trustDir, Signer: f.signer}); err == nil {
		t.Fatal("Build accepted packages signed by a key the feed does not publish")
	}
}

// A file in dist whose name starts with '-' reaches apk as ./name, not as an option.
func TestIntegrationAFileNamedLikeAnOption(t *testing.T) {
	tool := testapk.Require(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	f := newFeed(t, tool)
	if err := os.Rename(filepath.Join(f.pkgDir, "luci-app-one-1.0-r1.apk"), filepath.Join(f.pkgDir, "-v-1.0-r1.apk")); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Sign(ctx, tool, f.pkgDir, f.signer); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if name, err := index.PackageName(ctx, tool, f.pkgDir, "-v-1.0-r1.apk"); err != nil || name != "luci-app-one" {
		t.Fatalf("PackageName = %q, %v", name, err)
	}
}
