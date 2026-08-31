// Package build produces .apk packages with `apk mkpkg`.
//
// This is the SDK-less path: a directory of files already in their final layout
// becomes a package in about a second, with no cross toolchain and no per-
// architecture rebuild. It is the right path for anything architecture-independent,
// which is most of what third parties ship — LuCI themes and applications are
// CSS, JavaScript, Lua and templates, and the status quo asks for one twenty-minute
// SDK build per architecture to produce the same bytes 35 times.
//
// Building never signs. The signing key is a separate stage on purpose, so the job
// that runs arbitrary build code is not the job holding the key.
package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"owfeed.org/owfeed/internal/apk"
	"owfeed.org/owfeed/internal/config"
	"owfeed.org/owfeed/internal/meta"
)

// Request is one package to build.
type Request struct {
	Package config.Package
	// Feed supplies defaults for fields a package does not set for itself.
	Feed config.Feed
	// Root is the directory the config's relative paths resolve against.
	Root string
	// Version is the already-resolved version. ResolveVersion produces it.
	Version string
	// Arch is the architecture being built. It must be one of the package's own.
	Arch string
	// OutDir receives the finished .apk, under a subdirectory named for the
	// architecture. Two architectures of one package share a filename — apk derives
	// it from the name and version alone — so they cannot share a directory.
	OutDir string
	// SourceDateEpoch, when non-zero, is stamped on every staged file so the same
	// inputs produce the same package bytes. Without it the payload carries the
	// checkout's mtimes, which differ between a laptop and a fresh CI clone.
	SourceDateEpoch time.Time
	// Format is "apk" or "ipk". 24.10 and earlier are opkg, and a maintainer who
	// ships only apk has abandoned everyone who has not upgraded yet.
	Format string
}

// Result describes a built package.
type Result struct {
	Name    string
	Version string
	Arch    string
	// File is the path of the built package, inside Request.OutDir.
	File string
	// Notes are things worth saying that are not errors.
	Notes []string
	// Payload lists generated payload files, for reporting what a build produced
	// that the source tree did not contain.
	Payload []string
}

// Build stages the payload and runs `apk mkpkg`.
func Build(ctx context.Context, tool *apk.Tool, req Request) (*Result, error) {
	p := req.Package
	name := p.EffectiveName()

	if err := meta.ValidateVersion(req.Version); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	arch := req.Arch
	if arch == "" {
		if len(p.Arch.List) != 1 {
			return nil, fmt.Errorf("%s: builds for %s, so Request.Arch must say which", name, p.Arch)
		}
		arch = p.Arch.List[0]
	}
	if err := meta.ValidateArch(arch); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}

	outDir := filepath.Join(req.OutDir, arch)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	// The staging tree is a sibling of the output so the finished package moves into
	// place with a rename on the same filesystem.
	stage, err := os.MkdirTemp(outDir, ".owfeed-build-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)

	if req.Format == FormatIPK {
		// The directory is named for the architecture the package declares, and opkg
		// calls the architecture-independent one "all" where apk calls it noarch.
		// Writing an _all.ipk into a noarch/ directory would leave the two halves of
		// the tree disagreeing about the same package.
		return buildIPK(req, arch, req.OutDir)
	}

	payload := filepath.Join(stage, payloadDir)
	files := ExpandArch(p.Files, arch)
	if err := copyTree(filepath.Join(req.Root, files), payload, req.SourceDateEpoch); err != nil {
		return nil, fmt.Errorf("%s: staging %s: %w", name, files, err)
	}
	// Catalogues are compiled into the payload before the sidecars are generated,
	// so they appear in the package's own file list exactly as an SDK build's would.
	catalogues, err := compileCatalogues(payload, req.Root, p.I18n, req.SourceDateEpoch)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if err := writeSidecars(payload, name, p.Conffiles, req.SourceDateEpoch); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}

	args, err := mkpkgArgs(stage, req, arch)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}

	if _, err := tool.RunOK(ctx, apk.Invocation{Workdir: stage, Args: args}); err != nil {
		return nil, err
	}

	file := PackageFileName(name, req.Version)
	built := filepath.Join(stage, file)
	if _, err := os.Stat(built); err != nil {
		// mkpkg reports its own failures loudly, so reaching here means it claimed
		// success without producing anything.
		return nil, fmt.Errorf("%s: apk mkpkg exited 0 but wrote no %s", name, file)
	}
	out := filepath.Join(outDir, file)
	if err := os.Rename(built, out); err != nil {
		return nil, err
	}

	notes := meta.VersionAdvice(req.Version)
	if len(catalogues) > 0 {
		notes = append(notes, fmt.Sprintf("compiled %d translation catalogue(s): %s",
			len(catalogues), strings.Join(catalogues, ", ")))
	}

	return &Result{
		Name:    name,
		Version: req.Version,
		Arch:    arch,
		File:    out,
		Notes:   notes,
		Payload: catalogues,
	}, nil
}

