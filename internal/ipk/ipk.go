// Package ipk builds packages for OpenWrt 24.10 and earlier.
//
// 24.10 is opkg, not apk: a different container, a different metadata file, a
// different name for "architecture-independent". Nothing about it is going away
// soon — routers stay on a release for years — so a maintainer who ships only apk
// has abandoned everyone who has not upgraded yet.
//
// This builds the container directly rather than through the SDK's ipkg-build,
// because the format is small and knowable and requiring the SDK for a package with
// no compiled code would defeat the point. The layout is that script's, read from
// scripts/ipkg-build on the openwrt-24.10 branch:
//
//	<name>_<version>_<arch>.ipk = gzip(tar of ./debian-binary, ./data.tar.gz, ./control.tar.gz)
//
// in that order. An integration test installs the result with a real opkg, because
// a container that is merely plausible is one that fails on somebody's router.
package ipk

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"owfeed.org/owfeed/internal/squeeze"
)

// ArchAll is what 24.10 calls an architecture-independent package.
//
// apk calls the same thing "noarch" and rejects "all" as uninstallable, which is
// why OpenWrt's package-pack.mk translates between them. A package built for both
// lines carries both names, one per artifact.
const ArchAll = "all"

// Package describes what to build.
type Package struct {
	Name        string
	Version     string
	Arch        string
	Description string
	License     string
	Maintainer  string
	URL         string
	Section     string
	Depends     []string
	Provides    []string
	Conflicts   []string
	// Conffiles are absolute paths inside the payload that opkg must preserve
	// across an upgrade.
	Conffiles []string
	// Scripts maps a maintainer script name — postinst, prerm — to its contents.
	Scripts map[string]string
}

// Options configure a build.
type Options struct {
	// Payload is the staged rootfs.
	Payload string
	// OutDir receives the .ipk.
	OutDir string
	// Epoch is stamped on every archive member. opkg does not care, but two builds
	// of the same content should produce the same bytes.
	Epoch time.Time
	// Zopfli writes the three gzip streams with zopfli instead of the best standard
	// level. See config.Package.Compression for the trade.
	Zopfli bool
}

// FileName is the on-disk name, which uses underscores where apk uses dashes.
func FileName(name, version, arch string) string {
	return fmt.Sprintf("%s_%s_%s.ipk", name, version, arch)
}

// Build writes the package and returns its path.
func Build(p Package, opts Options) (string, error) {
	if p.Arch == "" {
		return "", fmt.Errorf("%s: arch is required", p.Name)
	}

	epoch := opts.Epoch
	if epoch.IsZero() {
		epoch = time.Unix(0, 0)
	}

	data, installed, err := dataArchive(opts.Payload, epoch, opts.Zopfli)
	if err != nil {
		return "", err
	}
	// Installed-Size is the uncompressed size of the payload archive, which is what
	// ipkg-build measures and what opkg's free-space check reads.
	control, err := controlArchive(p, installed, opts.Payload, epoch, opts.Zopfli)
	if err != nil {
		return "", err
	}

	var outer bytes.Buffer
	tw := tar.NewWriter(&outer)
	// The order is the script's. opkg reads the members by name rather than by
	// position, but a container that differs from every other one in the wild is a
	// container waiting to meet a stricter reader.
	for _, m := range []struct {
		name string
		body []byte
	}{
		{"./debian-binary", []byte("2.0\n")},
		{"./data.tar.gz", data},
		{"./control.tar.gz", control},
	} {
		if err := writeFile(tw, m.name, m.body, 0o644, epoch); err != nil {
			return "", err
		}
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	packed, err := compress(outer.Bytes(), opts.Zopfli)
	if err != nil {
		return "", fmt.Errorf("%s: %w", p.Name, err)
	}

	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(opts.OutDir, FileName(p.Name, p.Version, p.Arch))
	if err := os.WriteFile(out, packed, 0o644); err != nil {
		return "", err
	}
	return out, nil
}

// dataArchive packs the payload and reports its uncompressed size.
func dataArchive(payload string, epoch time.Time, zopfli bool) ([]byte, int64, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	var size int64
	var paths []string
	err := filepath.WalkDir(payload, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	// Sorted, like `tar --sort=name`: the order of members is part of the bytes, and
	// a build that reorders them for no reason is a build that looks changed.
	sort.Strings(paths)

	for _, path := range paths {
		rel, err := filepath.Rel(payload, path)
		if err != nil {
			return nil, 0, err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, 0, err
		}

		name := "./" + filepath.ToSlash(rel)
		if rel == "." {
			name = "./"
		}

		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return nil, 0, err
		}
		hdr.Name = name
		hdr.ModTime = epoch
		hdr.AccessTime, hdr.ChangeTime = time.Time{}, time.Time{}
		hdr.Format = tar.FormatGNU
		// Numeric owner, and the owner is root: the payload is a piece of a router's
		// root filesystem, not of the machine that built it.
		hdr.Uid, hdr.Gid = 0, 0
		hdr.Uname, hdr.Gname = "", ""

		switch {
		case info.IsDir():
			hdr.Name = strings.TrimSuffix(name, "/") + "/"
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return nil, 0, err
			}
			hdr.Linkname = target
		}

		if err := tw.WriteHeader(hdr); err != nil {
			return nil, 0, err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return nil, 0, err
			}
			n, err := io.Copy(tw, f)
			f.Close()
			if err != nil {
				return nil, 0, err
			}
			size += n
		}
	}

	if err := tw.Close(); err != nil {
		return nil, 0, err
	}
	gz, err := compress(buf.Bytes(), zopfli)
	return gz, size, err
}

