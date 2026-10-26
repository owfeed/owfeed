package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"owfeed.org/owfeed/internal/release"
	"owfeed.org/owfeed/internal/usign"
)

// The release job of package.yml is shell that runs only on a tag, against a live
// release, which is the one place nobody watches it fail. These tests run its steps the
// way GitHub does, `bash -eo pipefail`, with gh, curl, owfeed and sleep replaced by
// scripts that answer what each case needs.

type stepRun struct {
	code   int
	log    string
	output map[string]string // what the step wrote to $GITHUB_OUTPUT
}

type workspace struct {
	t   *testing.T
	dir string // the job's working directory
	bin string // the stubs, first on PATH
	tmp string // $RUNNER_TEMP
}

func newWorkspace(t *testing.T) *workspace {
	w := &workspace{t: t, dir: t.TempDir(), bin: t.TempDir(), tmp: t.TempDir()}
	w.stub("sleep", "exit 0")
	return w
}

// stub puts a script called name first on PATH. It sees its 1-based call number as
// $n, and every call's arguments are kept for calls().
func (w *workspace) stub(name, body string) {
	w.t.Helper()
	calls := filepath.Join(w.bin, name+".calls")
	writeFile(w.t, calls, "", 0o644)
	writeFile(w.t, filepath.Join(w.bin, name), "#!/bin/bash\n"+
		`n=$(( $(wc -l < "`+calls+`") + 1 ))`+"\n"+
		`printf '%s\n' "$*" >> "`+calls+`"`+"\n"+
		body+"\n", 0o755)
}

func (w *workspace) calls(name string) []string {
	w.t.Helper()
	data, err := os.ReadFile(filepath.Join(w.bin, name+".calls"))
	if err != nil {
		w.t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// run runs the release job's step with this id or name in the workspace, for tag
// v1.2.3 of example/repo, with env added to the environment.
func (w *workspace) run(step string, env ...string) stepRun {
	w.t.Helper()
	script := filepath.Join(w.tmp, "step.sh")
	writeFile(w.t, script, packageStep(w.t, step), 0o644)
	output := filepath.Join(w.tmp, "github_output")
	writeFile(w.t, output, "", 0o644)

	cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", script)
	cmd.Dir = w.dir
	cmd.Env = append(os.Environ(),
		"PATH="+w.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GITHUB_REPOSITORY=example/repo",
		"GITHUB_REF_NAME=v1.2.3",
		"GITHUB_OUTPUT="+output,
		"RUNNER_TEMP="+w.tmp,
	)
	cmd.Env = append(cmd.Env, env...)
	log, err := cmd.CombinedOutput()
	r := stepRun{log: string(log), output: map[string]string{}}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		r.code = exit.ExitCode()
	case err != nil:
		w.t.Fatalf("running step %q: %v", step, err)
	}

	data, err := os.ReadFile(output)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			r.output[k] = v
		}
	}
	return r
}

type workflowStep struct {
	ID, Name, If, Uses, Run string
	Env                     map[string]string
	With                    map[string]any
}

type workflowJob struct {
	Concurrency struct {
		Group            string
		CancelInProgress bool `yaml:"cancel-in-progress"`
	}
	Steps []workflowStep
}

// packageJob returns package.yml's release job.
func packageJob(t *testing.T) workflowJob {
	t.Helper()
	data, err := os.ReadFile("../../.github/workflows/package.yml")
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs map[string]workflowJob
	}
	if err := yaml.Unmarshal(data, &wf); err != nil {
		t.Fatalf("package.yml: %v", err)
	}
	return wf.Jobs["release"]
}

// packageSteps returns the steps of package.yml's release job.
func packageSteps(t *testing.T) []workflowStep {
	t.Helper()
	return packageJob(t).Steps
}

// packageStep returns the run: script of the release job's step with this id or name.
func packageStep(t *testing.T, step string) string {
	t.Helper()
	for _, s := range packageSteps(t) {
		if s.ID != step && s.Name != step {
			continue
		}
		if strings.Contains(s.Run, "${{") {
			t.Fatalf("step %q interpolates an expression into run:, which this test runs literally; pass it through env:", step)
		}
		return s.Run
	}
	t.Fatalf("package.yml has no release step %q", step)
	return ""
}

func (r stepRun) expect(t *testing.T, code int, output map[string]string, logs ...string) {
	t.Helper()
	if r.code != code {
		t.Errorf("exit %d, want %d\n%s", r.code, code, r.log)
	}
	if !reflect.DeepEqual(r.output, output) {
		t.Errorf("GITHUB_OUTPUT %v, want %v\n%s", r.output, output, r.log)
	}
	for _, s := range logs {
		if !strings.Contains(r.log, s) {
			t.Errorf("the log lacks %q:\n%s", s, r.log)
		}
	}
}

