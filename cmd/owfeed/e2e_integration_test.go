package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// This is the acceptance test: the commands a maintainer actually types, in order,
// followed by installing the result on a real OpenWrt image. Everything else in the
// repository tests a package; this tests the product.
//
//	OWFEED_INTEGRATION=1 go test ./cmd/owfeed/ -run Integration -v
//
// The router image is pinned to a release that exists on Docker Hub, which trails
// the newest point release by one.
const routerImage = "openwrt/rootfs:x86-64-25.12.4"

const feedConfig = `version: 1
feed:
  name: acceptance
  url: https://feed.example.org
  title: Acceptance feed
  maintainer: "owfeed tests <nobody@example.org>"
publish:
  - target: github-pages
packages:
  - name: luci-app-acceptance
    build: mkpkg
    arch: noarch
    version: 1.0.0-r1
    files: ./pkg/root
    description: "Built by owfeed's acceptance test."
    url: https://example.org/luci-app-acceptance
    depends: [luci-base]
    conffiles: ["/etc/config/acceptance"]
    i18n:
      from: ./i18n
      basename: acceptance-theme
`

func requireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("OWFEED_INTEGRATION") == "" {
		t.Skip("set OWFEED_INTEGRATION=1 to run (downloads an SDK and a router image, needs Docker)")
	}
}

// owfeed runs the CLI in-process so the test observes the same exit codes a shell
// would, without needing a built binary on PATH.
func owfeed(t *testing.T, dir string, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := run(append([]string{"-C", dir}, args...), &out, &out)
	t.Logf("owfeed %s -> %d\n%s", strings.Join(args, " "), code, out.String())
	return code, out.String()
}

func TestIntegrationAcceptance(t *testing.T) {
	requireIntegration(t)
	acceptance(t, feedConfig)
}

// The same feed with the package's deflate streams written by zopfli: sign must
// re-deflate the signed .apk, and index, doctor and a real router must all accept it.
func TestIntegrationAcceptanceZopfli(t *testing.T) {
	requireIntegration(t)
	out := acceptance(t, strings.Replace(feedConfig, "    files: ./pkg/root\n", "    files: ./pkg/root\n    compression: zopfli\n", 1))
	m := regexp.MustCompile(`zopfli noarch/luci-app-acceptance-1\.0\.0-r1\.apk: (\d+) -> (\d+) B`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("sign did not re-deflate the package:\n%s", out)
	}
	before, _ := strconv.Atoi(m[1])
	after, _ := strconv.Atoi(m[2])
	if after >= before {
		t.Errorf("zopfli %d -> %d B: nothing gained", before, after)
	}
}

