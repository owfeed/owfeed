package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// nameRE is what a feed or package name may look like. apk package names are
// dependency-notation tokens, so anything that could be read as a version operator
// or a tag separator is out.
var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._+-]*$`)

func (c *Config) validate(path string) error {
	if c.Version == 0 {
		return &Error{Path: path, Msg: "missing `version`", Hint: fmt.Sprintf("add `version: %d`", SchemaVersion)}
	}
	if c.Version != SchemaVersion {
		return errf(path, "version %d is not supported (this owfeed understands version %d)", c.Version, SchemaVersion)
	}

	if err := c.validateFeed(path); err != nil {
		return err
	}
	if err := c.validateReleases(path); err != nil {
		return err
	}
	if err := c.validateSigning(path); err != nil {
		return err
	}
	if err := c.validatePackages(path); err != nil {
		return err
	}
	if err := c.validatePublish(path); err != nil {
		return err
	}
	return c.notImplemented(path)
}

func (c *Config) validateFeed(path string) error {
	switch {
	case c.Feed.Name == "":
		return errf(path, "feed.name is required")
	case !nameRE.MatchString(c.Feed.Name):
		return &Error{
			Path: path,
			Msg:  fmt.Sprintf("feed.name %q is not a usable package-name token", c.Feed.Name),
			Hint: "lowercase letters, digits, and . _ + - only, starting with a letter or digit",
		}
	}

	// A feed named openwrt-something would publish openwrt-something.pem, and key
	// directories are scanned /etc/apk/keys then /lib/apk/keys with the first
	// filename winning — so it could shadow OpenWrt's own key and break the device's
	// ability to verify the official feed.
	if strings.HasPrefix(c.Feed.Name, "openwrt") {
		return &Error{
			Path: path,
			Msg:  fmt.Sprintf("feed.name %q would publish %s.pem", c.Feed.Name, c.Feed.Name),
			Hint: "apk scans /etc/apk/keys before /lib/apk/keys and the first file of a given name wins, so an openwrt-* name can shadow OpenWrt's own signing key; pick another name",
		}
	}

	if c.Feed.URL == "" {
		return errf(path, "feed.url is required")
	}
	u, err := url.Parse(c.Feed.URL)
	if err != nil {
		return errf(path, "feed.url is not a URL: %v", err)
	}
	switch {
	case u.Scheme == "http":
		return &Error{
			Path: path,
			Msg:  "feed.url uses http",
			Hint: "hosts that serve http usually redirect to https, and apk does not follow redirects with the stock uclient-fetch (openwrt#17180); use the https URL directly",
		}
	case u.Scheme != "https":
		return errf(path, "feed.url scheme %q is not supported; use https", u.Scheme)
	case u.Host == "":
		return errf(path, "feed.url has no host")
	case u.RawQuery != "" || u.Fragment != "":
		return errf(path, "feed.url must be a plain base URL, without a query or fragment")
	}

	// jsDelivr is what every existing OpenWrt feed README offers as the mirror "for
	// regions where GitHub may be blocked". It is the wrong recommendation: it lost
	// its China ICP licence in 2021, and it caches packages.adb and the .apk files it
	// references independently, so a subscriber can get a fresh index and a stale
	// package and see an integrity failure.
	if strings.Contains(u.Host, "jsdelivr.net") {
		return &Error{
			Path: path,
			Msg:  "feed.url points at jsDelivr",
			Hint: "jsDelivr caches the index and the packages it references independently, so subscribers hit signature and integrity failures during cache skew; it is also blocked in China, which is the case it is usually recommended for. Publish to an origin you control",
		}
	}
	return nil
}

var releaseLineRE = regexp.MustCompile(`^([0-9]{2}\.[0-9]{2}|snapshot)$`)

func (c *Config) validateReleases(path string) error {
	seen := map[string]bool{}
	defaults := 0
	for i, r := range c.Releases {
		where := fmt.Sprintf("releases[%d]", i)
		if r.Line == "" {
			return errf(path, "%s.line is required", where)
		}
		if !releaseLineRE.MatchString(r.Line) {
			return &Error{
				Path: path,
				Msg:  fmt.Sprintf("%s.line %q is not a release line", where, r.Line),
				Hint: "use a major.minor line such as 25.12, or snapshot; point releases like 25.12.5 belong in build.sdk.release",
			}
		}
		if seen[r.Line] {
			return errf(path, "%s.line %q is listed twice", where, r.Line)
		}
		seen[r.Line] = true

		switch r.Format {
		case FormatAPK, FormatIPK:
		default:
			return &Error{
				Path: path,
				Msg:  fmt.Sprintf("%s.format %q is not a package format", where, r.Format),
				Hint: "apk for 25.12 and later, ipk for 24.10 and earlier",
			}
		}
		// The usign key an ipk line needs is checked by `owfeed index`, which is
		// where it is used, and not here.
		//
		// An author publishing packages for someone else's feed to carry has an ipk
		// line and never builds an index: `owfeed release` signs a manifest, and the
		// feed at the far end signs the index with its own key. Requiring the index
		// key at load time made `owfeed build` refuse to run for that whole shape of
		// use, over a key it would not have touched.
		if r.Default {
			defaults++
		}
		for _, a := range r.Arches.List {
			if a == "all" {
				return &Error{
					Path: path,
					Msg:  fmt.Sprintf("%s.arches lists \"all\"", where),
					Hint: "apk rejects arch \"all\" as uninstallable; architecture-independent packages use \"noarch\"",
				}
			}
		}
	}
	if defaults > 1 {
		return errf(path, "more than one release is marked default")
	}
	return nil
}

func (c *Config) validateSigning(path string) error {
	scheme, rest, ok := strings.Cut(c.Signing.Key, ":")
	if !ok || rest == "" {
		return &Error{
			Path: path,
			Msg:  fmt.Sprintf("signing.key %q has no source", c.Signing.Key),
			Hint: "use env:VARNAME or file:PATH",
		}
	}
	switch scheme {
	case "env", "file":
	default:
		return errf(path, "signing.key source %q is not supported; use env: or file:", scheme)
	}

	if c.Signing.UsignKey != "" {
		switch s, rest, ok := strings.Cut(c.Signing.UsignKey, ":"); {
		case !ok || rest == "":
			return errf(path, "signing.usign-key %q has no source; use env:VARNAME or file:PATH", c.Signing.UsignKey)
		case s != "env" && s != "file":
			return errf(path, "signing.usign-key source %q is not supported; use env: or file:", s)
		}
	}
	return nil
}

func (c *Config) validatePackages(path string) error {
	// An empty list is legal. A feed that carries other people's work builds nothing
	// at all: every package is fetched already built and signed by its author, which
	// is the shape a feed should be aiming for -- rebuilding someone's package ships
	// something they never tested. `owfeed build` says there is nothing to do and
	// stops; what actually reaches subscribers is checked against the tree, by
	// `owfeed index` and by whatever the feed uses to confirm its fetch was complete.

	seenLine := map[string]bool{}
	for _, r := range c.Releases {
		seenLine[r.Line] = true
	}

	seen := map[string]bool{}
	for i, p := range c.Packages {
		where := fmt.Sprintf("packages[%d]", i)

		mode, err := p.mode()
		if err != nil {
			return &Error{Path: path, Msg: where + ": " + err.Error()}
		}
		if mode == BuildSDK {
			return &Error{
				Path: path,
				Msg:  fmt.Sprintf("%s builds through the SDK, which owfeed does not do", where),
				Hint: "owfeed packages, it does not compile: build elsewhere -- owlab, openwrt/gh-action-sdk, or your own SDK call -- and leave the result in dist/<arch>/, which every later stage reads. See docs/artifact-contract.md. To stage a finished tree here instead, set `build: mkpkg`, `arch: noarch`, and `files:`",
			}
		}

		name := p.EffectiveName()
		if name == "" {
			return errf(path, "%s.name is required", where)
		}
		if !nameRE.MatchString(name) {
			return &Error{
				Path: path,
				Msg:  fmt.Sprintf("%s.name %q is not a usable package name", where, name),
				Hint: "lowercase letters, digits, and . _ + - only",
			}
		}
		if seen[name] {
			return errf(path, "%s: package %q is defined twice", where, name)
		}
		seen[name] = true

		if err := validateArch(path, where, p); err != nil {
			return err
		}
		// A line nobody configured is a package that silently goes nowhere.
		for _, line := range p.Releases {
			if !seenLine[line] {
				return &Error{
					Path: path,
					Msg:  fmt.Sprintf("%s.releases names %q, which is not a configured release line", where, line),
					Hint: "add the line under `releases:`, or drop it here to publish on every line",
				}
			}
		}

		if p.Files == "" {
			return &Error{
				Path: path,
				Msg:  fmt.Sprintf("%s.files is required for a mkpkg build", where),
				Hint: "point it at a staged rootfs: the directory whose contents become the package payload",
			}
		}
		// Two architectures cannot share one payload — if they could, the package
		// would be noarch. Requiring the template makes the mistake impossible rather
		// than merely detectable.
		if len(p.Arch.List) > 1 && !strings.Contains(p.Files, ArchPlaceholder) {
			return &Error{
				Path: path,
				Msg:  fmt.Sprintf("%s builds for %d architectures but `files:` names one directory", where, len(p.Arch.List)),
				Hint: "put " + ArchPlaceholder + " in the path, e.g. files: ./staging/" + ArchPlaceholder +
					"; a payload that is the same for every architecture is a noarch package",
			}
		}
		if p.Version != "" && p.VersionFrom != "" {
			return errf(path, "%s sets both version and version-from", where)
		}
		if p.Version == "" && p.VersionFrom == "" {
			return &Error{
				Path: path,
				Msg:  fmt.Sprintf("%s has no version", where),
				Hint: "set `version:` or `version-from:` (tag, makefile:PATH or file:PATH)",
			}
		}
		if p.VersionFrom != "" {
			switch scheme, _, _ := strings.Cut(p.VersionFrom, ":"); scheme {
			case "tag", "makefile", "file":
			case "git-describe":
				// `git describe` produces v1.2.3-4-gabcdef, which apk cannot parse,
				// and every mapping onto apk's grammar is a decision about which of
				// two builds counts as newer. Making that decision silently is worse
				// than not making it.
				return &Error{
					Path: path,
					Msg:  fmt.Sprintf("%s uses version-from: git-describe, which is not implemented yet", where),
					Hint: "git describe output is not an apk version; set `version:` explicitly, or point version-from at a Makefile or a file holding one",
				}
			default:
				return errf(path, "%s.version-from %q is not a version source; use tag, makefile:PATH or file:PATH", where, p.VersionFrom)
			}
		}
		for _, cf := range p.Conffiles {
			if !strings.HasPrefix(cf, "/") {
				return errf(path, "%s.conffiles entry %q must be an absolute path inside the package", where, cf)
			}
		}
		if p.I18n != nil {
			if p.I18n.From == "" {
				return errf(path, "%s.i18n.from is required; point it at the directory holding <lang>/*.po", where)
			}
			if p.I18n.Dest != "" && !strings.HasPrefix(p.I18n.Dest, "/") {
				return errf(path, "%s.i18n.dest %q must be an absolute path inside the package", where, p.I18n.Dest)
			}
		}
		if p.Compression != "" && p.Compression != CompressionZopfli {
			return &Error{
				Path: path,
				Msg:  fmt.Sprintf("%s.compression %q is not supported", where, p.Compression),
				Hint: "leave it unset for deflate at its best standard level, or set `compression: zopfli` for smaller packages at the cost of build time",
			}
		}
		for t := range p.Scripts {
			if !validScriptTypes[t] {
				return &Error{
					Path: path,
					Msg:  fmt.Sprintf("%s.scripts has unknown type %q", where, t),
					Hint: "valid types: " + strings.Join(scriptTypeList, ", "),
				}
			}
		}
	}
	return nil
}

// ArchPlaceholder is substituted into `files:` for a per-architecture build.
const ArchPlaceholder = "{arch}"

// archNameRE is the shape of an OpenWrt architecture directory name.
var archNameRE = regexp.MustCompile(`^[a-z0-9]+(_[a-z0-9.+-]+)*$`)

func validateArch(path, where string, p Package) error {
	if len(p.Arch.List) == 0 {
		return &Error{
			Path: path,
			Msg:  fmt.Sprintf("%s.arch is required", where),
			Hint: "use \"noarch\" for architecture-independent packages, or list the architectures a compiled package is built for",
		}
	}

	seen := map[string]bool{}
	for _, a := range p.Arch.List {
		switch {
		case a == "all":
			return &Error{
				Path: path,
				Msg:  fmt.Sprintf("%s.arch is \"all\"", where),
				Hint: "apk rejects \"all\" as uninstallable; use \"noarch\" (this is the translation OpenWrt's own package-pack.mk applies to LUCI_PKGARCH:=all)",
			}
		case a == "":
			return errf(path, "%s.arch has an empty entry", where)
		case !archNameRE.MatchString(a):
			return errf(path, "%s.arch entry %q is not an architecture name", where, a)
		case seen[a]:
			return errf(path, "%s.arch lists %q twice", where, a)
		}
		seen[a] = true
	}

	// noarch means "installs anywhere"; naming it alongside a real architecture is
	// a contradiction rather than a union.
	if seen[Noarch] && len(p.Arch.List) > 1 {
		return &Error{
			Path: path,
			Msg:  fmt.Sprintf("%s.arch mixes \"noarch\" with specific architectures", where),
			Hint: "noarch already installs on every architecture; drop it, or drop the others",
		}
	}
	return nil
}

// validScriptTypes is apk's set. OpenWrt itself uses none of the trigger machinery and
// puts everything in post-install, but the others are accepted because a package that
// needs pre-deinstall has no other place to put it.
var scriptTypeList = []string{
	"pre-install", "post-install",
	"pre-upgrade", "post-upgrade",
	"pre-deinstall", "post-deinstall",
	"trigger",
}

var validScriptTypes = func() map[string]bool {
	m := make(map[string]bool, len(scriptTypeList))
	for _, s := range scriptTypeList {
		m[s] = true
	}
	return m
}()

// mode determines how a package is built, and rejects configurations that ask for both.
func (p Package) mode() (BuildMode, error) {
	switch {
	case p.Build == BuildMkpkg:
		if p.Path != "" {
			return "", fmt.Errorf("sets both `path` (an SDK build) and `build: mkpkg`")
		}
		return BuildMkpkg, nil
	case p.Build == BuildSDK, p.Build == "" && p.Path != "":
		return BuildSDK, nil
	case p.Build == "":
		return "", fmt.Errorf("has neither `path` nor `build: mkpkg`")
	default:
		return "", fmt.Errorf("build: %q is not a build mode (use mkpkg or sdk)", p.Build)
	}
}

// Mode reports how this package is built. Only valid after validation.
func (p Package) Mode() BuildMode {
	m, _ := p.mode()
	return m
}

// EffectiveName is the package's name, including any ABI suffix.
//
// The separator rule is OpenWrt's: a dash is inserted only when the base name already
// ends in a digit, which is why the ecosystem has both libjson-c5 and
// libblobmsg-json20260213.
func (p Package) EffectiveName() string {
	name := p.Name
	if name == "" && p.Path != "" {
		if i := strings.LastIndexByte(p.Path, '/'); i >= 0 {
			name = p.Path[i+1:]
		} else {
			name = p.Path
		}
	}
	if p.ABIVersion == "" {
		return name
	}
	if n := len(name); n > 0 && name[n-1] >= '0' && name[n-1] <= '9' {
		return name + "-" + p.ABIVersion
	}
	return name + p.ABIVersion
}

func (c *Config) validatePublish(path string) error {
	if len(c.Publish) == 0 {
		return errf(path, "publish is empty; there is nowhere to put the feed")
	}
	for i, p := range c.Publish {
		where := fmt.Sprintf("publish[%d]", i)
		switch p.Target {
		case TargetGitHubPages:
		case TargetS3, TargetRsync:
			return &Error{
				Path: path,
				Msg:  fmt.Sprintf("%s target %q is not implemented yet", where, p.Target),
				Hint: "this version publishes to github-pages",
			}
		case "":
			return errf(path, "%s.target is required", where)
		default:
			return errf(path, "%s.target %q is not a known target", where, p.Target)
		}
	}
	return nil
}

// notImplemented rejects schema sections this version parses but does not act on.
// Accepting them silently would mean a user who wrote `retention:` to bound their
// storage believes garbage collection is running when it is not.
func (c *Config) notImplemented(path string) error {
	if len(c.Overrides) > 0 {
		return &Error{Path: path, Msg: "`overrides` is not implemented yet", Hint: "remove the block; per-package selection is not applied in this version"}
	}
	if c.Retention != nil {
		return &Error{Path: path, Msg: "`retention` is not implemented yet", Hint: "remove the block; nothing is garbage-collected in this version"}
	}
	if c.Build.ChangedOnly {
		return &Error{Path: path, Msg: "`build.changed-only` is not implemented yet", Hint: "remove it; every configured package is built"}
	}
	return nil
}
