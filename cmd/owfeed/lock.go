package main

import (
	"context"
	"flag"
	"path/filepath"

	"owfeed.org/owfeed/internal/arch"
	"owfeed.org/owfeed/internal/config"
	"owfeed.org/owfeed/internal/keyring"
	"owfeed.org/owfeed/internal/keys"
	"owfeed.org/owfeed/internal/lock"
)

func (a *app) lock(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("owfeed lock", flag.ContinueOnError)
	fs.SetOutput(a.err)
	update := fs.Bool("update", false, "write the derived facts to owfeed.lock")
	if err := fs.Parse(args); err != nil {
		return wrap(exitConfig, err)
	}

	c, err := a.loadConfig()
	if err != nil {
		return err
	}

	want, err := a.derive(ctx, c)
	if err != nil {
		return err
	}

	path := a.lockPath()
	have, loadErr := lock.Load(path)

	if !*update {
		if loadErr != nil {
			return fail(exitConflict, "%s: %v\n  run `owfeed lock --update` to create it", path, loadErr)
		}
		if err := lock.Check(path, have, want); err != nil {
			return err
		}
		a.logf("%s is up to date", filepath.Base(path))
		return nil
	}

	for _, line := range lock.Diff(have, want) {
		a.logf("%s", line)
	}
	if err := lock.Save(path, want); err != nil {
		return wrap(exitInternal, err)
	}
	a.logf("wrote %s", filepath.Base(path))
	return nil
}

// derive resolves everything the lockfile records: which point release each line
// pins to, and which architectures that release publishes.
func (a *app) derive(ctx context.Context, c *config.Config) (*lock.Lock, error) {
	l := &lock.Lock{Version: lock.Version}

	for _, r := range c.Releases {
		point, err := a.pointFor(ctx, c, r)
		if err != nil {
			return nil, err
		}

		var res *arch.Result
		if a.noNetwork {
			res, err = arch.Cached(a.cacheRoot, point)
		} else {
			res, err = arch.Derive(ctx, upstreamHTTP, a.cacheRoot, point)
		}
		if err != nil {
			// No cached answer under --no-network stays 8, as it was: it is not a
			// finding about upstream, and a run with the network may well succeed.
			if a.noNetwork {
				return nil, wrap(exitUpstream, err)
			}
			return nil, wrapUpstream(err)
		}

		l.Releases = append(l.Releases, lock.Release{
			Line: r.Line, Point: point, Source: res.Source, Arches: res.Arches,
		})
		if r.Default {
			l.Toolchain.SDKRelease = point
		}
	}
	if l.Toolchain.SDKRelease == "" && len(l.Releases) > 0 {
		l.Toolchain.SDKRelease = l.Releases[0].Point
	}

	if err := a.deriveKeyring(c, l); err != nil {
		return nil, err
	}
	return l, nil
}

// deriveKeyring records the keyring package's version for the signing key in use.
//
// The version must change when the key changes and must never go backwards, and a
// value computed from the key alone cannot do both: identities are hashes, so the next
// one sorts below the current about half the time, and a keyring package whose version
// went down is one no router will install — which is exactly the moment a rotation has
// to work.
//
// So it counts. The minor number goes up by one each time the key changes, and the
// previous value is read from the lockfile that is already on disk. A feed that
// rebuilds without rotating keeps the same version and publishes no upgrade for a
// package whose contents did not change.
func (a *app) deriveKeyring(c *config.Config, l *lock.Lock) error {
	if !*c.Signing.KeyringPackage {
		return nil
	}
	// No key available is not an error here: `owfeed lock --update` is run by people
	// who have no reason to hold the signing key, and refusing them the architecture
	// matrix over a package they are not building would be the wrong trade. The
	// keyring entry is then left as it was.
	key, err := a.signingKey(c)
	if err != nil {
		if prev, prevErr := lock.Load(a.lockPath()); prevErr == nil {
			l.Keyring = prev.Keyring
		}
		return nil
	}
	id, err := keys.IdentityOf(&key.PublicKey)
	if err != nil {
		return wrap(exitKey, err)
	}

	minor := 0
	if prev, err := lock.Load(a.lockPath()); err == nil && prev.Keyring != nil {
		if prev.Keyring.Identity == id.String() {
			l.Keyring = prev.Keyring
			return nil
		}
		minor = keyring.MinorOf(prev.Keyring.Version)
	}
	l.Keyring = &lock.Keyring{
		Identity: id.String(),
		Version:  keyring.VersionFor(minor + 1),
	}
	return nil
}

// pointFor resolves a release line to the concrete point release to build against.
func (a *app) pointFor(ctx context.Context, c *config.Config, r config.Release) (string, error) {
	if c.Build.SDK.Release != config.LatestPoint {
		return c.Build.SDK.Release, nil
	}
	if a.noNetwork {
		return "", fail(exitUpstream, "build.sdk.release is %q and --no-network was given; "+
			"pin a concrete point release, or run without --no-network", config.LatestPoint)
	}
	point, err := arch.LatestPoint(ctx, upstreamHTTP, r.Line)
	if err != nil {
		return "", wrapUpstream(err)
	}
	return point, nil
}

func (a *app) lockPath() string {
	return filepath.Join(a.root(), lock.Name)
}

// requireLock reads the lockfile, and under --frozen-lock also proves it still
// matches upstream. That check is the point of the flag: a new architecture
// upstream is welcome and must still not change what a release publishes without a
// human seeing it, because the set of architectures a feed covers is part of its
// contract with subscribers.
func (a *app) requireLock(ctx context.Context, c *config.Config) (*lock.Lock, error) {
	path := a.lockPath()
	l, err := lock.Load(path)
	if err != nil {
		return nil, fail(exitConflict, "%v\n  run `owfeed lock --update`", err)
	}
	if !a.frozenLock {
		return l, nil
	}
	want, err := a.derive(ctx, c)
	if err != nil {
		return nil, err
	}
	if err := lock.Check(path, l, want); err != nil {
		return nil, err
	}
	return l, nil
}
