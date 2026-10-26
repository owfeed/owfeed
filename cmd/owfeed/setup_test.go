package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// fakeGH stands in for gh in the setup action. FAKE_GH says what it does: "ok" answers
// like a healthy GitHub, "hang-download" and "hang-verify" accept the call and never
// finish it, and anything else is printed to stderr with exit 1, which is how gh
// reports every failure. Every call is logged to FAKE_GH_LOG.
//
// It execs sleep rather than running it, so that stopping gh stops the sleep too: a
// leftover sleep would hold the test's output pipe open.
const fakeGH = `#!/usr/bin/env bash
echo "$*" >> "$FAKE_GH_LOG"
case "$FAKE_GH:$1 $2" in
  "hang-download:release download"|"hang-verify:attestation verify") exec sleep 60 ;;
  "ok:release view") echo v0.5.5 ;;
  "ok:attestation verify"|"hang-download:attestation verify") echo "Verification succeeded!" >&2 ;;
  "ok:release download"|"hang-verify:release download")
    while [ "$1" != --dir ]; do shift; done
    printf '#!/bin/sh\necho "owfeed 0.5.5"\n' > "$2/owfeed-linux-amd64" ;;
  *) echo "$FAKE_GH" >&2; exit 1 ;;
esac
`

// setupScript returns the install step of setup/action.yml, with its deadline and its
// pauses shortened so that a test of four attempts takes seconds. It fails the test
// when one of those lines is no longer there, rather than letting it run for minutes.
func setupScript(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../setup/action.yml")
	if err != nil {
		t.Fatal(err)
	}
	var action struct {
		Runs struct {
			Steps []struct {
				ID  string `yaml:"id"`
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(raw, &action); err != nil {
		t.Fatal(err)
	}
	for _, step := range action.Runs.Steps {
		if step.ID != "install" {
			continue
		}
		script := step.Run
		for old, new := range map[string]string{"GH_DEADLINE=120": "GH_DEADLINE=1", "delay=5": "delay=0"} {
			if !strings.Contains(script, old) {
				t.Fatalf("setup/action.yml no longer has %q; update this test", old)
			}
			script = strings.ReplaceAll(script, old, new)
		}
		return script
	}
	t.Fatal("setup/action.yml has no step with id: install")
	return ""
}

type setupRun struct {
	code    int
	output  string
	calls   []string
	took    time.Duration
	tempDir string
}

func runSetup(t *testing.T, mode, version string) setupRun {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Fatal("the setup action runs under bash, which is not on PATH")
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "bin")
	runnerTemp := filepath.Join(tmp, "runner")
	for _, d := range []string{bin, runnerTemp} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGH), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(tmp, "gh.log")

	// A bound of its own, so that a deadline that does not work fails the test
	// rather than holding it until go test's.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", setupScript(t))
	cmd.WaitDelay = time.Second
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_GH="+mode, "FAKE_GH_LOG="+log,
		"RUNNER_OS=Linux", "RUNNER_ARCH=X64", "RUNNER_TEMP="+runnerTemp,
		"GITHUB_PATH="+filepath.Join(tmp, "path"), "GITHUB_OUTPUT="+filepath.Join(tmp, "output"),
		"OWFEED_VERSION="+version, "OWFEED_VERIFY=true", "OWFEED_ACTION_REF=v0.5.5",
	)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	r := setupRun{output: string(out), took: time.Since(start), tempDir: runnerTemp}
	if ctx.Err() != nil {
		t.Fatalf("the setup action was still running after 30s\n%s", out)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		r.code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(log); err == nil {
		r.calls = strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	return r
}

// A gh call that never finishes is stopped, asked again, and reported as an outage.
// Before the deadline it held the step until the job's own timeout.
func TestSetupStopsAGhCallThatNeverFinishes(t *testing.T) {
	cases := map[string]struct {
		mode, call string
	}{
		"release download":   {"hang-download", "release download"},
		"attestation verify": {"hang-verify", "attestation verify"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := runSetup(t, c.mode, "v0.5.5")
			if r.code != exitUpstream {
				t.Fatalf("exit %d, want %d\n%s", r.code, exitUpstream, r.output)
			}
			n := 0
			for _, call := range r.calls {
				if strings.HasPrefix(call, c.call) {
					n++
				}
			}
			if n != 4 {
				t.Errorf("gh %s ran %d times, want 4\n%v", c.call, n, r.calls)
			}
			for _, want := range []string{"gh " + c.call + ": no answer within 1s", "safe to rerun"} {
				if !strings.Contains(r.output, want) {
					t.Errorf("the output does not say %q:\n%s", want, r.output)
				}
			}
			if _, err := os.Stat(filepath.Join(r.tempDir, "owfeed", "owfeed")); err == nil {
				t.Error("an unverified binary was installed")
			}
		})
	}
}

// gh's own 60 s API timeout reads `context deadline exceeded`, which is GitHub not
// answering. It used to exit 7, the code for an answer CI must not retry.
func TestSetupRetriesGhsOwnTimeout(t *testing.T) {
	r := runSetup(t, `Get "https://api.github.com/repos/owfeed/owfeed/releases/tags/v0.5.5": context deadline exceeded`, "v0.5.5")
	if r.code != exitUpstream || len(r.calls) != 4 {
		t.Fatalf("exit %d after %d calls, want %d after 4\n%s", r.code, len(r.calls), exitUpstream, r.output)
	}
}

// The other direction: a healthy GitHub is answered at once, through the $(...) that
// reads `gh release view`, and an answer is still not retried.
func TestSetupDeadlineStaysOutOfTheWay(t *testing.T) {
	r := runSetup(t, "ok", "latest")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.output)
	}
	if !strings.Contains(r.output, "owfeed 0.5.5") || len(r.calls) != 3 {
		t.Fatalf("want three gh calls and owfeed installed, got %v\n%s", r.calls, r.output)
	}
	// Each call returns at once; a second per call would mean the watchdog held it.
	if r.took > 2*time.Second {
		t.Errorf("a healthy install took %s", r.took)
	}

	r = runSetup(t, "release not found", "v0.5.5")
	if r.code != exitCheck || len(r.calls) != 1 {
		t.Fatalf("exit %d after %d calls, want %d after 1: an answer is not retried\n%s", r.code, len(r.calls), exitCheck, r.output)
	}
}
