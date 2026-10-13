package index

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackagesIsSortedAndUnprefixed(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"zeta-1.0-r1.apk", "alpha-2.0-r1.apk", "notes.txt", "packages.adb"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.apk"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Packages(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha-2.0-r1.apk", "zeta-1.0-r1.apk"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Packages = %v, want %v", got, want)
	}
	// Names carry no directory part: mkndx records what it is handed, and a path
	// prefix in the index makes every download URL it implies wrong.
	for _, p := range got {
		if strings.ContainsRune(p, os.PathSeparator) {
			t.Errorf("%q carries a path prefix", p)
		}
	}
}

func TestSigLineExtractsIdentity(t *testing.T) {
	const line = "# sig v00 h04 648ed1437a28ed118ab8885a422667243045022024a7a24edd51f40036e7485e1a17519c463fd878689cdae1c09f709d067927070221..: UNTRUSTED signature"
	m := sigLine.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("sigLine did not match adbdump's output:\n%s", line)
	}
	if m[1] != "648ed1437a28ed118ab8885a42266724" {
		t.Errorf("identity = %q, want the first 16 bytes of the hex run", m[1])
	}
	if sigLine.MatchString("  name: something") {
		t.Error("sigLine matched an ordinary dump line")
	}
}

func TestWriteSums(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "b.apk"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.adb"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteSums(dir); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(dir, SumsFile))
	if err != nil {
		t.Fatal(err)
	}
	// sha256("a") and sha256("b"), in the format OpenWrt's release directories use.
	want := "ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb *a.adb\n" +
		"3e23e8160039594a33894f6564e1b1348bbd7a0088d42c4acb73eeaed59c009d *b.apk\n"
	if string(got) != want {
		t.Errorf("sha256sums =\n%s\nwant\n%s", got, want)
	}

	// A second run must not fold the previous sha256sums into itself.
	if err := WriteSums(dir); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(filepath.Join(dir, SumsFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != want {
		t.Errorf("second run changed sha256sums:\n%s", again)
	}
}

// A killed `owfeed sign` can leave Squeeze's temporary file behind; it is not part
// of the feed and must not be vouched for. Other dotfiles are, so only the exact
// pattern is skipped.
func TestWriteSumsSkipsASqueezeLeftover(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a.apk", ".owfeed-squeeze-123.tmp", ".kept"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteSums(dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, SumsFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "owfeed-squeeze") {
		t.Errorf("sha256sums lists the leftover:\n%s", got)
	}
	if !strings.Contains(string(got), " *a.apk\n") || !strings.Contains(string(got), " *.kept\n") {
		t.Errorf("sha256sums dropped a real file:\n%s", got)
	}
}
