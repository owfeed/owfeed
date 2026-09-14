// Package smoke installs a built feed on a real OpenWrt image.
//
// Everything upstream of this checks a feed against a description of how apk
// behaves. This checks it against apk. The distinction matters because every
// failure this catches — a signature the device will not accept, a dependency that
// does not resolve, a package that installs its files somewhere useless — looks
// perfectly healthy in a static inspection of the tree.
//
// It is deliberately the last gate and not the only one: it exercises one
// architecture, so it proves the feed works rather than that every architecture in
// it does.
package smoke

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"owfeed.org/owfeed/internal/config"
	"owfeed.org/owfeed/internal/feedindex"
	"owfeed.org/owfeed/internal/netx"
)

// DefaultArch is the architecture smoked. It is x86_64 because that is the one
// OpenWrt publishes a container image for and the one Docker runs without
// emulation; the others are covered by the index checks rather than by installing.
const DefaultArch = "x86_64"

// Options configure a run.
type Options struct {
	// Dir is the published tree, as `owfeed index` wrote it.
	Dir string
	// FeedName seeds the key and repository filenames, matching the install snippet.
	FeedName string
	// Release is the release line, e.g. "25.12".
	Release string
	// LayoutPath is the layout template from the config.
	LayoutPath string
	// Arch is the architecture to install; empty means DefaultArch.
	Arch string
	// Image is the router image. Empty derives one from PointRelease.
	Image string
	// PointRelease is the concrete release the image should match, e.g. "25.12.4".
	PointRelease string
	// Packages are the names to install; empty means everything in the index.
	Packages []string
	// Format is "apk" or "ipk". The two managers are pointed at a feed in
	// incompatible ways, so the script differs entirely.
	Format string
	// UsignKeyID is the opkg key's id, which is also the filename opkg looks it up
	// under.
	UsignKeyID string
}

// Result describes what happened.
type Result struct {
	Image string
	Arch  string
	// Installed are the packages that were installed by name from the repository.
	Installed []string
	// LocalInstall records whether `apk add ./file.apk` worked without
	// --allow-untrusted, which is the claim per-package signing exists to make.
	LocalInstall bool
	// WorldPin is the /etc/apk/world entry a local install leaves behind.
	WorldPin string
	// Log is the container's combined output, for reporting a failure.
	Log string
}

// Run installs the feed on a router image and reports what happened.
func Run(ctx context.Context, opts Options) (*Result, error) {
	arch := opts.Arch
	if arch == "" {
		arch = DefaultArch
	}
	image := opts.Image
	if image == "" {
		var err error
		if image, err = ResolveImage(ctx, opts.Release, opts.PointRelease); err != nil {
			return nil, err
		}
	}

	repoDir := filepath.Join(opts.Dir, expandLayout(opts.LayoutPath, opts.Release, arch))
	pkgs := opts.Packages
	if len(pkgs) == 0 {
		idx, err := feedindex.ReadDir(repoDir)
		if err != nil {
			return nil, err
		}
		for _, e := range idx.Entries {
			pkgs = append(pkgs, e.Name)
		}
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("%s lists no packages to install", repoDir)
	}

	abs, err := filepath.Abs(opts.Dir)
	if err != nil {
		return nil, err
	}

	body := script(opts.FeedName, opts.Release, opts.LayoutPath, arch, pkgs)
	if opts.Format == "ipk" {
		body = scriptOpkg(opts.FeedName, opts.Release, opts.LayoutPath, arch, opts.UsignKeyID, pkgs)
	}
	// Pulled first and on its own, so that Docker Hub not answering is reported as
	// that. Left to `docker run`, a registry 503 came back as "the feed did not
	// install", exit 7, with no package ever tried.
	if err := ensureImage(ctx, image); err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, "docker", "run", "--rm",
		"--platform", "linux/amd64",
		"-v", abs+":/feed:ro",
		image, "sh", "-c", body)

	out, runErr := cmd.CombinedOutput()
	res := &Result{Image: image, Arch: arch, Installed: pkgs, Log: string(out)}

	for _, line := range strings.Split(res.Log, "\n") {
		if pin, ok := strings.CutPrefix(line, markerPin); ok {
			res.WorldPin = strings.TrimSpace(pin)
		}
		if strings.HasPrefix(line, markerLocal) {
			res.LocalInstall = true
		}
	}

	if runErr != nil {
		return res, fmt.Errorf("%s: the feed did not install: %w\n%s", image, runErr, res.Log)
	}
	if !strings.Contains(res.Log, markerDone) {
		return res, fmt.Errorf("%s: the check did not run to completion\n%s", image, res.Log)
	}
	// If apk ever asks for this, the feed is not trusted as published and the run
	// only appeared to succeed.
	if strings.Contains(res.Log, "--allow-untrusted") {
		return res, fmt.Errorf("%s: apk asked for --allow-untrusted, so the feed is not trusted as published\n%s", image, res.Log)
	}
	return res, nil
}

