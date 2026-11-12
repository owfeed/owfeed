# What is built in owfeed, and what is not

*How much of owfeed's side of the ecosystem exists, as of 2026-07-28. For anyone deciding whether
to rely on a part of it. [ECOSYSTEM.md](ECOSYSTEM.md) says where the boundaries between owlab,
owfeed and owfeed-packages run and why.*

- [Working, and verified](#working-and-verified-rather-than-assumed): ten paths, each checked by
  running it.
- [Built but not yet exercised](#built-but-not-yet-exercised-in-anger): three, waiting for a first
  real use.
- [Not built](#not-built-and-why): an author's in-package EC signature, and a consumer job inside
  `feed.yml`.
- [Known contradictions](#known-contradictions): one shared release key.

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
| `owfeed releases` against owlab | Both answer 25.12→25.12.5/apk and 24.10→24.10.8/ipk today; the nightly job ran green on a runner, and reports a disagreement when either answer is perturbed |
| `feed.yml` in `dry-run` mode | owfeed-packages ran it end to end: build with no key, throwaway keys, sign, index, check-tree, doctor, smoke on both lines, a tree digest, publish job skipped |
| `feed.yml` publishing | owfeed-packages publishes through it now. Signed with the `feed` environment's own keys, gated, deployed, and the live feed still serves three packages on both lines |
| A called job's `environment:` resolves against the **caller** | Probed: an environment variable set to a different value in each repository returned the caller's, and the caller's environment-scoped signing secrets reached the called job at their real lengths |
| The report contract, consumed | luci-theme-footstrap installs the published theme BY NAME out of the signed index and asserts its pages render — the whole path, from a source tree to a router, checked by CI rather than by hand |

## Built but not yet exercised in anger

| What | State | What is still unproven |
|---|---|---|
| `owfeed release --sign-also` | works, and is wired into luci-theme-footstrap's release job | That job only runs on a tag, and no tag has been cut since. The first real proof arrives with the next theme release. Its verify step will demand `install.sh.sig` and fail loudly if it is missing. |
| The intake funnel | answers correctly when run by hand | No third party has used it yet, so the first genuine request is still the first genuine test. |
| `feed.yml`'s `published` and `page-url` outputs | both now have a value on every publish | Nothing reads them yet, which is the part still untested. |

## Not built, and why

### An in-package EC signature from a package author

`owfeed sign` no longer needs a feed config, so the tooling is ready. Nobody uses it yet: it needs
a new key and a new repository secret, which is the maintainer's call.

Until then, the claim in owfeed-packages' [`CONTRIBUTING.md`](https://github.com/owfeed/owfeed-packages/blob/main/CONTRIBUTING.md#i-build-in-my-own-ci-and-want-my-package-in-this-feed) that an author's signature is not stripped is true of the design
and demonstrated by nothing. That claim says the package a router installs carries its author's
signature.

### A consumer job inside `feed.yml` itself

The claim is now proved, but from the package side: luci-theme-footstrap installs the published
theme by name and asserts its pages render.

That does not cover a feed's own pull request. The tree being built there is not published yet.
Proving it works means serving `out/` over HTTP from the runner and pointing owlab at it. `{host}`
in a `--feed` URL exists for exactly that.

Open question: does it belong here as a `consumer-test:` input, or in each feed's own workflow? That
is a question about a public interface, and there is one consumer of it so far. Contracts belong to
whoever has more than one.

## Known contradictions

One key, `vizzletf-release.pub`, covers two repositories, while `keys/README.md` asks for one key
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