var none = map[string]string{}

// A command at the start of a line, so that an error message naming it does not count.
var writesRelease = regexp.MustCompile(`(?m)^\s*(if\s+)?(owfeed (sign|release)|gh release (edit|upload|create|delete))\b`)

// Every step that signs or writes to the release is skipped once the state step has
// found the release published, and runs after it. A step added later without that
// guard would sign a published release again.
func TestPackageRerunSignsNothing(t *testing.T) {
	const guard = "steps.state.outputs.published != 'true'"
	state, guarded := -1, 0
	for i, s := range packageSteps(t) {
		if s.ID == "state" {
			state = i
			continue
		}
		touches := strings.Contains(s.Uses, "action-gh-release")
		for _, v := range s.Env {
			touches = touches || strings.Contains(v, "secrets.")
		}
		touches = touches || writesRelease.MatchString(s.Run)
		if !touches {
			continue
		}
		guarded++
		name := s.Name + s.ID + s.Uses
		if state < 0 {
			t.Errorf("step %q runs before the step that finds out whether the release is published", name)
		}
		if !strings.Contains(s.If, guard) {
			t.Errorf("step %q signs or writes to the release, and its if: lacks %s", name, guard)
		}
	}
	// The sign, manifest, upload and publish steps at least; fewer means this test lost them.
	if guarded < 4 {
		t.Errorf("found %d steps that sign or write to the release, want at least 4", guarded)
	}
}

// Two runs at one tag side by side would both find the release unpublished and both
// sign, so the job is queued per tag. Cancelling the running one instead would kill a
// release halfway through its upload.
func TestPackageRunsOncePerTag(t *testing.T) {
	c := packageJob(t).Concurrency
	if !strings.Contains(c.Group, "github.ref }}") {
		t.Errorf("the release job's concurrency group is %q, want one per github.ref", c.Group)
	}
	if c.CancelInProgress {
		t.Error("the release job cancels a run in progress, which can stop a release halfway through its upload")
	}
}

// A step that belongs to one path carries that path's condition in its if:, so a run on
// the other path never reaches it. Without the extra-artifact guard, a caller that sets
// no extra-artifact would have download-artifact fetch every artifact of its run, each
// in a directory, and the job would refuse them; without the published guard, a first
// release would try to download itself before it exists.
func TestPackageStepsStayOnTheirPath(t *testing.T) {
	for _, tc := range []struct{ ref, guard string }{
		{"inputs.extra-artifact", "inputs.extra-artifact != ''"},
		{"inputs.artifact", "steps.state.outputs.published != 'true'"},
		{"gh release download", "steps.state.outputs.published == 'true'"},
	} {
		found := 0
		for _, s := range packageSteps(t) {
			text := s.Run
			for _, v := range s.Env {
				text += "\n" + v
			}
			for _, v := range s.With {
				text += "\n" + fmt.Sprint(v)
			}
			if !strings.Contains(text, tc.ref) {
				continue
			}
			found++
			if !strings.Contains(s.If, tc.guard) {
				t.Errorf("step %q uses %s, and its if: lacks %s", s.Name+s.ID+s.Uses, tc.ref, tc.guard)
			}
		}
		if found == 0 {
			t.Errorf("no release step uses %s; this test has lost what it guards", tc.ref)
		}
	}
}

