// Command owfeed builds, signs and publishes apk feeds for OpenWrt 25.12 and later.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"

	"owfeed.org/owfeed/internal/config"
	"owfeed.org/owfeed/internal/lock"
)

// Exit codes. The distinction between 7 and 8 is load-bearing: CI may retry an
// upstream outage and must never retry a failed check.
const (
	exitOK       = 0
	exitInternal = 1
	exitConfig   = 2
	exitBuild    = 3
	exitKey      = 4
	exitIndex    = 5
	exitPublish  = 6
	exitCheck    = 7
	exitUpstream = 8
	exitConflict = 9
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = ""

// resolveVersion is what this binary calls itself.
//
// The release build passes a tag. `go install owfeed.org/owfeed/cmd/owfeed@latest`
// passes nothing, and that is the documented way to install this tool -- so
// without the fallback below the most common installation reports "dev" and every
// bug report arrives without a version in it. The module system already recorded
// what it resolved; this reads it back.
//
// A pseudo-version is not a version: `0.0.0-20260729...` looks like a release
// number and is not one, and `+dirty` means the tree had uncommitted changes.
// Both are refused, because "dev" is the honest answer for a local build.
func resolveVersion() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	v := strings.TrimPrefix(info.Main.Version, "v")
	if v == "" || v == "(devel)" || strings.HasPrefix(v, "0.0.0-") || strings.HasSuffix(v, "+dirty") {
		return "dev"
	}
	return v
}

type app struct {
	configPath string
	dir        string
	verbose    bool
	noNetwork  bool
	frozenLock bool
	cacheRoot  string

	// checkKeys is set only by `owfeed check`, and replaces whatever keys the config
	// names with the throwaway pair that command generates. See check.go.
	checkKeys *checkKeys
	// throwawayKey is `owfeed index --throwaway-key`: a dry-run job that generated its
	// own keys, outside `owfeed check`. See reconcileKeyring.
	throwawayKey bool

	out io.Writer
	err io.Writer
}

// checkKeys holds two key specs, each "file:PATH".
type checkKeys struct{ ec, usign string }

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	a := &app{out: stdout, err: stderr}

	fs := flag.NewFlagSet("owfeed", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { usage(stderr) }
	fs.StringVar(&a.configPath, "config", "", "path to owfeed.yml (default: owfeed.yml in the working directory)")
	fs.StringVar(&a.dir, "C", "", "change to this directory first")
	fs.BoolVar(&a.verbose, "v", false, "print every apk command as it runs")
	fs.BoolVar(&a.noNetwork, "no-network", false, "fail rather than reach the network")
	fs.BoolVar(&a.frozenLock, "frozen-lock", false, "fail if owfeed.lock disagrees with upstream")
	fs.StringVar(&a.cacheRoot, "cache", "", "cache directory (default: ~/.cache/owfeed)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitConfig
	}

	rest := fs.Args()
	if len(rest) == 0 {
		usage(stderr)
		return exitConfig
	}

	if a.dir != "" {
		if err := os.Chdir(a.dir); err != nil {
			fmt.Fprintln(stderr, err)
			return exitConfig
		}
	}
	if a.cacheRoot == "" {
		home, err := os.UserCacheDir()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitInternal
		}
		a.cacheRoot = filepath.Join(home, "owfeed")
	}
	if a.configPath == "" {
		a.configPath = "owfeed.yml"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd, cmdArgs := rest[0], rest[1:]
	var err error
	switch cmd {
	case "version":
		fmt.Fprintf(stdout, "owfeed %s\n", resolveVersion())
		return exitOK
	case "keygen":
		err = a.keygen(cmdArgs)
	case "lock":
		err = a.lock(ctx, cmdArgs)
	case "releases":
		err = a.releases(ctx, cmdArgs)
	case "plan":
		err = a.plan(ctx, cmdArgs)
	case "build":
		err = a.build(ctx, cmdArgs)
	case "sign":
		err = a.sign(ctx, cmdArgs)
	case "index":
		err = a.index(ctx, cmdArgs)
	case "doctor":
		err = a.doctor(ctx, cmdArgs)
	case "check":
		err = a.check(ctx, cmdArgs)
	case "init":
		err = a.init_(cmdArgs)
	case "install-snippet":
		err = a.installSnippet(cmdArgs)
	case "publish":
		err = a.publish(ctx, cmdArgs)
	case "smoke":
		err = a.smoke(ctx, cmdArgs)
	case "verify":
		err = a.verify(ctx, cmdArgs)
	case "verify-artifact":
		err = a.verifyArtifact(cmdArgs)
	case "release":
		err = a.release(cmdArgs)
	case "help", "-h", "--help":
		usage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "owfeed: unknown command %q\n\n", cmd)
		usage(stderr)
		return exitConfig
	}

	if err != nil {
		fmt.Fprintf(stderr, "owfeed %s: %v\n", cmd, err)
		return codeFor(err)
	}
	return exitOK
}