// ExpandArch fills the {arch} placeholder in a path.
func ExpandArch(path, arch string) string {
	return strings.ReplaceAll(path, config.ArchPlaceholder, arch)
}

// PackageFileName is the on-disk name of a built package, matching what OpenWrt's
// package-pack.mk produces so a feed built either way looks the same.
func PackageFileName(name, version string) string {
	return name + "-" + version + ".apk"
}

// Package formats. apk is 25.12 and later; ipk is 24.10 and earlier.
const (
	FormatAPK = "apk"
	FormatIPK = "ipk"
)

const (
	payloadDir = "payload"
	scriptsDir = "scripts"
	idRootDir  = "idroot"
)

// mkpkgArgs assembles the command line. Every path in it is relative to the staging
// directory: apk may be running in a container where absolute host paths do not
// exist, and the staging directory is the one thing mounted there.
// installIf renders the install-if entries, expanding {version} to the version this
// build is producing.
//
// The placeholder is what makes the field usable at all: apk requires one entry pinned
// with `=`, and a feed whose packages share a version — the ordinary case, and the one
// this exists for, a translation pinned to its application — would otherwise have to
// hand-edit that pin on every release, in a file no gate reads.
func installIf(p config.Package, version string) string {
	out := make([]string, 0, len(p.InstallIf))
	for _, e := range p.InstallIf {
		out = append(out, strings.ReplaceAll(e, "{version}", version))
	}
	return strings.Join(out, " ")
}

func mkpkgArgs(stage string, req Request, arch string) ([]string, error) {
	p := req.Package
	name := p.EffectiveName()

	idroot, err := writeIDRoot(stage)
	if err != nil {
		return nil, err
	}

	args := []string{
		// Resolve the payload's numeric owner through our own passwd file. See
		// writeIDRoot: without this, a package built by an ordinary user records
		// every file as owned by nobody:nobody.
		"--root", idroot,
		"mkpkg",
		// Do not record extended attributes. They are host residue, not package
		// content — macOS stamps com.apple.provenance on everything it downloads,
		// and shipping it would both leak the build host's platform and make the
		// same inputs produce different bytes on Linux and macOS.
		"--no-xattrs",
	}

	info := []struct{ k, v string }{
		{"name", name},
		{"version", req.Version},
		{"arch", arch},
		{"description", p.Description},
		{"license", firstNonEmpty(p.License, req.Feed.License)},
		{"url", firstNonEmpty(p.URL, req.Feed.Homepage)},
		{"maintainer", firstNonEmpty(p.Maintainer, req.Feed.Maintainer)},
		{"repo-commit", repoCommit(p)},
		{"depends", strings.Join(depends(p), " ")},
		{"provides", strings.Join(provides(p), " ")},
		{"replaces", strings.Join(p.Replaces, " ")},
		{"recommends", strings.Join(p.Recommends, " ")},
		{"install-if", installIf(p, req.Version)},
	}
	if p.ABIVersion != "" {
		// The suffix lives on the name, and ImageBuilder's GetABISuffix reads it back
		// out of this tag. Setting one without the other resolves to nothing.
		info = append(info, struct{ k, v string }{"tags", "openwrt:abiversion=" + p.ABIVersion})
	}

	for _, f := range info {
		if f.v == "" {
			continue
		}
		if err := meta.ValidateInfo(f.k, f.v); err != nil {
			return nil, err
		}
		args = append(args, "--info", f.k+":"+f.v)
	}

	scripts, err := writeScripts(stage, name, p.Scripts, req.Root)
	if err != nil {
		return nil, err
	}
	for _, s := range scripts {
		args = append(args, "--script", s.typ+":"+s.path)
	}

	args = append(args, "--files", payloadDir, "--output", PackageFileName(name, req.Version))
	return args, nil
}

// repoCommit resolves the commit a package was built from. Reading it from the
// environment is the normal case: CI knows the commit, and pinning it in the config
// would mean committing a value that changes with every commit.
func repoCommit(p config.Package) string {
	if v, ok := strings.CutPrefix(p.RepoCommit, "env:"); ok {
		return os.Getenv(v)
	}
	return p.RepoCommit
}

// depends folds conflicts into the dependency list, which is how apk spells them:
// a leading ! makes the entry a constraint against the package being installed
// rather than for it.
func depends(p config.Package) []string {
	out := append([]string(nil), p.Depends...)
	for _, c := range p.Conflicts {
		out = append(out, "!"+strings.TrimPrefix(c, "!"))
	}
	return out
}

