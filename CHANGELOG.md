# Changelog

Dates are when the tag was cut. Anything not listed is documentation or tests.

## v0.5.1 — 2026-08-31

- **Added:** `install-if` on a package. apk installs such a package by itself once every
  entry is satisfied, which is the one thing `depends` cannot express: a translation
  catalogue depends on its application, so installing or upgrading the APPLICATION pulls
  in no catalogue, and a router that had the language quietly loses it on the upgrade
  that splits catalogues out. Measured on luci-theme-footstrap 0.14.3 -> 0.14.4 — every
  `.lmo` left with the old package and nothing named the new one. apk wants at least two
  entries with one pinned by `=`; write that pin `{version}` and it expands to the version
  being built, so a release does not hand-edit it. OpenWrt's buildroot never passes this
  field to `mkpkg`, so a feed built here can express something an in-tree package cannot.
  opkg has no conditional equivalent — `Recommends:` is executed but unconditional — which
  `docs/examples.md` says rather than leaving the asymmetry to be discovered.

## v0.5.0 — 2026-07-30

The author's side of the ecosystem gets the shape the feed's side has had since
`feed.yml`. Five repositories were hand-rolling it, at 190 to 630 lines each, with
`owfeed release --repo` typed out in four of them.

- **Added:** `owfeed check` — build, sign, index and `doctor --require-origin`, against
  an EC key and a usign key generated for the run and discarded. It is the whole of what
  CI can verify before a tag, with no secret in scope, and it replaces the same
  two-`keygen`-and-four-commands block in every repository that publishes packages.
  It needs no `signing:` block, which removes the reason one appeared in repositories
  that publish no feed at all: `index` refuses to run without a usign key, so authors
  were declaring one pointing at a variable holding nothing. It leaves nothing behind —
  everything it produces is signed by a key that stops existing, so it works in a
  temporary directory and `dist/` keeps only what was staged into it.

- **Added:** `owfeed plan` — what a build would produce, offline, before it produces it.
  Filenames come from the functions `build` calls rather than a format string, including
  the rename opkg forces where the architecture apk calls `noarch` is `all`. `--json` is
  versioned (`owfeed-plan 1`) for a later job to read. An unresolvable version is
  reported rather than fatal: `version-from: file:./dist/VERSION` names a file the build
  script writes, and refusing to answer would make the command useless exactly when it
  is most wanted.

- **Added:** `version-from: tag`, optionally with a prefix to strip (`tag:v`). Replaces
  the eight lines of shell every package repository wrote to turn `GITHUB_REF` into a
  version, and lets `plan` answer before anything is staged. `GITHUB_REF` is read rather
  than `GITHUB_REF_NAME`, which on a branch or a pull request is still set and would
  produce a version out of a branch name. The tag is otherwise untouched: a version apk
  cannot parse is already reported by `ValidateVersion` with a position and a hint.

- **Added:** `.github/workflows/package.yml` — the author-side sibling of `feed.yml`,
  and the release half only. It signs, writes the signed manifest, verifies every
  signature against the public key committed in the repository, publishes as a draft and
  flips it, then fetches the manifest back through `latest/download` to prove it
  resolves. Building and asserting on a real router stay with the caller, because a
  called workflow is one job to its caller and nothing can sit between its build and its
  release — which is where the owlab verification has to go if a tag is not to publish
  bytes no router has installed.

- **Added:** `doctor` reads the payload. **OWF212** — JSON in `acl.d` or `menu.d` that
  does not parse: rpcd and LuCI both skip the file without a word, so the ACL is granted
  to nobody or the menu entry never appears. **OWF213** — shell in the payload that does
  not parse, `/etc/uci-defaults/*` above all, which runs once at first boot and is
  deleted whether it worked or not, so the package's registration never happens and the
  evidence removes itself.

- **Fixed:** OWF701 fired on every repository that publishes release assets rather than
  a feed. It gated on the README mentioning `feed.url`, which for those repositories is
  their own project page and appears in every badge and link — and its advice was to
  paste in a snippet naming URLs they do not serve, so following it documented a 404 to
  silence a finding about 404s. It now gates on the line that configures the repository,
  which carries the feed's name and appears for no other reason.

## v0.4.5 — 2026-07-29

- **Fixed:** the install instructions claimed a stock image cannot fetch over HTTPS until
  `ca-bundle` and `libustream-mbedtls` are installed. Both ship in the stock 25.12 and
  24.10 images — checked against the manifests for x86/64 and for ath79 generic, which is
  where a small-flash image would have dropped them. The line stays, because a custom or
  stripped build can lack them and then nothing else can be fetched at all, but it no
  longer says something untrue about the common case.

