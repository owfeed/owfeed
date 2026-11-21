package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owfeed.org/owfeed/internal/config"
	"owfeed.org/owfeed/internal/keyring"
	"owfeed.org/owfeed/internal/lock"
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

// The gate refuses what `index --throwaway-key` let through with a real key.
func TestPublishRefusesAnUnrecordedKey(t *testing.T) {
	on, off := true, false
	recorded := &lock.Keyring{Identity: recordedKey, Version: keyring.VersionFor(2)}
	for _, tc := range []struct {
		name    string
		keyring bool
		record  *lock.Keyring
		id      string
		refuse  bool
	}{
		{"recorded key", true, recorded, recordedKey, false},
		{"first publication", true, nil, currentKey, false},
		{"no keyring package", false, recorded, currentKey, false},
		{"unrecorded rotation", true, recorded, currentKey, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &config.Config{}
			c.Signing.KeyringPackage = &off
			if tc.keyring {
				c.Signing.KeyringPackage = &on
			}
			err := requireRecordedKey(c, &lock.Lock{Keyring: tc.record}, tc.id, "owfeed.lock")
			var e *exitError
			if tc.refuse != (err != nil) || (err != nil && (!errors.As(err, &e) || e.code != exitConflict)) {
				t.Errorf("err = %v, want refused: %v", err, tc.refuse)
			}
		})
	}
}
