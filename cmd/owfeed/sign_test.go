package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// A feed that does not sign packages never reaches the step that re-deflates an
// .apk, so a zopfli opt-in there must say what it did not do.
func TestSignSaysZopfliSkippedWhenNotSigning(t *testing.T) {
	root := t.TempDir()
	cfg := strings.Replace(checkConfig, "publish:", "signing:\n  sign-packages: false\npublish:", 1)
	cfg = strings.Replace(cfg, "    files: ./root\n", "    files: ./root\n    compression: zopfli\n", 1)
	write(t, filepath.Join(root, "owfeed.yml"), cfg)

	var out bytes.Buffer
	a := &app{configPath: filepath.Join(root, "owfeed.yml"), out: &out, err: &bytes.Buffer{}}
	if err := a.sign(context.Background(), []string{filepath.Join(root, "dist")}); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if !strings.Contains(out.String(), "compression: zopfli on demo reached the .ipk only") {
		t.Errorf("no note about the skipped re-deflate:\n%s", out.String())
	}
}

// The failure names the check that actually ran, the file, and the way out.
func TestSqueezeFailedSaysWhatRan(t *testing.T) {
	msg := squeezeFailed(errors.New("x.apk changed while it was re-deflated"), "noarch/x.apk", "x").Error()
	for _, want := range []string{"x.apk changed", "noarch/x.apk was not replaced", "verify", "remove `compression: zopfli` from package x"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "OWF303") {
		t.Errorf("message credits OWF303 with a verification it does not do:\n%s", msg)
	}
}