## v0.4.4 — 2026-07-29

- **Fixed:** `doctor` failed a feed on its own keyring package. OWF304 requires a
  signature by a pinned author key, and the keyring package is the one package a feed
  publishes about itself — signed by the feed, because there is no third-party author to
  sign it. Indexing already exempted it; the check did not, so every publish of a feed
  with `author-keys` and `keyring-package` both on failed on 35 architectures of a
  package that was correct.

## v0.4.3 — 2026-07-29

- **Fixed:** `signing.keyring-package` was unreachable for the feeds it exists for.
  `index` refused to build the package unless `owfeed.lock` already recorded a keyring
  version, and deriving that entry needs the feed's private signing key — which a feed
  keeps in CI secrets, not on the maintainer's laptop. Every publish failed with an
  instruction the maintainer could not carry out.

  The first publication now counts as `1.1-r1` and logs that it did. A later key that
  disagrees with the lockfile is still refused, because counting a rotation needs the
  state the lockfile holds — and whoever rotated has the new key in hand by then.

## v0.4.2 — 2026-07-29

- **Added:** `owfeed index` writes `subscribe.sh` at the feed root — one script that
  subscribes a router to the feed on either release line. Until now a person had to know
  whether their release used apk or opkg before they could paste anything, and the two
  sets of commands share nothing: the repository line names an index file on one and a
  directory on the other, the key is matched by identity on one and by filename on the
  other, and the signature schemes differ.

  The script asks the router instead of the reader. It is generated from the same config
  that laid the tree out, so it cannot document a URL the feed does not serve — the drift
  this package exists to prevent. Re-running it replaces the feed's line rather than
  appending a second one, and a release line the feed does not publish for stops it with
  a message naming the lines that do exist, instead of guessing a URL that would 404 at
  `apk update` and read as a broken feed.

  Verified on 25.12.5 and 24.10.8: subscribe, install by name from the feed, run twice
  with no duplication, and a router reporting 23.05.5 refused with exit 1.

- **Fixed:** the opkg snippet omitted the `keep.d` step. `customfeeds.conf` is a conffile
  and survives sysupgrade; the key file under `/etc/opkg/keys/` is shipped by no package
  and does not. Losing only the key leaves a feed configured and unverifiable, which
  reads as the feed being broken.

## v0.4.1 — 2026-07-29

- **Fixed:** the keyring package was never produced by the feeds that need it most. It
  was attached to `owfeed build`, and a feed carrying other people's work runs no build
  at all — its packages arrive already built, and its pipeline is fetch, sign, index. So
  the one feature whose whole purpose is reaching subscribers of such a feed was
  unreachable there. It is built during `index` now, which every feed runs.

  It is also signed with the feed's own key regardless of `sign-packages`. That setting
  exists so a feed does not put its signature inside a file somebody else built; this
  file is the feed's own, and mkndx refuses to index a package carrying no signature at
  all. For the same reason it is exempt from `author-keys`: holding the feed's own
  package to a rule about third-party authors would exclude the one package a feed
  publishes about itself.

  Found by looking at the published feed rather than at the test that passed — the
  package simply was not there.

## v0.4.0 — 2026-07-29

- **Added:** `signing.keyring-package` builds the package that carries a feed's public
  key to routers, and `signing.also-sign` signs the index with more than one key. They
  are one mechanism, and shipping either alone leaves a feed that still cannot rotate.

  apk has no revocation, so the only thing owfeed can offer is making rotation cheap
  enough that a feed actually does it. A key installed by hand can only be replaced by
  hand, on every subscriber's router — which in practice means never.

  The measured trap is that a keyring package cannot start a rotation on its own. It is
  fetched from the feed, and the feed's index is signed by the key being rotated to, so
  a subscriber holding only the old key cannot reach it: the upgrade returns `UNTRUSTED
  signature`, reports success, and changes nothing. With both keys signing the index the
  same router upgrades normally and receives the new key; the old one can then be
  dropped from the config and from `/etc/apk/keys`. The whole sequence is reproduced in
  `docs/apk-behaviour.md`.

  The keyring version lives in `owfeed.lock`, not in the key. A version derived from the
  key changes when the key changes and sorts below the previous one about half the time,
  and a keyring package whose version went backwards is one no router installs — at
  exactly the moment rotation has to work. So it counts, the count is recorded, and a
  signing key that disagrees with the record stops the build rather than republishing
  the new key under the old version.

  The payload is named for the key's own identity, so a second key is added beside the
  first rather than overwriting it. apk matches keys by identity and ignores filenames,
  which is what lets both sit there during the window.