// ResolveImage picks the router image to install on.
//
// A point release and never the branch tag. The branch image points at the
// -SNAPSHOT repositories, whose kmods directory is keyed by kernel vermagic and is
// replaced whenever the kernel moves — so `apk update` inside it fails for reasons
// that have nothing to do with the feed under test. A check that goes red for
// somebody else's rotation is a check people learn to ignore.
//
// The images trail the newest point release, sometimes by weeks, so the release
// the feed is *built* against is frequently one nobody has published an image for.
// Asking which tags exist and taking the newest is the difference between a check
// that works on the day a release ships and one that has to be repaired by hand.
func ResolveImage(ctx context.Context, line, point string) (string, error) {
	tags, err := imageTags(ctx, line)
	if err != nil || len(tags) == 0 {
		// The registry is not reachable, or has nothing for this line. Fall back to
		// the release the feed was built against and let docker report the truth.
		if point == "" {
			return "", fmt.Errorf("cannot tell which %s router image to use: %w", line, err)
		}
		return imageRef(point), nil
	}
	// Prefer the release the feed was built against, when an image for it exists.
	for _, t := range tags {
		if t == point {
			return imageRef(t), nil
		}
	}
	return imageRef(tags[len(tags)-1]), nil
}

func imageRef(point string) string { return "openwrt/rootfs:x86-64-" + point }

// Three pulls, 5 s and 10 s apart. Variables so tests need not wait.
var (
	pullAttempts = 3
	pullDelay    = 5 * time.Second
)

