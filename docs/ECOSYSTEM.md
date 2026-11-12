# The ecosystem: owlab, owfeed, owfeed-packages

*This page explains the boundary between four repositories that share one lifecycle: what crosses it,
and what each one promises never to do. It is for anyone changing one of them, or wiring them together in CI.*

- **No code dependency.** The tools share no Go module and never will. They meet only at file formats
  and CLI surfaces.
- **The boundary is whose key is in the environment.** owlab holds no keys. owfeed holds the author's key
  in an author's repository and the feed's key in a feed's.
- **One key at a time.** A workflow that can see an author's key and a feed's key at once is a bug.
- **Trust is added in one place.** `owfeed verify-artifact` checks a release against `keys/*.pub` in the feed.
- **Dependencies go up the stack only, at the workflow level.** owlab uses owfeed nowhere.

How much of this exists today is tracked per repository, beside the code that changes it:
[owfeed](STATUS.md),
[owlab](https://github.com/owfeed/owlab/blob/main/docs/STATUS.md),
[owfeed-packages](https://github.com/owfeed/owfeed-packages/blob/main/STATUS.md).

<details>
<summary>Why the contracts live here, and why status does not</summary>

Contracts belong to whoever has more than one consumer, and every contract below has owfeed on one
side of it.

One shared status file was tried first. It went stale twice, both times on a fact belonging to a
repository whose CI never reads this one.

File formats and CLI surfaces are the only things two independently released binaries can agree on
without having to be released together.

</details>

## Find it fast

| You want to know | Section |
|---|---|
| what each repository is for, in one line | [One line each](#one-line-each) |
| how the boundary is drawn | [Two axes](#two-axes) |
| what each repository never does | [Invariants](#invariants) |
| who owns compiling, testing, release tables, setup, signing | [Contested capabilities](#contested-capabilities) |
| which files and CLIs cross the boundary | [Contracts](#contracts) |
| which key lives where | [Keys](#keys) |
| why `owfeed.yml` and `owlab.yaml` stay separate | [Config](#config) |
| how authors, feeds, maintainers and routers use the tools | [CI/CD composition](#cicd-composition) |
| which repository may depend on which | [Dogfooding](#dogfooding) |
| why the licences differ | [Licences](#licences) |
| how actions are pinned | [Action pinning](#action-pinning) |

## One line each

> **owlab** — does the package work.
> **owfeed** — can anyone trust these bytes.
> **owfeed-packages** — who exactly do we trust, and what follows from that.
> **luci-theme-footstrap** — what it looks like when all three are used as intended.

## Two axes

The boundary follows trust: whose key is in this environment. Lifecycle stage organises the
documentation and reading order, and decides nothing about the boundary.

- **Trust is the normative axis.** Invariants and CI gates are written against it. "There is no key in
  this job" is mechanically checkable; "this is the release stage" is not.
- **Lifecycle is the teaching axis.** Documentation and reading order follow it.

|  | **T0 — no keys** | **T1 — author's key** | **T2 — feed's key** |
|---|---|---|---|
| **L1 inner loop** | `owlab up/sync/shell/exec/install`, `owlab build`, `owlab test` | — | — |
| **L2 release** (author's repo) | `owlab build` in CI, `owlab/action` on PRs | `owfeed sign` (EC, in-package), `owfeed release` (usign, manifest) | — |
| **L3 distribution** (the feed) | ingest scripts, `owfeed verify-artifact` (reads `keys/*.pub`) | — | `owfeed sign`, `owfeed index`, `owfeed publish` |
| **L4 consumption** | — | optional: a subscriber adds the author's key | `/etc/apk/keys/<feed>.pem` |

Three things follow from the table, and they are the reason for it:

- **T0 spans two stages.** owlab is the only inhabitant of the column with no keys, in development and
  in CI alike. That is its definition, not a gap in it.
- **owfeed sits in T1 and T2, in different repositories.** The boundary is whose key is in the
  environment, not which binary is running. This is the reason for the gate in [Keys](#keys): a workflow
  that can see both keys at once is a bug.
- **The T1→T2 transition exists in exactly one place:** `owfeed verify-artifact` against `keys/*.pub`.
  The whole third-party intake funnel is a procedure for adding a line to that directory.

```
        T0 ─ no keys ─────────┬─ T1 ─ author's key ┬─ T2 ─ feed's key ── consumer
                              │                    │
 owlab up/sync/test/build ────┤                    │
   "does the package work"    │                    │
                              │                    │
              owfeed sign ────┤                    │
              owfeed release ─┘ manifest.txt ──────┤
                "who signed this"                  │
                                                   │
        owfeed-packages: verify-artifact ──────────┤
                "we decided to trust this key"     │
                                                   │
                        owfeed sign/index/publish ─┘
                          "the feed vouches for the channel"
```

<details>
<summary>Why one axis alone does not work</summary>

Along the lifecycle, owfeed is smeared across two stages. `owfeed release` is the author's side;
`owfeed sign` and `owfeed index` are the feed's. Both are the same binary. Any rule of the form "this
stage belongs to this repository" produces an exception immediately.

</details>

## Invariants

**owlab never:**

- stores, reads or creates a cryptographic key;
- generates a feed index or publishes anything outward;
- verifies a package signature. It installs with `--allow-untrusted`, because trust is not what it is
  checking;
- depends on owfeed, in `go.mod` or in a workflow.

**owfeed never:**

- compiles sources. This is a decision, not a gap: see [Compilation](#compilation-belongs-to-owlab);
- logs into LuCI, renders a page, or asserts anything about how a package behaves;
- uploads. `owfeed publish` is a gate, and `actions/deploy-pages` does the upload;
- takes a key into a job that runs code from a pull request;
- depends on owlab in `go.mod`. For the workflow level, see [Dogfooding](#dogfooding).

**owfeed-packages never:**

- builds or rebuilds a package;
- accepts a package whose key is not already in `keys/` at ingest time;
- lands a diff touching `keys/`, `tools/`, `.github/` or `owfeed.yml` without a person;
- holds a write token for somebody else's repository, or hands its own key to anyone.

**luci-theme-footstrap never:**

- reimplements what owlab and owfeed already do. It is a worked example, not a showcase for hand-rolled CI.

### None of them takes a position on what a package is for

owlab starts routers and runs assertions. owfeed packages, signs and indexes. Neither inspects what a
package does, and neither has an opinion about it. They are build and distribution plumbing, in the
same category as `tar`, `make` or a web server.

The documentation stays on that side of the line deliberately. Examples name a generic package and
describe a mechanism, never a purpose. No third-party project is presented as a worked example.

<details>
<summary>Why neutrality is written down as a boundary</summary>

This is a boundary rather than an omission, written down for the same reason the others are. A tool
that stays neutral is judged as a tool. Documentation that reads as advocacy for one use invites the
question of whether the tool exists for that use, and nothing is gained by having to answer it.

The repositories were separated for engineering reasons: a build tool, a publishing tool, and a feed
that decides whose keys it pins. That separation holds on this axis too.

</details>

### Where responsibility sits

A feed's signature states provenance and channel: these bytes are what the feed published, from the
author whose key it pinned. It is not a statement about a package's contents, safety, quality or
lawfulness.

The feed does not review code, assess security or judge legality in any jurisdiction. Responsibility
for those rests with the package's author. The author's repository is recorded in the package metadata
and shown by `apk info` on the router.

[`owfeed-packages/SECURITY.md`](https://github.com/owfeed/owfeed-packages/blob/main/SECURITY.md) is the operative statement, with the notice-and-action procedure that
goes with it.

## Contested capabilities

Each capability below could plausibly belong to more than one repository. Each subsection names the
owner first.

### Compilation belongs to owlab

> **owfeed packages, it does not compile.**

`owlab build` compiles through the OpenWrt SDK. `owfeed` does not. `build: sdk` is **rejected by
design** rather than unimplemented.

The price: an author who does not want Docker or owlab has no build path inside owfeed. They can use
`openwrt/gh-action-sdk` or their own SDK invocation. The only obligation is to leave the bytes in
`dist/<arch>/`. The artifact contract is the interface.

<details>
<summary>Why owfeed does not compile, strongest reason first</summary>

1. **The feature has no consumer.** The flagship feed keeps `packages:` deliberately empty, and its
   doctrine forbids building. Implementing `build: sdk` means writing owfeed's hardest component for a
   scenario its own reference feed prohibits.
2. **It drags the whole target table with it.** owlab already carries one: architectures, distro
   tables, `arch → feed path`, SDK tags. Duplicating that is the most expensive duplication available,
   because the table moves with every OpenWrt release.
3. **Compilation is a verification concern, not a distribution one.** `luci.mk` minifies JS and CSS on
   the way into the package. "Works unminified, breaks minified" is invisible until somebody builds the
   package. That is an argument from testing.
4. **`mkpkg` stays and does not contradict this.** Staging a finished tree is packaging, not compiling.

</details>

### `owlab test` vs `owfeed smoke`

> **owlab asserts about the package. owfeed asserts about the channel.**

| | `owlab test` | `owfeed smoke` |
|---|---|---|
| Question | does the package work on this release | does the feed install without `--allow-untrusted` |
| Install | a local file, `--allow-untrusted` | from the feed, through the index, trust required |
| Assertions | `http`/`service`/`file`/`package`/`uci`/`exec`, LuCI login, traceback markers | `apk add <name>` returned, and `--allow-untrusted` was never said |
| Matrix | N routers × M releases × fixtures | one architecture × release line |
| Report | JSON `owlab.test/v1` | exit code |
| Keys | none | the feed's public half |

Two invariants follow:

- **`owfeed smoke` never asserts HTTP against a LuCI page.** The moment it does, it becomes a second
  owlab and has to carry fixtures, ssh and a login.
- **`owlab test` never asserts anything about signatures.** The moment it does, it needs keys, and the
  T0 column collapses.

owfeed keeps its own Docker runner. `owfeed smoke` is a `docker run` against `openwrt/rootfs:*` with no
fixtures, no ssh and no LuCI session.

One hole remains: smoke proves the channel works and says nothing about what arrived through it. A
feed's PR pipeline closes it with an `owlab/action` consumer job. That is a soft dependency at the
workflow level, described under [Dogfooding](#dogfooding).

<details>
<summary>Why owfeed does not reuse owlab's runner</summary>

Sharing owlab's runner would give owfeed's core gate a hard dependency on a development tool. A plain
`docker run` is cheaper than the dependency. It also has to work for somebody who has never heard of
owlab.

</details>

### Release and architecture tables

Three sources answer different questions. They overlap in one place: the rule that 25.12 and later
means apk, and resolving the newest point release in a line.

| Question | Owner | Contract |
|---|---|---|
| which architectures does release X have | **owfeed** | `owfeed.lock` — committed, `--frozen-lock` |
| how do I build or run architecture X | **owlab** | internal target table; outward, the `--arch` flag |
| which releases exist, and which are apk | **owlab** | `owlab releases --all --json` |
| the same question, owfeed's own answer | **owfeed** | `owfeed releases --json` |

Both tools answer the last question from the same server, without reading each other. The duplication
is deliberate: a shared library between them is the code-level dependency both designs refuse.

The drift it invites is not deliberate. The defence is `.github/workflows/crosscheck.yml` in this
repository. Nightly, it runs both and files an issue when they disagree about a newest point release
or about apk vs opkg. The duplication stays; it becomes observable.

The two tools name the same verdict differently:

- **owlab** reports the package *manager* (`apk` / `opkg`). It is deciding what to run on a router.
- **owfeed** reports the package *format* (`apk` / `ipk`). It is deciding what extension to write.

The cross-check maps between them. Neither repository renames its own field, because each name is the
right one where it lives.

`owfeed.lock` is the ecosystem's fact file. owlab may read one lying beside it. owlab never writes one.

### The setup action and attestation verification

owlab and owfeed each ship their own `setup` action. Each verifies its release binary with
`gh attestation verify --signer-workflow` before the binary reaches `PATH`. These are two copies of the
same forty lines, and they stay two copies.

What is shared is the convention, not the code:

- the same input names: `version`, `verify`, `token`;
- the same behaviour on `version: latest`: a warning, not a refusal;
- the same verification shape: `--signer-workflow <owner>/<repo>/.github/workflows/release.yml`,
  never `--repo` alone;
- the same log output, naming what was verified.

<details>
<summary>Why the duplication is correct here</summary>

Factoring the two copies into a fourth repository would add a hop to the trust chain of the code whose
only job is checking the trust chain. One compromise would then reach both tools. Static,
security-critical, rarely-changing code is exactly where duplication is correct.

</details>

### Signing, indexing and publishing

> **Everything involving a cryptographic signature, index generation, or the publish gate
> belongs to owfeed. Neither owlab, nor a feed's ingest scripts, nor an author's CI
> reimplements any of it.**

## Contracts

There are five contracts. Each is a file or a CLI surface; none is a shared Go package.

| Contract | Spec | Written by | Read by |
|---|---|---|---|
| artifact tree | [artifact-contract.md](artifact-contract.md) | `owlab build`, `owfeed build`, any SDK invocation | `owfeed sign`, `owfeed release`, `owfeed index`, feed ingest |
| release manifest | [manifest-format.md](manifest-format.md) | `owfeed release` | `owfeed verify-artifact`, feed ingest |
| test report | `owlab.test/v1` (owlab) | `owlab test --json`, `owlab/action` | an author's CI today; a feed-side consumer gate once it exists |
| keys | [below](#keys) | — | — |
| config | [below](#config) | — | — |

### The test report has no feed-side consumer yet

An author's CI already fails on the report. On the feed side, the tooling exists, and only sequencing
is missing: no feed's pipeline runs it yet.

- `owlab install --feed`, `owlab test --feed` and the `feed:` input on `owlab/action` all take a feed
  URL and its public key.
- A `{host}` placeholder in that URL resolves to whatever address the router's tier reaches the runner
  at, which is what used to make the job unwritable.

Until a feed runs it, `owfeed smoke` proves the channel works, and nothing proves what arrived through it.

### Keys

> **A workflow that can see an author's key and a feed's key at the same time is a bug.**
> No legitimate scenario produces one.

| Stage | Key | Scheme | Env / file | Where the secret lives |
|---|---|---|---|---|
| `owlab build` / `test` | — | — | — | nowhere; installs `--allow-untrusted` |
| author: `owfeed sign` | author's package key | EC prime256v1, SEC1 PEM | `OWFEED_SIGN_KEY` | author's repo secret |
| author: `owfeed release` | author's release key | usign ed25519 | `OWFEED_RELEASE_KEY` | author's repo secret |
| feed: `owfeed verify-artifact` | author's **public** half | usign pub | `keys/<repo>.pub`, pinned by key id | committed to git |
| feed: `owfeed sign` | feed's package key | EC prime256v1 | `OWFEED_SIGN_KEY` | `feed` environment |
| feed: `owfeed index` (opkg line) | feed's index key | usign ed25519 | `OWFEED_USIGN_KEY` | `feed` environment |
| consumer | feed's public half | via `owfeed install-snippet` | `/etc/apk/keys/<feed>.pem` | on the router |

**`OWFEED_SIGN_KEY` has two meanings.** In an author's repository it is the author's package key. In a
feed's, it is the feed's package key. That is fine because the repositories are different. It is
written down here because the gate above depends on it.

**A feed's keys live in an environment, not in repository secrets.** The reusable workflow can still
reach them. A called job that declares `environment:` resolves that environment against the
**caller's** repository. The caller's environment-scoped secrets are readable inside it. This keeps a
feed's signing key out of reach of every other workflow in the feed's own repository.

<details>
<summary>How the environment behaviour was established</summary>

That is measured rather than inferred. The documentation states that the environment's secret wins
over a passed-in one, and never says whose environment.

</details>

**An author's signature is never stripped.** apk signature blocks are additive: the package a router
installs carries the author's signature and the feed's. A subscriber who adds the author's key verifies
the author directly. A subscriber who trusts only the feed is unaffected.

### Config

> **`owfeed.yml` describes a feed. A package repository carries only `owlab.yaml`.**

They are separate files, and `owfeed.yml` does not grow an `owlab:` section.

| File | What it is | How it is edited |
|---|---|---|
| `owlab.yaml` | a developer's machine ergonomics | often, reviewed by nobody |
| `owfeed.yml` | release policy | every diff should be read by a person |

**An author needs no feed config.** Neither of the two commands an author runs needs one.

- `owfeed release` never did.
- `owfeed sign` now takes `--key`. When no lockfile names an apk tool, it resolves one from the newest
  point release.

A feed still signs from its config. That is the pinned answer, and the one to prefer.

<details>
<summary>Why the files are not merged, and what <code>owfeed sign</code> used to need</summary>

Merging a file people edit without looking into a file people edit with review is a way to smuggle a
policy change in as a router addition. Beyond that, owfeed's validator rejects unknown blocks by
design. Permitting an `owlab:` section means whitelisting it, which is coupling the schemas.

`owfeed sign` used to need a feed config. It read the signing key from the config and the SDK release
from the lockfile beside it. Putting a signature on one file in `dist/noarch/` meant writing a feed
config and a 36-architecture lockfile first.

</details>

## CI/CD composition

Four roles use the tools. Each role is a composition of the pieces above.

### Author, own repository

- **PR CI is T0.** `owlab build` produces `dist/<arch>/`, and `owlab/action` asserts the package works.
  No key exists anywhere in the workflow, so fork PRs are safe by construction.
- **The release job is T1.** It is the only place the author's keys appear: `owfeed sign` for the
  in-package signature, `owfeed release` for the signed manifest, then the release upload.

### Feed operator

- **Ingest runs in a job with no key**, because the ingest scripts carry values contributed by pull request.
- **Signing, indexing and the publish gate** run in a separate job bound to the `feed` environment.
- **`owfeed publish` refuses on error-level findings** and has no override flag. The upload itself is
  `actions/deploy-pages`.

### Third-party maintainer

The intended funnel:

1. a request issue;
2. an automated intake check that runs `owfeed verify-artifact` against the claimed release *before*
   a person looks;
3. human review of the key addition;
4. one PR adding `upstream.sh` and the public key;
5. a scheduled bot for updates after that.

The key from the issue proves the release is internally coherent; it is not trust. Trust happens
exactly once, when the key is committed.

**All of it exists except enforcement on the last human step.** `CODEOWNERS` names `keys/`, but no
branch rule requires the review. The review becomes a mechanism on the day there is a second maintainer.

The automation cannot reach `keys/` anyway. The update job writes one `upstream.sh`.
`tools/land-updates.sh` refuses to push a branch whose diff against `main` names any other path.

<details>
<summary>Why no branch rule requires the review</summary>

A review requirement needs a reviewer. With one maintainer, the author of a pull request cannot
approve their own. The rule would block every key addition permanently rather than gate it.

</details>

Conformance tiers follow from **what the signature covers**, not from how hard the author tried:

| Tier | What the author publishes | What the signature covers | Automated updates |
|---|---|---|---|
| **A** | `owfeed release` — a signed `owfeed-manifest 1` | the whole inventory: sizes, hashes, repo, tag | yes |
| **B** | finished artifacts, each with a detached signature | each asset, but not the list of them | yes, while the set of containers is unchanged |
| **C** | unsigned binaries the feed packages itself | nothing but "it downloaded intact" | never |

Where an update is automatic, it is still refused in three cases:

- on a major version bump;
- on a diff touching anything but the pins;
- on the third update to one package inside a day.

The third rule guards against a run of releases rather than one. A stolen key signs each of them
perfectly and publishes faster than anyone reads the notifications.

### Consumer

**The feed is the default path.** An author's own release channel remains valid for routers that
cannot add a repository.

**Do not mix the two on one router.** On 25.12, `apk add ./file.apk` writes a pin into
`/etc/apk/world`. That pin survives sysupgrade and outlives the feed. A self-updater that finds the feed
configured is expected to refuse and point at `apk upgrade`.

## Dogfooding

> **Dependencies go up the stack only, and only at the workflow level. At the code level
> there are none.**

```
owlab            ← depends on nothing (bottom layer)
  ↑
owfeed           ← may use owlab/action in a test-only job
  ↑
owfeed-packages  ← owfeed (reusable workflow) + owlab/action (consumer test)
  ↑
package repos    ← owlab (build/test) + owfeed (sign/release)
```

**owlab uses owfeed nowhere**, including at the workflow level. owlab is the bottom layer, and a cycle
would mean neither could be released first.

owfeed may reach for owlab under three conditions:

1. **Only in a job that is not on the path to a released binary.** An owlab outage then reddens
   owfeed's CI and never produces a broken owfeed.
2. **Never in `go.mod`**, never in the reusable workflow other people call, never in `owfeed/setup`.
3. **Strictly one directional.**

### owfeed jobs that use owlab

| Job | State | What it does |
|---|---|---|
| `crosscheck.yml` | exists | installs owlab through its setup action and compares `owlab releases --all --json` against `owfeed releases --json` |
| test-feed consumer job | not written yet | builds a test feed, serves it locally, and uses `owlab/action` to prove a package from that feed installs and renders its page |

`crosscheck.yml` is scheduled rather than on push. Its failure files an issue and says nothing about
whether owfeed builds.

The consumer job would catch a class of bug `owfeed smoke` cannot catch by design. Smoke checks that
apk never asked for `--allow-untrusted`, and checks nothing about whether what installed works.

### owlab checks against the specification

owlab's CI does not use owfeed. Where owlab needs to confirm its `dist/<arch>/` output matches the
contract, it checks against the specification. The specification is a shared artifact; a binary is not.

## Licences

The licences differ, and the difference is a decision rather than drift.

| | Licence | Why this one |
|---|---|---|
| **owlab** | GPL-2.0-only | Copyleft, deliberately. owlab ships an overlay -- `rc.local`, `uci-defaults`, the fixture scripts -- inside every image it builds, so what it distributes is more than the binary somebody ran |
| **owfeed** | Apache-2.0 | The explicit patent grant is the reason. This is a signing and packaging tool; a permissive licence that says nothing about patents is a worse answer for code people are asked to run against their keys |
| **owfeed-packages** | MIT | The repository's own content is glue: workflows, three shell scripts and a directory of pinned versions. The packages it publishes are other people's, under their own terms -- see its LEGAL.md |
| **luci-theme-footstrap** | Apache-2.0 | Matches LuCI upstream, which is what a theme is built against and read alongside |

Two consequences of the difference are often misread:

- **A licence difference is not a coupling problem.** Nothing here links against anything else here.
  The tools meet at file formats and CLI surfaces, which is the whole point of the boundary above. GPL
  in owlab imposes nothing on a repository that only *calls* `owlab test` in CI. It imposes nothing on
  owfeed, which does not call it at all.
- **Unifying them would cost something and buy nothing.** Moving owlab off copyleft is a one-way door.
  Every version already distributed stays under the old terms, and the decision cannot be taken back
  for anyone who already has a copy. Tidiness is not a reason.

## Action pinning

| Reference | Pinned to | Bumped |
|---|---|---|
| internal `owfeed/*` | an exact tag | as part of the release procedure |
| external actions | a full commit SHA, with the version in a trailing comment | — |

<details>
<summary>Why the two are pinned differently</summary>

Our own tag is under our control, and the binary it installs is verified by attestation anyway. So the
tag is a convenience over a SHA rather than a trust decision. Somebody else's tag is moved by somebody else.

</details>