- **Changed:** `signing.keyring-package: true` is no longer an error. It was declared,
  defaulted to on, and rejected when asked for explicitly, for as long as it was
  unimplemented.

## v0.3.3 — 2026-07-29

- **Added:** `signing.author-keys` — a directory of pinned author public keys. Every
  package must then carry a signature by one of them, and `owfeed doctor
  --author-keys DIR` checks a tree by hand.

  For a feed that carries other people's work this is the only claim that survives the
  feed itself. The index proves that this feed published a file; an author signature
  proves who built it, and can be checked by somebody who does not trust the feed at
  all. The pinned copy is the source of truth: a key travelling beside a release proves
  nothing on its own, since whoever replaced the package would replace the key too.

- **Changed:** a package with no author signature is left out of the index rather than
  failing the whole tree. One author who has not adopted signing yet used to cost every
  other author their publication.

  It is left out loudly. The name goes to stdout as it happens, the count into the
  summary, and a record beside the tree. That record keeps OWF406 working — a package
  that vanished because a build half-failed is still an error, where one deliberately
  excluded is OWF407, a warning naming the subscribers who have silently stopped
  receiving updates. Tolerating the second without recording it would have meant giving
  up the first, and a feed publishing one package of three while reporting itself
  healthy is a failure this ecosystem has already had.

## v0.3.2 — 2026-07-29

- **Fixed:** the releases badge named only the apk line. Badge data was collected in
  the apk branch of `owfeed index` alone, so a package the feed also serves through
  opkg showed `25.12` when it was on both, and a package served *only* on 24.10 got
  no badge at all. Both formats now contribute, and the lines are ordered newest
  first — numerically per component, because "9.10" beats "25.12" as a string.

## v0.3.1 — 2026-07-29

- **Added:** `owfeed index` writes `badge/<package>.json` and
  `badge/<package>-releases.json` into the feed root, in the shape shields.io renders
  as an endpoint badge. A maintainer whose package a feed carries can now show it,
  with a version that updates when the feed does.

  The obvious approach does not work: shields rejects a JSONPath filter over the
  existing `index.json` with "query not supported", so selecting a package by name is
  impossible and selecting it by position is wrong, because nothing fixes the order of
  an index. Publishing one file per package makes the URL name the package instead.

  The numbers are read back from the index that was just built rather than taken from
  the config, so a badge cannot claim a version the feed is not serving.

- **Changed:** the install snippet keeps the key and the repository across a firmware
  upgrade through `/lib/upgrade/keep.d/<feed>` rather than by appending to
  `/etc/sysupgrade.conf`. sysupgrade reads both -- `list_static_conffiles` feeds `find`
  from the two together -- but a file of the feed's own is idempotent where `>>` is
  not: re-running the install rewrites it instead of adding a second copy of both
  paths, and removing the feed is `rm` rather than editing a config by hand.

  Verified on 25.12.5: with the keep.d file present `sysupgrade --create-backup`
  contains the key and the repository list; without it, neither; and running the
  snippet twice leaves two lines rather than four.

## v0.3.0 — 2026-07-29

- **Added:** `signing.sign-packages: false` now does what it says. The field was in the
  config and in the published schema, but nothing read it -- `owfeed sign` signed every
  package regardless. A feed that carries other people's work can now decline to put its
  own signature inside their artifacts and sign only the index.

  `owfeed sign` reports that it skipped them rather than doing it silently, and `--key`
  still overrides: an author signing their own packages before a release is exactly the
  case that must not be skipped. `owfeed index` passes `mkndx --allow-untrusted`, without
  which mkndx refuses an unsigned package with `UNTRUSTED signature` and exit 99 and
  writes no index at all. `owfeed doctor` no longer reports OWF303 in this mode, where the
  absence of the feed's signature is the point rather than a defect.

  Measured on OpenWrt 25.12.5 against a feed built this way, driving LuCI's own
  `package-manager-call` rather than apk directly: the package appears in the Software
  list, and Install, Upgrade and Remove all return 0. `/etc/apk/world` carries the bare
  name with no content-hash pin, so upgrades resolve. Trust comes from the signed index;
  the signature inside a package is never consulted on that path. What still fails is
  `apk add ./file.apk` and LuCI's Upload Package -- there is no index to check against,
  and `--allow-untrusted` is dropped by `package-manager-call`. Both already fail that way
  for OpenWrt's own packages, which are unsigned individually. The reproduction is in
  `docs/apk-behaviour.md`.

## v0.2.1 — 2026-07-29

