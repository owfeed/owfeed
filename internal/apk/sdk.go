package apk

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/klauspost/compress/zstd"

	"owfeed.org/owfeed/internal/netx"
	"owfeed.org/owfeed/internal/usign"
)

// Acquire returns a directory holding the SDK's host apk for pointRelease,
// downloading and verifying it if it is not already cached.
//
// pointRelease is a concrete version like "25.12.5", never a branch name and never
// SNAPSHOT: SNAPSHOT tarballs are rotated on the mirrors, so a checksum taken a
// moment before the download can legitimately fail to match the bytes that arrive.
//
// Nothing is published into the cache until every check has passed. The extraction
// runs into a temporary directory and is renamed into place only after the tarball's
// SHA-256 matches the signed list; on any failure the temporary tree is removed, so
// a partially-verified toolchain can never be picked up by a later run.
//
// A cached extraction is used only while checkCache passes. One that does not -- from
// an owfeed that kept no record, or changed since -- is fetched again, and replaced
// only once the new extraction has passed every check.
func Acquire(ctx context.Context, hc *http.Client, cacheRoot, pointRelease string) (string, error) {
	if !pointRE.MatchString(pointRelease) {
		return "", fmt.Errorf("SDK release %q is not a point release such as 25.12.5; "+
			"set one as sdk-release in owfeed.lock (`owfeed lock --update` writes it) or pass --sdk-release", pointRelease)
	}
	dir := filepath.Join(cacheRoot, "sdk", pointRelease)
	stale := checkCache(dir)
	if stale == nil {
		return dir, nil
	}

	base := fmt.Sprintf("https://downloads.openwrt.org/releases/%s/targets/%s", pointRelease, sdkTarget)

	sums, sig, key, err := verifiedChecksums(ctx, hc, base)
	if err != nil {
		return "", err
	}

	name, wantHash, err := findSDK(sums)
	if err != nil {
		return "", fmt.Errorf("%s: %w", base, err)
	}

	// The staging directory is a sibling of the final one so the publish step is a
	// rename within one filesystem, which is atomic; a copy across filesystems could
	// be observed half-done by a concurrent run.
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), ".tmp-sdk-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	if err := downloadAndExtract(ctx, hc, base+"/"+name, wantHash, tmp); err != nil {
		return "", err
	}
	if err := writeRecord(tmp, sums, sig, key); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dir); err != nil {
		// A concurrent owfeed may have won the race and populated the cache; if the
		// destination now passes the check, that is a success, not a conflict.
		if checkCache(dir) == nil {
			return dir, nil
		}
		// What is there is the extraction that failed its check above. It goes only
		// now, so a failed download leaves the cache as it found it.
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			return "", fmt.Errorf("replacing the cached SDK at %s, which failed its check (%v): %w; "+
				"delete that directory and run again", dir, stale, rmErr)
		}
		if err := os.Rename(tmp, dir); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// pointRE is the shape of a release Acquire accepts. The name becomes a directory
