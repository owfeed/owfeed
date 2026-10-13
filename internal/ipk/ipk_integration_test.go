package ipk_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owfeed.org/owfeed/internal/ipk"
)

// 24.10 is opkg, and the only useful proof that a container is right is opkg
// installing it. A package that merely looks plausible is one that fails on
// somebody's router.
//
//	OWFEED_INTEGRATION=1 go test ./internal/ipk/ -run Integration -v
const image = "openwrt/rootfs:x86-64-24.10.8"

func TestIntegrationInstallsOnOpkg(t *testing.T) {
	if os.Getenv("OWFEED_INTEGRATION") == "" {
		t.Skip("set OWFEED_INTEGRATION=1 to run (needs Docker)")
	}

	payload := t.TempDir()
	for path, body := range map[string]string{
		"etc/config/demo":            "config demo\n\toption enabled '1'\n",
		"www/luci-static/demo/x.css": "body{}\n",
		"usr/lib/lua/luci/demo.lua":  "return {}\n",
	} {
		full := filepath.Join(payload, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// zopfli writes the same container with smaller streams; opkg must not notice.
	for _, zopfli := range []bool{false, true} {
		t.Run(fmt.Sprintf("zopfli=%v", zopfli), func(t *testing.T) {
			out := t.TempDir()
			file, err := ipk.Build(ipk.Package{
				Name: "luci-app-demo", Version: "1.0.0-r1", Arch: ipk.ArchAll,
				Description: "A demo application.\n\nWith a second paragraph.",
				License:     "Apache-2.0",
				Maintainer:  "Demo <demo@example.org>",
				Section:     "luci",
				Depends:     []string{"libc"},
				Conffiles:   []string{"/etc/config/demo"},
				Scripts:     map[string]string{"postinst": "#!/bin/sh\nexit 0\n"},
			}, ipk.Options{Payload: payload, OutDir: out, Epoch: time.Unix(1700000000, 0), Zopfli: zopfli})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if filepath.Base(file) != "luci-app-demo_1.0.0-r1_all.ipk" {
				t.Errorf("built %s, want the name ipkg-build would produce", filepath.Base(file))
			}

			script := `set -e
		# The rootfs image ships no /var/lock, and opkg refuses to start without it.
		mkdir -p /var/lock
		opkg install /pkg/` + filepath.Base(file) + `
		test -f /etc/config/demo
		test -f /www/luci-static/demo/x.css
		grep -q '^/etc/config/demo$' /usr/lib/opkg/info/luci-app-demo.conffiles
		opkg status luci-app-demo | grep -q '^Architecture: all'
		echo OPKG-OK
		`
			cmd := exec.Command("docker", "run", "--rm", "--platform", "linux/amd64",
				"-v", out+":/pkg:ro", image, "sh", "-c", script)
			got, err := cmd.CombinedOutput()
			t.Logf("opkg:\n%s", got)
			if err != nil {
				t.Fatalf("opkg refused the package: %v", err)
			}
			if !strings.Contains(string(got), "OPKG-OK") {
				t.Fatal("the check did not run to completion")
			}
		})
	}
}

// The same inputs must produce the same bytes.
func TestIntegrationReproducible(t *testing.T) {
	if os.Getenv("OWFEED_INTEGRATION") == "" {
		t.Skip("set OWFEED_INTEGRATION=1 to run")
	}
	payload := t.TempDir()
	if err := os.WriteFile(filepath.Join(payload, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := ipk.Package{Name: "d", Version: "1.0-r1", Arch: ipk.ArchAll}
	for _, zopfli := range []bool{false, true} {
		var sums []string
		for i := 0; i < 2; i++ {
			out := t.TempDir()
			f, err := ipk.Build(p, ipk.Options{Payload: payload, OutDir: out, Epoch: time.Unix(1700000000, 0), Zopfli: zopfli})
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			sums = append(sums, string(b))
		}
		if sums[0] != sums[1] {
			t.Errorf("zopfli=%v: two builds of identical inputs differ", zopfli)
		}
	}
}