- **Fixed:** `owfeed version` said `dev` when the tool was installed the way the
  README tells people to install it. `go install owfeed.org/owfeed/cmd/owfeed@latest`
  stamps nothing, so the most common installation reported no version at all and
  every bug report from it would have arrived without one. The module system had
  already recorded what it resolved; the binary now reads that back. A stamped
  release build still wins, and a pseudo-version is still refused -- `0.0.0-2026...`
  looks like a release number and is not one. owlab has answered this correctly
  since v0.2.0; this brings the two into line.

## v0.2.0 — 2026-07-29

- **owfeed lives at `github.com/owfeed/owfeed`, and its module path is
  `owfeed.org/owfeed`.** `go install github.com/VizzleTF/owfeed/...` stops
  working; `go install owfeed.org/owfeed/cmd/owfeed@latest` replaces it. The path
  names a host rather than a forge so that this is the last move that breaks
  anyone's install -- Go module paths have no redirect, and neither does `uses:`.
- **Release attestations now name `owfeed/owfeed`.** An attestation records the
  repository that produced it, so every binary released before this one fails
  verification against the new name, and `owfeed/setup` at v0.2.0 refuses it. A
  feed pinning `feed.yml@v0.1.7` therefore has to move: that tag installs a setup
  action pointed at a repository name the attestation service no longer answers
  for.
- The reusable workflow and the setup action move with it; the `uses:` line in
  every feed that calls them has to change, because Actions follows no redirect.


- **Fixed:** `owfeed/setup` used twice in one job failed the second time. `gh
  release download` refuses to overwrite a file it downloaded a minute earlier,
  and the action had no reason to download it again -- it now returns early when
  the requested version is already installed, and clobbers when it is not. Found
  in owlab's copy of the same forty lines, fixed in both, which is the cost of the
  duplication those forty lines are worth.

## v0.1.7 — 2026-07-28

Both of these were found by the first real run of `feed.yml`, which is the point
of having had one.

- **Fixed:** under `dry-run`, the throwaway keys were exported inside the signing
  step and shredded at the end of it, so every later stage ran without them. A
  feed's config names those variables — `signing.usign-key: env:OWFEED_USIGN_KEY`
  — so `owfeed doctor` exited 4 on a key that was empty by the time it looked.
  They go into the job's environment now, masked, and still never leave the runner.
- **Fixed:** the `check` job declares `permissions: contents: read`. A called
  workflow is validated as a whole, so a caller has to grant the ceiling the
  publish job asks for even when only the check job will run; narrowing here is
  what keeps that grant off everything that actually executes. The usage comment
  says so, because a check job declaring `pages: write` otherwise reads as a
  mistake.

## v0.1.6 — 2026-07-28

- `owfeed releases` reports what the download server publishes per line and which
  package format that line takes. It exists to be compared against: owlab answers
  the same question from the same server and neither reads the other, which is
  deliberate, but until now owfeed had no way to state its answer outside a feed
  repository — `arch.LatestPoint` is internal and `owfeed lock` needs a config.
  Duplication that nothing can observe is just drift with a delay.
- A nightly cross-check against `owlab releases --all --json`, which files an
  issue when the two disagree about a newest point release or about apk vs opkg.
  One open issue, not one per night.
- `feed.yml` takes `dry-run: true` and runs the whole pipeline against throwaway
  keys without an environment and without publishing, so a feed's pull-request
  check and its publication are one workflow in two modes. Two hand-written
  pipelines mean a green pull request proves nothing about the one that publishes;
  the divergence between them was exactly what nothing tested.
- `feed.yml` reports `digest` (sha256 over the published tree), `published` and
  `page-url`, so a caller can do something after publication rather than guess.
- `feed.yml`'s publish job reads `OWFEED_SIGN_KEY` and `OWFEED_USIGN_KEY` from the
  environment it declares, falling back to the `sign-key` / `usign-key` inputs.
  Taking them only as `workflow_call` secrets meant the calling workflow had to
  read them, and a calling job has no environment — so the workflow whose purpose
  is keeping a signing key inside a protected environment was forcing every feed
  that adopted it to hold that key at repository scope instead. Existing callers
  are unaffected: the input still wins when no environment secret is set.

## v0.1.5 — 2026-07-28

- `owfeed sign` no longer requires a feed config. It takes `--key`, and resolves the
  apk tool from the newest point release when no lockfile names one, so an author
  can put the in-package signature on their own build without writing an
  `owfeed.yml` and a 36-architecture lockfile to sign one file. A feed still signs
  from its config, which is the pinned answer.
