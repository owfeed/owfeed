// Package index signs packages and builds the repository index.
//
// The two steps are separate commands but one chain, and the chain is what makes
// it trustworthy: `apk adbsign` reports its failures on stdout and exits 0 anyway,
// so its exit status carries no information at all. Rather than trusting it, the
// signing step re-reads each package and confirms the expected key identity is
// present, and the index step then hands apk the feed's own public key so that
// building the index verifies every signature for real instead of waving it
// through with --allow-untrusted.
//
// Order matters and is enforced: the index records each package's file-size, so
// signing a package after it has been indexed silently invalidates the index.
package index

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"owfeed.org/owfeed/internal/apk"
	"owfeed.org/owfeed/internal/keys"
)

// File names produced in a published directory.
const (
	IndexFile = "packages.adb"
	JSONFile  = "index.json"
	SumsFile  = "sha256sums"
)

// Signer describes the key a step signs with.
type Signer struct {
	// KeyDir holds the private key; it is mounted separately and never staged into
	// the directory being published.
	KeyDir string
	// KeyName is the file name inside KeyDir.
	KeyName string
	// Identity is what apk records for this key, used to confirm signing happened.
	Identity keys.Identity
}

// NewSigner derives the identity of a private key so signatures can be checked
// against it afterwards.
func NewSigner(keyDir, keyName string, key *ecdsa.PrivateKey) (Signer, error) {
	id, err := keys.IdentityOf(&key.PublicKey)
	if err != nil {
		return Signer{}, err
	}
	return Signer{KeyDir: keyDir, KeyName: keyName, Identity: id}, nil
}

// Sign signs every package in dir and verifies that it worked.
//
// `--allow-untrusted` appears here, and only here. It is not a way around someone
// else's trust decision: apk treats a package with no signature as untrusted, so
// there is no way to sign a freshly built package without it. Everything
// downstream of this step verifies properly.
func Sign(ctx context.Context, tool *apk.Tool, dir string, s Signer) ([]string, error) {
	pkgs, err := Packages(dir)
	if err != nil {
		return nil, err
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("%s contains no .apk files to sign", dir)
	}

	for _, p := range pkgs {
		if _, err := tool.RunOK(ctx, apk.Invocation{
			Workdir: dir,
			KeyDir:  s.KeyDir,
			Args:    []string{"--allow-untrusted", "adbsign", "--sign-key", apk.KeyRef(s.KeyName), "./" + p},
		}); err != nil {
			return nil, err
		}

		// adbsign's exit status means nothing, so ask the artifact instead.
		ids, err := Signatures(ctx, tool, dir, p)
		if err != nil {
			return nil, err
		}
		if !contains(ids, s.Identity.String()) {
			return nil, fmt.Errorf("apk adbsign reported success but %s carries no signature by key %s (found: %s)",
				p, s.Identity, strings.Join(ids, ", "))
		}
	}
	return pkgs, nil
}

// Options controls index generation.
type Options struct {
	// Dir is the publish directory. mkndx runs with it as the working directory and
	// takes ./name.apk arguments, so no path prefix can leak into the index.
	Dir string
	// TrustDir holds the feed's public key. With it, building the index verifies the
	// signatures the signing step produced.
	TrustDir string
	Signer   Signer
	// AlsoSign are additional keys the index is signed with, for a rotation window.
	//
	// mkndx accepts a repeated --sign-key and the resulting index carries every
	// signature, so a subscriber holding either the old or the new key keeps working
	// while the keyring package delivers the replacement. Without an overlap the new
	// index is signed by a key no router has yet, the index fails verification, and
	// the very package that would have carried the new key becomes unreachable —
	// measured on 25.12.5, where the upgrade returned UNTRUSTED signature and the
	// rotation could not start.
	AlsoSign []Signer
	// Description is the index's own description field.
	Description string
	// UnsignedPackages indexes packages the feed did not sign.
	//
	// mkndx refuses a package it cannot verify -- "UNTRUSTED signature", exit 99 --
	// so a feed that leaves per-package signing to the authors has to say so. The
	// index itself is still signed, and that is where a router's trust comes from:
	// it verifies the index against the key in /etc/apk/keys and then checks each
	// package against the hash the index recorded. Measured on 25.12.5: install,
	// upgrade and remove by name all succeed with no package signature present.
	//
	// What it costs is `apk add ./file.apk` and LuCI's Upload Package, which have
	// no index to check against. Both already require --allow-untrusted for
	// OpenWrt's own packages, which are unsigned individually.
	UnsignedPackages bool
}

// Result describes a built index.
type Result struct {
	Packages []string
	// Signers are the key identities the index is signed by.
	Signers []string
	Size    int64
}

