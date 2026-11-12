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

// DefaultArch is the architecture smoked when none is named. It is x86_64 because
// that is the image Docker runs without emulation on most hosts; any other needs QEMU
// registered with binfmt_misc.
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
	// Image is the router image. Empty derives one from Arch and PointRelease.
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
	imgArch := imageArch(arch)
	image := opts.Image
	if image == "" {
		var err error
		if image, err = ResolveImage(ctx, imgArch, opts.Release, opts.PointRelease); err != nil {
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
	platform, err := ensureImage(ctx, image, dockerPlatform(imgArch))
	if err != nil {
		return nil, err
	}
	// The script adds arch to the router's accepted list, so an image of another
	// instruction set would install the package without ever being able to run it.
	if isa(platform) != isa(arch) {
		return nil, fmt.Errorf("%s is a %s image and cannot run %s packages; pass -image with an %s one",
			image, platform, arch, isa(arch))
	}

	// The platform is the image's own. OpenWrt labels its images with the package
	// arch (linux/aarch64_generic), and `docker run` refuses any other platform.
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm",
		"--platform", platform,
		"-v", abs+":/feed:ro",
		image, "sh", "-c", body)

	out, runErr := cmd.CombinedOutput()
	res := &Result{Image: image, Arch: arch, Installed: pkgs, Log: string(out)}

	if runErr != nil && strings.Contains(res.Log, "exec /bin/sh: exec format error") {
		return res, fmt.Errorf("%s: this host cannot run %s images, so nothing was installed. "+
			"Register QEMU for it with binfmt_misc, e.g. `docker run --privileged --rm tonistiigi/binfmt --install all`\n%s",
			image, platform, res.Log)
	}

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
//
// arch is the image's arch, as imageArch gives it.
func ResolveImage(ctx context.Context, arch, line, point string) (string, error) {
	tags, err := imageTags(ctx, arch, line)
	if err != nil {
		// The registry is not reachable. Fall back to the release the feed was built
		// against and let docker report the truth.
		if point == "" {
			return "", fmt.Errorf("cannot tell which %s router image to use: %w", line, err)
		}
		return imageRef(arch, point), nil
	}
	if len(tags) == 0 {
		return "", fmt.Errorf("no openwrt/rootfs:%s-%s.N image is published on Docker Hub; "+
			"pass -image with one that runs %s packages", arch, line, arch)
	}
	// Prefer the release the feed was built against, when an image for it exists.
	for _, t := range tags {
		if t == point {
			return imageRef(arch, t), nil
		}
	}
	return imageRef(arch, tags[len(tags)-1]), nil
}

// The image tags are named after the package arch, x86_64 included: x86_64-25.12.6
// and x86-64-25.12.6 are one digest.
func imageRef(arch, point string) string { return "openwrt/rootfs:" + arch + "-" + point }

// imageArch is the arch of the image a package for arch is installed on.
//
// OpenWrt publishes images for a handful of arches only. Every aarch64_* arch is
// ARMv8-A with one ABI, so its packages run on the aarch64_generic image. The 32-bit
// arm and mips arches differ in float ABI and byte order, so none stands in for
// another; those without an image of their own need -image.
func imageArch(arch string) string {
	if isa(arch) == "aarch64" {
		return "aarch64_generic"
	}
	return arch
}

// dockerPlatform is the platform an OpenWrt image of arch is published under.
// x86_64 is the one labelled the Docker way; the rest carry the package arch.
func dockerPlatform(arch string) string {
	if arch == "x86_64" {
		return "linux/amd64"
	}
	return "linux/" + arch
}

// isa is the instruction set a package arch or a Docker platform names: the part
// before the first "_", so aarch64_cortex-a53 and linux/aarch64_generic agree.
func isa(arch string) string {
	arch = strings.TrimPrefix(arch, "linux/")
	switch arch {
	case "amd64", "x86_64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	case "386":
		return "i386"
	}
	name, _, _ := strings.Cut(arch, "_")
	return name
}

// Three pulls, 5 s and 10 s apart. Variables so tests need not wait.
var (
	pullAttempts = 3
	pullDelay    = 5 * time.Second
)

// ensureImage makes sure image is present, pulling it for platform when it is not,
// and returns the platform the local copy is for.
//
// A copy already present is used without asking the registry, as `docker run` would:
// `docker pull` checks the digest even then, and during a Hub outage that would fail
// a run that needs nothing from the Hub. Any copy of platform's instruction set will
// do, so an -image of a sibling arch is not pulled again on every run.
//
// The platform is read back rather than assumed: `docker pull --platform` does not
// check it against an image published for one platform only.
func ensureImage(ctx context.Context, image, platform string) (string, error) {
	inspect := func() string {
		have, err := exec.CommandContext(ctx, "docker", "image", "inspect",
			"--format", "{{.Os}}/{{.Architecture}}", image).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(have))
	}
	if have := inspect(); have != "" && isa(have) == isa(platform) {
		return have, nil
	}

	delay := pullDelay
	var out []byte
	var err error
	for attempt := 1; ; attempt++ {
		out, err = exec.CommandContext(ctx, "docker", "pull", "--platform", platform, image).CombinedOutput()
		if err == nil {
			if have := inspect(); have != "" {
				return have, nil
			}
			return "", fmt.Errorf("docker image inspect %s: no image after a successful pull", image)
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if !pullOutage(string(out)) {
			// `manifest unknown`, `pull access denied`: the registry answered.
			return "", fmt.Errorf("docker pull %s: %w\n%s", image, err, out)
		}
		if attempt >= pullAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
	return "", netx.Outage(fmt.Errorf("docker pull %s failed %d times and the registry is not answering "+
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

// imageTags lists the published point releases of arch's image for a line, oldest
// first.
func imageTags(ctx context.Context, arch, line string) ([]string, error) {
	url := "https://hub.docker.com/v2/repositories/openwrt/rootfs/tags?page_size=100&name=" + arch + "-" + line
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

	names := make([]string, len(doc.Results))
	for i, r := range doc.Results {
		names[i] = r.Name
	}
	return pointTags(arch, names), nil
}

// pointTags keeps the final point releases of arch's image, oldest first. Release
// candidates are dropped: an -rc image is a router nobody is running. The Hub's name
// filter is a LIKE pattern, where "_" matches any character, so x86_64-25.12 also
// returns x86-64-25.12.6; the exact match here is what tells them apart.
func pointTags(arch string, names []string) []string {
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(arch) + `-([0-9]+\.[0-9]+\.[0-9]+)$`)
	var out []string
	for _, n := range names {
		if m := re.FindStringSubmatch(n); m != nil {
			out = append(out, m[1])
		}
	}
	sort.Slice(out, func(i, j int) bool { return lessPoint(out[i], out[j]) })
	return out
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
# An aarch64_cortex-a53 package on the aarch64_generic image is "uninstallable"
# until its arch is one apk accepts here. On the arch's own image this changes nothing.
grep -qx '` + arch + `' /etc/apk/arch || echo '` + arch + `' >> /etc/apk/arch
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

# As for apk, a sibling arch is refused until opkg accepts it. The first arch line
# replaces opkg's built-in list, so that list is written out before it is extended.
if ! opkg print-architecture | grep -q '^arch ` + arch + ` '; then
	opkg print-architecture >> /etc/opkg.conf
	echo 'arch ` + arch + ` 20' >> /etc/opkg.conf
fi

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
