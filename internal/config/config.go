// Package config parses owfeed.yml.
//
// Unknown keys are an error, not a warning. A feed is defined by a file the user
// rarely reads back, and the failure mode of tolerating typos is the worst kind:
// `sign-packages` misspelled as `sign_packages` silently produces an unsigned feed
// that works fine for the maintainer (who has the key) and fails for every subscriber.
//
// Fields that the schema defines but this version does not act on are also errors.
// Silently ignoring a `retention:` block that the user wrote to bound their storage
// is worse than refusing to run.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the only value accepted in the top-level `version` field.
const SchemaVersion = 1

// Config is a parsed owfeed.yml.
type Config struct {
	Version  int       `yaml:"version"`
	Feed     Feed      `yaml:"feed"`
	Layout   Layout    `yaml:"layout"`
	Releases []Release `yaml:"releases"`
	Signing  Signing   `yaml:"signing"`
	Build    Build     `yaml:"build"`
	Packages []Package `yaml:"packages"`
	Publish  []Publish `yaml:"publish"`

	// Declared so that writing them is a clear "not in this version" error rather
	// than a silent no-op. See notImplemented.
	// Typed as free-form maps rather than yaml.Node: strict decoding recurses, and a
	// yaml.Node field would have every key inside these blocks checked against
	// yaml.Node's own fields.
	Overrides []map[string]any `yaml:"overrides"`
	Retention map[string]any   `yaml:"retention"`
}

// Feed identifies the feed and where it will be served from.
type Feed struct {
	// Name seeds every derived filename: <name>.pem, <name>.list, <name>-keyring.
	Name string `yaml:"name"`
	// URL is the final, user-facing base URL. It must be the address apk will
	// actually fetch: apk does not follow redirects with the stock uclient-fetch, so
	// an apex that redirects to www, or http that upgrades to https, is a broken feed
	// even though it works in a browser. `owfeed doctor` proves this against the live
	// URL; here we only reject the shapes that are wrong on their face.
	URL         string `yaml:"url"`
	Title       string `yaml:"title"`
	Maintainer  string `yaml:"maintainer"`
	License     string `yaml:"license"`
	Homepage    string `yaml:"homepage"`
	Description string `yaml:"description"`
}

// Layout controls the directory shape under Feed.URL.
type Layout struct {
	// Path is templated with {release} and {arch}. The default matches what the
	// ecosystem already publishes, so install snippets look native.
	Path string `yaml:"path"`
}

const DefaultLayoutPath = "releases/{release}/{arch}"

// ExpandLayout fills in the layout template. Every part of owfeed that names a
// published path goes through this, so the directory the index is written to, the
// URL the snippet documents and the path a check fetches cannot drift apart.
func ExpandLayout(path, release, arch string) string {
	if path == "" {
		path = DefaultLayoutPath
	}
	path = strings.ReplaceAll(path, "{release}", release)
	return strings.ReplaceAll(path, "{arch}", arch)
}

// LayoutPath is the directory one (release, arch) publishes into, relative to the
// feed root.
func (c *Config) LayoutPath(release, arch string) string {
	return ExpandLayout(c.Layout.Path, release, arch)
}

// Release is one OpenWrt release line.
type Release struct {
	// Line is a major.minor line such as "25.12", or "snapshot".
	Line string `yaml:"line"`
	// Default marks the line advertised in the install snippet.
	Default bool `yaml:"default"`
	// Arches is "auto" (derived from downloads.openwrt.org and pinned in owfeed.lock)
	// or an explicit list. Explicit lists are how every existing feed ends up with a
	// hand-maintained 36-entry matrix, so "auto" is the default.
	Arches Arches `yaml:"arches"`
	// Prerelease builds and publishes the line without advertising it.
	Prerelease bool `yaml:"prerelease"`
	// Format is "apk" or "ipk": apk for 25.12 and later, ipk for 24.10 and earlier.
	// Both are implemented, and a feed serving both lines signs each with the scheme
	// its package manager verifies -- see Signing.UsignKey.
	Format string `yaml:"format"`
}

