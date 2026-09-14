package main

import (
	"context"
	"crypto/ecdsa"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"owfeed.org/owfeed/internal/apk"
	"owfeed.org/owfeed/internal/arch"
	"owfeed.org/owfeed/internal/config"
	"owfeed.org/owfeed/internal/index"
	"owfeed.org/owfeed/internal/keys"
)

func (a *app) sign(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("owfeed sign", flag.ContinueOnError)
	fs.SetOutput(a.err)
	keySpec := fs.String("key", "", "signing key, as env:VAR or file:PATH (default: signing.key, or env:OWFEED_SIGN_KEY)")
	sdkRelease := fs.String("sdk-release", "", "point release to take the apk tool from (default: the lockfile's, or the newest of "+config.DefaultReleaseLine+")")
	if err := fs.Parse(args); err != nil {
		return wrap(exitConfig, err)
	}

	dir := defaultDist
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}

	// A feed signs with its config; an author signing their own packages before a
	// release has no feed and no reason to invent one. Requiring owfeed.yml and a
	// 36-architecture lockfile to put a signature on one file in dist/noarch/ is
	// what kept package repositories hand-rolling this step.
	//
	// The architectures are already on disk as directory names, and the only other
	// thing a signature needs is a key.
	c, cfgErr := a.loadConfig()
	if cfgErr != nil && *keySpec == "" {
		// The config error is right for someone building a feed and wrong for the
		// author who just wants a signature on their own packages, so say both.
		return fail(exitConfig, "%v\n  signing your own packages needs no feed: "+
			"`owfeed sign --key env:OWFEED_SIGN_KEY %s`", cfgErr, dir)
	}

	// A feed can decline to put its signature inside packages it did not build.
	// The index is signed either way, and that is what a router's trust comes from.
	//
	// Only honoured when the key comes from the config: `--key` means somebody is
	// signing deliberately, and an author signing their own packages before a
	// release is exactly the case this must not silently skip.
	if c != nil && *keySpec == "" && !*c.Signing.SignPackages {
		a.logf("signing.sign-packages is false: leaving packages as their authors built them")
		a.logf("the index is still signed by this feed — run `owfeed index` next")
		return nil
	}

	tool, err := a.signingTool(ctx, c, *sdkRelease)
	if err != nil {
		return err
	}

	key, err := a.signingKeyFrom(c, *keySpec)
	if err != nil {
		return err
	}
	keyDir, cleanup, err := a.stageKey(key)
	if err != nil {
		return err
	}
	defer cleanup()

	signer, err := index.NewSigner(keyDir, keyFileName, key)
	if err != nil {
		return wrap(exitKey, err)
	}

	// The build directory holds one subdirectory per architecture.
	tree, err := index.Tree(dir)
	if err != nil {
		return wrap(exitKey, err)
	}
	if len(tree) == 0 {
		return fail(exitKey, "%s holds no packages; run `owfeed build` first", dir)
	}

	arches := make([]string, 0, len(tree))
	for arch := range tree {
		arches = append(arches, arch)
	}
	sort.Strings(arches)

	var signed []string
	for _, arch := range arches {
		got, err := index.Sign(ctx, tool, filepath.Join(dir, arch), signer)
		if err != nil {
			return wrap(exitKey, err)
		}
		for _, p := range got {
			a.logf("signed %s/%s", arch, p)
		}
		signed = append(signed, got...)
	}
	// Signing each package is what makes `apk add ./file.apk` work without a flag,
	// and what makes LuCI's Upload Package flow usable at all: package-manager-call
	// drops arguments it does not recognise, so it cannot pass --allow-untrusted.
	a.logf("%d package(s) signed by key %s", len(signed), signer.Identity)
	return nil
}

// keyFileName is what the private key is called inside its staging directory. The
// name is arbitrary: apk matches keys by identity, never by filename.
const keyFileName = "signing.pem"

func (a *app) signingKey(c *config.Config) (*ecdsa.PrivateKey, error) {
	key, err := keys.Source(c.Signing.Key).Load(a.root())
	if err != nil {
		return nil, wrap(exitKey, err)
	}
	return key, nil
}

// signingKeyFrom takes the key from the flag when there is one, and from the config
// otherwise. The flag wins so that a repository with a config can still sign with a
// different key without editing it.
func (a *app) signingKeyFrom(c *config.Config, spec string) (*ecdsa.PrivateKey, error) {
	if spec == "" {
		if c == nil {
			return nil, fail(exitKey, "no --key, and no %s to take one from", a.configPath)
		}
		return a.signingKey(c)
	}
	key, err := keys.Source(spec).Load(a.root())
	if err != nil {
		return nil, wrap(exitKey, err)
	}
	return key, nil
}

// signingTool resolves the apk binary that does the signing.
//
// It comes out of an OpenWrt SDK, so something has to name a release. In a feed
// that is the lockfile, which is the pinned answer and the one to prefer. Without
// one -- an author signing their own build -- the newest point release of the
// default line is close enough: this only decides which `apk adbsign` runs, not
// what the signature says, and the signature is verified afterwards either way.
func (a *app) signingTool(ctx context.Context, c *config.Config, override string) (*apk.Tool, error) {
	if explicit := os.Getenv("OWFEED_APK"); explicit != "" {
		t, err := apk.Resolve(ctx, apk.Options{Explicit: explicit})
		return t, wrap(exitBuild, err)
	}

	release := override
	if release == "" && c != nil {
		if l, err := a.requireLock(ctx, c); err == nil {
			release = l.Toolchain.SDKRelease
		}
	}
	if release == "" {
		point, err := arch.LatestPoint(ctx, upstreamHTTP, config.DefaultReleaseLine)
		if err != nil {
			return nil, wrapUpstream(fmt.Errorf("%w\n  name one with --sdk-release", err))
		}
		a.debugf("no lockfile: taking the apk tool from %s", point)
		release = point
	}

	sdkDir, err := apk.Acquire(ctx, upstreamHTTP, a.cacheRoot, release)
	if err != nil {
		return nil, wrapUpstream(err)
	}
	t, err := apk.Resolve(ctx, apk.Options{SDKDir: sdkDir, AllowContainer: true})
	if err != nil {
		return nil, wrap(exitBuild, err)
	}
	a.debugf("apk: %s (%s)", t.Version, t.Origin)
	return t, nil
}

// stageKey materialises the private key in a directory of its own, outside anything
// that will be published or uploaded, and returns a function that removes it.
func (a *app) stageKey(key *ecdsa.PrivateKey) (string, func(), error) {
	parent := os.Getenv("RUNNER_TEMP")
	if parent == "" {
		parent = os.TempDir()
	}
	dir, err := keys.StageDir(parent, keyFileName, key)
	if err != nil {
		return "", func() {}, wrap(exitKey, err)
	}
	return dir, func() { os.RemoveAll(dir) }, nil
}
