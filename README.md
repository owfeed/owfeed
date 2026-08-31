# owfeed

*[Русская версия](README_ru.md)*

One binary. Turns a directory into a signed feed for **both release lines** — apk for OpenWrt 25.12
and later, opkg for 24.10 and earlier — so your users run three lines and `apk add` works.

A noarch package across all 35 architectures — built, signed, indexed, laid out — takes ~25 seconds.
The status quo is 35 SDK builds.

**New here?** The [cookbook](https://owfeed.org/cookbook/) walks the whole path — build a package,
test it on a real router, sign it, get it into a feed — with files you can copy.

---

## Install

```sh
go install owfeed.org/owfeed/cmd/owfeed@latest
```

Or a release binary, checked against the attestation GitHub's own workflow produced for it — not
against a checksum from the same release, which whoever replaced the binary could replace too:

```sh
gh release download v0.5.1 -R owfeed/owfeed -p 'owfeed-linux-amd64'
gh attestation verify owfeed-linux-amd64 -R owfeed/owfeed \
  --signer-workflow owfeed/owfeed/.github/workflows/release.yml
chmod +x owfeed-linux-amd64 && sudo mv owfeed-linux-amd64 /usr/local/bin/owfeed
```

In GitHub Actions, `owfeed/owfeed/setup@v0.5.1` does that verification for you. Builds exist for
linux and darwin, amd64 and arm64.

**What it needs.** Nothing for `build`, `sign`, `index` or `publish`: the apk toolchain is fetched
from the OpenWrt SDK and verified against a pinned signing key, so there is no toolchain to install.
`smoke` needs Docker, because installing on a real OpenWrt image is the whole point of it. On macOS
`build` needs Docker too — apk-tools does not build there.

---

## Do you need a feed at all?

Probably not, and this says so before it sells you anything.

A key in `/etc/apk/keys` is a trust anchor for **every package name**, not just yours. A feed whose
key leaks can offer a higher version of `dropbear` and win the resolution, and apk has no
revocation — no CRL, no expiry, no way to say a key is dead.

**One package people install occasionally:** publish signed release artifacts instead.
`owfeed release` does exactly that — packages plus a signed manifest — and asks your users for
nothing but a signature check. Skip the feed.

**Several packages, or you want `apk upgrade` to work:** a feed is the only thing that does that,
because apk upgrades from an index and nothing else. Then read on.

---

## Why not `openwrt/gh-action-sdk`?

Because it is a compiler and this is a publisher. They do different halves, and most third-party
packages only need the second.

| | `gh-action-sdk` | `owfeed` |
|---|---|---|
| what it does | builds a package from source, in the SDK | packages a directory that is already built |
| noarch across 35 architectures | 35 SDK builds | one, ~25 seconds |
| index | — | signed, apk and opkg both |
| signing | `PRIVATE_KEY` written into `$TOPDIR` | key never enters the build job |
| publishing | — | GitHub Pages, gated |
| proof it installs | — | `smoke`, on a real OpenWrt image, failing if apk wants `--allow-untrusted` |
| proof the live feed works | — | `verify`, over the documented URL |
| release lines | one per run | apk and opkg from one config, each signed with the scheme its manager verifies |
| config | workflow inputs | `owfeed.yml`, with a [published schema](schema/v1.json) generated from the code |

If your package is compiled C, you want the SDK — and `owfeed` will happily package what it
produced. If it is a LuCI theme, a LuCI app, a script or a static binary, the SDK is 35 builds to
produce something that did not need compiling.

---
## How it works

**A feed is a directory of files served over HTTPS.** No server software, no database. You produce
the directory; anything that can serve static files will do.

```
feed/
  myfeed.pem                            your PUBLIC key
  releases/25.12/x86_64/
    packages.adb                        the signed index (binary)
    index.json                          the same thing as JSON
    sha256sums
    netwatch-1.4.0-r1.apk                  the packages, flat, beside the index
    luci-app-netwatch-1.4.0-r1.apk
  releases/25.12/aarch64_cortex-a53/    the same files again
  …                                     35 directories, one per architecture
```

**On the router, four things happen:**

1. The user drops your `.pem` into `/etc/apk/keys/`. The router now trusts your signature.
2. They write one line into `/etc/apk/repositories.d/<feed>.list` — the direct URL of `packages.adb`.
3. `apk update` fetches that index and verifies it against your key.
4. `apk add netwatch` finds `netwatch 1.4.0-r1` in the index, **derives the filename from the name and
   version**, fetches `netwatch-1.4.0-r1.apk` from the same directory, checks it against the hash the
   index recorded, and installs it.

**Where does it install to?** A package's payload *is* a piece of the root filesystem. What you put
in `staging/netwatch/etc/config/netwatch` arrives at `/etc/config/netwatch`.

**Why 35 copies of the same file?** In apk's `ndx` mode a router reads exactly one index — the one
for its own architecture. A noarch package has to be in all of them. The copies are byte-identical.

The stages map onto that one to one:

| | |
|---|---|
| `build` | your staged directory → `.apk` files |
| `sign` | a signature into each `.apk` |
| `index` | fan out into the 35 directories, write a signed `packages.adb` in each |
| `publish` | check it all, hand the directory to whatever uploads it |

---

**Two release lines, two package managers.** 25.12 and later is apk; 24.10 and earlier is opkg, and
they agree on almost nothing — a different index, a different signature scheme, a different word for
"architecture-independent". owfeed publishes either or both from one config, and holds both to the
same checks.

| | 25.12+ (apk) | 24.10 and earlier (opkg) |
|---|---|---|
| index | binary `packages.adb` | text `Packages` + `Packages.gz` |
| signed over | the index itself | the **uncompressed** `Packages` |
| signature | EC prime256v1 | usign / ed25519 |
| repository line | URL of the index **file** | URL of the **directory** |
| key installed as | `/etc/apk/keys/<any name>.pem` | `/etc/opkg/keys/<key id>` |
| per-package signature | yes | none — trust rests on the index |
| "any architecture" | `noarch` | `all` |

A feed serving both signs with two keys. That is not a choice: each manager verifies only its own
scheme.

## I want to publish a feed

```sh
owfeed init --url https://feed.example.org
owfeed keygen -o ~/keys/myfeed.pem      # outside the repo. A published key cannot be revoked.
export OWFEED_SIGN_KEY="$(cat ~/keys/myfeed.pem)"

# edit owfeed.yml: point `files:` at a directory laid out the way it should install

owfeed lock --update                    # derives the architecture list; commit it
owfeed build && owfeed sign && owfeed index
owfeed doctor
```

`out/` is now the feed. Upload it.

## I want to ship a new version

Bump `version:` in `owfeed.yml`, then:

```sh
owfeed build && owfeed sign && owfeed index && owfeed doctor
```

## I want to add a package

Add another entry under `packages:`. Same four commands.

## I want a package on one line only

```yaml
releases:
  - line: "25.12"
    default: true
    format: apk
  - line: "24.10"
    format: ipk

packages:
  - name: luci-app-mine        # no `releases:` — goes to both
    ...
  - name: luci-app-mine-new
    releases: ["25.12"]        # 25.12 only
    ...
```

One `owfeed build` produces every line, in each line's format. One `owfeed index` publishes them all.

## I want to tell users how to install it

```sh
owfeed install-snippet             # markdown, paste into your README
owfeed install-snippet --format sh # just the commands
```

Do not write your own. `doctor` compares your README against this output, because a feed whose
documented URL 404s is a live bug in a major feed right now.

## I want this in CI

The whole thing, correctly shaped, is one job:

```yaml
jobs:
  publish:
    permissions:                 # a called workflow can only narrow these, never widen them,
      contents: read             # so the caller has to grant what the publish job needs
      pages: write
      id-token: write
      actions: read
    uses: owfeed/owfeed/.github/workflows/feed.yml@v0.5.1
    with:
      owfeed-version: v0.5.1
      smoke-releases: "25.12 24.10"
    secrets:
      sign-key: ${{ secrets.OWFEED_SIGN_KEY }}
      usign-key: ${{ secrets.OWFEED_USIGN_KEY }}   # only if you serve 24.10
```

That splits build from publish so the signing key is never in the job that runs your build
scripts, gates the upload on `owfeed publish`, and installs the packages on a real OpenWrt image
before any of it goes out. `pre-build:` and `post-index:` take shell if your feed fetches or
checks anything of its own.

The signing secrets have to be repository or organization secrets, not environment ones: a
calling job has no environment, so it cannot read an environment secret in order to pass it on.
The `environment:` on the publish job is still what gates the run behind a reviewer.

If you want the steps yourself, take the tool and leave the shape:

```yaml
- uses: owfeed/owfeed/setup@v0.5.1
  with: { version: v0.5.1 }
- run: owfeed --frozen-lock build && owfeed sign && owfeed index
  env:
    OWFEED_SIGN_KEY: ${{ secrets.OWFEED_SIGN_KEY }}
- run: owfeed publish            # refuses to publish a broken tree. No override flag.
- uses: actions/upload-pages-artifact@v3
  with: { path: out }
- uses: actions/deploy-pages@v4
```

Put `publish` in a separate job with `environment:` so a fork PR cannot reach the key.

`setup` downloads one binary and checks it against GitHub's build attestation before running it —
not against a checksum file from the same release, which whoever replaced the binary could replace
too.

## Upstream added an architecture

```sh
owfeed lock --update    # prints the diff; commit it
```

`--frozen-lock` fails the build until you do. What your feed covers should not change without you
seeing it.

## I want to be sure it installs

```sh
owfeed smoke           # installs the built feed on a real OpenWrt image
```

`doctor` proves the tree is coherent. This proves a router accepts it — following your own published
snippet, and failing if `apk` asks for `--allow-untrusted`. They are different claims.

**This is not [owlab](https://github.com/owfeed/owlab), and does not use it.** owlab, by the same
author, is the development cycle: several releases running side by side for as long as you are
working, sources syncing into them, LuCI open in a browser. `owfeed smoke` is one gate before a
publish — one architecture, one install, one answer, then gone. The two are independent on purpose:
making a publishing tool depend on a development tool would make "will this feed install" depend on
whether owlab was installed correctly, for the sake of sharing some Docker invocation. Develop with
owlab if you want to; publish with owfeed either way.

They still compose, through a file format rather than a dependency: `owlab build` writes
`dist/<arch>/` and every stage here reads it. The boundary, the invariants either side of it, and
the contracts that cross it are written down in [ECOSYSTEM.md](docs/ECOSYSTEM.md), and
[STATUS.md](docs/STATUS.md) says how much of owfeed's side of it exists today.

## I want to check what is already live

```sh
owfeed verify out      # fetches the published feed over its documented URL
```

Catches a redirect apk will not follow, a package the live index names that is missing or the wrong
size, and — given the tree about to replace it — a version being republished with different
contents.

## Something broke

```sh
owfeed doctor          # numbered findings, each says why it matters and what to do
```

---

## Things that will bite you

owfeed refuses each of these. Every one has burned a real maintainer.

| | |
|---|---|
| `arch: all` | apk rejects it as uninstallable. Use `noarch`. |
| PKCS#8 key | `openssl genpkey -algorithm EC` writes the wrong PEM. Only SEC1 works. |
| `1.0~beta` | after `~` apk reads a commit hash, so only hex. Use `_beta1`. |
| A feed URL that redirects | apk does not follow 30x ([openwrt#17180](https://github.com/openwrt/openwrt/issues/17180)). |
| No `keep.d` entry | `/etc/apk/keys/*.pem` do **not** survive sysupgrade. Top cause of post-upgrade `UNTRUSTED signature`. |
| Indexing before signing | signing appends bytes, so the index no longer matches the file. |
| `/etc/config/foo` not in `conffiles:` | sysupgrade replaces the user's settings with your defaults, silently, every upgrade. |
| `.po` files in the payload | LuCI reads compiled `.lmo`. Point `i18n.from:` at them and owfeed compiles them. |
| A README that drifted | already live in a major feed today. |
| A package the build dropped | absence is invisible to every check that reads the tree, so owfeed checks the tree against your config. |

## Things owfeed will not pretend

- **apk has no revocation.** No CRL, no expiry, no kill signal. owfeed makes rotation cheap; it does
  not claim revocation exists.
- **Your key is a trust anchor for every package name**, not just yours — see
  [Do you need a feed at all?](#do-you-need-a-feed-at-all), which is the first thing this README
  says for a reason.
- **Attended Sysupgrade will not carry your packages across.** `owut` forwards no custom repositories
  and the ASU server's `repository_allow_list` is empty by default, which denies everything.
- **`build` packages a directory; it does not build one.** Your CSS must already be built. owfeed
  does compile your `.po` catalogues — LuCI reads `.lmo` and ignores `.po`, so that gap is silent
  and worth closing — but it will not run your build for you, and it refuses to package sources.

## Not there yet

Declared in the config schema so that writing them is a clear error rather than a silent
no-op, but **not implemented**: `retention:`, `overrides:`, `build.changed-only`,
`version-from: git-describe`, and the `s3` and `rsync` publish targets.
`github-pages` is the only target that works.

Also absent: SBOM, key rotation commands, and reusing an already-published package instead of
re-signing it unchanged.

**SDK builds are not on this list, and are not coming.** owfeed packages, it does not compile.
Build with [owlab](https://github.com/owfeed/owlab), `openwrt/gh-action-sdk`, or your own SDK
call, and leave the result in `dist/<arch>/` — every stage here reads that directory and asks
nothing about how the bytes were made. See [the artifact contract](docs/artifact-contract.md) and
[ECOSYSTEM.md](docs/ECOSYSTEM.md) for where the boundary runs and why it is drawn there.

---

## A feed built with it

[owfeed/owfeed-packages](https://github.com/owfeed/owfeed-packages) — a live feed carrying a LuCI
theme and a static Go binary across 20 architectures. Pull requests build, sign, index and check the
whole thing with a throwaway key, so a fork never comes near the feed's own.


## Someone else's CI, my feed

A feed that carries other people's packages cannot hand them its signing key, and does not have to.
Authors build and sign in their own CI and publish a signed release; the feed pulls and verifies the
author's signature against a pinned key.

What the feed signs after that is a choice. By default it signs each package too, and apk signature
blocks are additive, so the file a router installs carries **both**. A feed that would rather not put
its own signature inside somebody else's artifact sets `signing.sign-packages: false` and signs only
the index — which is where a router takes its trust from either way. Installing, upgrading and
removing by name work identically; what stops working is `apk add ./file.apk` and LuCI's Upload
Package, both of which already need `--allow-untrusted` for OpenWrt's own packages.

```sh
owfeed sign                       # in the author's CI, with the author's key
owfeed doctor --require-origin    # every package says which repository it is from
```

### If you are the author

Your repository has an ipk line and never builds an index: the feed at the far end signs that with
its own key. What you publish is a release plus a signed inventory of it.

```sh
owfeed --frozen-lock build
owfeed release --repo "$GITHUB_REPOSITORY" --tag "$GITHUB_REF_NAME"
```

On a pull request there is no tag and there must be no key — a signing secret in the job that runs
a fork's code is a secret the fork can aim work at. One command covers everything that can be
checked without one:

```sh
owfeed check                      # build, sign, index, doctor --require-origin
```

It generates a throwaway key of each kind, uses them for the length of the run and discards them.
What it answers is whether the package builds, signs and indexes and whether the tree survives
`doctor` — none of which depends on whose key signed it. **It needs no `signing:` block**, which is
the other reason it exists: repositories that publish release assets and no feed were declaring one
purely because `index` refuses to run without a usign key.

`release` writes `manifest.txt` — what belongs to this release, each file's size and hash — and
signs it with a usign key, the scheme OpenWrt already ships so a router can verify it with nothing
installed. The manifest records which repository it belongs to and readers check that: a signature
proves who wrote something, never what it is about, so without that line a manifest lifted from
another of your releases verifies perfectly as this one.

Release assets are flat, and an apk's filename carries no architecture — in a feed the architecture
*is* the directory. A package built for twenty architectures would therefore produce twenty files
with one name, so `release` appends the architecture where names collide, and only where they
collide: a noarch package keeps the filename an installer already on a router looks it up by.

[The feed's CONTRIBUTING](https://github.com/owfeed/owfeed-packages/blob/main/CONTRIBUTING.md)
walks through both sides.

## Docs

- [Examples](docs/examples.md) — a LuCI theme, a two-package feed, a compiled binary.
  *([Русский](docs/examples_ru.md))*
- [Design summary](docs/DESIGN.md) — why the tool exists, the facts it is built on, and the
  decisions worth arguing about. *([Полный документ](docs/DESIGN_ru.md), по-русски.)*
- [Verified package manager behaviour](docs/apk-behaviour.md) — what apk and opkg actually do, with
  reproductions.

## License

Apache-2.0.
