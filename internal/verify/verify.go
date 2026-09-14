// Package verify checks a published feed from outside, over its documented URL.
//
// Everything else looks at a directory on disk. This looks at what subscribers
// actually reach, which is a different thing: a tree can be perfect and still be
// served behind a redirect apk will not follow, or through a cache that hands out
// yesterday's package with today's index.
//
// It also compares what is about to be published against what is already live,
// which is the one check that catches a version republished with different
// contents. apk identifies a package by the hash the index records, so two
// payloads under one version are two packages claiming to be the same one — and
// the maintainer, whose local tree is self-consistent, sees nothing wrong.
package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"owfeed.org/owfeed/internal/config"
	"owfeed.org/owfeed/internal/feedindex"
	"owfeed.org/owfeed/internal/netx"
)

// Options configure a run.
type Options struct {
	// BaseURL is the feed's documented address, exactly as the install snippet
	// gives it.
	BaseURL string
	// FeedName seeds the published key's filename.
	FeedName string
	// Release is the release line.
	Release string
	// LayoutPath is the layout template.
	LayoutPath string
	// Arch is the architecture whose index is fetched.
	Arch string
	// Format is "apk" or "ipk". The two publish different index files under
	// different names, so even fetching one needs to know which.
	Format string
	// LocalDir is the tree about to be published. When set, every package it holds
	// that is already live is compared against the published one.
	LocalDir string
}

// Finding is one problem with the published feed.
type Finding struct {
	ID   string
	What string
	Why  string
	Fix  string
}

func (f Finding) String() string {
	s := f.ID + ": " + f.What
	if f.Why != "" {
		s += "\n    " + f.Why
	}
	if f.Fix != "" {
		s += "\n    fix: " + f.Fix
	}
	return s
}

// Report is the outcome of a run.
type Report struct {
	Findings []Finding
	Checked  int
	// Compared counts packages that exist both live and locally.
	Compared int
	// Notes are things worth saying that are not findings.
	Notes []string
}

func (r *Report) add(f Finding) { r.Findings = append(r.Findings, f) }