// controlArchive packs the metadata opkg reads.
func controlArchive(p Package, installed int64, payload string, epoch time.Time, zopfli bool) ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Package: %s\n", p.Name)
	fmt.Fprintf(&b, "Version: %s\n", p.Version)
	fmt.Fprintf(&b, "Architecture: %s\n", p.Arch)
	fmt.Fprintf(&b, "Installed-Size: %d\n", installed)
	writeField(&b, "Depends", strings.Join(p.Depends, ", "))
	writeField(&b, "Provides", strings.Join(p.Provides, ", "))
	writeField(&b, "Conflicts", strings.Join(p.Conflicts, ", "))
	writeField(&b, "Section", p.Section)
	writeField(&b, "License", p.License)
	writeField(&b, "Maintainer", p.Maintainer)
	writeField(&b, "Source", p.URL)
	// Description last, because a continuation line belongs to whatever precedes it
	// and a multi-line description would swallow any field written after it.
	writeField(&b, "Description", describe(p.Description))

	members := map[string]string{"./control": b.String()}

	// opkg reads this to decide what survives an upgrade. Only paths that are
	// actually shipped are listed, which is what ipkg-build's resolve step does.
	var present []string
	for _, cf := range p.Conffiles {
		if _, err := os.Stat(filepath.Join(payload, strings.TrimPrefix(cf, "/"))); err == nil {
			present = append(present, cf)
		}
	}
	if len(present) > 0 {
		sort.Strings(present)
		members["./conffiles"] = strings.Join(present, "\n") + "\n"
	}
	for name, body := range p.Scripts {
		members["./"+name] = body
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	names := make([]string, 0, len(members))
	for n := range members {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, n := range names {
		mode := int64(0o644)
		if n == "./postinst" || n == "./prerm" || n == "./postrm" || n == "./preinst" {
			mode = 0o755
		}
		if err := writeFile(tw, n, []byte(members[n]), mode, epoch); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return compress(buf.Bytes(), zopfli)
}

// compress writes one of the container's three gzip streams, at the best level.
//
// Go's default level is not zlib's: on one 274 KB data.tar it wrote 67,762 B where
// GNU gzip -6, the level OpenWrt's own 24.10 packages use, wrote 65,555 B and
// BestCompression 65,382 B. See docs/apk-behaviour.md.
func compress(raw []byte, zopfli bool) ([]byte, error) {
	if zopfli {
		return squeeze.Gzip(raw)
	}
	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := gz.Write(raw); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeField(b *strings.Builder, name, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(b, "%s: %s\n", name, value)
}

// describe folds a description into the one-line-plus-continuations shape the
// control format uses. An empty line inside one would end the field.
func describe(s string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			lines[i] = " ."
		} else {
			lines[i] = " " + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

func writeFile(tw *tar.Writer, name string, body []byte, mode int64, epoch time.Time) error {
	hdr := &tar.Header{
		Name: name, Mode: mode, Size: int64(len(body)),
		ModTime: epoch, Format: tar.FormatGNU, Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(body)
	return err
}