// Signing again over a published release replaces live packages with other bytes, so
// the first step has to tell a published release from a draft, and an outage from both.
func TestPackageStepState(t *testing.T) {
	const notFound = `echo '{"message":"Not Found"}'; echo 'gh: Not Found (HTTP 404)' >&2; exit 1`
	const outage = `echo 'gh: HTTP 502' >&2; exit 1`
	for _, tc := range []struct {
		name   string
		gh     string
		code   int
		output map[string]string
		calls  int
		log    string
	}{{
		name:   "published and latest: re-check, do not sign",
		gh:     `case "$*" in *releases/tags/v1.2.3*) echo true ;; *releases/latest*) echo v1.2.3 ;; esac`,
		output: map[string]string{"published": "true", "latest": "true"},
		calls:  2,
		log:    "not signing it again",
	}, {
		name:   "published, and a newer release is latest",
		gh:     `case "$*" in *releases/tags/v1.2.3*) echo true ;; *releases/latest*) echo v1.3.0 ;; esac`,
		output: map[string]string{"published": "true"},
		calls:  2,
	}, {
		name:   "published, and no release is latest",
		gh:     `case "$*" in *releases/tags/v1.2.3*) echo true ;; *) ` + notFound + ` ;; esac`,
		output: map[string]string{"published": "true"},
		calls:  2,
	}, {
		// /releases/tags/ answers 404 for a draft as well, and a draft is finished as before.
		name:   "a draft or no release: sign",
		gh:     notFound,
		output: none,
		calls:  1,
		log:    "has no published release",
	}, {
		name:   "published by something else, with no manifest: sign",
		gh:     `echo false`,
		output: none,
		calls:  1,
		log:    "holds no manifest.txt",
	}, {
		name:   "one 502, then an answer",
		gh:     `if [ "$n" = 1 ]; then ` + outage + `; fi; case "$*" in *releases/tags/*) echo true ;; *) echo v1.2.3 ;; esac`,
		output: map[string]string{"published": "true", "latest": "true"},
		calls:  3,
	}, {
		name:   "an outage is never a 'not published'",
		gh:     outage,
		code:   8,
		output: none,
		calls:  4,
		log:    "failed 4 times, so whether v1.2.3 is already published is unknown",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorkspace(t)
			w.stub("gh", tc.gh)
			w.run("state").expect(t, tc.code, tc.output, tc.log)
			if got := w.calls("gh"); len(got) != tc.calls {
				t.Errorf("gh called %d times, want %d: %q", len(got), tc.calls, got)
			}
			if got := w.calls("gh")[0]; !strings.Contains(got, "api repos/example/repo/releases/tags/v1.2.3") {
				t.Errorf("first gh call %q does not ask about the tag", got)
			}
		})
	}
}

// Exit 8 is the one owfeed code that means "ask again"; any other is an answer.
func TestPackageStepSignRetriesOnlyAnOutage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		owfeed string
		code   int
		calls  int
		sleeps []string
	}{
		{"signed", `exit 0`, 0, 1, nil},
		{"two outages, then signed", `[ "$n" -lt 3 ] && exit 8; exit 0`, 0, 3, []string{"30", "60"}},
		{"a failure that is an answer", `exit 7`, 7, 1, nil},
		{"an outage that outlasts the retries", `exit 8`, 8, 3, []string{"30", "60"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorkspace(t)
			w.stub("owfeed", tc.owfeed)
			w.run("Sign the packages", "OWFEED_SIGN_KEY=k").expect(t, tc.code, none)
			if got := w.calls("owfeed"); len(got) != tc.calls {
				t.Errorf("owfeed called %d times, want %d: %q", len(got), tc.calls, got)
			}
			if got := w.calls("sleep"); strings.Join(got, " ") != strings.Join(tc.sleeps, " ") {
				t.Errorf("slept %q, want %q", got, tc.sleeps)
			}
		})
	}
}

// One 5xx on the publishing call used to leave a finished draft unpublished.
func TestPackageStepPublishRetries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		gh     string
		code   int
		output map[string]string
		calls  int
	}{
		{"published", `exit 0`, 0, map[string]string{"released": "true"}, 1},
		{"two 5xx, then published", `[ "$n" -lt 3 ] && exit 1; exit 0`, 0, map[string]string{"released": "true"}, 3},
		{"still a draft after four tries", `exit 1`, 1, none, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorkspace(t)
			w.stub("gh", tc.gh)
			w.run("publish").expect(t, tc.code, tc.output)
			if got := w.calls("gh"); len(got) != tc.calls {
				t.Errorf("gh called %d times, want %d: %q", len(got), tc.calls, got)
			}
		})
	}
}

func TestPackageStepFetchPublished(t *testing.T) {
	for _, tc := range []struct {
		name  string
		gh    string
		code  int
		calls int
	}{
		{"fetched", `mkdir -p dist && touch dist/manifest.txt`, 0, 1},
		{"github.com down", `exit 1`, 1, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorkspace(t)
			w.stub("gh", tc.gh)
			w.run("Fetch the published release").expect(t, tc.code, none)
			got := w.calls("gh")
			if len(got) != tc.calls {
				t.Errorf("gh called %d times, want %d: %q", len(got), tc.calls, got)
			}
			if !strings.HasPrefix(got[0], "release download v1.2.3 --repo example/repo --dir dist") {
				t.Errorf("gh %s", got[0])
			}
		})
	}
}

