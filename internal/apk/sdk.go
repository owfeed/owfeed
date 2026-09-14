package apk

import (
	"archive/tar"
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
func Acquire(ctx context.Context, hc *http.Client, cacheRoot, pointRelease string) (string, error) {
	dir := filepath.Join(cacheRoot, "sdk", pointRelease)
	if ok, err := cacheValid(dir); err != nil {
		return "", err
	} else if ok {
		return dir, nil
	}

	base := fmt.Sprintf("https://downloads.openwrt.org/releases/%s/targets/%s", pointRelease, sdkTarget)

	sums, err := verifiedChecksums(ctx, hc, base)
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
	if err := os.Rename(tmp, dir); err != nil {
		// A concurrent owfeed may have won the race and populated the cache; if the
		// destination is now valid, that is a success, not a conflict.
		if ok, verr := cacheValid(dir); verr == nil && ok {
			return dir, nil
		}
		return "", err
	}
	return dir, nil
}

// cacheValid reports whether dir already holds a usable extraction. Presence of the
// directory is not enough — an interrupted earlier run could have left a partial tree
// that would then be trusted forever.
func cacheValid(dir string) (bool, error) {
	for _, m := range sdkMembers {
		st, err := os.Stat(filepath.Join(dir, m))
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if st.Size() == 0 {
			return false, nil
		}
	}
	return true, nil
}

// verifiedChecksums fetches sha256sums and returns it only if its detached usign
// signature verifies under a pinned key. Every failure path here returns an error;
// there is no mode in which an unverified list is used.
func verifiedChecksums(ctx context.Context, hc *http.Client, base string) ([]byte, error) {
	sums, err := fetch(ctx, hc, base+"/sha256sums")
	if err != nil {
		return nil, err
	}
	sigBytes, err := fetch(ctx, hc, base+"/sha256sums.sig")
	if err != nil {
		return nil, err
	}

	sig, err := usign.ParseSignature(sigBytes)
	if err != nil {
		return nil, fmt.Errorf("sha256sums.sig: %w", err)
	}

	id := sig.ID.String()
	wantKeyHash, ok := trustedKeys[id]
	if !ok {
		return nil, fmt.Errorf("sha256sums is signed by key %s, which is not pinned in owfeed. "+
			"If OpenWrt rotated its branch key this is expected — verify the new key out of band "+
			"and add it to internal/apk/pin.go", id)
	}

	keyBytes, err := fetch(ctx, hc, fmt.Sprintf(keyringURLTemplate, keyringCommit, id))
	if err != nil {
		return nil, fmt.Errorf("fetching key %s from openwrt/keyring: %w", id, err)
	}
	if got := sha256hex(keyBytes); got != wantKeyHash {
		return nil, fmt.Errorf("openwrt/keyring key %s hashes to %s, pinned value is %s — "+
			"refusing to verify anything with it", id, got, wantKeyHash)
	}

	pub, err := usign.ParsePublicKey(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("openwrt/keyring key %s: %w", id, err)
	}
	if err := usign.Verify(pub, sig, sums); err != nil {
		return nil, fmt.Errorf("sha256sums from %s: %w", base, err)
	}
	return sums, nil
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
func downloadAndExtract(ctx context.Context, hc *http.Client, url, wantHash, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return netx.Status(url, resp)
	}

	hasher := sha256.New()
	zr, err := zstd.NewReader(io.TeeReader(resp.Body, hasher))
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, netx.Status(url, resp)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
