# owfeed

*[Русская версия](README_ru.md)*

owfeed turns a directory into a signed package feed for OpenWrt. One binary covers both release lines:
apk for 25.12 and later, opkg for 24.10 and earlier.

- **Your users:** three commands, then `apk add` works.
- **You:** a noarch package across all 35 architectures is built, signed and indexed in about 25 seconds.
  The usual way is 35 SDK builds.
- **First time?** The [cookbook](https://owfeed.org/cookbook/) walks the whole path with files to copy.

## Quick start

1. Install owfeed (see [Install](#install)).
2. Create the config and a key. Keep the key outside the repository: a published key cannot be revoked.

   ```sh
   owfeed init --url https://feed.example.org
   owfeed keygen -o ~/keys/myfeed.pem
   export OWFEED_SIGN_KEY="$(cat ~/keys/myfeed.pem)"
   ```

3. Edit the package entry `init` wrote into `owfeed.yml`. `files:` is a directory laid out the way it
   should install: `<files>/etc/config/netwatch` lands at `/etc/config/netwatch`. owfeed packages that
   directory as it is and compiles nothing, so build your CSS or binary first.

   ```yaml
   packages:
     - name: luci-app-example
       build: mkpkg
       arch: noarch          # never "all": apk rejects it as uninstallable
       version: 1.0.0-r1
       files: ./dist/root
       description: "One line. LuCI truncates past 512 bytes."
       depends: [luci-base]
       conffiles: []         # every /etc/config file the package ships
   ```

   More shapes (a theme, two packages, a compiled binary) are in [Examples](docs/examples.md).
4. Build the feed:

   ```sh
   owfeed lock --update        # reads the architectures from downloads.openwrt.org; commit owfeed.lock
   owfeed build && owfeed sign && owfeed index
   owfeed doctor
   ```

5. Serve `out/` over HTTPS. That directory is the feed, and `index` has put your public key in it
   (`out/<feed>.pem`). [CI](#ci) publishes it to GitHub Pages for you.
6. Check it on a real router image: `owfeed smoke` (needs Docker).

## Install

Download a release binary and check it against GitHub's build attestation:

```sh
gh release download v0.5.8 -R owfeed/owfeed -p 'owfeed-linux-amd64'
gh attestation verify owfeed-linux-amd64 -R owfeed/owfeed \
  --signer-workflow owfeed/owfeed/.github/workflows/release.yml
chmod +x owfeed-linux-amd64 && sudo mv owfeed-linux-amd64 /usr/local/bin/owfeed
```

Or build it with Go: `go install owfeed.org/owfeed/cmd/owfeed@latest`.

- Builds exist for linux and darwin, amd64 and arm64.
- In GitHub Actions, `owfeed/owfeed/setup@v0.5.8` installs and verifies it for you.
- The attestation is checked, not a checksum: whoever replaced the binary could replace a checksum
  from the same release too.

### What it needs

| Command | Needs |
|---|---|
| `build`, `sign`, `index`, `publish` | nothing; owfeed takes apk-tools out of the OpenWrt SDK, verified against a pinned key, and compiles no packages with it |
| `build` on macOS | Docker, because apk-tools does not build there |
| `smoke` | Docker |
| `smoke -arch` other than x86_64, on an x86_64 host | QEMU: `docker run --privileged --rm tonistiigi/binfmt --install arm64` |

## What do you want to do?

| I want to… | Run | Details |
|---|---|---|
| decide whether I need a feed | read the section | [Do you need a feed at all?](#do-you-need-a-feed-at-all) |
| ship a new version | bump `version:`, then `owfeed build && owfeed sign && owfeed index && owfeed doctor` | [Quick start](#quick-start) |
| add a package | add an entry under `packages:`, same four commands | [Examples](docs/examples.md) |
| serve one release line only | `releases:` on the package | [One line only](#a-package-on-one-line-only) |
| tell users how to install | `owfeed install-snippet` | [Install snippet](#the-install-snippet) |
| publish from CI | one reusable workflow | [CI](#ci) |
| follow a new upstream architecture | `owfeed lock --update` | [New architecture](#upstream-added-an-architecture) |
| prove a router installs it | `owfeed smoke` | [Smoke](#prove-it-installs) |
| check the live feed | `owfeed verify out` | [Verify](#check-what-is-live) |
| find out what broke | `owfeed doctor` | numbered findings, each with the fix |
| carry other people's packages | `owfeed sign`, `owfeed release` | [Someone else's CI](#someone-elses-ci-my-feed) |

## Do you need a feed at all?

A feed is the right tool only for some packages. Check before you start.

- **One package, installed now and then:** publish signed release artifacts with `owfeed release`.
  Your users only check a signature. Skip the feed.
- **Several packages, or `apk upgrade` must work:** you need a feed. apk upgrades from an index and
  from nothing else.

Why it matters: a key in `/etc/apk/keys` is trusted for **every package name**, not only yours. A
leaked feed key can offer a higher `dropbear` and win. apk has no revocation: no CRL, no expiry, no
way to mark a key dead.

## How it works

A feed is a directory of static files served over HTTPS. No server software, no database.

```
feed/
  myfeed.pem                            your PUBLIC key
  releases/25.12/x86_64/
    packages.adb                        the signed index (binary)
    index.json                          the same thing as JSON
    sha256sums
    netwatch-1.4.0-r1.apk               the packages, flat, beside the index
    luci-app-netwatch-1.4.0-r1.apk
  releases/25.12/aarch64_cortex-a53/    the same files again
  …                                     35 directories, one per architecture
```

On the router:

1. The user puts your `.pem` into `/etc/apk/keys/`.
2. They write the direct URL of `packages.adb` into `/etc/apk/repositories.d/<feed>.list`.
3. `apk update` fetches the index and verifies it against your key.
4. `apk add netwatch` finds `netwatch 1.4.0-r1`, derives the filename from name and version,
   fetches `netwatch-1.4.0-r1.apk` from the same directory, checks its hash, and installs it.

- **Where files land:** the payload is a piece of the root filesystem.
  `staging/netwatch/etc/config/netwatch` arrives at `/etc/config/netwatch`.
- **Why 35 copies:** in apk's `ndx` mode a router reads only the index for its own architecture, so a
  noarch package sits in every one. The copies are byte-identical.

| Stage | Does |
|---|---|
| `build` | your staged directory → `.apk` files |
| `sign` | a signature into each `.apk` |
| `index` | fan out into the 35 directories, write a signed `packages.adb` in each |
| `publish` | check everything, hand the directory to whatever uploads it |

<details>
<summary>apk and opkg side by side</summary>

25.12 and later is apk; 24.10 and earlier is opkg. They agree on almost nothing. owfeed publishes
either or both from one config and holds both to the same checks.

| | 25.12+ (apk) | 24.10 and earlier (opkg) |
|---|---|---|
| index | binary `packages.adb` | text `Packages` + `Packages.gz` |
| signed over | the index itself | the **uncompressed** `Packages` |
| signature | EC prime256v1 | usign / ed25519 |
| repository line | URL of the index **file** | URL of the **directory** |
| key installed as | `/etc/apk/keys/<any name>.pem` | `/etc/opkg/keys/<key id>` |
| per-package signature | yes | none; trust rests on the index |
| "any architecture" | `noarch` | `all` |

A feed serving both signs with two keys, because each manager verifies only its own scheme.

</details>

## A package on one line only

A package with no `releases:` goes to every line. Name the lines to narrow it:

```yaml
releases:
  - line: "25.12"
    default: true
    format: apk
  - line: "24.10"
    format: ipk

packages:
  - name: luci-app-mine        # no `releases:`: both lines
    ...
  - name: luci-app-mine-new
    releases: ["25.12"]        # 25.12 only
    ...
```

One `owfeed build` produces every line in its own format. One `owfeed index` publishes them all.

## The install snippet

```sh
owfeed install-snippet             # markdown, paste into your README
owfeed install-snippet --format sh # just the commands
```

Do not write your own. `doctor` compares your README with this output: a documented URL that 404s is
a live bug in a major feed today.

## CI

One job does the whole thing:

```yaml
jobs:
  publish:
    permissions:                 # a called workflow can only narrow these, never widen them,
      contents: read             # so the caller grants what the publish job needs
      pages: write
      id-token: write
      actions: read
    uses: owfeed/owfeed/.github/workflows/feed.yml@v0.5.8
    secrets: inherit             # the publish job reads the keys from its environment
    with:
      smoke-releases: "25.12 24.10"
```

1. Store the keys as secrets of the `feed` environment: `OWFEED_SIGN_KEY`, and `OWFEED_USIGN_KEY` if
   you serve 24.10. Make the usign key with `owfeed keygen -usign`.
2. Add the job above.
3. Push. The workflow builds, smokes on a real OpenWrt image, gates on `owfeed publish`, and uploads
   to GitHub Pages. A complete workflow file is in the [cookbook](https://owfeed.org/cookbook/).

- The signing key never enters the job that runs your build scripts.
- Only the publish job declares the `feed` environment, so no other workflow can read the keys. The
  `sign-key:` and `usign-key:` secrets are the fallback for repository-scope keys.
- The `uses:` line is the pin: the workflow installs the owfeed release it is tagged with, so a
  dependabot bump moves the binary too. Set `owfeed-version:` only to run a different release.
- `pre-build:` and `post-index:` take shell for anything your feed fetches or checks itself.

<details>
<summary>The same steps by hand</summary>

```yaml
- uses: owfeed/owfeed/setup@v0.5.8       # installs owfeed v0.5.8
- run: owfeed --frozen-lock build && owfeed sign && owfeed index
  env:
    OWFEED_SIGN_KEY: ${{ secrets.OWFEED_SIGN_KEY }}
- run: owfeed publish            # refuses to publish a broken tree. No override flag.
- uses: actions/upload-pages-artifact@v3
  with: { path: out }
- uses: actions/deploy-pages@v4
```

Split it in two before you use it: build in one job, then `publish` and the uploads in a second job
with `environment:`, so a fork PR cannot reach the key. `setup`
checks the binary against GitHub's build attestation before running it.

</details>

## Upstream added an architecture

```sh
owfeed lock --update    # prints the diff; commit it
```

`--frozen-lock` fails the build until you commit. What your feed covers never changes without you
seeing it.

## Prove it installs

```sh
owfeed smoke
```

- `smoke` installs the built `out/` on a real OpenWrt image (`openwrt/rootfs`, pulled by Docker). It
  follows your install snippet and fails if apk asks for `--allow-untrusted`.
- `doctor` proves the tree is coherent; `smoke` proves a router accepts it.
- The default is x86_64 and the line `owfeed.yml` marks `default:`. `-release 24.10` smokes the opkg
  line.
- `smoke -arch aarch64_cortex-a53` installs on an image of that architecture. See the QEMU line in
  [What it needs](#what-it-needs).

<details>
<summary>smoke and owlab</summary>

`smoke` does not use [owlab](https://github.com/owfeed/owlab). owlab is the development cycle: several
releases running side by side, sources syncing in, LuCI in a browser. `smoke` is one gate before a
publish: one architecture, one install, one answer. A publishing tool that depended on a development
tool would make "will this feed install" depend on owlab being installed correctly.

They compose through a file format: `owlab build` writes `dist/<arch>/`, and every stage here reads
it. [ECOSYSTEM.md](docs/ECOSYSTEM.md) has the boundary and its contracts;
[STATUS.md](docs/STATUS.md) says how much of owfeed's side exists today.

</details>

## Check what is live

```sh
owfeed verify out      # fetches the published feed over its documented URL
```

It catches a redirect apk will not follow and a listed package that is missing or the wrong size.
Given the tree about to replace it, it also catches a version republished with different contents.

## Common mistakes owfeed refuses

owfeed refuses each of these. Every one has burned a real maintainer.

| Mistake | What happens |
|---|---|
| `arch: all` | apk rejects it as uninstallable. Use `noarch`. |
| PKCS#8 key | `openssl genpkey -algorithm EC` writes the wrong PEM. Only SEC1 works. |
| `1.0~beta` | after `~` apk reads a commit hash, so only hex. Use `_beta1`. |
| A feed URL that redirects | apk does not follow 30x ([openwrt#17180](https://github.com/openwrt/openwrt/issues/17180)). |
| No `keep.d` entry | `/etc/apk/keys/*.pem` do **not** survive sysupgrade: the top cause of post-upgrade `UNTRUSTED signature`. |
| Indexing before signing | signing appends bytes, so the index no longer matches the file. |
| `/etc/config/foo` not in `conffiles:` | sysupgrade replaces the user's settings with your defaults, silently, every upgrade. |
| `.po` files in the payload | LuCI reads compiled `.lmo`. Point `i18n.from:` at them and owfeed compiles them. |
| A README that drifted | already live in a major feed today. |
| A package the build dropped | absence is invisible to checks that read the tree, so owfeed checks the tree against your config. |

## Limits

- **No revocation in apk.** No CRL, no expiry, no kill signal. owfeed makes rotation cheap; it does not
  claim revocation exists.
- **Your key is trusted for every package name.** See [Do you need a feed at all?](#do-you-need-a-feed-at-all).
- **Attended Sysupgrade drops your packages.** `owut` forwards no custom repositories, and the ASU
  server's `repository_allow_list` is empty by default, which denies everything.
- **`build` packages a directory; it does not build one.** Build your CSS first. owfeed compiles `.po`
  catalogues, because LuCI ignores `.po` silently. It runs no build for you and refuses to package
  sources.
- **No SDK builds, now or later.** Build with [owlab](https://github.com/owfeed/owlab),
  `openwrt/gh-action-sdk` or your own SDK call, and leave the result in `dist/<arch>/`. See
  [the artifact contract](docs/artifact-contract.md).

Not implemented yet, though the schema accepts them so that using one is a clear error:
`retention:`, `overrides:`, `build.changed-only`, `version-from: git-describe`, and the `s3` and
`rsync` publish targets. `github-pages` is the only target that works. Also absent: SBOM, key
rotation commands, and reusing an already-published package instead of re-signing it.

## Why not `openwrt/gh-action-sdk`?

It compiles; owfeed publishes. Most third-party packages need only the second half.

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

Compiled C needs the SDK, and owfeed packages what it produced. A LuCI theme, a LuCI app, a script
or a static binary needs no compiling.

## Someone else's CI, my feed

A feed carrying other people's packages never hands them its key. Authors build and sign in their own
CI and publish a signed release. The feed pulls it and verifies the author's signature against a
pinned key.

```sh
owfeed sign                       # in the author's CI, with the author's key
owfeed doctor --require-origin    # every package says which repository it is from
```

- By default the feed signs each package too. apk signature blocks add up, so the installed file
  carries **both** signatures.
- `signing.sign-packages: false` makes the feed sign only the index, where a router takes its trust
  from anyway. Install, upgrade and remove by name work the same. `apk add ./file.apk` and LuCI's
  Upload Package stop working; both already need `--allow-untrusted` for OpenWrt's own packages.

### If you are the author

Your repository has an ipk line and never builds an index; the feed signs that with its own key. You
publish a release plus a signed inventory of it:

```sh
owfeed plan                       # what the build would produce, offline
owfeed check                      # on a pull request: build, sign, index, doctor, throwaway keys
owfeed --frozen-lock build
owfeed release --repo "$GITHUB_REPOSITORY" --tag "$GITHUB_REF_NAME"
```

- **`plan`** prints the version, the architectures and every file path without building. `--json` is
  versioned, so a later job can read it instead of globbing `dist/`. It answers earliest with
  `version-from: tag` (`tag:v` strips the `v`).
- **`check`** generates a throwaway key of each kind and discards them after the run. Use it on pull
  requests: a signing secret in a job that runs a fork's code is a secret the fork can aim at. It
  needs no `signing:` block.
- **`release`** writes `manifest.txt` with each file's size and hash and signs it with usign, which a
  router verifies with nothing installed. The manifest names its repository, and readers check it: a
  signature proves who wrote it, not what it is about.
- Release assets are flat, and an apk filename has no architecture. Where names collide, `release`
  appends the architecture; a noarch package keeps the filename installers look it up by.

[The feed's CONTRIBUTING](https://github.com/owfeed/owfeed-packages/blob/main/CONTRIBUTING.md) walks
through both sides. A live feed built this way:
[owfeed/owfeed-packages](https://github.com/owfeed/owfeed-packages), a LuCI theme and a static Go
binary across 20 architectures. Its pull requests build, sign, index and check with a throwaway key.

## Docs

- [Examples](docs/examples.md): a LuCI theme, a two-package feed, a compiled binary.
  *([Русский](docs/examples_ru.md))*
- [Design summary](docs/DESIGN.md): why the tool exists and the decisions behind it.
  *([Полный документ](docs/DESIGN_ru.md), по-русски.)*
- [Verified package manager behaviour](docs/apk-behaviour.md): what apk and opkg do, measured, with
  reproductions.

## License

Apache-2.0.