// Arches is either the literal "auto" or an explicit list of architectures.
type Arches struct {
	Auto bool
	List []string
}

func (a *Arches) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		if node.Value != "auto" {
			return fmt.Errorf("arches: %q is not valid; use \"auto\" or a list", node.Value)
		}
		a.Auto = true
		return nil
	}
	if node.Kind != yaml.SequenceNode {
		return errors.New("arches: want \"auto\" or a list of architectures")
	}
	return node.Decode(&a.List)
}

// Signing controls how the feed is signed.
type Signing struct {
	// Key is "env:VAR" or "file:PATH".
	Key string `yaml:"key"`
	// SignPackages also signs each .apk, not just the index.
	//
	// On by default, because it fixes two paths that are otherwise broken for
	// third-party packages: `apk add ./file.apk` and LuCI's Upload Package flow,
	// which cannot pass --allow-untrusted since package-manager-call drops
	// unrecognised arguments. OpenWrt's own packages are unsigned individually, so
	// both already require the flag for everything in the official feeds.
	//
	// Turn it off in a feed that carries other people's packages and does not want
	// to put its own signature inside a file somebody else built. Nothing else
	// changes: the index is still signed, and that is where a router takes its
	// trust from. Measured on 25.12.5 against an unsigned package in a signed
	// index — install, upgrade and remove by name all return 0, through LuCI's own
	// package-manager-call, and /etc/apk/world carries no content pin.
	SignPackages *bool `yaml:"sign-packages"`
	// UsignKey signs opkg indexes and release manifests, as "env:VAR" or "file:PATH".
	//
	// It is a second key and cannot be avoided: opkg verifies usign/ed25519 while
	// apk verifies EC prime256v1, so a feed serving both release lines signs each
	// with the scheme its package manager understands.
	UsignKey string `yaml:"usign-key"`
	// AlsoSign are additional keys the index is signed with, as "env:VAR" or
	// "file:PATH". Empty outside a rotation.
	//
	// Rotation needs an overlap or it cannot start at all. The keyring package
	// delivers the new key through `apk upgrade`, but a router can only fetch it from
	// an index it already trusts — so an index signed by the new key alone is one the
	// subscriber rejects, and the package that would have fixed that is behind it.
	// Measured on 25.12.5: the upgrade returns UNTRUSTED signature and the rotation
	// never begins.
	//
	// With both keys here the index carries both signatures, everyone keeps working,
	// and the entry is removed once subscribers have had time to upgrade.
	AlsoSign []string `yaml:"also-sign"`
	// AuthorKeys is a directory of pinned author public keys. When set, every
	// package published must carry a signature by one of them.
	//
	// For a feed that carries other people's work this is the only claim that
	// survives the feed itself. The index proves that this feed published a file;
	// an author signature proves who built it, and can be checked by somebody who
	// does not trust the feed at all. Without it, "the author is responsible for
	// this package" is a statement with nothing behind it.
	//
	// The pinned key is the source of truth. A key that arrives beside a release
	// proves nothing on its own — whoever replaced the package would replace it too
	// — so it is only ever compared against what is pinned here, which is a diff a
	// person had to approve.
	AuthorKeys string `yaml:"author-keys"`
	// KeyringPackage ships a <name>-keyring package carrying the feed's public key,
	// which is the only way a rotated key reaches already-installed routers.
	KeyringPackage *bool `yaml:"keyring-package"`
}

// Build controls package construction.
type Build struct {
	SDK         SDK  `yaml:"sdk"`
	ChangedOnly bool `yaml:"changed-only"`
}

// SDK pins the toolchain.
type SDK struct {
	// Release is a concrete point release such as "25.12.5", or "latest-point".
	// Never SNAPSHOT: those tarballs are rotated on the mirrors, so a checksum can
	// legitimately fail to match the bytes that arrive moments later.
	Release string `yaml:"release"`
}

const LatestPoint = "latest-point"