// releaseTree lays dist/ out the way the job has it before the manifest check: built by
// owfeed release itself, then flattened by the workflow's own step.
func releaseTree(t *testing.T, w *workspace, tag string) {
	t.Helper()
	dist := filepath.Join(w.dir, "dist")
	writeFile(t, filepath.Join(dist, "noarch", "luci-app-example-1.0-r1.apk"), "apk bytes", 0o644)
	writeFile(t, filepath.Join(dist, "all", "luci-app-example_1.0-r1_all.ipk"), "ipk bytes", 0o644)
	writeFile(t, filepath.Join(dist, release.NotesName), "notes", 0o644)
	key, err := usign.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := release.Build(release.Options{Dir: dist, Repo: "example/repo", Tag: tag, Key: key}); err != nil {
		t.Fatal(err)
	}
	w.run("Flatten").expect(t, 0, none)
}

// A second signing pass leaves packages the manifest does not vouch for, and this is
// the step that has to see it. On a healthy release it must stay quiet.
func TestPackageStepManifestCheck(t *testing.T) {
	const apk = "dist/luci-app-example-1.0-r1.apk"
	for _, tc := range []struct {
		name      string
		tag       string
		published string
		damage    func(t *testing.T, dist string)
		code      int
		logs      []string
	}{{
		name: "healthy",
		code: 0,
		logs: []string{"lists 3 file(s)"},
	}, {
		name:      "healthy, re-checked after publishing",
		published: "true",
		code:      0,
		logs:      []string{"lists 3 file(s)"},
	}, {
		name:      "a package signed again after the manifest",
		published: "true",
		damage: func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, apk), "APK BYTES", 0o644)
		},
		code: 1,
		logs: []string{"luci-app-example-1.0-r1.apk hashes to", "gh release delete v1.2.3 --repo example/repo --yes"},
	}, {
		name: "a package changed before publishing",
		damage: func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, apk), "apk bytes, longer", 0o644)
		},
		code: 1,
		logs: []string{"luci-app-example-1.0-r1.apk is 17 bytes, and dist/manifest.txt says 9", "nothing is published"},
	}, {
		name: "a listed file missing",
		damage: func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "dist", "luci-app-example_1.0-r1_all.ipk")); err != nil {
				t.Fatal(err)
			}
		},
		code: 1,
		logs: []string{"lists luci-app-example_1.0-r1_all.ipk, and the release has no such file"},
	}, {
		name: "the manifest of another tag",
		tag:  "v1.2.2",
		code: 1,
		logs: []string{"has no 'tag v1.2.3' line"},
	}, {
		// The tag is compared as text: a dot in it is a dot, not any character.
		name: "the manifest of a tag that only matches as a pattern",
		tag:  "v1x2x3",
		code: 1,
		logs: []string{"has no 'tag v1.2.3' line"},
	}, {
		name: "no manifest",
		damage: func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "dist", "manifest.txt")); err != nil {
				t.Fatal(err)
			}
		},
		code: 1,
		logs: []string{"there is no dist/manifest.txt"},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorkspace(t)
			tag := tc.tag
			if tag == "" {
				tag = "v1.2.3"
			}
			releaseTree(t, w, tag)
			if tc.damage != nil {
				tc.damage(t, w.dir)
			}
			w.run("The manifest describes these files", "PUBLISHED="+tc.published).expect(t, tc.code, none, tc.logs...)
		})
	}
}

