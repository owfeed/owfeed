package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"owfeed.org/owfeed/internal/keyring"
	"owfeed.org/owfeed/internal/lock"
)

const (
	recordedKey = "fa584d0cc8f67cafd8528efabc1511af"
	currentKey  = "7b7f6b217239f67f4e24ee3452472e7c"
)

// A key that disagrees with the record is refused unless it is throwaway. #44: a
// dry-run job generates its keys with `owfeed keygen` rather than through `owfeed
// check`, and every pull request failed once the feed had published once.
func TestIndexRefusesAnUnrecordedRotationUnlessThrowaway(t *testing.T) {
	for _, tc := range []struct {
		name      string
		throwaway bool
		check     bool
		recorded  *lock.Keyring
		wantCode  int
		wantVer   string
		wantLog   string
	}{
		{name: "same key", recorded: &lock.Keyring{Identity: currentKey, Version: keyring.VersionFor(3)}, wantVer: keyring.VersionFor(3)},
		{name: "unrecorded rotation", recorded: &lock.Keyring{Identity: recordedKey, Version: keyring.VersionFor(3)}, wantCode: exitConflict},
		{name: "first publication", wantVer: keyring.VersionFor(1), wantLog: "record it with `owfeed lock --update`"},
		{name: "throwaway, recorded", throwaway: true, recorded: &lock.Keyring{Identity: recordedKey, Version: keyring.VersionFor(3)}, wantVer: keyring.VersionFor(3)},
		{name: "throwaway, unrecorded", throwaway: true, wantVer: keyring.VersionFor(1)},
		{name: "owfeed check", check: true, recorded: &lock.Keyring{Identity: recordedKey, Version: keyring.VersionFor(2)}, wantVer: keyring.VersionFor(2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			a := &app{configPath: "owfeed.yml", throwawayKey: tc.throwaway, out: &out, err: &bytes.Buffer{}}
			if tc.check {
				a.checkKeys = &checkKeys{}
			}
			l := &lock.Lock{Keyring: tc.recorded}

			err := a.reconcileKeyring(l, currentKey)
			if tc.wantCode != 0 {
				var e *exitError
				if !errors.As(err, &e) || e.code != tc.wantCode {
					t.Fatalf("err = %v, want exit %d", err, tc.wantCode)
				}
				// The usual advice records the key; for a throwaway one that breaks the
				// next real publish, so the message has to name the other way out.
				if !strings.Contains(err.Error(), "--throwaway-key") {
					t.Errorf("the refusal does not mention --throwaway-key:\n%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if l.Keyring.Identity != currentKey || l.Keyring.Version != tc.wantVer {
				t.Errorf("keyring = %+v, want %s at %s", *l.Keyring, currentKey, tc.wantVer)
			}
			if tc.wantLog != "" && !strings.Contains(out.String(), tc.wantLog) {
				t.Errorf("log lacks %q:\n%s", tc.wantLog, out.String())
			}
			// Advice to record a key that dies with the runner is the harmful half of #44.
			if (tc.throwaway || tc.check) && strings.Contains(out.String(), "lock --update") {
				t.Errorf("a throwaway key was offered for recording:\n%s", out.String())
			}
		})
	}
}

// The flag reaches the field reconcileKeyring reads. It is parsed before the config
// is loaded, so a missing config is enough to see it.
func TestIndexParsesThrowawayKey(t *testing.T) {
	a := &app{configPath: filepath.Join(t.TempDir(), "owfeed.yml"), out: &bytes.Buffer{}, err: &bytes.Buffer{}}
	if err := a.index(context.Background(), []string{"--throwaway-key"}); err == nil {
		t.Fatal("index ran without a config")
	}
	if !a.throwawayKey {
		t.Error("--throwaway-key did not set throwawayKey")
	}
}