// exitError lets a command choose its exit code without the caller having to
// pattern-match on message text.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func fail(code int, format string, args ...any) error {
	return &exitError{code: code, err: fmt.Errorf(format, args...)}
}

func wrap(code int, err error) error {
	if err == nil {
		return nil
	}
	return &exitError{code: code, err: err}
}

func codeFor(err error) int {
	var e *exitError
	if errors.As(err, &e) {
		return e.code
	}
	var cfgErr *config.Error
	if errors.As(err, &cfgErr) {
		return exitConfig
	}
	if errors.Is(err, lock.ErrStale) {
		return exitConflict
	}
	return exitInternal
}

func usage(w io.Writer) {
	fmt.Fprint(w, `owfeed builds, signs and publishes apk feeds for OpenWrt 25.12 and later.

Usage:
  owfeed [flags] <command> [arguments]

Commands:
  init        scaffold owfeed.yml in this directory
  keygen      create the feed's signing key
  lock        derive the architecture matrix and write owfeed.lock
  releases    what the download server publishes per line, and which format it takes
  plan        what a build would produce, offline and before it produces it
  build       build every configured package into a flat directory
  sign        sign the packages in a directory
  index       fan out the signed packages and build a signed index per architecture
  doctor      check the built tree against everything that has burned a feed before
  check       build, sign, index and doctor, on throwaway keys — the whole of what
              CI can verify before a tag, with no secret in scope
  publish     gate the tree on those checks and hand it to the target
  smoke       install the built feed on a real OpenWrt image
  verify      check the published feed from outside, over its documented URL
  verify-artifact  check an upstream release against its author's signature
  release     sign the built packages and write a manifest for a feed to consume
  install-snippet  print the instructions your subscribers follow
  version     print the version

Flags:
  -C DIR          change to DIR first
  --config PATH   path to owfeed.yml (default owfeed.yml)
  --cache DIR     cache directory (default ~/.cache/owfeed)
  --frozen-lock   fail if owfeed.lock disagrees with upstream
  --no-network    fail rather than reach the network
  -v              print every apk command as it runs

Run a stage on its own or all of them in order; each takes a directory in and
leaves a directory behind, with no hidden state between them.
`)
}

// loadConfig reads owfeed.yml.
func (a *app) loadConfig() (*config.Config, error) {
	c, err := config.Load(a.configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fail(exitConfig, "no %s here; run `owfeed init` to create one", a.configPath)
		}
		return nil, wrap(exitConfig, err)
	}
	// `owfeed check` runs the same stages with keys that live for the length of the
	// run. Overriding here rather than in each stage is what lets it reuse them
	// unchanged, and it is the only thing that ever writes to this field.
	if a.checkKeys != nil {
		signPackages := true
		c.Signing.Key = a.checkKeys.ec
		c.Signing.UsignKey = a.checkKeys.usign
		c.Signing.SignPackages = &signPackages
	}
	return c, nil
}

// root is the directory the config's relative paths resolve against.
func (a *app) root() string {
	return filepath.Dir(a.configPath)
}

func (a *app) logf(format string, args ...any) {
	fmt.Fprintf(a.out, format+"\n", args...)
}

func (a *app) debugf(format string, args ...any) {
	if a.verbose {
		fmt.Fprintf(a.err, format+"\n", args...)
	}
}