// Package formats. apk is 25.12 and later; ipk is 24.10 and earlier, where the
// package manager is opkg and almost every detail differs.
const (
	FormatAPK = "apk"
	FormatIPK = "ipk"
)

// BuildMode selects how a package is produced.
type BuildMode string

const (
	// BuildMkpkg stages a rootfs and calls apk mkpkg. No SDK build, no cross
	// toolchain, seconds rather than tens of minutes — correct for anything
	// architecture-independent, which is most of what third parties ship.
	BuildMkpkg BuildMode = "mkpkg"
	// BuildSDK compiles through the OpenWrt SDK, which owfeed does not do and is not
	// going to. It is recognised so the config can be refused by name, with somewhere
	// to go, rather than as an unknown key.
	//
	// Compilation belongs to a development tool: the target table it needs moves with
	// every OpenWrt release, and the reason to build a package before publishing it --
	// luci.mk minifies JS and CSS on the way in, so "works unminified, breaks minified"
	// is invisible until something builds it -- is an argument from testing, not from
	// distribution. Build with owlab, openwrt/gh-action-sdk, or a hand-written SDK call,
	// and hand owfeed the directory. See docs/artifact-contract.md.
	BuildSDK BuildMode = "sdk"
)

// Package is one package to build.
type Package struct {
	// Path points at a directory containing an OpenWrt Makefile (SDK build).
	Path string `yaml:"path"`

	// Name, with Build: mkpkg, names a package built from a staged rootfs.
	Name  string    `yaml:"name"`
	Build BuildMode `yaml:"build"`

	// Arch is "noarch" for architecture-independent packages, or a list of
	// architectures for one carrying compiled code. It is never "all": apk rejects
	// "all" as uninstallable, which is why OpenWrt's own package-pack.mk translates
	// LUCI_PKGARCH:=all into arch:noarch.
	Arch PkgArch `yaml:"arch"`

	// Version is a literal version, mutually exclusive with VersionFrom.
	Version string `yaml:"version"`
	// VersionFrom reads the version from somewhere: "makefile:PATH", "file:PATH",
	// or "git-describe".
	VersionFrom string `yaml:"version-from"`

	// Releases are the release lines this package is published to. Empty means all
	// of them, which is right for anything that runs on both — but a package that
	// depends on something only one line has must say so, or it lands in a feed
	// where it cannot resolve.
	Releases []string `yaml:"releases"`

	// Files is the staged rootfs handed to `apk mkpkg --files`.
	//
	// It may contain {arch}, which is required when the package names more than one
	// architecture: those packages differ by definition, so one payload cannot serve
	// them all.
	Files string `yaml:"files"`

	Description string `yaml:"description"`
	License     string `yaml:"license"`
	// URL is where the package comes from. In a feed that carries other people's
	// work this is not decoration: it is the only thing in the installed package
	// that says who to go to when it misbehaves, and `owfeed doctor` can be told to
	// require it.
	URL string `yaml:"url"`
	// RepoCommit is the commit the package was built from, recorded in apk's
	// repo-commit field. "env:VAR" reads it from the environment, which is how a CI
	// job passes $CI_COMMIT_SHA or $GITHUB_SHA without the value being committed.
	RepoCommit string   `yaml:"repo-commit"`
	Maintainer string   `yaml:"maintainer"`
	Depends    []string `yaml:"depends"`
	Provides   []string `yaml:"provides"`
	Replaces   []string `yaml:"replaces"`
	Recommends []string `yaml:"recommends"`

	// InstallIf makes apk install this package BY ITSELF once every entry here is
	// satisfied — the mechanism a translation package needs and the one thing
	// `depends` cannot express: a catalogue depends on its application, so installing
	// the application never pulls the catalogue in, and a user upgrading a theme
	// silently loses the language they had (measured on luci-theme-footstrap 0.14.3 ->
	// 0.14.4: the catalogues left with the old package and nothing named the new one).
	//
	// apk requires at least two entries, one of them pinned with `=`, which is also
	// what makes it re-fire on every upgrade of the pinned package:
	//
	//     install-if: [ "luci-theme-footstrap={version}", "luci-i18n-base-ru" ]
	//
	// {version} expands to the version this build is producing, so a release does not
	// hand-edit the pin in a file no gate reads.
	//
	// opkg has no conditional equivalent. `Recommends:` is executed by OpenWrt's opkg
	// but is unconditional, so the ipk leg of a package using this simply does not get
	// the behaviour; that is a property of the format, not of this field.
	InstallIf []string `yaml:"install-if"`

	// Conflicts are packages that must not be installed alongside this one. apk
	// spells a conflict as a negative dependency, so these become !name entries in
	// depends.
	//
	// OpenWrt's own apk build drops CONFLICTS on the floor: package-pack.mk emits it
	// only into the ipk control file and never passes it to mkpkg. A package whose
	// Makefile declares a conflict therefore does not enforce it on 25.12 at all,
	// which is how two packages that both rewrite the routing table end up installed
	// together.
	Conflicts []string `yaml:"conflicts"`

	// Conffiles become /lib/apk/packages/<name>.conffiles and .conffiles_static.
	// sysupgrade reads the latter to decide which config files to carry across an
	// upgrade, so omitting it silently loses the user's configuration.
	Conffiles []string `yaml:"conffiles"`

	// Scripts maps an apk script type (post-install, pre-deinstall, ...) to a file.
	Scripts map[string]string `yaml:"scripts"`

	// I18n compiles gettext catalogues into the payload. LuCI reads compiled .lmo
	// files and ignores .po entirely, so without this a package's translations
	// simply do not exist on the router.
	I18n *I18n `yaml:"i18n"`

	// ABIVersion is appended to the package name and mirrored into
	// tags:openwrt:abiversion, which ImageBuilder needs to resolve the dependency.
	ABIVersion string `yaml:"abiversion"`
}

