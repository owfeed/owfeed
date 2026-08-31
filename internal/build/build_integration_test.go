package build_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owfeed.org/owfeed/internal/apk"
	"owfeed.org/owfeed/internal/build"
	"owfeed.org/owfeed/internal/config"
	"owfeed.org/owfeed/internal/testapk"
)

// stageFixture writes a small but representative payload: a conffile, an init
// script and the nested static assets a LuCI application ships.
func stageFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]struct {
		content string
		mode    os.FileMode
	}{
		"dist/root/etc/config/demo":                {"config demo\n\toption enabled '1'\n", 0o644},
		"dist/root/etc/init.d/demo":                {"#!/bin/sh /etc/rc.common\nSTART=99\n", 0o755},
		"dist/root/www/luci-static/demo/style.css": {"body{}\n", 0o644},
		"dist/root/usr/lib/lua/luci/demo.lua":      {"return {}\n", 0o644},
		"post.sh":                                  {"#!/bin/sh\necho packaged by owfeed\n", 0o755},
	}
	for name, f := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(f.content), f.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, f.mode); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func request(root, out string) build.Request {
	return build.Request{
		Package: config.Package{
			Name:        "luci-app-demo",
			Build:       config.BuildMkpkg,
			Arch:        config.PkgArch{List: []string{config.Noarch}},
			Version:     "1.2.3-r1",
			Files:       "dist/root",
			Description: "A demo application, with \"quotes\" and $vars in the text.",
			Depends:     []string{"luci-base"},
			Conffiles:   []string{"/etc/config/demo"},
			Scripts:     map[string]string{"post-install": "post.sh"},
		},
		Feed:            config.Feed{Maintainer: "Demo <demo@example.org>", License: "Apache-2.0"},
		Root:            root,
		Version:         "1.2.3-r1",
		OutDir:          out,
		SourceDateEpoch: time.Unix(1750000000, 0),
	}
}

func TestIntegrationBuild(t *testing.T) {
	tool := testapk.Require(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	root := stageFixture(t)
	out := t.TempDir()

	res, err := build.Build(ctx, tool, request(root, out))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := filepath.Base(res.File); got != "luci-app-demo-1.2.3-r1.apk" {
		t.Errorf("built %s, want the name OpenWrt's package-pack.mk would produce", got)
	}

	dump, err := tool.RunOK(ctx, apk.Invocation{Workdir: filepath.Dir(res.File), Args: []string{"adbdump", filepath.Base(res.File)}})
	if err != nil {
		t.Fatalf("adbdump: %v", err)
	}
	got := dump.Stdout

	// Ownership is the finding that motivates the --root trick: apk resolves a file's
	// numeric owner through the passwd file under its root, and an unknown id becomes
	// the literal "nobody" rather than root. Without the fix every file in a package
	// built by an ordinary user is installed owned by nobody.
	if strings.Contains(got, "nobody") {
		t.Errorf("package records files owned by nobody:\n%s", got)
	}
	if !strings.Contains(got, "user: root") {
		t.Errorf("package does not record root-owned files:\n%s", got)
	}

	// Extended attributes are build-host residue. macOS stamps com.apple.provenance
	// on downloaded files, so without --no-xattrs the same inputs produce a different
	// package on macOS than on Linux — and ship the difference to routers.
	if strings.Contains(got, "xattr") {
		t.Errorf("package carries extended attributes:\n%s", got)
	}

	for _, want := range []string{
		"name: luci-app-demo",
		"version: 1.2.3-r1",
		"arch: noarch",
		// Shell metacharacters reach apk intact: owfeed execs apk directly, so the
		// backslash-escaping OpenWrt's make-based path needs would appear literally
		// in the published description.
		`"quotes"`,
		"$vars",
		"luci-base",
		"lib/apk/packages",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("index entry does not contain %q:\n%s", want, got)
		}
	}
}

// The same inputs must produce the same bytes, or a republished feed looks changed
// to every subscriber and cache-skew checks cannot tell a rebuild from tampering.
func TestIntegrationBuildIsReproducible(t *testing.T) {
	tool := testapk.Require(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	root := stageFixture(t)

	first := t.TempDir()
	a, err := build.Build(ctx, tool, request(root, first))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	second := t.TempDir()
	b, err := build.Build(ctx, tool, request(root, second))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	ab, err := os.ReadFile(a.File)
	if err != nil {
		t.Fatal(err)
	}
	bb, err := os.ReadFile(b.File)
	if err != nil {
		t.Fatal(err)
	}
	if string(ab) != string(bb) {
		t.Errorf("two builds of identical inputs differ (%d vs %d bytes)", len(ab), len(bb))
	}
}

// The version reaches apk twice: once through owfeed's own grammar check and once
// through mkpkg. They must agree, or owfeed either builds something apk will reject
// or refuses something apk would accept.
func TestIntegrationBuildRejectsBadVersion(t *testing.T) {
	tool := testapk.Require(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	req := request(stageFixture(t), t.TempDir())
	req.Version = "1.0~beta"

	_, err := build.Build(ctx, tool, req)
	if err == nil {
		t.Fatal("Build accepted a version apk cannot parse")
	}
	if !strings.Contains(err.Error(), "commit hash") {
		t.Errorf("error does not explain what ~ means to apk: %v", err)
	}
}

// apk spells a conflict as a negative dependency. OpenWrt's own apk build does not
// emit them at all — package-pack.mk puts CONFLICTS in the ipk control file and
// never passes it to mkpkg — so a package whose Makefile declares a conflict does
// not enforce it on 25.12. A package that conflicts with others that all
// rewrite the routing table, is the case that makes this matter.
// A catalogue has to install itself: `depends` points from it to its application, so
// installing the application pulls in nothing, and apk's install-if is the only field
// that says "install me once both of these are true". The pin is written {version} in
// the manifest and must reach the package expanded, or apk refuses the field outright
// (it wants at least two entries with one `=`).
func TestIntegrationInstallIfCarriesTheExpandedPin(t *testing.T) {
	tool := testapk.Require(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	out := t.TempDir()
	req := request(stageFixture(t), out)
	req.Package.InstallIf = []string{"luci-theme-footstrap={version}", "luci-i18n-base-ru"}

	res, err := build.Build(ctx, tool, req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	dump, err := tool.RunOK(ctx, apk.Invocation{Workdir: filepath.Dir(res.File), Args: []string{"adbdump", filepath.Base(res.File)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"install-if", "luci-theme-footstrap=" + req.Version, "luci-i18n-base-ru"} {
		if !strings.Contains(dump.Stdout, want) {
			t.Errorf("install-if does not carry %s:\n%s", want, dump.Stdout)
		}
	}
	if strings.Contains(dump.Stdout, "{version}") {
		t.Errorf("the placeholder reached the package unexpanded:\n%s", dump.Stdout)
	}
}

func TestIntegrationConflictsBecomeNegativeDependencies(t *testing.T) {
	tool := testapk.Require(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	out := t.TempDir()
	req := request(stageFixture(t), out)
	req.Package.Conflicts = []string{"othermon", "luci-app-othermon"}

	res, err := build.Build(ctx, tool, req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	dump, err := tool.RunOK(ctx, apk.Invocation{Workdir: filepath.Dir(res.File), Args: []string{"adbdump", filepath.Base(res.File)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"'!othermon'", "'!luci-app-othermon'", "luci-base"} {
		if !strings.Contains(dump.Stdout, want) {
			t.Errorf("depends does not carry %s:\n%s", want, dump.Stdout)
		}
	}
}
