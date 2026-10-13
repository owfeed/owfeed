package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite THIRD_PARTY_LICENSES from the module cache")

// licensesPath is the file the release ships beside the binaries.
const licensesPath = "../../THIRD_PARTY_LICENSES"

// TestThirdPartyLicenses is the drift check for THIRD_PARTY_LICENSES. The release
// binaries link third-party code whose licences require their text to travel with
// it, so a module linked into any release target and missing from the file, or
// present at another version, fails here.
//
// Regenerate with: go test ./cmd/owfeed -run TestThirdPartyLicenses -update
func TestThirdPartyLicenses(t *testing.T) {
	got, err := thirdPartyLicenses()
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile(licensesPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", licensesPath)
		return
	}
	want, err := os.ReadFile(licensesPath)
	if err != nil {
		t.Fatalf("%v\n\nrun: go test ./cmd/owfeed -run TestThirdPartyLicenses -update", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s does not match the modules linked into cmd/owfeed\n\nrun: go test ./cmd/owfeed -run TestThirdPartyLicenses -update", licensesPath)
	}
}

type linkedModule struct {
	path, version, dir string
	pkgDirs            map[string]bool
}

// releaseTargets reads the GOOS/GOARCH list from the release workflow, so the check
// covers exactly what is shipped rather than a copy of it that can fall behind.
func releaseTargets() ([]string, error) {
	wf, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		return nil, err
	}
	m := regexp.MustCompile(`for target in ([^;]+);`).FindSubmatch(wf)
	if m == nil {
		return nil, fmt.Errorf("release.yml has no `for target in ...;` line")
	}
	return strings.Fields(string(m[1])), nil
}

func linkedModules() ([]*linkedModule, error) {
	targets, err := releaseTargets()
	if err != nil {
		return nil, err
	}
	mods := map[string]*linkedModule{}
	for _, target := range targets {
		goos, goarch, _ := strings.Cut(target, "/")
		cmd := exec.Command("go", "list", "-deps", "-f",
			`{{if and .Module (not .Module.Main)}}{{.Module.Path}}{{"\t"}}{{.Module.Version}}{{"\t"}}{{.Module.Dir}}{{"\t"}}{{.Dir}}{{end}}`, ".")
		cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("go list -deps for %s: %w", target, err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			f := strings.Split(line, "\t")
			if len(f) != 4 {
				continue
			}
			m := mods[f[0]]
			if m == nil {
				m = &linkedModule{path: f[0], version: f[1], dir: f[2], pkgDirs: map[string]bool{}}
				mods[f[0]] = m
			}
			m.pkgDirs[f[3]] = true
		}
	}
	out := make([]*linkedModule, 0, len(mods))
	for _, m := range mods {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}

var licenceFile = regexp.MustCompile(`(?i)^(licen[cs]e|copying|notice)(\.[a-z]+)?$`)

// licenceFiles finds the licence files that cover the linked packages: those in each
// package's own directory and in every directory above it up to the module root,
// which is where a vendored subpackage keeps its own.
func licenceFiles(m *linkedModule) ([]string, error) {
	seen := map[string]bool{}
	for pkg := range m.pkgDirs {
		for dir := pkg; ; dir = filepath.Dir(dir) {
			entries, err := os.ReadDir(dir)
			if err != nil {
				return nil, err
			}
			for _, e := range entries {
				if !e.IsDir() && licenceFile.MatchString(e.Name()) {
					rel, err := filepath.Rel(m.dir, filepath.Join(dir, e.Name()))
					if err != nil {
						return nil, err
					}
					seen[filepath.ToSlash(rel)] = true
				}
			}
			if dir == m.dir || len(dir) <= len(m.dir) {
				break
			}
		}
	}
	files := make([]string, 0, len(seen))
	for f := range seen {
		files = append(files, f)
	}
	sort.Strings(files)
	return files, nil
}

// headers returns the distinct licence comments that open the linked packages'
// source files, for a module that ships no licence file and states its licence
// only there.
func headers(m *linkedModule) ([]string, error) {
	dirs := make([]string, 0, len(m.pkgDirs))
	for d := range m.pkgDirs {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	var out []string
	seen := map[string]bool{}
	for _, d := range dirs {
		files, err := filepath.Glob(filepath.Join(d, "*.go"))
		if err != nil {
			return nil, err
		}
		sort.Strings(files)
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.PackageClauseOnly|parser.ParseComments)
			if err != nil {
				return nil, err
			}
			if len(f.Comments) == 0 || f.Comments[0] == f.Doc || f.Comments[0].End() > f.Package {
				continue
			}
			text := f.Comments[0].Text()
			if strings.Contains(strings.ToLower(text), "license") && !seen[text] {
				seen[text] = true
				out = append(out, text)
			}
		}
	}
	return out, nil
}