- `owfeed release --sign-also FILE` signs a file published beside the packages —
  an installer script, most often — with the same key, without adding it to the
  manifest. The manifest is an inventory of packages a feed ingests, and a feed
  has no use for an installer; the signature is for a person checking what they
  are about to run as root, out of band. Repeatable. This is what lets a package
  repository drop its own usign loop entirely.
- `build: sdk` is refused by design rather than reported as unimplemented, and
  the error points at owlab and at `docs/artifact-contract.md`. owfeed packages;
  it does not compile.
- A JSON Schema for `owfeed.yml`, generated from `internal/config` and published
  at `https://owfeed.org/schema/v1.json`. It describes shape, not the validator's
  rules, and marks the keys that parse but are refused. `owfeed init` gets its
  `$schema` modeline back once that URL answers.
- **Docs:** `docs/ECOSYSTEM.md` states the boundary between owlab and owfeed, and
  `docs/artifact-contract.md` and `docs/manifest-format.md` write down the two
  formats that cross it. Both had more than one consumer and no specification.

## v0.1.4 — 2026-07-28

- A feed that carries only other people's packages and builds nothing itself is now
  expressible: an empty `packages:` list is legal, and `owfeed build` says there is
  nothing to do instead of failing the config.
- **Fixed:** the doc-drift check demanded the install snippet's `<package>`
  placeholder appear verbatim in a README, which made good documentation the finding
  for any feed with no packages of its own.
- **Fixed:** `owfeed release` dated the manifest from `SOURCE_DATE_EPOCH`. A feed sets
  that to a constant so a package's identity depends only on its payload, and the
  result was a signed document dating a 2026 release to 2023. `OWFEED_RELEASE_DATE`
  overrides it for anyone who needs a fixed one.

## v0.1.3 — 2026-07-28

- **Fixed:** `owfeed build` demanded the index-signing key from any config with an
  ipk line, at load time. An author publishing packages for someone else's feed has
  an ipk line and never builds an index, so this made that whole shape unusable. The
  requirement moved to `owfeed index`, where the key is read.
- **Fixed:** `owfeed release` produced one asset name per package regardless of
  architecture. Release assets are flat and an apk's filename carries no
  architecture — in a feed the architecture is the directory — so a package built for
  twenty architectures published twenty files with one name and nineteen of them did
  not exist. Colliding names now carry their architecture; a noarch package keeps the
  name an installer already on a router looks it up by.

## v0.1.2 — 2026-07-28

- `owfeed/setup` prints what it verified. `gh attestation verify` writes to stderr
  and a composite step swallows it, so the only evidence the check ran was the
  absence of a failure.
- Every action pinned by commit, with Dependabot keeping the pins current.
- Immutable releases enabled: the tag is locked to a commit and assets cannot be
  replaced.
- **Fixed:** the documented reusable-workflow usage would have failed for anyone whose
  default `GITHUB_TOKEN` is read-only — which is the recommended setting. Permissions
  can only be narrowed down a call chain, never widened, so the caller has to grant
  them.

## v0.1.1 — 2026-07-28

- `owfeed/setup` passes `--signer-workflow`, not only `--repo`. Checking the
  repository alone accepts an attestation from any workflow in it holding
  `attestations: write`, so one merged pull request adding a workflow would have been
  enough to mint a valid attestation for arbitrary bytes.
- **New check OWF514:** `.nojekyll` is fetched from the live site.
  `actions/upload-pages-artifact` excludes dotfiles from v4, and `.nojekyll` is what
  stops Pages running Jekyll over a tree of binaries — so a feed can be correct
  everywhere owfeed can inspect and be deployed without the file that keeps it
  correct. The reusable workflow sets `include-hidden-files: true`.
- `actions/deploy-pages` has needed `actions: read` since v4, and a `permissions:`
  block silently withholds it.

## v0.1.0 — 2026-07-28

First tagged release. Builds, signs, indexes and publishes apk feeds for OpenWrt
25.12 and later, and opkg feeds for 24.10 and earlier, from one configuration.

- `build`, `sign`, `index`, `publish`, `doctor`, `verify`, `smoke`, `release`,
  `keygen`, `lock`, `init`, `install-snippet`.
- Both release lines from one config, with the signature scheme each package manager
  actually verifies: EC prime256v1 for apk, usign for opkg.
- `smoke` installs the built feed on a real OpenWrt image and fails if apk asks for
  `--allow-untrusted`.
- `verify` checks the published feed over its documented URL: redirects apk will not
  follow, cache skew, a version republished with different contents.
- `owfeed.lock` makes the architecture list a reviewable diff.
- Release binaries carry build provenance attestations; `owfeed/setup` refuses to
  install one that does not verify.
