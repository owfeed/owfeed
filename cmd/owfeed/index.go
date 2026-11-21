package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"owfeed.org/owfeed/internal/apk"

	"owfeed.org/owfeed/internal/badge"
	"owfeed.org/owfeed/internal/build"
	"owfeed.org/owfeed/internal/config"
	"owfeed.org/owfeed/internal/feedindex"
	"owfeed.org/owfeed/internal/index"
	"owfeed.org/owfeed/internal/keyring"
	"owfeed.org/owfeed/internal/keys"
	"owfeed.org/owfeed/internal/lock"
	"owfeed.org/owfeed/internal/snippet"
)

const defaultOut = "out"

func (a *app) index(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("owfeed index", flag.ContinueOnError)
	fs.SetOutput(a.err)
	out := fs.String("o", defaultOut, "directory to write the publishable tree into")
	onlyLine := fs.String("release", "", "index only this release line")
	fs.BoolVar(&a.throwawayKey, "throwaway-key", false,
		"the signing key was generated for this run: build the keyring package without comparing it to owfeed.lock")
	if err := fs.Parse(args); err != nil {
		return wrap(exitConfig, err)
	}

	dist := defaultDist
	if fs.NArg() > 0 {
		dist = fs.Arg(0)
	}

	c, err := a.loadConfig()
	if err != nil {
		return err
	}
	l, err := a.requireLock(ctx, c)
	if err != nil {
		return err
	}
	// Badge data is collected across both formats: a package on 24.10 only would
	// otherwise get no badge, and one on both lines would claim only 25.12.
	// The pinned author keys, when the config names them. Empty means the feed makes
	// no claim about who built what, and every package is placed.
	authorKeys, err := loadAuthorKeys(authorKeyDirFor(a, c))
	if err != nil {
		return err
	}

	// Packages excluded for want of an author signature, so the summary can name how
	// many rather than leaving it to whoever reads 35 architectures of output.
	skipped := map[string]bool{}

	badges := map[string]badge.Package{}

	// One command, every line the config declares. A feed serving both is two feeds
	// under one URL, and building them separately is how one of them goes stale.
	lines := 0
	for _, r := range c.Releases {
		if *onlyLine != "" && r.Line != *onlyLine {
			continue
		}
		if r.Format == config.FormatIPK {
			if err := a.indexIPK(c, l, r, dist, *out, badges); err != nil {
				return err
			}
			lines++
		}
	}

	var apkLines []config.Release
	for _, r := range c.Releases {
		if (*onlyLine == "" || r.Line == *onlyLine) && r.Format == config.FormatAPK {
			apkLines = append(apkLines, r)
		}
	}
	if len(apkLines) == 0 {
		if lines == 0 {
			return fail(exitConfig, "no release line matched --release %q", *onlyLine)
		}
		// The ipk side already wrote its key and its trees; its badges are still ours
		// to write, because this is the only place both formats have been seen.
		if err := a.writeBadges(*out, badges); err != nil {
			return err
		}
		return a.writeSubscribe(c, *out)
	}

	tool, err := a.tool(ctx, l)
	if err != nil {
		return err
	}

	// The keyring package is built HERE rather than in `owfeed build`, and that is not
	// tidiness. A feed that carries other people's work runs no build at all — its
	// packages arrive already built, and its pipeline is fetch, sign, index — so a
	// keyring attached to the build step would never be produced by exactly the feeds
	// that need rotation most. Indexing is the one step every feed runs.
	if *c.Signing.KeyringPackage {
		cleanup, err := a.buildKeyring(ctx, c, l, dist)
		if err != nil {
			return err
		}
		defer cleanup()
	}

	tree, err := index.Tree(dist)
	if err != nil {
		return wrap(exitIndex, err)
	}
	if len(tree) == 0 {
		return fail(exitIndex, "%s holds no packages; run `owfeed build` first", dist)
	}

	key, err := a.signingKey(c)
	if err != nil {
		return err
	}
	keyDir, cleanupKey, err := a.stageKey(key)
	if err != nil {
		return err
	}
	defer cleanupKey()

	// apk verifies the packages it indexes against this directory, so the index step
	// checks the signing step's work instead of waving it through. adbsign reports
	// its failures and exits 0 regardless, so something has to.
	trustDir, err := keys.StagePublic(os.TempDir(), c.Feed.Name+".pem", &key.PublicKey)
	if err != nil {
		return wrap(exitKey, err)
	}
	defer os.RemoveAll(trustDir)

	signer, err := index.NewSigner(keyDir, keyFileName, key)
	if err != nil {
		return wrap(exitKey, err)
	}

	// Rotation keys go into the SAME directory: mkndx resolves every --sign-key
	// against one --keys-dir, so a second key staged anywhere else is simply not found.
	var alsoSign []index.Signer
	for i, spec := range c.Signing.AlsoSign {
		k, err := keys.Source(spec).Load(a.root())
		if err != nil {
			return wrap(exitKey, err)
		}
		name := fmt.Sprintf("also-%d.pem", i)
		if err := keys.WriteInto(keyDir, name, k); err != nil {
			return wrap(exitKey, err)
		}
		s, err := index.NewSigner(keyDir, name, k)
		if err != nil {
			return wrap(exitKey, err)
		}
		alsoSign = append(alsoSign, s)
		a.logf("rotation: the index will also be signed by %s", s.Identity)
	}

	seenLine := map[string]bool{}

	for _, r := range apkLines {
		lr, ok := l.Release(r.Line)
		if !ok {
			return fail(exitConflict, "owfeed.lock has no entry for release line %s; run `owfeed lock --update`", r.Line)
		}
		total := 0
		for _, arch := range lr.Arches {
			dir := filepath.Join(*out, expandLayout(c.Layout.Path, r.Line, arch))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return wrap(exitIndex, err)
			}

			// A noarch package goes into every architecture's directory, because a
			// router reads exactly one index — its own — and would otherwise never
			// see it. A package built for a specific architecture goes only into
			// that one.
			//
			// Copying after signing rather than before is not incidental: ECDSA
			// signatures are randomised, so signing each copy separately would
			// produce 35 different files where subscribers should see one.
			var placed []string
			for _, src := range []string{config.Noarch, arch} {
				for _, p := range tree[src] {
					// A package nobody signed is left out rather than failing the
					// whole tree. One author who has not adopted signing yet should
					// cost their own package and nothing else — a feed carrying
					// several people's work cannot make everyone wait for the
					// slowest of them.
					//
					// Left out LOUDLY. A package that quietly disappears is the
					// failure this project has already had: every check reads the
					// tree, so absence looks identical to "there was nothing to
					// publish", and a feed once shipped one package of three and
					// reported itself healthy. Hence the name on stdout and the
					// count in the summary, not a debug line.
					// The keyring package is the feed's own and is signed by the
					// feed. Holding it to the author rule would exclude the one
					// package a feed publishes about itself.
					if len(authorKeys) > 0 && !strings.HasPrefix(p, keyring.NameFor(c.Feed.Name)+"-") {
						ok, err := signedByAuthor(ctx, tool, filepath.Join(dist, src), p, authorKeys)
						if err != nil {
							return wrap(exitIndex, err)
						}
						if !ok {
							if !skipped[p] {
								a.logf("EXCLUDED %s: no signature by a pinned author key", p)
								skipped[p] = true
							}
							continue
						}
					}
					if err := copyFile(filepath.Join(dist, src, p), filepath.Join(dir, p)); err != nil {
						return wrap(exitIndex, err)
					}
					placed = append(placed, p)
				}
			}
			if len(placed) == 0 {
				continue
			}

			res, err := index.Build(ctx, tool, index.Options{
				Dir: dir, TrustDir: trustDir, Signer: signer,
				Description:      c.Feed.Title,
				AlsoSign:         alsoSign,
				UnsignedPackages: !*c.Signing.SignPackages,
			})
			if err != nil {
				return wrap(exitIndex, err)
			}
			total += len(placed)
			a.debugf("%s: %d packages, index %d bytes", dir, len(res.Packages), res.Size)

			// Badge data, read back from the index that was just written rather than
			// from the config: a badge that claims a version the feed does not serve
			// is worse than no badge, and this is the only place both are known to
			// agree. One architecture per line is enough — a package present on a
			// line appears in every architecture directory it belongs to, at the
			// same version.
			if !seenLine[r.Line] {
				idx, err := feedindex.ReadDir(dir)
				if err != nil {
					return wrap(exitIndex, err)
				}
				for _, e := range idx.Entries {
					b := badges[e.Name]
					b.Name, b.Version = e.Name, e.Version
					b.Releases = append(b.Releases, r.Line)
					badges[e.Name] = b
				}
				seenLine[r.Line] = true
			}
		}
		a.logf("%s: %d package placement(s) across %d architecture(s)", r.Line, total, len(lr.Arches))
	}

	// The public key belongs at the root of the feed, because that is the URL the
	// install snippet tells people to fetch it from.
	pub, err := keys.MarshalPublic(&key.PublicKey)
	if err != nil {
		return wrap(exitKey, err)
	}
	if err := os.WriteFile(filepath.Join(*out, c.Feed.Name+".pem"), pub, 0o644); err != nil {
		return wrap(exitIndex, err)
	}

	if err := a.writeBadges(*out, badges); err != nil {
		return err
	}
	if err := a.writeSubscribe(c, *out); err != nil {
		return err
	}
	// GitHub Pages runs Jekyll unless told not to, and Jekyll drops paths beginning
	// with an underscore or a dot. On a tree of binaries that silently removes files.
	if err := os.WriteFile(filepath.Join(*out, ".nojekyll"), nil, 0o644); err != nil {
		return wrap(exitIndex, err)
	}

	// A record of what was left out, beside the tree rather than only in a log that
	// scrolls away. It is what lets `doctor` tell a package excluded on purpose from
	// one that vanished because a build half-failed — the second is an error and
	// always has been, and blurring the two would give up the check that exists
	// because this feed once published one package of three.
	if err := writeExclusions(*out, skipped); err != nil {
		return wrap(exitIndex, err)
	}

	// Named, not merely counted: a maintainer reading CI output has to be able to see
	// WHICH package stopped being published, and act on it.
	if len(skipped) > 0 {
		names := make([]string, 0, len(skipped))
		for n := range skipped {
			names = append(names, n)
		}
		sort.Strings(names)
		a.logf("%d package(s) excluded for want of an author signature: %s",
			len(names), strings.Join(names, ", "))
	}
	a.logf("wrote %s, signed by key %s", *out, signer.Identity)
	return nil
}

