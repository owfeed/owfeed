package index

import (
	"bytes"
	"compress/flate"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"owfeed.org/owfeed/internal/apk"
	"owfeed.org/owfeed/internal/squeeze"
)

// adbDeflate is the magic apk writes for an ADB compressed with deflate at its
// default level, which is what the on-device apk installs. See docs/apk-behaviour.md.
var adbDeflate = []byte("ADBd")

// nameLine matches the package name in adbdump's output, which opens the info block.
var nameLine = regexp.MustCompile(`(?m)^info:\n  name: (\S+)$`)

// PackageName reads the name a package declares from the package itself, so a
// decision about a file never rests on how its name splits around a dash.
func PackageName(ctx context.Context, tool *apk.Tool, dir, file string) (string, error) {
	res, err := tool.RunOK(ctx, apk.Invocation{Workdir: dir, Args: []string{"adbdump", "./" + file}})
	if err != nil {
		return "", err
	}
	m := nameLine.FindStringSubmatch(res.Stdout)
	if m == nil {
		return "", fmt.Errorf("apk adbdump %s printed no info.name", file)
	}
	return m[1], nil
}

// Squeeze re-deflates a signed package with zopfli and replaces it only once the new
// file is proven to be the same package.
//
// It runs after Sign because adbsign re-deflates the whole file at apk's own level.
// It can run after Sign because apk signs the uncompressed ADB, so another deflate
// stream over the same ADB carries the same valid signature. The new file must keep
// the `ADBd` magic, inflate to the identical ADB, and pass `apk verify` against the
// signer's public key in trustDir before it is renamed over the old one.
//
// It returns the sizes before and after; equal sizes mean zopfli gained nothing and
// apk's own bytes were kept.
func Squeeze(ctx context.Context, tool *apk.Tool, dir, file string, s Signer, trustDir string) (int, int, error) {
	path := filepath.Join(dir, file)
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, err
	}
	old, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	if !bytes.HasPrefix(old, adbDeflate) {
		return 0, 0, fmt.Errorf("%s starts %q, not %q: only apk's default deflate container is re-deflated", file, old[:min(4, len(old))], adbDeflate)
	}
	r := bytes.NewReader(old[len(adbDeflate):])
	adb, err := io.ReadAll(flate.NewReader(r))
	if err != nil {
		return 0, 0, fmt.Errorf("%s does not inflate: %w", file, err)
	}
	if r.Len() != 0 {
		return 0, 0, fmt.Errorf("%s carries %d B after its deflate stream, which re-deflating would drop", file, r.Len())
	}

	// squeeze.Deflate inflates its own output and compares it with adb.
	z, err := squeeze.Deflate(adb)
	if err != nil {
		return 0, 0, fmt.Errorf("%s: %w", file, err)
	}
	if len(adbDeflate)+len(z) >= len(old) {
		return len(old), len(old), nil
	}

	// Not *.apk: a temporary file left by a killed run would be signed and indexed.
	tmp, err := os.CreateTemp(dir, ".owfeed-squeeze-*.tmp")
	if err != nil {
		return 0, 0, err
	}
	defer os.Remove(tmp.Name())
	// CreateTemp makes the file 0600, and a package a web server cannot read is a
	// feed that 403s; the replacement keeps the mode of the file it replaces.
	werr := tmp.Chmod(info.Mode().Perm())
	if werr == nil {
		_, werr = tmp.Write(append(append([]byte{}, adbDeflate...), z...))
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return 0, 0, werr
	}

	// apk verify reports on stdout, so RunOK's stderr-only error would say nothing.
	name := filepath.Base(tmp.Name())
	res, err := tool.Run(ctx, apk.Invocation{
		Workdir: dir, TrustDir: trustDir,
		Args: []string{"--keys-dir", apk.TrustDirRef(), "verify", "./" + name},
	})
	if err != nil {
		return 0, 0, err
	}
	if res.ExitCode != 0 {
		return 0, 0, fmt.Errorf("%s re-deflated with zopfli no longer verifies: `apk --keys-dir <trust-dir> verify ./%s` exit %d: %s",
			file, name, res.ExitCode, strings.TrimSpace(res.Stdout+res.Stderr))
	}
	ids, err := Signatures(ctx, tool, dir, name)
	if err != nil {
		return 0, 0, err
	}
	if !contains(ids, s.Identity.String()) {
		return 0, 0, fmt.Errorf("%s re-deflated with zopfli lost its signature by key %s (found: %v)", file, s.Identity, ids)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return 0, 0, err
	}
	return len(old), len(adbDeflate) + len(z), nil
}