// ensureImage makes sure the linux/amd64 image is present, pulling it when it is not.
//
// A copy already present is used without asking the registry, as `docker run` would:
// `docker pull` checks the digest even then, and during a Hub outage that would fail
// a run that needs nothing from the Hub.
func ensureImage(ctx context.Context, image string) error {
	have, err := exec.CommandContext(ctx, "docker", "image", "inspect",
		"--format", "{{.Os}}/{{.Architecture}}", image).Output()
	if err == nil && strings.TrimSpace(string(have)) == "linux/amd64" {
		return nil
	}

	delay := pullDelay
	var out []byte
	for attempt := 1; ; attempt++ {
		out, err = exec.CommandContext(ctx, "docker", "pull", "--platform", "linux/amd64", image).CombinedOutput()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !pullOutage(string(out)) {
			// `manifest unknown`, `pull access denied`: the registry answered.
			return fmt.Errorf("docker pull %s: %w\n%s", image, err, out)
		}
		if attempt >= pullAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
	return netx.Outage(fmt.Errorf("docker pull %s failed %d times and the registry is not answering "+
		"(an upstream outage, not a finding about the feed; safe to run again later): %w\n%s",
		image, pullAttempts, err, out))
}

// pullOutageRE matches what `docker pull` prints when the registry, or the way to it,
// is failing. Measured with Docker 29.4.0: an unresolvable registry printed
// `Get "https://nonexistent.invalid/v2/": Bad Gateway` (Docker Desktop's proxy), a
// refused one `dial tcp 127.0.0.1:1: connect: connection refused`. A missing tag
// printed `manifest unknown`, and a missing repository `pull access denied ...
// repository does not exist` -- answers, and deliberately not matched. The rest are
// the registry's own 429 and 5xx texts and Go's transport errors.
var pullOutageRE = regexp.MustCompile(`(?i)toomanyrequests|too many requests|bad gateway|` +
	`service unavailable|gateway time-?out|internal server error|i/o timeout|` +
	`tls handshake timeout|connection reset|connection refused|no such host|unexpected eof|` +
	`request canceled|context deadline exceeded|server misbehaving`)

func pullOutage(out string) bool { return pullOutageRE.MatchString(out) }

// pointTagRE matches a final point release, so release candidates are not picked
// up: an -rc image is a router nobody is running.
var pointTagRE = regexp.MustCompile(`^x86-64-([0-9]+\.[0-9]+\.[0-9]+)$`)

// imageTags lists the published point releases for a line, oldest first.
func imageTags(ctx context.Context, line string) ([]string, error) {
	url := "https://hub.docker.com/v2/repositories/openwrt/rootfs/tags?page_size=100&name=x86-64-" + line
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}

	var doc struct {
		Results []struct {
			Name string `json:"name"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return nil, err
	}

	var out []string
	for _, r := range doc.Results {
		if m := pointTagRE.FindStringSubmatch(r.Name); m != nil {
			out = append(out, m[1])
		}
	}
	sort.Slice(out, func(i, j int) bool { return lessPoint(out[i], out[j]) })
	return out, nil
}

// lessPoint orders point releases numerically, so 25.12.10 follows 25.12.9.
func lessPoint(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, _ := strconv.Atoi(as[i])
		y, _ := strconv.Atoi(bs[i])
		if x != y {
			return x < y
		}
	}
	return len(as) < len(bs)
}

const (
	markerDone  = "OWFEED-SMOKE-OK"
	markerLocal = "OWFEED-SMOKE-LOCAL-OK"
	markerPin   = "OWFEED-SMOKE-PIN "
)

// script is the sequence a subscriber follows, plus the assertions that make it a
// check rather than a demonstration.
func script(feed, release, layout, arch string, pkgs []string) string {
	repo := "/feed/" + expandLayout(layout, release, arch) + "/packages.adb"
	first := pkgs[0]

	return `set -e
mkdir -p /etc/apk/keys /etc/apk/repositories.d
cp /feed/` + feed + `.pem /etc/apk/keys/` + feed + `.pem
echo "` + repo + `" > /etc/apk/repositories.d/` + feed + `.list

apk update
apk add ` + strings.Join(pkgs, " ") + `

# Every installed package must own the files it claims to.
for p in ` + strings.Join(pkgs, " ") + `; do
	apk info -L "$p" >/dev/null || { echo "no file list for $p" >&2; exit 1; }
done

# A trusted per-package signature is what makes this work without a flag, and it is
# the only way LuCI's Upload Package flow can work at all: package-manager-call
# drops arguments it does not recognise, so it cannot pass --allow-untrusted.
apk del ` + first + ` >/dev/null 2>&1 || true
file=$(ls /feed/` + expandLayout(layout, release, arch) + `/` + first + `-*.apk | head -1)
if apk add "$file" >/dev/null 2>&1; then echo "` + markerLocal + `"; fi
echo "` + markerPin + `$(grep '^` + first + `' /etc/apk/world || true)"

echo "` + markerDone + `"
`
}

// scriptOpkg is the 24.10 sequence. Nothing carries over from the apk one: opkg is
// pointed at a directory rather than at the index file, and looks its key up by an
// id that is also the filename.
func scriptOpkg(feed, release, layout, arch, keyID string, pkgs []string) string {
	repo := "file:///feed/" + expandLayout(layout, release, arch)

	return `set -e
# The stock image ships no /var/lock, and opkg refuses to start without it.
mkdir -p /var/lock /etc/opkg/keys
cp /feed/` + keyID + ` /etc/opkg/keys/` + keyID + `

# Signature checking must be on, or this proves nothing about the signature.
grep -q '^option check_signature' /etc/opkg.conf

echo "src/gz ` + feed + ` ` + repo + `" > /etc/opkg/customfeeds.conf
opkg update
opkg install ` + strings.Join(pkgs, " ") + `

for p in ` + strings.Join(pkgs, " ") + `; do
	opkg files "$p" >/dev/null || { echo "no file list for $p" >&2; exit 1; }
done

echo "` + markerDone + `"
`
}

func expandLayout(path, release, arch string) string {
	return config.ExpandLayout(path, release, arch)
}