func thirdPartyLicenses() ([]byte, error) {
	mods, err := linkedModules()
	if err != nil {
		return nil, err
	}
	rule := strings.Repeat("=", 78)

	var b bytes.Buffer
	b.WriteString("Third-party software linked into the owfeed release binaries, and the licence\n" +
		"texts it is distributed under.\n\n" +
		"Generated from the Go module cache by\n" +
		"  go test ./cmd/owfeed -run TestThirdPartyLicenses -update\n" +
		"which fails while a module linked into any release target is missing here.\n")

	// Every binary links the standard library and runtime of the toolchain go.mod
	// pins, which is the one the release workflow installs from it.
	goVersion, goLicence, err := stdlibLicence()
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(&b, "\n%s\nGo standard library and runtime go%s\n%s\n\n--- LICENSE\n\n%s",
		rule, goVersion, rule, strings.TrimRight(string(goLicence), "\n")+"\n")
	for _, m := range mods {
		fmt.Fprintf(&b, "\n%s\n%s %s\n%s\n", rule, m.path, m.version, rule)
		files, err := licenceFiles(m)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			body, err := os.ReadFile(filepath.Join(m.dir, filepath.FromSlash(f)))
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(&b, "\n--- %s\n\n%s", f, strings.TrimRight(string(body), "\n")+"\n")
		}
		if len(files) > 0 {
			continue
		}

		// No licence file: the licence is stated only in each source file's header.
		hs, err := headers(m)
		if err != nil {
			return nil, err
		}
		if len(hs) == 0 {
			return nil, fmt.Errorf("%s %s: no licence file and no licence header in the linked packages", m.path, m.version)
		}
		b.WriteString("\nThis module ships no licence file. Its source files carry these headers:\n")
		for _, h := range hs {
			fmt.Fprintf(&b, "\n%s", strings.TrimRight(h, "\n")+"\n")
			if !strings.Contains(h, "Apache License, Version 2.0") {
				return nil, fmt.Errorf("%s %s: a header names a licence this generator cannot reproduce:\n%s", m.path, m.version, h)
			}
		}
		// Apache-2.0 4(a) asks for the licence itself, not a pointer to it. It is
		// the licence owfeed is released under, so the text comes from LICENSE.
		apache, err := os.ReadFile("../../LICENSE")
		if err != nil {
			return nil, err
		}
		if !strings.Contains(string(apache[:min(200, len(apache))]), "Apache License\n                           Version 2.0") {
			return nil, fmt.Errorf("../../LICENSE is not the Apache License 2.0 text %s needs", m.path)
		}
		fmt.Fprintf(&b, "\nThe Apache License, Version 2.0, those headers refer to:\n\n%s", strings.TrimRight(string(apache), "\n")+"\n")
	}
	return b.Bytes(), nil
}

// stdlibLicence reads LICENSE from the GOROOT of the toolchain go.mod pins, named
// through GOTOOLCHAIN so the text and version do not depend on the Go that runs the
// test. A distribution's Go may not ship the file at all.
func stdlibLicence() (string, []byte, error) {
	mod, err := os.ReadFile("../../go.mod")
	if err != nil {
		return "", nil, err
	}
	m := regexp.MustCompile(`(?m)^toolchain go(\S+)$`).FindSubmatch(mod)
	if m == nil {
		m = regexp.MustCompile(`(?m)^go (\S+)$`).FindSubmatch(mod)
	}
	if m == nil {
		return "", nil, fmt.Errorf("go.mod names no Go version")
	}
	version := string(m[1])
	cmd := exec.Command("go", "env", "GOROOT")
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=go"+version)
	root, err := cmd.Output()
	if err != nil {
		return "", nil, fmt.Errorf("GOTOOLCHAIN=go%s go env GOROOT: %w", version, err)
	}
	text, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(root)), "LICENSE"))
	return version, text, err
}