// After publishing, the CDN can answer 404 or the previous manifest for a while. That
// is waited out; a manifest that stays wrong is not.
func TestPackageStepManifestResolves(t *testing.T) {
	const (
		latest = "https://github.com/example/repo/releases/latest/download/manifest.txt"
		ofTag  = "https://github.com/example/repo/releases/download/v1.2.3/manifest.txt"
	)
	// Serves curl.<call>, or curl.all, from beside itself, and answers 404 with neither.
	const curl = `out=; url=
while [ $# -gt 0 ]; do case "$1" in -o) out=$2; shift 2 ;; *) url=$1; shift ;; esac; done
f="$(dirname "$0")/curl.$n"; [ -f "$f" ] || f="$(dirname "$0")/curl.all"
[ -f "$f" ] || { echo "curl: (22) The requested URL returned error: 404" >&2; exit 22; }
cp "$f" "$out"`
	const ours, previous = "owfeed-manifest 1\ntag v1.2.3\n", "owfeed-manifest 1\ntag v1.2.2\n"
	for _, tc := range []struct {
		name      string
		serve     map[string]string // curl.<call> or curl.all -> body
		published string
		latest    string
		code      int
		calls     int
		url       string
		log       string
	}{
		{name: "resolves", serve: map[string]string{"all": ours}, calls: 1, url: latest},
		{name: "404 twice, then resolves", serve: map[string]string{"3": ours}, calls: 3, url: latest},
		{name: "the previous manifest twice, then ours", serve: map[string]string{"1": previous, "2": previous, "3": ours}, calls: 3, url: latest},
		{name: "never resolves", code: 1, calls: 12, url: latest, log: "failed 12 times"},
		{name: "another release stays latest", serve: map[string]string{"all": previous}, code: 1, calls: 12, url: latest, log: "--latest --repo example/repo"},
		{name: "re-run while latest", serve: map[string]string{"all": ours}, published: "true", latest: "true", calls: 1, url: latest},
		{name: "re-run after a newer release", serve: map[string]string{"all": ours}, published: "true", calls: 1, url: ofTag},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorkspace(t)
			w.stub("curl", curl)
			for k, v := range tc.serve {
				writeFile(t, filepath.Join(w.bin, "curl."+k), v, 0o644)
			}
			writeFile(t, filepath.Join(w.dir, "dist", "manifest.txt"), ours, 0o644)
			w.run("The manifest resolves through latest/download", "PUBLISHED="+tc.published, "LATEST="+tc.latest).
				expect(t, tc.code, none, tc.log)
			got := w.calls("curl")
			if len(got) != tc.calls {
				t.Errorf("curl called %d times, want %d", len(got), tc.calls)
			}
			for _, c := range got {
				if !strings.HasSuffix(c, " "+tc.url) {
					t.Fatalf("curl %s, want %s", c, tc.url)
				}
			}
		})
	}
}

// Files from extra-artifact go up unsigned, so they may not take a name that a router or
// a feed reads as signed content, nor replace a file the release already carries.
func TestPackageStepExtraArtifact(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string // under the downloaded artifact
		code  int
		log   string
	}{
		{name: "a demo site", files: map[string]string{"playground.tar.gz": "tar"}, log: "unsigned, not in the manifest: playground.tar.gz"},
		{name: "two files", files: map[string]string{"a.tar.gz": "a", "b.txt": "b"}},
		{name: "a package", files: map[string]string{"luci-app-other-1.0-r1.apk": "apk"}, code: 1, log: "belongs in the owfeed-packages artifact"},
		{name: "a manifest", files: map[string]string{"manifest.txt": "forged"}, code: 1, log: "readers take as signed"},
		{name: "the installer's name", files: map[string]string{"install.sh": "other"}, code: 1, log: "would replace it"},
		{name: "a directory", files: map[string]string{"site/index.html": "<p>"}, code: 1, log: "site in artifact playground is not a regular file"},
		{name: "nothing", code: 1, log: "holds no files to attach"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorkspace(t)
			src := filepath.Join(w.tmp, "extra-artifact")
			if err := os.MkdirAll(src, 0o755); err != nil {
				t.Fatal(err)
			}
			for name, body := range tc.files {
				writeFile(t, filepath.Join(src, name), body, 0o644)
			}
			writeFile(t, filepath.Join(w.dir, "dist", "install.sh"), "signed installer", 0o644)
			w.run("Add the unsigned files", "SRC="+src, "ARTIFACT=playground", "PACKAGES=owfeed-packages").
				expect(t, tc.code, none, tc.log)

			if got, err := os.ReadFile(filepath.Join(w.dir, "dist", "install.sh")); err != nil || string(got) != "signed installer" {
				t.Errorf("dist/install.sh is now %q (%v)", got, err)
			}
			if tc.code != 0 {
				return
			}
			for name, body := range tc.files {
				if got, err := os.ReadFile(filepath.Join(w.dir, "dist", name)); err != nil || string(got) != body {
					t.Errorf("dist/%s = %q (%v), want %q", name, got, err, body)
				}
			}
		})
	}
}

// The unsigned files are added after the manifest is written, so it does not list them,
// and uploaded with the draft: an immutable release refuses any upload once published.
func TestPackageExtraArtifactRidesInTheDraft(t *testing.T) {
	order := []string{"Write and sign the manifest", "Add the unsigned files", "upload", "publish"}
	at := map[string]int{}
	for i, s := range packageSteps(t) {
		key := s.Name + s.ID
		if strings.Contains(s.Uses, "action-gh-release") {
			key = "upload"
		}
		at[key] = i
	}
	for i, step := range order {
		if _, ok := at[step]; !ok {
			t.Fatalf("package.yml has no release step %q", step)
		}
		if i > 0 && at[order[i-1]] >= at[step] {
			t.Errorf("step %q runs before %q; want the order %q", step, order[i-1], order)
		}
	}
}
