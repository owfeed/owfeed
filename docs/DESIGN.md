# owfeed — design summary

*The full design document is [DESIGN_ru.md](DESIGN_ru.md), in Russian. This is the argument
in two pages: why the tool exists, the facts it is built on, and the decisions that
are not obvious. It is a summary and not a translation — where the two disagree, the
code and [apk-behaviour.md](apk-behaviour.md) are the authority, because those were
measured.*

## The problem

OpenWrt 25.12 replaced opkg with apk (APKv3, apk-tools 3.0.5). Every third-party feed
broke at once: the index is now a binary `packages.adb`, signatures are EC prime256v1
instead of usign ed25519, and `ipkg-make-index.sh` is gone with nothing documented in
its place.

`ERROR: <file>.ipk: v2 package format error` appears in at least eleven independent
repositories. `UNTRUSTED signature` in as many. The maintainers' answers were "I will
not convert", "build it yourself", "stay on 24.10", or silence. The forum asks the
same question every release cycle, and the request that keeps recurring is precise:
*a script that signs packages and produces an index, without a full SDK build.*

No such tool exists. Every feed in the wild is a hand-written GitHub Actions file
around `openwrt/gh-action-sdk` plus a copy-pasted deploy step.

The cost falls hardest where it is least deserved. A LuCI theme or app is
architecture-independent — one file serves every target — and the status quo builds
it 35 times, twenty minutes each, to produce 35 identical copies.

## What owfeed is

A single static Go binary that turns a directory into a signed feed. Stateless, like
`deb-s3` or `repo-add` rather than aptly: each stage is a pure function over
directories.

```
sources ──build──►  dist/<arch>/*.apk                 ← no key in this process
                          │
                       sign            per-package signature       ← key here
                          │
                      index            out/releases/<release>/<arch>/:
                                       packages.adb + index.json + sha256sums
                          │
                     publish           packages first, index last
                          │
                      verify           black box, from outside, over the documented URL
```

State lives in exactly three places, deliberately: `owfeed.yml` is human intent,
`owfeed.lock` is derived fact under review, and the published tree is the database.
There is no local database and no `gh-pages` branch accumulating binaries — one
existing feed's repository reached 2.6 GB against a 1 GB Pages limit, all of it git
history.

**Go, for three reasons.** One artifact, so a 35-job matrix pays no `docker pull` or
`pip install` per job. `crypto/ed25519` verifies usign natively in about thirty
lines, which removes bootstrapping a C `usign` build from every consumer's critical
path. And redirect and TLS-chain inspection — `CheckRedirect` with
`ErrUseLastResponse`, chain validation *without* following AIA the way mbedtls does
not — are product features here, not plumbing, and are not writable in shell.

## The facts it is built on

Established by reading apk-tools 3.0.5 and the `openwrt-25.12` branch, then confirmed
by running them. The full table is in [apk-behaviour.md](apk-behaviour.md); these are
the ones that shaped the design.

**Trust flows from the signed index, not from the package.** Installing from a repo
verifies the package against a SHA-256 in an already-trusted `packages.adb`. A lone
`.apk` is therefore always UNTRUSTED, and shipping bare `.apk` files condemns users
to `--allow-untrusted` forever.

**OpenWrt never signs individual packages, but `apk mkpkg --sign-key` works.** So
owfeed signs each one by default. It fixes `apk add ./file.apk` without a flag — and
LuCI's "Upload Package", which physically cannot pass `--allow-untrusted` because
`package-manager-call` swallows unknown flags. Nobody else does this.

It is a default rather than a rule, because it is not free for every feed. A feed
carrying other people's work puts its own signature inside their artifact by doing it,
and `signing.sign-packages: false` declines that. Trust is unaffected: it comes from
the signed index, and installing, upgrading and removing by name were measured working
with no package signature at all. What is given up is the two paths above — the same
two that never worked for OpenWrt's own packages.

**`arch: all` is rejected; the value is `noarch`.** **After `~` only hex is legal**,
because that position is a commit hash. **Never `-C zstd`**: OpenWrt builds apk with
zstd disabled, so a zstd index parses on the build host and dies on every router.