// Build produces packages.adb, index.json and sha256sums.
func Build(ctx context.Context, tool *apk.Tool, opts Options) (*Result, error) {
	pkgs, err := Packages(opts.Dir)
	if err != nil {
		return nil, err
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("%s contains no .apk files to index", opts.Dir)
	}

	args := []string{
		// Trust the feed's own key, so mkndx checks every package signature rather
		// than being told to ignore them.
		"--keys-dir", apk.TrustDirRef(),
	}
	if opts.UnsignedPackages {
		// Only reachable when the feed deliberately does not sign packages. The
		// index is still signed below; this says nothing about it.
		args = append(args, "--allow-untrusted")
	}
	args = append(args, "mkndx", "--sign-key", apk.KeyRef(opts.Signer.KeyName))
	for _, s := range opts.AlsoSign {
		args = append(args, "--sign-key", apk.KeyRef(s.KeyName))
	}
	args = append(args, "--output", IndexFile)
	if opts.Description != "" {
		args = append(args, "--description", opts.Description)
	}
	// Never -C zstd. OpenWrt builds apk with -Dzstd=disabled, so a zstd index parses
	// on the build host and fails on every router with "ADB compression not
	// supported". The default is deflate, and the default is what we want.
	for _, p := range pkgs {
		args = append(args, "./"+p)
	}

	if _, err := tool.RunOK(ctx, apk.Invocation{
		Workdir:  opts.Dir,
		KeyDir:   opts.Signer.KeyDir,
		TrustDir: opts.TrustDir,
		Args:     args,
	}); err != nil {
		return nil, err
	}

	st, err := os.Stat(filepath.Join(opts.Dir, IndexFile))
	if err != nil {
		return nil, fmt.Errorf("apk mkndx exited 0 but wrote no %s: %w", IndexFile, err)
	}

	signers, err := Signatures(ctx, tool, opts.Dir, IndexFile)
	if err != nil {
		return nil, err
	}
	for _, s := range opts.AlsoSign {
		if !contains(signers, s.Identity.String()) {
			return nil, fmt.Errorf("%s carries no signature by the additional key %s (found: %s)",
				IndexFile, s.Identity, strings.Join(signers, ", "))
		}
	}
	if !contains(signers, opts.Signer.Identity.String()) {
		return nil, fmt.Errorf("%s is not signed by key %s (found: %s)",
			IndexFile, opts.Signer.Identity, strings.Join(signers, ", "))
	}

	if err := writeJSON(ctx, tool, opts.Dir); err != nil {
		return nil, err
	}
	if err := WriteSums(opts.Dir); err != nil {
		return nil, err
	}

	return &Result{Packages: pkgs, Signers: signers, Size: st.Size()}, nil
}

// writeJSON renders the index as JSON beside it, in the shape owut, the attended
// sysupgrade server and the firmware selector already read.
func writeJSON(ctx context.Context, tool *apk.Tool, dir string) error {
	res, err := tool.RunOK(ctx, apk.Invocation{
		Workdir: dir,
		Args:    []string{"adbdump", "--format", "json", IndexFile},
	})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, JSONFile), []byte(res.Stdout), 0o644)
}

// sigLine matches adbdump's per-signature line. The hex run begins with the signing
// key's identity, which is the first 16 bytes of SHA-512 over the public key point.
var sigLine = regexp.MustCompile(`^# sig [^ ]+ [^ ]+ ([0-9a-f]{32})`)

// Signatures returns the key identities a file is signed by. file is a name inside
// dir, and reaches apk as ./file so a name starting with '-' is not read as an option.
func Signatures(ctx context.Context, tool *apk.Tool, dir, file string) ([]string, error) {
	res, err := tool.RunOK(ctx, apk.Invocation{Workdir: dir, Args: []string{"adbdump", "./" + file}})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		if m := sigLine.FindStringSubmatch(line); m != nil {
			out = append(out, m[1])
		}
	}
	return out, nil
}

// Tree lists the built packages by architecture. A build directory holds one
// subdirectory per architecture, because two architectures of the same package
// share a filename: apk derives it from the name and version alone.
func Tree(dir string) (map[string][]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		pkgs, err := Packages(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if len(pkgs) > 0 {
			out[e.Name()] = pkgs
		}
	}
	return out, nil
}

// Packages lists the .apk files in dir, sorted, without path prefixes.
func Packages(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".apk") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// WriteSums writes a sha256sums covering everything published beside the index, in
// the same format OpenWrt's own release directories use.
//
// It is not a trust anchor and is not treated as one: it is served from the same
// place as the files it describes. What it is good for is telling a mirror or a CDN
// apart from a corrupted transfer.
func WriteSums(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || e.Name() == SumsFile || IsSqueezeTemp(e.Name()) {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	var b strings.Builder
	for _, n := range names {
		sum, err := sha256File(filepath.Join(dir, n))
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s *%s\n", sum, n)
	}
	return os.WriteFile(filepath.Join(dir, SumsFile), []byte(b.String()), 0o644)
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