// writeSubscribe publishes the one script that subscribes a router to this feed,
// whichever release line it runs.
//
// It is written from the same config that laid the tree out, at the same moment,
// so a URL it prints cannot describe a layout the feed does not have. A script
// maintained by hand beside the deploy is the drift this package exists to
// prevent — see the package comment for a feed currently living with it.
func (a *app) writeSubscribe(c *config.Config, out string) error {
	in := snippet.Input{Config: c}

	// The opkg branch needs the key's id, because for opkg the id IS the filename.
	// Without a usign key there are no ipk lines to serve, and the script says so
	// rather than fetching a name that does not exist.
	for _, r := range c.Releases {
		if r.Format != config.FormatIPK {
			continue
		}
		key, err := a.usignKey(c)
		if err != nil {
			return err
		}
		in.UsignKeyID = key.ID.String()
		break
	}

	path := filepath.Join(out, snippet.ScriptName)
	if err := os.WriteFile(path, []byte(snippet.Script(in)), 0o755); err != nil {
		return wrap(exitIndex, err)
	}
	return nil
}

// expandLayout fills in the layout template from owfeed.yml, as a host path.
func expandLayout(path, release, arch string) string {
	return filepath.FromSlash(config.ExpandLayout(path, release, arch))
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// writeBadges renders one badge file per package, so a maintainer whose work a feed
// carries can show it in their README without the feed having to know they did.
func (a *app) writeBadges(out string, badges map[string]badge.Package) error {
	list := make([]badge.Package, 0, len(badges))
	for _, b := range badges {
		list = append(list, b)
	}
	badge.Sort(list)
	if err := badge.Write(out, list); err != nil {
		return wrap(exitIndex, err)
	}
	if len(list) > 0 {
		a.debugf("wrote %d badge file(s) under %s/", len(list)*2, badge.Dir)
	}
	return nil
}

// authorKeyDirFor resolves signing.author-keys against the config's directory.
func authorKeyDirFor(a *app, c *config.Config) string {
	if c.Signing.AuthorKeys == "" {
		return ""
	}
	return filepath.Join(a.root(), c.Signing.AuthorKeys)
}

// signedByAuthor reports whether a package carries a signature by one of the pinned
// keys.
//
// Read from the built artifact rather than taken on trust from whoever staged it:
// this is the one place the feed can still tell the difference, and after indexing it
// is too late.
func signedByAuthor(ctx context.Context, tool *apk.Tool, dir, pkg string, allowed map[string]keys.Identity) (bool, error) {
	ids, err := index.Signatures(ctx, tool, dir, pkg)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		for _, want := range allowed {
			if id == want.String() {
				return true, nil
			}
		}
	}
	return false, nil
}

