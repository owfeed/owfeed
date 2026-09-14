package main

import (
	"context"
	"flag"

	"owfeed.org/owfeed/internal/config"
	"owfeed.org/owfeed/internal/lock"
	"owfeed.org/owfeed/internal/smoke"
)

// smoke is the only check that asks apk rather than telling it. Everything else
// compares the feed against a description of how apk behaves; this installs it.
func (a *app) smoke(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("owfeed smoke", flag.ContinueOnError)
	fs.SetOutput(a.err)
	image := fs.String("image", "", "router image (default: derived from the release line)")
	arch := fs.String("arch", smoke.DefaultArch, "architecture to install")
	release := fs.String("release", "", "release line (default: the one owfeed.yml advertises)")
	if err := fs.Parse(args); err != nil {
		return wrap(exitConfig, err)
	}

	out := defaultOut
	if fs.NArg() > 0 {
		out = fs.Arg(0)
	}

	c, err := a.loadConfig()
	if err != nil {
		return err
	}
	l, err := a.requireLock(ctx, c)
	if err != nil {
		return err
	}
	line := *release
	if line == "" {
		line = c.DefaultRelease().Line
	}

	// The format belongs to the line being smoked, not to the default one. Reading
	// it from the default sends the apk script to a 24.10 router, where the first
	// command is `apk` and there is no apk.
	format := ""
	for _, r := range c.Releases {
		if r.Line == line {
			format = r.Format
		}
	}
	if format == "" {
		return fail(exitConfig, "owfeed.yml configures no release line %q", line)
	}

	opts := smoke.Options{Format: format}
	if format == config.FormatIPK {
		key, err := a.usignKey(c)
		if err != nil {
			return err
		}
		opts.UsignKeyID = key.ID.String()
		// The 24.10 images are the ones that carry opkg; the apk default would be a
		// router that cannot read this feed at all.
		if *image == "" {
			opts.Image = ""
		}
	}

	res, err := smoke.Run(ctx, smoke.Options{
		Format:       opts.Format,
		UsignKeyID:   opts.UsignKeyID,
		Dir:          out,
		FeedName:     c.Feed.Name,
		Release:      line,
		PointRelease: pointFor(l, line),
		LayoutPath:   c.Layout.Path,
		Arch:         *arch,
		Image:        *image,
	})
	if err != nil {
		// 7 for everything the router said, 8 only when Docker Hub would not hand
		// over the image: nothing about the feed was tried then.
		return wrapUpstream(err)
	}

	a.logf("installed %d package(s) from %s on %s", len(res.Installed), res.Arch, res.Image)
	for _, p := range res.Installed {
		a.logf("  %s", p)
	}
	switch {
	case format == config.FormatIPK:
		// opkg has no per-package signature, so there is nothing here to claim. Its
		// trust rests entirely on the signed index, which is why check_signature
		// being on is asserted inside the container rather than assumed.
		a.logf("opkg verified the index signature before reading it; individual packages carry none, by design")
	case res.LocalInstall:
		a.logf("`apk add ./file.apk` works without --allow-untrusted, so LuCI's Upload Package flow can install these")
	default:
		a.logf("note: `apk add ./file.apk` needed a flag, so LuCI's Upload Package flow cannot install these")
	}
	if res.WorldPin != "" {
		// Worth printing every time: it is the reason the install snippet must never
		// tell anyone to install the file directly.
		a.logf("a local install pinned it in /etc/apk/world as %q — such a package never upgrades from the feed again", res.WorldPin)
	}
	// No silent caps: one architecture was installed, and the report says so rather
	// than implying the whole matrix was exercised.
	a.logf("this covered %s only; the other architectures are covered by the index checks", res.Arch)
	return nil
}

// pointFor is the concrete release recorded for a line, so a smoke run installs on
// an image from the line it is testing rather than from whichever one the toolchain
// happens to be pinned to.
func pointFor(l *lock.Lock, line string) string {
	if r, ok := l.Release(line); ok {
		return r.Point
	}
	return l.Toolchain.SDKRelease
}
