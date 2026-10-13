package config

import (
	"strings"
	"testing"
)

// minimal is the config from the design's "8 lines" case, plus the one field a mkpkg
// build cannot infer. Everything else must come from defaults.
const minimal = `
version: 1
feed:
  name: footstrap
  url: https://feed.example.org
publish:
  - target: github-pages
packages:
  - name: luci-theme-footstrap
    build: mkpkg
    arch: noarch
    version: 1.2.3-r1
    files: ./dist/root
`

func parse(t *testing.T, src string) (*Config, error) {
	t.Helper()
	return Parse(strings.NewReader(src), "owfeed.yml")
}

func mustParse(t *testing.T, src string) *Config {
	t.Helper()
	c, err := parse(t, src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return c
}

func TestMinimalConfigDefaults(t *testing.T) {
	c := mustParse(t, minimal)

	if c.Layout.Path != DefaultLayoutPath {
		t.Errorf("layout.path = %q, want %q", c.Layout.Path, DefaultLayoutPath)
	}
	if got := len(c.Releases); got != 1 {
		t.Fatalf("releases = %d, want 1 defaulted line", got)
	}
	r := c.DefaultRelease()
	if r.Line != DefaultReleaseLine {
		t.Errorf("release line = %q, want %q", r.Line, DefaultReleaseLine)
	}
	if !r.Auto() {
		t.Error("arches should default to auto; an explicit list is how feeds end up with a hand-maintained matrix")
	}
	if r.Format != "apk" {
		t.Errorf("format = %q, want apk", r.Format)
	}
	if !r.Default {
		t.Error("the only release line should be the default one")
	}
	// Signing each package is what makes `apk add ./file.apk` and LuCI's upload flow
	// work at all on 25.12, so it must not require opting in.
	if c.Signing.SignPackages == nil || !*c.Signing.SignPackages {
		t.Error("signing.sign-packages should default to true")
	}
	if c.Signing.Key != "env:"+DefaultKeyEnv {
		t.Errorf("signing.key = %q, want env:%s", c.Signing.Key, DefaultKeyEnv)
	}
	if c.Build.SDK.Release != LatestPoint {
		t.Errorf("build.sdk.release = %q, want %q", c.Build.SDK.Release, LatestPoint)
	}
}

// The single most important property of this package: a key we do not know stops the
// run. A tolerated typo in sign-packages produces a feed that works for the maintainer
// and fails for everyone else.
func TestUnknownKeyIsAnError(t *testing.T) {
	src := strings.Replace(minimal, "publish:", "signing:\n  sign_packages: true\npublish:", 1)
	_, err := parse(t, src)
	if err == nil {
		t.Fatal("Parse accepted an unknown key")
	}
	if !strings.Contains(err.Error(), "sign_packages") {
		t.Errorf("error does not name the offending key: %v", err)
	}
}

func TestRejects(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		wantSub string
	}{
		{
			// The single most common apk metadata mistake: "all" is what OpenWrt
			// Makefiles say, and apk rejects it as uninstallable.
			name:    "arch all",
			src:     strings.Replace(minimal, "arch: noarch", "arch: all", 1),
			wantSub: "noarch",
		},
		{
			// A typo here would otherwise build at the standard level without a word.
			name:    "unknown compression",
			src:     strings.Replace(minimal, "files: ./dist/root", "files: ./dist/root\n    compression: zopfli-max", 1),
			wantSub: "compression",
		},
		{
			name:    "missing version field",
			src:     strings.Replace(minimal, "version: 1\n", "", 1),
			wantSub: "version",
		},
		{
			name:    "http feed url",
			src:     strings.Replace(minimal, "https://feed.example.org", "http://feed.example.org", 1),
			wantSub: "redirect",
		},
		{
			// Every existing OpenWrt feed README recommends jsDelivr as the mirror for
			// blocked regions. It caches the index and its packages independently.
			name:    "jsdelivr",
			src:     strings.Replace(minimal, "https://feed.example.org", "https://cdn.jsdelivr.net/gh/o/r", 1),
			wantSub: "jsDelivr",
		},
		{
			// Would publish openwrt-foo.pem and shadow OpenWrt's own key, since the
			// first filename seen across the key directories wins.
			name:    "openwrt-prefixed feed name",
			src:     strings.Replace(minimal, "name: footstrap", "name: openwrt-foo", 1),
			wantSub: "shadow",
		},
		{
			name:    "point release as line",
			src:     strings.Replace(minimal, "publish:", "releases:\n  - line: \"25.12.5\"\npublish:", 1),
			wantSub: "build.sdk.release",
		},
		{
			name:    "both version and version-from",
			src:     strings.Replace(minimal, "version: 1.2.3-r1", "version: 1.2.3-r1\n    version-from: git-describe", 1),
			wantSub: "version-from",
		},
		{
			name:    "no version at all",
			src:     strings.Replace(minimal, "    version: 1.2.3-r1\n", "", 1),
			wantSub: "version",
		},
		{
			name:    "unknown script type",
			src:     strings.Replace(minimal, "    files: ./dist/root", "    files: ./dist/root\n    scripts:\n      postinst: ./x.sh", 1),
			wantSub: "post-install",
		},
		{
			name:    "relative conffile",
			src:     strings.Replace(minimal, "    files: ./dist/root", "    files: ./dist/root\n    conffiles: [\"etc/config/x\"]", 1),
			wantSub: "absolute",
		},
		{
			// An SDK build is a real thing to ask for and owfeed does not do it, by
			// design rather than by omission. Refusing by name and pointing at where
			// compilation lives beats building nothing and reporting success.
			name:    "sdk build refused",
			src:     strings.Replace(minimal, "  - name: luci-theme-footstrap\n    build: mkpkg\n", "  - path: net/foo\n", 1),
			wantSub: "does not compile",
		},
		{
			name:    "retention silently ignored",
			src:     minimal + "retention:\n  keep-versions: 2\n",
			wantSub: "not implemented",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse(t, tc.src)
			if err == nil {
				t.Fatalf("Parse accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

// Accepted both ways round now that the package is built. The default is on, and
// asking for it explicitly must not be an error — it was one for as long as the field
// was declared and unimplemented, and a test that still expected that would be
// asserting the old behaviour rather than the current one.
func TestKeyringPackageAccepted(t *testing.T) {
	c, err := parse(t, minimal)
	if err != nil {
		t.Fatalf("default keyring-package should not error: %v", err)
	}
	if c.Signing.KeyringPackage == nil || !*c.Signing.KeyringPackage {
		t.Error("keyring-package should default to on")
	}
	src := strings.Replace(minimal, "publish:", "signing:\n  keyring-package: true\npublish:", 1)
	if _, err := parse(t, src); err != nil {
		t.Errorf("explicitly requesting keyring-package: %v", err)
	}
	src = strings.Replace(minimal, "publish:", "signing:\n  keyring-package: false\npublish:", 1)
	c, err = parse(t, src)
	if err != nil {
		t.Fatalf("turning keyring-package off: %v", err)
	}
	if c.Signing.KeyringPackage == nil || *c.Signing.KeyringPackage {
		t.Error("keyring-package: false should be honoured")
	}
}

func TestCompressionZopfliAccepted(t *testing.T) {
	c := mustParse(t, strings.Replace(minimal, "files: ./dist/root", "files: ./dist/root\n    compression: zopfli", 1))
	if c.Packages[0].Compression != CompressionZopfli {
		t.Errorf("compression = %q", c.Packages[0].Compression)
	}
	if mustParse(t, minimal).Packages[0].Compression != "" {
		t.Error("compression must default to unset: the standard level")
	}
}

// The dash appears only when the base name already ends in a digit — which is why the
// ecosystem contains both libjson-c5 and libblobmsg-json20260213.
func TestEffectiveNameABISuffix(t *testing.T) {
	tests := []struct{ name, abi, want string }{
		{"libjson-c", "5", "libjson-c5"},
		{"libblobmsg-json", "20260213", "libblobmsg-json20260213"},
		{"libfoo2", "3", "libfoo2-3"},
		{"libfoo", "", "libfoo"},
	}
	for _, tc := range tests {
		p := Package{Name: tc.name, ABIVersion: tc.abi}
		if got := p.EffectiveName(); got != tc.want {
			t.Errorf("EffectiveName(%q, abi %q) = %q, want %q", tc.name, tc.abi, got, tc.want)
		}
	}
}

func TestArchesAcceptsAutoOrList(t *testing.T) {
	c := mustParse(t, strings.Replace(minimal, "publish:",
		"releases:\n  - line: \"25.12\"\n    arches: [x86_64, aarch64_cortex-a53]\npublish:", 1))
	got := c.Releases[0].Arches
	if got.Auto || len(got.List) != 2 {
		t.Fatalf("arches = %+v, want an explicit list of 2", got)
	}

	if _, err := parse(t, strings.Replace(minimal, "publish:",
		"releases:\n  - line: \"25.12\"\n    arches: everything\npublish:", 1)); err == nil {
		t.Error("Parse accepted a bogus arches scalar")
	}
}

func TestDuplicatePackageName(t *testing.T) {
	dup := minimal + `  - name: luci-theme-footstrap
    build: mkpkg
    arch: noarch
    version: 2.0-r1
    files: ./dist/root2
`
	if _, err := parse(t, dup); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("Parse(duplicate) = %v, want a duplicate-name error", err)
	}
}

// A static binary — Go, Rust — needs no OpenWrt SDK, only the right GOARCH, so the
// SDK-less path is not restricted to noarch.
func TestMultiArchPackage(t *testing.T) {
	src := strings.Replace(minimal,
		"    arch: noarch\n    version: 1.2.3-r1\n    files: ./dist/root",
		"    arch: [x86_64, aarch64_cortex-a53]\n    version: 1.2.3-r1\n    files: ./dist/{arch}/root", 1)

	c := mustParse(t, src)
	got := c.Packages[0].Arch
	if got.IsNoarch() || len(got.List) != 2 {
		t.Fatalf("arch = %+v, want two architectures", got)
	}

	// Two architectures cannot share one payload: if they could, the package would
	// be noarch. Requiring the template makes the mistake impossible.
	noTemplate := strings.Replace(src, "./dist/{arch}/root", "./dist/root", 1)
	if _, err := parse(t, noTemplate); err == nil || !strings.Contains(err.Error(), "{arch}") {
		t.Errorf("Parse(no {arch}) = %v, want it to demand the placeholder", err)
	}

	// noarch already installs everywhere; naming it alongside a real architecture is
	// a contradiction, not a union.
	mixed := strings.Replace(src, "[x86_64, aarch64_cortex-a53]", "[noarch, x86_64]", 1)
	if _, err := parse(t, mixed); err == nil || !strings.Contains(err.Error(), "noarch") {
		t.Errorf("Parse(noarch + arch) = %v, want it rejected", err)
	}

	// A single architecture is still fine without a template.
	one := strings.Replace(src, "[x86_64, aarch64_cortex-a53]", "x86_64", 1)
	one = strings.Replace(one, "./dist/{arch}/root", "./dist/root", 1)
	if _, err := parse(t, one); err != nil {
		t.Errorf("Parse(single arch) = %v", err)
	}
}

// An author publishing packages for someone else's feed to carry has an ipk line
// and never builds an index: `owfeed release` signs a manifest, and the feed at the
// far end signs the index with its own key. Requiring the index-signing key at load
// time made `owfeed build` refuse to run for that whole shape of use, over a key it
// would never have touched. The requirement belongs to `owfeed index`, which is
// where the key is read and where it already lives.
func TestIPKLineWithoutAnIndexKeyLoads(t *testing.T) {
	c, err := parse(t, `
version: 1
feed:
  name: example-daemon
  url: https://github.com/someone/example-daemon
releases:
  - line: "25.12"
    default: true
    format: apk
  - line: "24.10"
    format: ipk
publish:
  - target: github-pages
packages:
  - name: example-daemon
    build: mkpkg
    arch: [x86_64]
    version: 1.2.3-r1
    files: ./dist/root/{arch}
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Signing.UsignKey != "" {
		t.Errorf("usign key = %q, want empty", c.Signing.UsignKey)
	}
	if got := len(c.Releases); got != 2 {
		t.Fatalf("releases = %d, want 2", got)
	}
	if c.Releases[1].Format != FormatIPK {
		t.Errorf("releases[1].format = %q, want ipk", c.Releases[1].Format)
	}
}

// A feed that carries only other people's work builds nothing at all: every package
// is fetched already built and signed by its author, which is the shape a feed
// should be aiming for -- rebuilding someone's package ships something they never
// tested. This used to be a config error, which made that shape unreachable.
func TestNoPackagesIsAFeedThatOnlyCarries(t *testing.T) {
	src := minimal[:strings.Index(minimal, "packages:")]
	c, err := parse(t, src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(c.Packages) != 0 {
		t.Errorf("packages = %d, want none", len(c.Packages))
	}
}