// provides mirrors package-pack.mk: a package carrying an ABI suffix also provides
// its unsuffixed name, otherwise nothing depending on plain `libfoo` can resolve
// against the published `libfoo5`.
func provides(p config.Package) []string {
	out := append([]string(nil), p.Provides...)
	if p.ABIVersion != "" && p.Name != "" {
		out = append(out, p.Name)
	}
	sort.Strings(out)
	return out
}

// writeIDRoot creates the minimal root apk resolves user and group names against.
//
// mkpkg records each file's owner by name, resolving the numeric uid through the
// passwd file under apk's --root. An id with no entry there does not fall back to
// root: apk_id_cache_resolve_user returns the literal "nobody" (src/io.c), so a
// package built by an ordinary user records every file as nobody:nobody and the
// router chowns them to nobody on install. OpenWrt avoids this by running mkpkg
// under fakeroot; owfeed avoids it by handing apk a passwd file in which the
// building uid is called root, which needs no extra tooling and behaves the same
// natively and in a container.
func writeIDRoot(stage string) (string, error) {
	dir := filepath.Join(stage, idRootDir, "etc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	uid, gid := os.Getuid(), os.Getgid()

	passwd := "root:x:0:0:root:/root:/bin/sh\n"
	group := "root:x:0:\n"
	// In a container the bind-mounted tree may already appear as uid 0, in which
	// case the standard line is all that is needed.
	if uid != 0 {
		passwd += fmt.Sprintf("root:x:%d:%d:root:/root:/bin/sh\n", uid, gid)
	}
	if gid != 0 {
		group += fmt.Sprintf("root:x:%d:\n", gid)
	}

	if err := os.WriteFile(filepath.Join(dir, "passwd"), []byte(passwd), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "group"), []byte(group), 0o644); err != nil {
		return "", err
	}
	return idRootDir, nil
}

type script struct{ typ, path string }

// writeScripts materialises package scripts inside the staging tree.
//
// The three lifecycle scripts are wrapped the way OpenWrt's package-pack.mk wraps
// them, because the wrapper is not boilerplate: default_postinst is what runs the
// package's /etc/uci-defaults/* and enables its /etc/init.d/* services. A package
// built with a bare post-install script installs its files and then does none of
// that, silently — the files are there, the service never starts, and nothing
// reports an error.
func writeScripts(stage, pkgname string, in map[string]string, root string) ([]script, error) {
	dir := filepath.Join(stage, scriptsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	bodies := map[string]string{}
	for typ, path := range in {
		b, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			return nil, fmt.Errorf("scripts.%s: %w", typ, err)
		}
		bodies[typ] = stripShebang(string(b))
	}

	var out []script
	write := func(typ, content string) error {
		p := filepath.Join(dir, typ)
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			return err
		}
		out = append(out, script{typ: typ, path: scriptsDir + "/" + typ})
		return nil
	}

	postInstall := openwrtPostInstall(pkgname, bodies["post-install"])
	if err := write("post-install", postInstall); err != nil {
		return nil, err
	}
	// An upgrade runs the same steps with PKG_UPGRADE set, which is how
	// default_postinst knows not to re-apply uci-defaults that already ran.
	if err := write("post-upgrade", "#!/bin/sh\nexport PKG_UPGRADE=1\n"+stripShebang(postInstall)); err != nil {
		return nil, err
	}
	if err := write("pre-deinstall", openwrtPreDeinstall(pkgname, bodies["pre-deinstall"])); err != nil {
		return nil, err
	}

	// Anything else the user asked for goes through untouched: there is no OpenWrt
	// convention to preserve for it.
	for _, typ := range meta.ScriptTypes {
		switch typ {
		case "post-install", "post-upgrade", "pre-deinstall":
			continue
		}
		body, ok := bodies[typ]
		if !ok {
			continue
		}
		if err := write(typ, "#!/bin/sh\n"+body); err != nil {
			return nil, err
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].typ < out[j].typ })
	return out, nil
}

func openwrtPostInstall(pkgname, body string) string {
	return `#!/bin/sh
[ "${IPKG_NO_SCRIPT}" = "1" ] && exit 0
[ -s "${IPKG_INSTROOT}/lib/functions.sh" ] || exit 0
. ${IPKG_INSTROOT}/lib/functions.sh
export root="${IPKG_INSTROOT}"
export pkgname="` + pkgname + `"
add_group_and_user
default_postinst
` + body
}

func openwrtPreDeinstall(pkgname, body string) string {
	return `#!/bin/sh
[ -s "${IPKG_INSTROOT}/lib/functions.sh" ] || exit 0
. ${IPKG_INSTROOT}/lib/functions.sh
export root="${IPKG_INSTROOT}"
export pkgname="` + pkgname + `"
default_prerm
` + body
}

func stripShebang(s string) string {
	if !strings.HasPrefix(s, "#!") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