**apk does not follow redirects** with the stock `uclient-fetch`. That single fact
rules out GitHub Releases as a package host, URL shorteners, apex-to-www, and
http-to-https — and it is why `verify` treats any 30x as a hard failure.

**`/etc/apk/keys/*.pem` do not survive sysupgrade.** This is the single largest cause
of post-upgrade `UNTRUSTED signature` reports, and no existing feed emits the
line that prevents it. owfeed's install snippet writes one, into
`/lib/upgrade/keep.d/<feed>` — sysupgrade reads that alongside `/etc/sysupgrade.conf`,
and a file of the feed's own can be rewritten on reinstall rather than appended to.

**`apk add ./file.apk` writes an identity-hash pin into `/etc/apk/world`**, and the
package then never updates from the repo again. So local installation never appears
in user-facing documentation.

## Decisions worth arguing about

**noarch fans out rather than being shared.** The same `.apk` is copied into every
architecture directory. It looks wasteful — 35 copies of a 50 KB theme — and the
alternative is worse: in `ndx` mode a client reads exactly one index, so a shared
noarch directory means a second line in `.list`, and because apk's wget backend
ignores `If-Modified-Since`, that is a second full index download on every `apk
update`, forever.

**Architectures are derived, never hardcoded.** From `.overview.json`, pinned into
`owfeed.lock`, with `--frozen-lock` the CI default. A renamed architecture is simply
one that is no longer in the source; a new one arrives as a diff someone approves.
The only hardcoded architecture knowledge is a three-entry rename table, and it exists
so GC reports read sensibly rather than to make decisions.

**Fail-closed everywhere.** A check that *cannot* run counts as failed, not skipped.
An unknown config key is an error, not a warning. `publish` refuses an unsigned or
unchecked tree and has no override flag. `--allow-untrusted` appears in exactly one
place — `mkndx` over our own unsigned inputs — and never in text a user will read.

**The tool argues against itself where it should.** A key in `/etc/apk/keys` is a
trust anchor for *every* package name, so a compromised feed can offer a higher
`dropbear` and win the resolution. For one package installed occasionally, signed
release artifacts are a smaller ask — and `owfeed release` serves exactly that case.
This is the third section of the README, not a footnote.

## What was deliberately cut

**GitHub Releases as a package host.** Assets 302 to `objects.githubusercontent.com`.
Not negotiable. Releases remain useful as a staging hop between an author and a feed,
which is what `owfeed release` is for.

**jsDelivr as a recommended mirror.** It is blocked in China more thoroughly than
GitHub Pages, so every README recommending it "for China" says the opposite of what it
means; plus 20 MB per file and independent caching of the index and the packages,
which is guaranteed skew.

**A DSL for building packages.** A Makefile stays a Makefile. owfeed orchestrates; it
does not become a second `package-pack.mk`.

**kmods.** Tied to the kernel ABI. Skipped by default.

## What was cut and came back

The original plan deferred **ipk and the 24.10 line to v1.0**, on the grounds that a
second index format and a second trust model nearly doubles the check surface for a
legacy line.

That was wrong, and the reason it was wrong is worth keeping: routers stay on a
release for years, so serving only the newer line leaves most of the installed base
where it is. Both lines now come out of one configuration, each with the signature
scheme its package manager actually verifies — EC for apk, usign for opkg — and the
extra surface turned out to be one index writer and one verifier.

## What is still true and still uncomfortable

**Attended sysupgrade will not carry third-party packages across.** `owut` forwards no
custom repositories at all, and the ASU server's `repository_allow_list` is empty by
default, which denies everything. owfeed says so in fixed text it never softens,
rather than letting users find out from a failed upgrade.

**apk has no revocation.** No CRL, no expiry, no way to say a key is dead. A
compromised feed key plus control of the feed URL is a total compromise of every
subscriber, with no recovery path for a device that is offline when you find out.
owfeed mitigates — the key is absent from build jobs, publishing sits behind a
protected environment, rotation is cheap enough to actually do — and does not claim
the problem is solved.