// noRedirect turns every redirect into a response we can inspect, because a
// redirect is the finding rather than something to follow.
func client() *http.Client {
	return &http.Client{
		// Retries a 5xx or a dropped connection before a live-feed check gives up,
		// so a CDN hiccup on the feed's host is not reported as the feed broken.
		Transport:     &netx.Transport{},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Run checks the published feed.
func Run(ctx context.Context, opts Options) (*Report, error) {
	r := &Report{}
	hc := client()
	base := strings.TrimSuffix(opts.BaseURL, "/")
	repo := base + "/" + expandLayout(opts.LayoutPath, opts.Release, opts.Arch)

	// 510: apk does not follow 30x with the stock uclient-fetch (openwrt#17180), so
	// a redirect anywhere on these URLs is a feed that works in a browser and not on
	// a router.
	for _, u := range redirectTargets(base, repo, opts) {
		r.Checked++
		resp, err := hc.Get(u)
		if err != nil {
			return nil, fmt.Errorf("GET %s: %w", u, err)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()

		switch {
		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			r.add(Finding{
				ID:   "OWF510",
				What: fmt.Sprintf("%s answers %s, redirecting to %s", u, resp.Status, resp.Header.Get("Location")),
				Why:  "apk does not follow redirects with the stock uclient-fetch (openwrt#17180), so this URL works in a browser and fails on a router",
				Fix:  "publish at the address the install snippet gives, without an apex-to-www or http-to-https hop",
			})
		case resp.StatusCode != http.StatusOK:
			r.add(Finding{
				ID:   "OWF510",
				What: fmt.Sprintf("%s answers %s", u, resp.Status),
				Fix:  "the install snippet points here, so this has to be fetchable",
			})
		}
	}

	checkNoJekyll(r, hc, base)

	entries, err := liveIndex(ctx, hc, repo, opts.Format)
	if err != nil {
		return nil, err
	}

	var local map[string]string
	if opts.LocalDir != "" {
		if local, err = localHashes(opts.LocalDir, opts.LayoutPath, opts.Release, opts.Arch, opts.Format); err != nil {
			return nil, err
		}
	}

	for _, e := range entries {
		name := e.File
		if name == "" {
			// apk stores no filename: it builds one from the name and version, so
			// that is the name the file has to be published under.
			name = fmt.Sprintf("%s-%s.apk", e.Name, e.Version)
		}
		u := repo + "/" + name

		// 512: the index and the packages it names are cached independently by every
		// CDN, so a package that is missing or a different size than the index
		// records is cache skew — which reaches subscribers as an integrity failure.
		r.Checked++
		body, status, err := get(ctx, hc, u)
		switch {
		case err != nil:
			return nil, err
		case status != http.StatusOK:
			r.add(Finding{
				ID:   "OWF512",
				What: fmt.Sprintf("the index lists %s but %s answers %d", name, u, status),
				Why:  "the index and the packages it names are cached independently, so subscribers see this as a broken download",
				Fix:  "republish, and check the cache headers on the index",
			})
			continue
		case int64(len(body)) != e.FileSize:
			r.add(Finding{
				ID:   "OWF512",
				What: fmt.Sprintf("%s is %d bytes, the live index says %d", name, len(body), e.FileSize),
				Why:  "apk checks the package against the size and hash the index recorded, so this fails on every device",
				Fix:  "republish packages first and the index last, so the index is never ahead of what it describes",
			})
			continue
		}

		// 513: a version that is already published must keep its content.
		//
		// The comparison is the payload identity apk records as `hashes`, not the
		// file's bytes. Those differ on every run for a reason that is not a change:
		// ECDSA signatures are randomised, so signing the same package twice produces
		// two different files with identical contents. Comparing bytes would report
		// every republication as tampering and teach people to ignore the check.
		if local == nil {
			continue
		}
		lh, ok := local[e.Name+" "+e.Version]
		if !ok {
			continue
		}

		r.Checked++
		r.Compared++
		if lh != e.Hashes {
			r.add(Finding{
				ID:   "OWF513",
				What: fmt.Sprintf("%s is already published with different content", name),
				Why: "a package is identified by this hash — apk by the payload's, opkg by the file's — so two different contents under one version " +
					"are two packages claiming to be the same one; the local tree is self-consistent, which is why nothing else notices",
				Fix: "bump the revision — the -r<n> on the version — rather than replacing what is already out there",
			})
		}
	}

	// Re-signing changes an apk package's bytes without changing what it contains,
	// so every republication invalidates whatever a CDN has cached for packages that
	// did not change. This does not apply to opkg: it has no per-package signature,
	// so an unchanged package rebuilds byte for byte and stays cacheable.
	if r.Compared > 0 && opts.Format != "ipk" {
		r.Notes = append(r.Notes, fmt.Sprintf(
			"%d package(s) are being republished unchanged; their bytes differ because ECDSA signatures are randomised, "+
				"so any cached copy a CDN holds no longer matches the new index", r.Compared))
	}
	return r, nil
}

// localHashes reads the payload identity of each package from the index owfeed
// just wrote, so no apk toolchain is needed to make the comparison.
func localHashes(dir, layout, release, arch, format string) (map[string]string, error) {
	if format == "ipk" {
		idx, err := feedindex.ReadDir(filepath.Join(dir, expandLayout(layout, release, arch)))
		if err != nil {
			return nil, nil
		}
		out := map[string]string{}
		for _, e := range idx.Entries {
			out[e.Name+" "+e.Version] = e.SHA256
		}
		return out, nil
	}
	b, err := os.ReadFile(filepath.Join(dir, expandLayout(layout, release, arch), "index.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Packages []indexEntry `json:"packages"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, p := range doc.Packages {
		out[p.Name+" "+p.Version] = p.Hashes
	}
	return out, nil
}

// redirectTargets are the URLs the install snippet sends people to. A redirect on
// any of them is a feed that works in a browser and not on a router.
func redirectTargets(base, repo string, opts Options) []string {
	if opts.Format == "ipk" {
		// opkg fetches the compressed index and its signature; the key is published
		// under its own id.
		return []string{repo + "/Packages.gz", repo + "/Packages.sig"}
	}
	return []string{base + "/" + opts.FeedName + ".pem", repo + "/packages.adb"}
}

type indexEntry struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// Hashes is apk's payload identity, or opkg's SHA256 of the file. Both answer
	// "is this the same package", which is what the immutability check asks.
	Hashes string `json:"hashes"`
	// File is the name to fetch. apk stores none and derives it; opkg records it.
	File     string `json:"-"`
	FileSize int64  `json:"file-size"`
}

func liveIndex(ctx context.Context, hc *http.Client, repo, format string) ([]indexEntry, error) {
	if format == "ipk" {
		return liveIndexIPK(ctx, hc, repo)
	}
	body, status, err := get(ctx, hc, repo+"/index.json")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, &netx.StatusError{URL: repo + "/index.json", Code: status, Status: strconv.Itoa(status)}
	}
	var doc struct {
		Packages []indexEntry `json:"packages"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("%s/index.json: %w", repo, err)
	}
	return doc.Packages, nil
}

// liveIndexIPK reads the text index opkg uses. It carries the filename and the
// file's own SHA256, which makes the comparisons below stricter than on the apk
// side rather than merely equivalent.
func liveIndexIPK(ctx context.Context, hc *http.Client, repo string) ([]indexEntry, error) {
	body, status, err := get(ctx, hc, repo+"/Packages")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, &netx.StatusError{URL: repo + "/Packages", Code: status, Status: strconv.Itoa(status)}
	}

	var out []indexEntry
	for _, stanza := range strings.Split(string(body), "\n\n") {
		if strings.TrimSpace(stanza) == "" {
			continue
		}
		var e indexEntry
		for _, line := range strings.Split(stanza, "\n") {
			key, value, ok := strings.Cut(line, ": ")
			if !ok {
				continue
			}
			switch key {
			case "Package":
				e.Name = value
			case "Version":
				e.Version = value
			case "Filename":
				e.File = value
			case "SHA256sum":
				e.Hashes = value
			case "Size":
				// An unparsed size used to become zero silently, and OWF512 would
				// then report every package in the feed as the wrong size. A field
				// that cannot be read is a broken index, not a size of nothing.
				n, err := strconv.ParseInt(value, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("%s: Size %q is not a number", e.Name, value)
				}
				e.FileSize = n
			}
		}
		if e.Name != "" {
			out = append(out, e)
		}
	}
	return out, nil
}

func get(ctx context.Context, hc *http.Client, url string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	return body, resp.StatusCode, err
}

func expandLayout(path, release, arch string) string {
	return config.ExpandLayout(path, release, arch)
}

// 514: the deploy has to have carried `.nojekyll`.
//
// This is the one failure `owfeed publish` cannot catch. It refuses a tree without
// that file, and it is right to: Pages runs Jekyll unless told not to, and Jekyll
// drops every path beginning with a dot or an underscore. But what publish inspects
// is the tree on disk, and what subscribers fetch is whatever the upload step put in
// the artifact -- and actions/upload-pages-artifact stopped including dotfiles by
// default in v4. A feed can therefore be correct at every point owfeed can see and
// still be deployed without the file that keeps it correct.
//
// So it is checked from outside, against the live site, which is the only place the
// answer exists.
func checkNoJekyll(r *Report, hc *http.Client, base string) {
	r.Checked++
	u := base + "/.nojekyll"
	resp, err := hc.Get(u)
	if err != nil {
		// Not fatal on its own: a target that is not Pages has no reason to serve it,
		// and a network failure here should not masquerade as a finding.
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return
	}
	r.add(Finding{
		ID:   "OWF514",
		What: fmt.Sprintf("%s answers %s", u, resp.Status),
		Why: "GitHub Pages runs Jekyll without this file, and Jekyll drops every path beginning with a dot or an underscore; " +
			"owfeed put it in the tree, so something between the tree and the site removed it",
		Fix: "actions/upload-pages-artifact excludes dotfiles from v4 onwards -- set `include-hidden-files: true`",
	})
}
