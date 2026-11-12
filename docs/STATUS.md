# What is built in owfeed, and what is not

*How much of owfeed's side of the ecosystem exists, as of 2026-11-12. For anyone deciding whether
to rely on a part of it. [ECOSYSTEM.md](ECOSYSTEM.md) says where the boundaries between owlab,
owfeed and owfeed-packages run and why.*

- [Working, and verified](#working-and-verified-rather-than-assumed): twelve paths, each checked by
  running it.
- [Built but not yet exercised](#built-but-not-yet-exercised-in-anger): two, waiting for a first
  real use.
- [Not built](#not-built-and-why): a consumer job inside `feed.yml`.
- [Known contradictions](#known-contradictions): one shared release key, now over four repositories.

The other repositories keep their own status files:

- **owlab**: [`docs/STATUS.md`](https://github.com/owfeed/owlab/blob/main/docs/STATUS.md)
- **owfeed-packages**: [`STATUS.md`](https://github.com/owfeed/owfeed-packages/blob/main/STATUS.md)

<details>
<summary>Why each repository keeps its own status file</summary>

This file used to cover all three repositories, and it went stale twice. Both times it was on a
fact belonging to another repository, never on one of its own. Nothing in owfeed's CI touches
owlab, so nothing here could notice when owlab shipped a feature this file called unimplemented.
Each repository now keeps its own.

The boundaries are meant to outlive any particular week. A status file is a snapshot and will go
stale. Kept beside the code that changes it, going stale becomes a diff somebody has to write
rather than something that happens quietly.

</details>

## Working, and verified rather than assumed

Everything here was checked by running it, not by reading the code: against live routers, the
published feed, or released binaries.

| | Evidence |
|---|---|
| `owlab build` → `owfeed release` compose | Built luci-theme-footstrap through the SDK, produced a signed 7-field manifest, verified the signature back |
| `dist/<arch>/` agreed by three implementations | owlab's `archDirs`, owfeed's `buildIPK`, the feed's `fetch.sh` — apk to `noarch/`, ipk to `all/` |
| Install from a signed feed by name | `owlab test --feed` on 25.12: package installs, LuCI page renders. With the key removed the router reports it as not existing at all |
| Third-party intake | Run against a real release: signature verifies, and the six-field manifest is correctly refused as the wrong format |
| Schema published and generated | `owfeed.org/schema/v1.json` is byte-identical to the checked-in copy; the drift test fails when a field is added and the annotation guard fails when a key is renamed |
| `owfeed releases` against owlab | On 2026-11-12 `owfeed releases` answers 25.12→25.12.6/apk and 24.10→24.10.8/ipk, matching downloads.openwrt.org. The nightly cross-check ran green that day, and reports a disagreement when either answer is perturbed |
| `feed.yml` in `dry-run` mode | owfeed-packages ran it end to end: build with no key, throwaway keys, sign, index, check-tree, doctor, smoke on both lines, a tree digest, publish job skipped |
| `feed.yml` publishing | owfeed-packages publishes through it. Signed with the `feed` environment's own keys, gated, deployed. The last Publish run on `main` passed on 2026-11-12, and the live 24.10 `x86_64` index lists 11 packages |
| A called job's `environment:` resolves against the **caller** | Probed: an environment variable set to a different value in each repository returned the caller's, and the caller's environment-scoped signing secrets reached the called job at their real lengths |
| The report contract, consumed | luci-theme-footstrap installed the published theme BY NAME out of the signed index and asserted its pages render — the whole path, from a source tree to a router, checked by CI rather than by hand. On 2026-11-12 its workflows no longer do this: `tools/ci-install.sh` installs the built `.apk` from `dist/` with `--no-network` |
| `owfeed release --sign-also` | Every luci-theme-footstrap release from v0.11.7 (2026-07-29) to v0.14.20 (2026-11-02) carries `install.sh.sig`. Its release job passes `sign-also: install.sh` to `package.yml` |
| An in-package EC signature from a package author | luci-theme-footstrap's release job passes `sign-key: OWFEED_AUTHOR_KEY` to `package.yml`. owfeed-packages sets `author-keys: ./keys` and `sign-packages: false`, with one EC public half pinned per package in [`keys/`](https://github.com/owfeed/owfeed-packages/blob/main/keys/README.md). An unsigned package is dropped from the index (OWF407) |

## Built but not yet exercised in anger

| What | State | What is still unproven |
|---|---|---|
| The intake funnel | answers correctly when run by hand | No package request has come through it yet. Its five runs, all on issues that were not package requests, were skipped. A third party did add a package by pull request instead: [owfeed-packages#66](https://github.com/owfeed/owfeed-packages/pull/66), `luci-app-podkop-bot`, merged 2026-09-11. |
| `feed.yml`'s `published` and `page-url` outputs | both now have a value on every publish | Nothing reads them yet, which is the part still untested. |

## Not built, and why

### A consumer job inside `feed.yml` itself

The claim was proved from the package side: luci-theme-footstrap installed the published theme by
name and asserted its pages render. Its workflows no longer do this (see
[the report contract row](#working-and-verified-rather-than-assumed)).

That never covered a feed's own pull request. The tree being built there is not published yet.
Proving it works means serving `out/` over HTTP from the runner and pointing owlab at it. `{host}`
in a `--feed` URL exists for exactly that.

Open question: does it belong here as a `consumer-test:` input, or in each feed's own workflow? That
is a question about a public interface, and there is one consumer of it so far. Contracts belong to
whoever has more than one.

## Known contradictions

One release key,
[`vizzletf-release.pub`](https://github.com/owfeed/owfeed-packages/blob/main/keys/vizzletf-release.pub),
covers four repositories: `luci-theme-footstrap`, `luci-app-footstrap-cmd`, `luci-app-footstrap-files`
and `luci-app-gitbackup`. owfeed-packages'
[`keys/README.md`](https://github.com/owfeed/owfeed-packages/blob/main/keys/README.md) asks for one key
per upstream repository.

- `owfeed` checks the manifest's `repo` line, so a manifest cannot be lifted between them. The
  shared key is tolerable.
- What separate keys would buy is blast radius, not correctness.
- The doctrine now says this out loud instead of being quietly contradicted by the table beneath it.

## Where to look

- [ECOSYSTEM.md](ECOSYSTEM.md): the boundaries, the invariants, the contracts.
- [artifact-contract.md](artifact-contract.md) · [manifest-format.md](manifest-format.md): the two
  formats that cross those boundaries.
- [The cookbook](https://owfeed.org/cookbook/): the same path written for somebody who has not read
  any of the above *([по-русски](https://owfeed.org/cookbook/ru/))*.