// ExclusionsFile records packages left out of the index, and why.
const exclusionsFile = ".excluded"

// writeExclusions leaves the record beside the tree. It is rewritten every run,
// including to empty, so a package that starts being signed stops being listed.
func writeExclusions(out string, skipped map[string]bool) error {
	path := filepath.Join(out, exclusionsFile)
	if len(skipped) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	names := make([]string, 0, len(skipped))
	for n := range skipped {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("# Packages left out of this index for want of a signature by a pinned\n")
	b.WriteString("# author key. Written by `owfeed index`; read by `owfeed doctor`.\n")
	for _, n := range names {
		fmt.Fprintf(&b, "%s\n", n)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// stageKeyring writes the keyring package's payload and builds it.
//
// Staged rather than committed: the payload is one file derived from the signing key,
// so keeping a copy in the repository would be a second place for it to be wrong.
func (a *app) buildKeyring(ctx context.Context, c *config.Config, l *lock.Lock, dist string) (func(), error) {
	noop := func() {}
	key, err := a.signingKey(c)
	if err != nil {
		return noop, err
	}
	id, err := keys.IdentityOf(&key.PublicKey)
	if err != nil {
		return noop, wrap(exitKey, err)
	}
	if err := a.reconcileKeyring(l, id.String()); err != nil {
		return noop, err
	}

	// Beside the config rather than in TMPDIR: `files:` resolves against the config's
	// directory, and on macOS the system temp directory is on another volume, where a
	// relative path to it cannot be formed at all.
	const stageDir = ".owfeed-keyring"
	dir := filepath.Join(a.root(), stageDir)
	if err := os.RemoveAll(dir); err != nil {
		return noop, wrap(exitIndex, err)
	}
	cleanup := func() { os.RemoveAll(dir) }

	if _, err := keyring.Stage(dir, &key.PublicKey); err != nil {
		cleanup()
		return noop, wrap(exitIndex, err)
	}
	kp, err := keyring.Package(c.Feed, l.Keyring.Version, stageDir)
	if err != nil {
		cleanup()
		return noop, wrap(exitIndex, err)
	}

	// Into dist/, where every other package already is, so the rest of indexing does
	// not have to know this one exists.
	tool, err := a.tool(ctx, l)
	if err != nil {
		cleanup()
		return noop, err
	}
	res, err := build.Build(ctx, tool, build.Request{
		Package: kp, Feed: c.Feed, Root: a.root(),
		Version: l.Keyring.Version, Arch: config.Noarch,
		OutDir: dist, Format: config.FormatAPK,
	})
	if err != nil {
		cleanup()
		return noop, wrap(exitIndex, err)
	}
	// Signed here, with the feed's own key, whatever signing.sign-packages says. That
	// setting exists so a feed does not put its signature inside a file somebody else
	// built; this file is the feed's own, and mkndx will not index a package carrying
	// no signature at all.
	keyDir, cleanupKey, err := a.stageKey(key)
	if err != nil {
		cleanup()
		return noop, err
	}
	defer cleanupKey()
	signer, err := index.NewSigner(keyDir, keyFileName, key)
	if err != nil {
		cleanup()
		return noop, wrap(exitKey, err)
	}
	if _, err := index.Sign(ctx, tool, filepath.Dir(res.File), signer); err != nil {
		cleanup()
		return noop, wrap(exitKey, err)
	}

	a.logf("built %s (keyring, key %s)", rel(res.File), id)
	return cleanup, nil
}

// reconcileKeyring settles which identity the keyring package is built for, and
// refuses a signing key the lockfile does not record.
func (a *app) reconcileKeyring(l *lock.Lock, id string) error {
	// A key generated a moment ago — by `owfeed check`, or by a dry-run job that
	// passed --throwaway-key — disagrees with the record every single time and nothing
	// has rotated. The keyring is still built, under the recorded version, so the tree
	// comes out the shape a publish would make it. Only the comparison has nothing to
	// say, and the advice to record the key would be harmful: the next real publish
	// would then fail the same comparison the other way round.
	if a.checkKeys != nil || a.throwawayKey {
		if l.Keyring == nil {
			l.Keyring = &lock.Keyring{Version: keyring.VersionFor(1)}
		}
		l.Keyring.Identity = id
		return nil
	}
	// The version comes from the lockfile, which is where a rotation is recorded and
	// reviewed — a number invented per run either never moves or moves for no reason.
	//
	// Except for the first one. Demanding `owfeed lock --update` here made the feature
	// unreachable for the feeds it is for: deriving the entry needs the private key, and
	// a feed keeps its key in CI secrets, not on the maintainer's laptop. So the first
	// publication counts as 1 and says so. A LATER key that disagrees with the record is
	// still refused below, because counting a rotation needs the state the lockfile
	// holds, and by then whoever rotated has the new key in hand anyway.
	if l.Keyring == nil {
		l.Keyring = &lock.Keyring{Identity: id, Version: keyring.VersionFor(1)}
		a.logf("no keyring version in %s: publishing %s for key %s",
			a.lockPath(), l.Keyring.Version, id)
		a.logf("record it with `owfeed lock --update` when you next have the signing key")
		return nil
	}
	// A key that disagrees with the record is a rotation nobody wrote down. Building
	// anyway would publish the new key under the old version, which every router
	// declines as an upgrade it already has.
	if l.Keyring.Identity != id {
		return fail(exitConflict,
			"the signing key is %s but %s records %s for the keyring package\n"+
				"  run `owfeed lock --update` and commit the diff: a rotation is a fact worth seeing\n"+
				"  if this key is throwaway, as in a pull-request check, pass `owfeed index --throwaway-key` instead;\n"+
				"  never in a job that publishes",
			id, a.lockPath(), l.Keyring.Identity)
	}
	return nil
}
