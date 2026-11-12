package smoke

import (
	"strings"
	"testing"
)

// A dual-line feed must send each router the script its package manager can run.
// Reading the format off the default line instead of the requested one sent the apk
// script to a 24.10 router, where the first command is `apk` and there is no apk.
func TestScriptMatchesTheRequestedFormat(t *testing.T) {
	pkgs := []string{"luci-app-demo"}

	apk := script("myfeed", "25.12", "releases/{release}/{arch}", "x86_64", pkgs)
	if !strings.Contains(apk, "apk update") || strings.Contains(apk, "opkg") {
		t.Errorf("the apk script is not an apk script:\n%s", apk)
	}
	if !strings.Contains(apk, "/etc/apk/repositories.d/myfeed.list") {
		t.Error("the apk repository line does not name the index file")
	}

	opkg := scriptOpkg("myfeed", "24.10", "releases/{release}/{arch}", "x86_64", "0bdde418579c2850", pkgs)
	if !strings.Contains(opkg, "opkg update") || strings.Contains(opkg, "apk ") {
		t.Errorf("the opkg script is not an opkg script:\n%s", opkg)
	}
	// The key's filename is its id: opkg looks it up by that, where apk matches on
	// the identity inside the signature and ignores the name.
	if !strings.Contains(opkg, "/etc/opkg/keys/0bdde418579c2850") {
		t.Error("the opkg script does not install the key under its id")
	}
	// opkg is pointed at the directory and appends the filename itself.
	if strings.Contains(opkg, "Packages.gz\"") {
		t.Error("the opkg repository line names a file rather than the directory")
	}
	// Nothing a smoke run executes may turn verification off. The apk script names
	// the flag in a comment explaining why it is not needed, which is the point of
	// looking at commands rather than at the whole text.
	for _, s := range []string{apk, opkg} {
		for _, line := range strings.Split(s, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if strings.Contains(line, "allow-untrusted") {
				t.Errorf("a smoke script disables verification: %s", line)
			}
		}
	}
}

// A package is installed on the image of its own arch where OpenWrt publishes one,
// on aarch64_generic for any aarch64 arch, and run under the platform the image is
// labelled with: linux/amd64 for x86_64 only, the package arch for the rest.
func TestImageFollowsTheArch(t *testing.T) {
	cases := []struct{ arch, image, platform string }{
		{"x86_64", "openwrt/rootfs:x86_64-25.12.6", "linux/amd64"},
		{"aarch64_cortex-a53", "openwrt/rootfs:aarch64_generic-25.12.6", "linux/aarch64_generic"},
		{"aarch64_generic", "openwrt/rootfs:aarch64_generic-25.12.6", "linux/aarch64_generic"},
		{"mips_24kc", "openwrt/rootfs:mips_24kc-25.12.6", "linux/mips_24kc"},
		{"arm_cortex-a7_neon-vfpv4", "openwrt/rootfs:arm_cortex-a7_neon-vfpv4-25.12.6", "linux/arm_cortex-a7_neon-vfpv4"},
	}
	for _, c := range cases {
		a := imageArch(c.arch)
		if got := imageRef(a, "25.12.6"); got != c.image {
			t.Errorf("%s: image %s, want %s", c.arch, got, c.image)
		}
		if got := dockerPlatform(a); got != c.platform {
			t.Errorf("%s: platform %s, want %s", c.arch, got, c.platform)
		}
		if isa(dockerPlatform(a)) != isa(c.arch) {
			t.Errorf("%s: its own image's platform %s is refused", c.arch, dockerPlatform(a))
		}
	}
	// An image of another instruction set must be refused, or the arch line the
	// script adds would install the package where it can never run.
	for _, p := range []struct{ platform, arch string }{
		{"linux/amd64", "aarch64_cortex-a53"},
		{"linux/aarch64_generic", "arm_cortex-a7_neon-vfpv4"},
		{"linux/mips_24kc", "mipsel_24kc"},
	} {
		if isa(p.platform) == isa(p.arch) {
			t.Errorf("a %s image is accepted for %s", p.platform, p.arch)
		}
	}
	if isa("linux/arm64") != isa("aarch64_cortex-a53") {
		t.Error("a Docker-labelled linux/arm64 image is refused for aarch64")
	}
}

// What the Hub returned for name=x86_64-25.12: its "_" is a wildcard, so the x86-64
// spelling and release candidates come back too.
func TestPointTagsKeepsOnlyTheArchsFinalReleases(t *testing.T) {
	names := []string{"x86-64-25.12.6", "x86_64-25.12.10", "x86_64-25.12.0-rc1",
		"x86_64-25.12-SNAPSHOT", "x86_64-25.12.9", "x86_64-v25.12.9"}
	got := strings.Join(pointTags("x86_64", names), " ")
	if got != "25.12.9 25.12.10" {
		t.Errorf("pointTags = %q, want %q", got, "25.12.9 25.12.10")
	}
}

// Both scripts must make the package's arch acceptable, or a sibling-arch package
// is refused as uninstallable before anything about the feed is tried.
func TestScriptsAcceptThePackageArch(t *testing.T) {
	pkgs := []string{"luci-app-demo"}
	apk := script("myfeed", "25.12", "releases/{release}/{arch}", "aarch64_cortex-a53", pkgs)
	if !strings.Contains(apk, "echo 'aarch64_cortex-a53' >> /etc/apk/arch") {
		t.Errorf("the apk script does not add the arch:\n%s", apk)
	}
	opkg := scriptOpkg("myfeed", "24.10", "releases/{release}/{arch}", "aarch64_cortex-a53", "0bdde418579c2850", pkgs)
	if !strings.Contains(opkg, "echo 'arch aarch64_cortex-a53 20' >> /etc/opkg.conf") {
		t.Errorf("the opkg script does not add the arch:\n%s", opkg)
	}
}