// Noarch is the architecture of a package that carries no compiled code.
//
// It is not "all". apk rejects "all" as uninstallable, which is why OpenWrt's own
// package-pack.mk rewrites LUCI_PKGARCH:=all into arch:noarch on its way to mkpkg.
const Noarch = "noarch"

// PkgArch is a package's architecture: the single value "noarch", or a list of the
// architectures a compiled package is built for.
//
// A static binary — Go, Rust, anything with no libc dependency to cross-link —
// needs no OpenWrt SDK, only the right GOARCH. Restricting the SDK-less path to
// noarch would have excluded that whole class for no reason.
type PkgArch struct {
	List []string
}

func (a *PkgArch) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		a.List = []string{node.Value}
		return nil
	}
	if node.Kind != yaml.SequenceNode {
		return errors.New("arch: want an architecture name or a list of them")
	}
	return node.Decode(&a.List)
}

// IsNoarch reports whether this package is architecture-independent.
func (a PkgArch) IsNoarch() bool {
	return len(a.List) == 1 && a.List[0] == Noarch
}

func (a PkgArch) String() string { return strings.Join(a.List, ", ") }

// I18n describes a package's translation catalogues.
type I18n struct {
	// From is a directory laid out as <lang>/*.po, which is where both LuCI's own
	// po/ convention and the i18n/ variant put them.
	From string `yaml:"from"`

	// Basename names the compiled catalogues: <basename>.<lang>.lmo. It defaults to
	// the .po file's own name, which is what luci.mk uses.
	//
	// It is worth setting deliberately. LuCI's loader globs *.<lang>.lmo, so any
	// basename is found — but if a package previously shipped its translations
	// through a separate luci-i18n-<name>-<lang> package, a router upgrading from
	// that release still owns the old path. Reusing it is a file conflict, and apk
	// refuses the upgrade. luci-theme-footstrap moved to footstrap-theme.<lang>.lmo
	// for exactly this reason.
	Basename string `yaml:"basename"`

	// Dest is where the catalogues are installed. The default is where LuCI looks.
	Dest string `yaml:"dest"`
}

// DefaultI18nDest is LUCI_LIBRARYDIR/i18n, which is where lmo_load_catalog scans.
const DefaultI18nDest = "/usr/lib/lua/luci/i18n"

