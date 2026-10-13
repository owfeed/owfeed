package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The upload takes the whole tree, so publish has to refuse what it cannot skip.
func TestPublishRefusesASqueezeLeftover(t *testing.T) {
	out := t.TempDir()
	write(t, filepath.Join(out, "demo.pem"), "key")
	write(t, filepath.Join(out, ".nojekyll"), "")
	arch := filepath.Join(out, "releases", "25.12", "x86_64")
	if err := os.MkdirAll(arch, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(arch, "demo-1.0-r1.apk"), "apk")
	if err := requireTreeFiles(out, "demo"); err != nil {
		t.Fatalf("a clean tree was refused: %v", err)
	}

	write(t, filepath.Join(arch, ".owfeed-squeeze-42.tmp"), "half a package")
	err := requireTreeFiles(out, "demo")
	if err == nil || !strings.Contains(err.Error(), ".owfeed-squeeze-42.tmp") {
		t.Fatalf("a leftover from a killed sign was not refused: %v", err)
	}
}
