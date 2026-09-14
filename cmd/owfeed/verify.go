package main

import (
	"context"
	"flag"
	"fmt"

	"owfeed.org/owfeed/internal/config"
	"owfeed.org/owfeed/internal/smoke"
	"owfeed.org/owfeed/internal/verify"
)

// verify looks at the feed from outside, over the URL the install snippet gives.
// Given a local tree it also compares what is about to be published against what
// is already live, which is the only way to catch a version republished with
// different bytes.
func (a *app) verify(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("owfeed verify", flag.ContinueOnError)
	fs.SetOutput(a.err)
	url := fs.String("url", "", "feed URL (default: the one in owfeed.yml)")
	arch := fs.String("arch", smoke.DefaultArch, "architecture whose index is fetched")
	release := fs.String("release", "", "release line (default: the one owfeed.yml advertises)")
	if err := fs.Parse(args); err != nil {
		return wrap(exitConfig, err)
	}

	// A local tree is optional: without one this is a pure black-box check of what
	// is already published.
	local := ""
	if fs.NArg() > 0 {
		local = fs.Arg(0)
	}

	c, err := a.loadConfig()
	if err != nil {
		return err
	}
	base := *url
	if base == "" {
		base = c.Feed.URL
	}
	line := *release
	if line == "" {
		line = c.DefaultRelease().Line
	}

	report, err := verify.Run(ctx, verify.Options{
		BaseURL:    base,
		FeedName:   c.Feed.Name,
		Release:    line,
		LayoutPath: c.Layout.Path,
		Arch:       *arch,
		Format:     formatOf(c, line),
		LocalDir:   local,
	})
	if err != nil {
		// A check that cannot run counts as failed. This one reaches the network, so
		// an unreachable feed is exit 8 and retryable, unlike a finding -- and an
		// index that answers but does not parse is a finding, exit 7.
		return wrapUpstream(err)
	}

	for _, f := range report.Findings {
		fmt.Fprintln(a.out, f)
	}
	if len(report.Findings) > 0 {
		return fail(exitCheck, "%d finding(s) against %s, out of %d checks", len(report.Findings), base, report.Checked)
	}

	a.logf("%d checks passed against %s", report.Checked, base)
	if report.Compared > 0 {
		a.logf("%d package(s) already published were compared against what is about to replace them", report.Compared)
	}
	for _, n := range report.Notes {
		a.logf("note: %s", n)
	}
	return nil
}

// formatOf is the package format a release line uses. Like smoke, verify accepts a
// line and must not read the default's format instead: the two lines publish
// different files under different names, so even fetching the index needs this
// right.
func formatOf(c *config.Config, line string) string {
	for _, r := range c.Releases {
		if r.Line == line {
			return r.Format
		}
	}
	return config.FormatAPK
}
