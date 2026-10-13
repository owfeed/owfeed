package main

import (
	"context"
	"flag"
	"io/fs"
	"os"
	"path/filepath"

	"owfeed.org/owfeed/internal/config"
	"owfeed.org/owfeed/internal/doctor"
	"owfeed.org/owfeed/internal/index"
)

// publish gates a built tree on the checks that matter and then hands it to the
// target.
//
// For github-pages the handing over is done by actions/upload-pages-artifact, not
// by owfeed: the artifact deployment model has no upload for us to perform, and
// pretending otherwise would mean reimplementing it badly. What owfeed contributes
// there is the gate — and the gate is the part that is missing from every existing
// feed's workflow, which uploads whatever the build step happened to leave behind.
func (a *app) publish(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("owfeed publish", flag.ContinueOnError)
	fs.SetOutput(a.err)
	target := fs.String("target", "", "publish target (default: the one in owfeed.yml)")
	dryRun := fs.Bool("dry-run", false, "check the tree and report what would happen")
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

	name := *target
	if name == "" {
		name = c.Publish[0].Target
	}
	if name != config.TargetGitHubPages {
		return fail(exitPublish, "publish target %q is not implemented yet", name)
	}

	// The same input the `doctor` command builds. Assembling it separately here is
	// exactly how an ipk index once reached publication unverified.
	in, err := a.doctorInput(ctx, c, l, out, requireOriginFor(c), "")
	if err != nil {
		return err
	}

	// There is no flag to publish an unsigned or unchecked tree. Every existing
	// feed's deploy step is an unconditional upload, and the failures that reach
	// subscribers are the ones nothing looked for.
	report, err := doctor.Run(ctx, in)
	if err != nil {
		return wrap(exitCheck, err)
	}
	if report.Failed(doctor.Error) {
		for _, f := range report.Findings {
			a.logf("%s", f)
		}
		return fail(exitCheck, "refusing to publish: %d finding(s) at error level", len(report.Findings))
	}
	for _, f := range report.Findings {
		a.logf("%s", f)
	}

	if err := requireTreeFiles(out, c.Feed.Name); err != nil {
		return err
	}

	if *dryRun {
		a.logf("%s passed %d checks and is ready to publish", out, report.Checked)
		return nil
	}

	a.logf("%s passed %d checks, signed by key %s", out, report.Checked, in.Identity)
	a.logf("")
	a.logf("GitHub Pages deploys an artifact rather than a branch, so the upload belongs to")
	a.logf("the workflow. Point it at this directory:")
	a.logf("")
	a.logf("  - uses: actions/upload-pages-artifact@v3")
	a.logf("    with:")
	a.logf("      path: %s", out)
	a.logf("  - uses: actions/deploy-pages@v4")
	a.logf("")
	a.logf("Deploying an artifact also keeps the feed's binaries out of git history, which is")
	a.logf("how an existing feed repository reached 2.6 GB against a 1 GB Pages limit.")
	return nil
}

// requireTreeFiles checks the things a publishable tree must carry that are not
// part of any index.
func requireTreeFiles(out, feedName string) error {
	pub := filepath.Join(out, feedName+".pem")
	if _, err := os.Stat(pub); err != nil {
		return fail(exitPublish, "%s is missing: subscribers fetch the public key from the feed root, "+
			"and the documented install snippet points at exactly this URL", pub)
	}
	// Jekyll drops paths beginning with an underscore or a dot, which on a tree of
	// binaries removes files without saying so.
	if _, err := os.Stat(filepath.Join(out, ".nojekyll")); err != nil {
		return fail(exitPublish, "%s has no .nojekyll: GitHub Pages runs Jekyll unless told not to, "+
			"and Jekyll silently drops paths starting with _ or .", out)
	}
	if _, err := os.Stat(filepath.Join(out, index.IndexFile)); err == nil {
		return fail(exitPublish, "%s holds a %s at its root; indexes belong in the per-architecture "+
			"directories the install snippet points at", out, index.IndexFile)
	}
	// publish cannot leave a file out of the upload, which takes the whole tree, so a
	// temporary file from a killed `owfeed sign` is a reason to stop.
	return filepath.WalkDir(out, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && index.IsSqueezeTemp(d.Name()) {
			return fail(exitPublish, "%s is a temporary file a killed `owfeed sign` left behind, "+
				"and the upload would publish it\n  delete it, then run `owfeed publish` again", path)
		}
		return nil
	})
}

// requireOriginFor decides whether every package must say where it came from.
//
// It is always on at publish time. A package that does not name its upstream is one
// a user cannot trace back to whoever wrote it, and a feed is the one place that
// information can still be attached.
func requireOriginFor(*config.Config) bool { return true }
