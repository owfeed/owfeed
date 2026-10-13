package build

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"owfeed.org/owfeed/internal/config"
	"owfeed.org/owfeed/internal/ipk"
)

// buildIPK produces the 24.10 container from the same staged rootfs the apk path
// uses. The two lines differ in more than packaging: opkg calls an
// architecture-independent package "all" where apk requires "noarch" and rejects
// "all", so the same package carries a different architecture name per artifact.
func buildIPK(req Request, arch, root string) (*Result, error) {
	p := req.Package
	name := p.EffectiveName()

	pkgArch := arch
	if arch == config.Noarch {
		pkgArch = ipk.ArchAll
	}
	outDir := filepath.Join(root, pkgArch)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}

	payload, err := os.MkdirTemp(outDir, ".owfeed-ipk-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(payload)

	files := ExpandArch(p.Files, arch)
	if err := copyTree(filepath.Join(req.Root, files), payload, req.SourceDateEpoch); err != nil {
		return nil, fmt.Errorf("%s: staging %s: %w", name, files, err)
	}
	catalogues, err := compileCatalogues(payload, req.Root, p.I18n, req.SourceDateEpoch)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}

	out, err := ipk.Build(ipk.Package{
		Name:        name,
		Version:     req.Version,
		Arch:        pkgArch,
		Description: p.Description,
		License:     firstNonEmpty(p.License, req.Feed.License),
		Maintainer:  firstNonEmpty(p.Maintainer, req.Feed.Maintainer),
		URL:         firstNonEmpty(p.URL, req.Feed.Homepage),
		Depends:     p.Depends,
		Provides:    provides(p),
		Conflicts:   p.Conflicts,
		Conffiles:   p.Conffiles,
		Scripts:     ipkScripts(name, p, req.Root),
	}, ipk.Options{
		Payload: payload, OutDir: outDir, Epoch: req.SourceDateEpoch,
		Zopfli: p.Compression == config.CompressionZopfli,
	})
	if err != nil {
		return nil, err
	}

	notes := []string{"built for the 24.10 line, which is opkg rather than apk"}
	// opkg has no upgrade hook: it removes the old version and installs the new one,
	// so an upgrade is prerm + postrm followed by preinst + postinst. A package
	// relying on an upgrade script gets those instead, and saying so is better than
	// dropping the script without a word.
	for _, kind := range []string{"pre-upgrade", "post-upgrade"} {
		if _, ok := p.Scripts[kind]; ok {
			notes = append(notes, kind+" has no opkg equivalent; an upgrade runs prerm, postrm, preinst, postinst instead")
		}
	}
	if len(catalogues) > 0 {
		notes = append(notes, fmt.Sprintf("compiled %d translation catalogue(s)", len(catalogues)))
	}
	return &Result{Name: name, Version: req.Version, Arch: pkgArch, File: out, Notes: notes, Payload: catalogues}, nil
}

// ipkScripts wraps the lifecycle scripts the way OpenWrt's ipk path does. The
// wrapper is the same idea as the apk one and a different name: opkg calls them
// postinst and prerm, and default_postinst is what runs a package's uci-defaults
// and enables its init scripts.
//
// What opkg 24.10 actually runs, measured in openwrt/rootfs:x86-64-24.10.8 with a
// package whose four scripts each logged their arguments:
//
//	install    preinst install    postinst configure
//	upgrade    prerm remove       postrm remove      preinst install   postinst configure
//	remove     prerm remove       postrm remove
//
// Two things follow. Every one of the four runs, so a package that ships only
// postinst and prerm loses its cleanup on removal — which is what happened here.
// And /lib/functions.sh defines only default_postinst and default_prerm: there is no
// default_postrm to call, so postrm carries the author's body and nothing else.
func ipkScripts(name string, p config.Package, root string) map[string]string {
	body := func(kind string) string {
		path, ok := p.Scripts[kind]
		if !ok {
			return ""
		}
		b, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			return ""
		}
		return stripShebang(string(b))
	}

	s := map[string]string{
		"postinst": strings.Join([]string{
			"#!/bin/sh",
			`[ "${IPKG_NO_SCRIPT}" = "1" ] && exit 0`,
			`[ -s "${IPKG_INSTROOT}/lib/functions.sh" ] || exit 0`,
			`. ${IPKG_INSTROOT}/lib/functions.sh`,
			`default_postinst $0 $@`,
			body("post-install"),
		}, "\n"),
		"prerm": strings.Join([]string{
			"#!/bin/sh",
			`[ -s "${IPKG_INSTROOT}/lib/functions.sh" ] || exit 0`,
			`. ${IPKG_INSTROOT}/lib/functions.sh`,
			`default_prerm $0 $@`,
			body("pre-deinstall"),
		}, "\n"),
	}

	// postinst and prerm exist unconditionally because their default_ wrappers do
	// real work on every package. These two have no default to run, so an empty one
	// would be a file opkg executes for nothing.
	if b := body("post-deinstall"); b != "" {
		s["postrm"] = "#!/bin/sh\n" + b
	}
	if b := body("pre-install"); b != "" {
		s["preinst"] = "#!/bin/sh\n" + b
	}
	return s
}