// under the cache that Acquire may delete, so "../.." must never get that far.
var pointRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-rc[0-9]+)?$`)

// The record Acquire keeps beside an extraction: the signed list exactly as OpenWrt
// serves it, which names the sha256 the tarball was verified against, the key it was
// verified with, and the hash of every member as it came out of that tarball. A
// person can check it by hand in that directory with
// `usign -V -m sha256sums -p sha256sums.pub` and `sha256sum -c members.sha256`.
const (
	sumsFile    = "sha256sums"
	sigFile     = "sha256sums.sig"
	keyFile     = "sha256sums.pub"
	membersFile = "members.sha256"
)

// checkCache returns nil when dir holds an extraction that this binary would publish
// today, and says why not otherwise.
//
// Until it existed a cached SDK was trusted for being there: six non-empty files. A
// truncated or replaced .apk.bin passed, and so did an extraction verified with a key
// that a later owfeed no longer pins. Now the stored sha256sums has to verify under a
// key pinned in this binary and name one SDK tarball, and every member has to hash to
// what members.sha256 recorded when it came out of that tarball. Nothing here reaches
// the network, so --no-network keeps working.
//
// shortcut: members.sha256 is owfeed's record, not OpenWrt's signature, so whoever can
// write the cache can rewrite it to match. This catches a damaged or stale cache, not
// a planted one: a cache saved by a less trusted job (one that runs `npm ci`, say) is
// still not safe to restore into a job that holds a signing key. Keeping the 285 MB
// tarball and hashing it against the signed list on every use would make it safe;
// do that when such a restore is wanted.
func checkCache(dir string) error {
	var rec [3][]byte
	for i, name := range []string{sumsFile, sigFile, keyFile} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		rec[i] = b
	}
	sums, sig, key := rec[0], rec[1], rec[2]
	if err := verifySums(dir, sums, sig, key); err != nil {
		return err
	}
	if _, _, err := findSDK(sums); err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}

	recorded, err := os.ReadFile(filepath.Join(dir, membersFile))
	if err != nil {
		return err
	}
	want := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(recorded), "\n"), "\n") {
		hash, member, ok := strings.Cut(line, "  ")
		if !ok {
			return fmt.Errorf("%s: %q is not a sha256sum line", filepath.Join(dir, membersFile), line)
		}
		want[member] = hash
	}
	if len(want) != len(sdkMembers) {
		return fmt.Errorf("%s lists %d files, the extraction has %d", filepath.Join(dir, membersFile), len(want), len(sdkMembers))
	}
	for _, m := range sdkMembers {
		got, err := hashFile(filepath.Join(dir, filepath.FromSlash(m)))
		if err != nil {
			return err
		}
		if got != want[m] {
			return fmt.Errorf("%s hashes to %s, %s recorded %s when it was extracted",
				filepath.Join(dir, filepath.FromSlash(m)), got, membersFile, want[m])
		}
	}
	return nil
}

// writeRecord stores beside a fresh extraction in dir what checkCache reads back.
func writeRecord(dir string, sums, sig, key []byte) error {
	var members bytes.Buffer
	for _, m := range sdkMembers {
		h, err := hashFile(filepath.Join(dir, filepath.FromSlash(m)))
		if err != nil {
			return err
		}
		fmt.Fprintf(&members, "%s  %s\n", h, m)
	}
	for name, b := range map[string][]byte{sumsFile: sums, sigFile: sig, keyFile: key, membersFile: members.Bytes()} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verifiedChecksums fetches sha256sums, its detached usign signature and the key that
// signature names, and returns all three only if the signature verifies under a pinned
// key. Every failure path here returns an error; there is no mode in which an
// unverified list is used.
func verifiedChecksums(ctx context.Context, hc *http.Client, base string) (sums, sig, key []byte, err error) {
	if sums, err = fetch(ctx, hc, base+"/sha256sums"); err != nil {
		return nil, nil, nil, err
	}
	if sig, err = fetch(ctx, hc, base+"/sha256sums.sig"); err != nil {
		return nil, nil, nil, err
	}
	// Checked before the key is fetched, so that an id nobody pinned is refused
	// without a request built from it.
	_, id, err := pinnedSigner(sig)
	if err != nil {
		return nil, nil, nil, err
	}
	if key, err = fetch(ctx, hc, fmt.Sprintf(keyringURLTemplate, keyringCommit, id)); err != nil {
		return nil, nil, nil, fmt.Errorf("fetching key %s from openwrt/keyring: %w", id, err)
	}
	if err := verifySums(base, sums, sig, key); err != nil {
		return nil, nil, nil, err
	}
	return sums, sig, key, nil
}

// pinnedSigner parses sha256sums.sig and returns the key id it names, if that id is
// pinned in trustedKeys.
func pinnedSigner(sigBytes []byte) (*usign.Signature, string, error) {
	sig, err := usign.ParseSignature(sigBytes)
	if err != nil {
		return nil, "", fmt.Errorf("sha256sums.sig: %w", err)
	}
	id := sig.ID.String()
	if _, ok := trustedKeys[id]; !ok {
		return nil, "", fmt.Errorf("sha256sums is signed by key %s, which is not pinned in owfeed. "+
			"If OpenWrt rotated its branch key this is expected — verify the new key out of band "+
			"and add it to internal/apk/pin.go", id)
	}
	return sig, id, nil
}

// verifySums checks sigBytes over sums with keyBytes, whose hash has to be the one
// pinned for the key id the signature names. from says where sums came from.
func verifySums(from string, sums, sigBytes, keyBytes []byte) error {
	sig, id, err := pinnedSigner(sigBytes)
	if err != nil {
		return err
	}
	if got, want := sha256hex(keyBytes), trustedKeys[id]; got != want {
		return fmt.Errorf("openwrt/keyring key %s hashes to %s, pinned value is %s — "+
			"refusing to verify anything with it", id, got, want)
	}
	pub, err := usign.ParsePublicKey(keyBytes)
	if err != nil {
		return fmt.Errorf("openwrt/keyring key %s: %w", id, err)
	}
	if err := usign.Verify(pub, sig, sums); err != nil {
		return fmt.Errorf("sha256sums from %s: %w", from, err)
	}
	return nil
}

// sdkLine matches the SDK entry in a sha256sums file. The toolchain version is part
// of the filename and changes between releases, so it is matched rather than
// constructed — hardcoding it is how build scripts silently break on a point release.
var sdkLine = regexp.MustCompile(`^([0-9a-f]{64}) \*(openwrt-sdk-\S+\.Linux-x86_64\.tar\.(?:zst|xz))$`)

func findSDK(sums []byte) (name, hash string, err error) {
	for _, line := range strings.Split(string(sums), "\n") {
		m := sdkLine.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		if name != "" {
			return "", "", fmt.Errorf("sha256sums lists more than one Linux-x86_64 SDK (%s and %s)", name, m[2])
		}
		hash, name = m[1], m[2]
	}
	if name == "" {
		return "", "", errors.New("sha256sums lists no Linux-x86_64 SDK tarball")
	}
	if strings.HasSuffix(name, ".xz") {
		return "", "", fmt.Errorf("%s is xz-compressed; owfeed currently reads only .tar.zst", name)
	}
	return name, hash, nil
}

// downloadAndExtract streams the tarball through a hash and a zstd decoder straight
// into the member extractor, so the 285 MB archive is never written to disk. The
// hash is checked after the stream is fully consumed; the caller keeps the output in
// a temporary directory until then.
//
// netx.Get picks a broken download up where it stopped, below the hash, so the hash
// still covers every byte once and in order.
func downloadAndExtract(ctx context.Context, hc *http.Client, url, wantHash, dest string) error {
	body, err := netx.Get(ctx, hc, url)
	if err != nil {
		return err
	}
	defer body.Close()

	hasher := sha256.New()
	zr, err := zstd.NewReader(io.TeeReader(body, hasher))
	if err != nil {
		return err
	}
	defer zr.Close()

	if err := extractMembers(tar.NewReader(zr), dest); err != nil {
		return err
	}
	// Drain whatever the extractor did not read, so the hash covers the whole file
	// rather than just the prefix containing the members we wanted.
	if _, err := io.Copy(io.Discard, zr); err != nil {
		return fmt.Errorf("reading %s: %w", url, err)
	}

	if got := hex.EncodeToString(hasher.Sum(nil)); got != wantHash {
		return fmt.Errorf("%s: sha256 is %s, signed list says %s", url, got, wantHash)
	}
	return nil
}

// extractMembers pulls exactly the wanted members out of the tar stream.
//
// Paths are matched after stripping the archive's single top-level directory, whose
// name embeds the release and toolchain versions. Nothing is created from a path the
// archive supplies: each member is written to a path built from the entry in
// sdkMembers that it matched, so a crafted tarball cannot escape dest.
func extractMembers(tr *tar.Reader, dest string) error {
	want := make(map[string]bool, len(sdkMembers))
	for _, m := range sdkMembers {
		want[m] = true
	}
	found := 0

	for found < len(want) {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		rel := strings.TrimPrefix(hdr.Name, "./")
		if i := strings.IndexByte(rel, '/'); i >= 0 {
			rel = rel[i+1:]
		}
		if !want[rel] {
			continue
		}

		out := filepath.Join(dest, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		// Executable bits matter: the loader and .apk.bin have to be runnable.
		f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(hdr.Mode)&0o777)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		found++
	}

	if found < len(want) {
		var missing []string
		for _, m := range sdkMembers {
			if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(m))); err != nil {
				missing = append(missing, m)
			}
		}
		return fmt.Errorf("SDK tarball is missing expected members: %s", strings.Join(missing, ", "))
	}
	return nil
}

func fetch(ctx context.Context, hc *http.Client, url string) ([]byte, error) {
	body, err := netx.Get(ctx, hc, url)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(io.LimitReader(body, 8<<20))
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
