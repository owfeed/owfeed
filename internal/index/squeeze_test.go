package index

import (
	"bytes"
	"compress/flate"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Every refusal here happens before apk is reached, so no tool is needed.

func TestSqueezeRefusesANonRegularFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real.apk"), []byte("ADBd"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.apk", filepath.Join(dir, "link.apk")); err != nil {
		t.Fatal(err)
	}
	_, _, err := Squeeze(context.Background(), nil, dir, "link.apk", Signer{}, "")
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("a symlink was accepted: %v", err)
	}
}

func TestSqueezeCapsTheInflate(t *testing.T) {
	was := maxADB
	maxADB = 1 << 10
	t.Cleanup(func() { maxADB = was })

	var z bytes.Buffer
	w, _ := flate.NewWriter(&z, flate.BestCompression)
	w.Write(make([]byte, 64<<10))
	w.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bomb.apk"), append([]byte("ADBd"), z.Bytes()...), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := Squeeze(context.Background(), nil, dir, "bomb.apk", Signer{}, "")
	if err == nil || !strings.Contains(err.Error(), "inflates past") {
		t.Fatalf("64 KiB inflated under a 1 KiB cap: %v", err)
	}
}

func TestUnchangedNoticesARewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.apk")
	write := func(b string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stat := func() os.FileInfo {
		t.Helper()
		fi, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		return fi
	}

	write("before")
	was := stat()
	if err := unchanged(path, was); err != nil {
		t.Fatalf("an untouched file: %v", err)
	}

	write("longer than before")
	if unchanged(path, was) == nil {
		t.Error("a size change went unnoticed")
	}

	write("before")
	was = stat()
	if err := os.Chtimes(path, time.Now(), was.ModTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if unchanged(path, was) == nil {
		t.Error("an mtime change went unnoticed")
	}

	// Same size and mtime, but another file renamed into place.
	was = stat()
	other := filepath.Join(dir, "other")
	if err := os.WriteFile(other, []byte("BEFORE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, time.Now(), was.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, path); err != nil {
		t.Fatal(err)
	}
	if unchanged(path, was) == nil {
		t.Error("a replacement with the same size and mtime went unnoticed")
	}
}