// acceptance runs the maintainer's commands over a feed made from config, installs
// the result on a router, and returns what `owfeed sign` printed.
func acceptance(t *testing.T, config string) string {
	t.Helper()

	// run() chdirs, so restore it for whatever runs next in this process.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	feed := t.TempDir()
	payload := filepath.Join(feed, "pkg", "root")
	writeFile(t, filepath.Join(payload, "etc", "config", "acceptance"), "config acceptance 'main'\n\toption enabled '1'\n", 0o644)
	writeFile(t, filepath.Join(payload, "etc", "init.d", "acceptance"), "#!/bin/sh /etc/rc.common\nSTART=95\nstart() { :; }\n", 0o755)
	writeFile(t, filepath.Join(payload, "www", "luci-static", "acceptance", "style.css"), "body{margin:0}\n", 0o644)
	writeFile(t, filepath.Join(feed, "owfeed.yml"), config, 0o644)

	// Real catalogues, so the compiled .lmo files are the ones LuCI would load
	// rather than a toy the compiler happens to accept.
	for _, lang := range []string{"ru", "es"} {
		po, err := os.ReadFile(filepath.Join("..", "..", "internal", "lmo", "testdata", "footstrap-"+lang+".po"))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(feed, "i18n", lang, "acceptance.po"), string(po), 0o644)
	}

	// The key lives outside the feed directory, which is what keygen insists on for
	// a directory inside a git working tree.
	keyPath := filepath.Join(t.TempDir(), "acceptance.pem")
	if code, out := owfeed(t, feed, "keygen", "-o", keyPath); code != 0 {
		t.Fatalf("keygen exited %d:\n%s", code, out)
	}
	pem, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OWFEED_SIGN_KEY", string(pem))

	var signOut string
	for _, step := range []struct {
		name string
		args []string
	}{
		{"lock", []string{"lock", "--update"}},
		{"build", []string{"build"}},
		{"sign", []string{"sign"}},
		{"index", []string{"index"}},
		// --frozen-lock is the CI default, and it must pass immediately after a
		// lock --update rather than only after a second run.
		{"doctor", []string{"--frozen-lock", "doctor"}},
		{"publish", []string{"publish", "--dry-run"}},
	} {
		code, out := owfeed(t, feed, step.args...)
		if code != 0 {
			t.Fatalf("%s exited %d:\n%s", step.name, code, out)
		}
		if step.name == "sign" {
			signOut = out
		}
	}

	// The generated instructions and the built tree have to agree; 701 checks that
	// against a README, and here the point is that the snippet is not decorative.
	_, snippet := owfeed(t, feed, "install-snippet", "--format", "sh")
	for _, want := range []string{"/etc/apk/keys/acceptance.pem", "packages.adb", "/lib/upgrade/keep.d/acceptance"} {
		if !strings.Contains(snippet, want) {
			t.Errorf("install-snippet omits %q:\n%s", want, snippet)
		}
	}
	if strings.Contains(snippet, "--allow-untrusted") {
		t.Errorf("install-snippet tells subscribers to bypass verification:\n%s", snippet)
	}

	installOnRouter(t, filepath.Join(feed, "out"))
	return signOut
}

// installOnRouter is the assertion the whole project exists for: a stock image, the
// published key, the documented repository line, and `apk add` with no
// --allow-untrusted anywhere.
func installOnRouter(t *testing.T, out string) {
	t.Helper()

	script := `set -e
mkdir -p /etc/apk/keys /etc/apk/repositories.d
cp /repo/acceptance.pem /etc/apk/keys/acceptance.pem
echo "/repo/releases/25.12/$(cat /etc/apk/arch)/packages.adb" > /etc/apk/repositories.d/acceptance.list
apk update
apk add luci-app-acceptance
test -f /etc/config/acceptance
test -f /www/luci-static/acceptance/style.css
test -f /lib/apk/packages/luci-app-acceptance.list
test -f /lib/apk/packages/luci-app-acceptance.conffiles_static
# Translations: LuCI reads compiled .lmo and ignores .po, so a package that shipped
# the sources would install exactly like this one and have no translations.
test -s /usr/lib/lua/luci/i18n/acceptance-theme.ru.lmo
test -s /usr/lib/lua/luci/i18n/acceptance-theme.es.lmo
grep -q acceptance-theme.ru.lmo /lib/apk/packages/luci-app-acceptance.list
# Ownership: apk records owners by name, and an unresolvable uid becomes "nobody",
# so a package built by an ordinary user installs owned by nobody. busybox here has
# no stat applet, hence find.
[ -n "$(find /etc/config/acceptance -user root)" ]
[ -z "$(find /www/luci-static/acceptance -user nobody)" ]
# A trusted per-package signature makes this work without a flag, which is what
# LuCI's Upload Package flow needs since it cannot pass one.
apk del luci-app-acceptance >/dev/null
apk add /repo/releases/25.12/x86_64/luci-app-acceptance-1.0.0-r1.apk
echo ACCEPTED
`
	cmd := exec.Command("docker", "run", "--rm", "--platform", "linux/amd64",
		"-v", mustAbs(t, out)+":/repo:ro", routerImage, "sh", "-c", script)

	outBytes, err := cmd.CombinedOutput()
	t.Logf("router:\n%s", outBytes)
	if err != nil {
		t.Fatalf("installing on %s failed: %v", routerImage, err)
	}
	if !strings.Contains(string(outBytes), "ACCEPTED") {
		t.Fatal("the router script did not run to completion")
	}
	if strings.Contains(string(outBytes), "allow-untrusted") {
		t.Error("apk asked for --allow-untrusted, so the feed is not trusted as published")
	}
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}