// Publish is one destination.
type Publish struct {
	Target string `yaml:"target"`

	// s3 / rsync fields, declared for schema completeness.
	Bucket       string            `yaml:"bucket"`
	Endpoint     string            `yaml:"endpoint"`
	Region       string            `yaml:"region"`
	Prefix       string            `yaml:"prefix"`
	Credentials  string            `yaml:"credentials"`
	CacheControl map[string]string `yaml:"cache-control"`
	Dest         string            `yaml:"dest"`
}

// Publish target names.
const (
	TargetGitHubPages = "github-pages"
	TargetS3          = "s3"
	TargetRsync       = "rsync"
)

// Error is a configuration problem. It maps to exit code 2.
type Error struct {
	Path string
	Msg  string
	Hint string
}

func (e *Error) Error() string {
	s := e.Msg
	if e.Path != "" {
		s = e.Path + ": " + s
	}
	if e.Hint != "" {
		s += "\n  " + e.Hint
	}
	return s
}

func errf(path, format string, args ...any) *Error {
	return &Error{Path: path, Msg: fmt.Sprintf(format, args...)}
}

// Load reads and validates a config file.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f, path)
}

// Parse reads a config from r. name is used in error messages.
func Parse(r io.Reader, name string) (*Config, error) {
	dec := yaml.NewDecoder(r)
	// The whole point: a key we do not recognise stops the run.
	dec.KnownFields(true)

	var c Config
	if err := dec.Decode(&c); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errf(name, "file is empty")
		}
		return nil, &Error{
			Path: name,
			Msg:  err.Error(),
			Hint: "owfeed rejects unknown keys rather than ignoring them; check for a typo or a field from a newer schema version",
		}
	}

	if err := c.applyDefaults(); err != nil {
		return nil, err
	}
	if err := c.validate(name); err != nil {
		return nil, err
	}
	return &c, nil
}

func boolPtr(b bool) *bool { return &b }

func (c *Config) applyDefaults() error {
	if c.Layout.Path == "" {
		c.Layout.Path = DefaultLayoutPath
	}
	if len(c.Releases) == 0 {
		// A config that names no release line means "the current apk line".
		c.Releases = []Release{{Line: DefaultReleaseLine, Default: true, Arches: Arches{Auto: true}}}
	}
	if c.Signing.SignPackages == nil {
		c.Signing.SignPackages = boolPtr(true)
	}
	if c.Signing.KeyringPackage == nil {
		c.Signing.KeyringPackage = boolPtr(true)
	}
	if c.Signing.Key == "" {
		c.Signing.Key = "env:" + DefaultKeyEnv
	}
	if c.Build.SDK.Release == "" {
		c.Build.SDK.Release = LatestPoint
	}

	defaulted := false
	for i := range c.Releases {
		if c.Releases[i].Format == "" {
			c.Releases[i].Format = "apk"
		}
		if !c.Releases[i].Auto() && len(c.Releases[i].Arches.List) == 0 {
			c.Releases[i].Arches.Auto = true
		}
		if c.Releases[i].Default {
			defaulted = true
		}
	}
	if !defaulted && len(c.Releases) > 0 {
		c.Releases[0].Default = true
	}
	return nil
}

// DefaultReleaseLine is the line assumed when the config names none.
const DefaultReleaseLine = "25.12"

// DefaultKeyEnv is the environment variable a signing key is read from by default.
const DefaultKeyEnv = "OWFEED_SIGN_KEY"

// PublishedOn reports whether a package belongs on a release line.
func (p Package) PublishedOn(line string) bool {
	if len(p.Releases) == 0 {
		return true
	}
	for _, r := range p.Releases {
		if r == line {
			return true
		}
	}
	return false
}

// Auto reports whether this release derives its architecture set.
func (r Release) Auto() bool { return r.Arches.Auto }

// DefaultRelease returns the line advertised to users.
func (c *Config) DefaultRelease() Release {
	for _, r := range c.Releases {
		if r.Default {
			return r
		}
	}
	return c.Releases[0]
}
