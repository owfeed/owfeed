# `manifest.txt` — the release manifest format

*A signed inventory of what a release contains. For anyone writing or reading one: `owfeed release`
writes it, `owfeed verify-artifact` and any feed ingesting that release read it.*

- Line-oriented, space-separated, parsed positionally. The first line is `owfeed-manifest 1`.
- Each `pkg` line has seven tokens: `pkg`, then name, format, release asset name, size, sha256 and
  architecture.
- Verify `manifest.txt.sig` against a pinned key before you parse anything.
- Check `repo` and `tag`, then each file's size and hash.

Without a manifest a consumer has to trust whatever the hosting service says a release holds. A
signature over an API response is not available, and an API response is not a signature.

Owned by owfeed and versioned with it.

| Section | What it holds |
|---|---|
| [Format](#format) | the lines, in order |
| [`pkg`](#pkg) | the seven tokens of a package line |
| [Versioning](#versioning) | what bumps the format version |
| [Reader obligations](#reader-obligations) | four checks every reader runs |
| [Signatures](#signatures) | which files carry a usign signature |
| [Non-goals](#non-goals) | what the manifest is not |

## Format

```
owfeed-manifest 1
repo <owner>/<name>
tag <tag>
version <tag without a leading v>
date <RFC3339, UTC>
notes <sha256> <filename>
pkg <name> <format> <file> <size> <sha256> <arch>
pkg <name> <format> <file> <size> <sha256> <arch>
```

- Line-oriented, space-separated, parsed positionally.
- The first line is the format identifier.
- `notes` appears only when the release carries notes.
- `pkg` repeats, sorted by package name and then architecture, so the file is stable across
  rebuilds.

### `pkg`

Seven tokens, fixed order:

| # | Field | Example | Meaning |
|---|---|---|---|
| 1 | literal | `pkg` | |
| 2 | `name` | `luci-theme-footstrap` | package name, no version |
| 3 | `format` | `apk` / `ipk` | |
| 4 | `file` | `luci-theme-footstrap-0.11.6-r1_x86_64.apk` | the **release asset** name, with `_<arch>` where flat assets would collide |
| 5 | `size` | `48213` | bytes — lets a consumer check free space before downloading |
| 6 | `sha256` | `e2e7bd…` | |
| 7 | `arch` | `noarch` / `all` / `x86_64` | the directory name from the [artifact contract](artifact-contract.md) |

Field 4 is the name in the release; field 7 says which directory it came from. A consumer has both
and never has to guess either. The name a feed publishes the package under is neither of them: it
is the canonical name the index derives.

<details>
<summary>Why the architecture is field seven and not field three</summary>

The architecture is field seven and not field three for a reason worth preserving. Readers already
deployed parse the first six positionally. `$1=="pkg" && $2==name && $3==ext {print $4, $5, $6}`
is running on routers that cannot be fixed remotely.

So the first six fields keep the shape those readers expect. The new field goes after them, where
it costs nothing.

</details>

## Versioning

The first line is `<name> <integer>`.

| Change | Version |
|---|---|
| Appending a new trailing field | stays: readers ignore surplus trailing tokens, so this is compatible |
| Changing the meaning or order of an existing field | `owfeed-manifest 2` |
| Adding a field a reader must understand to be correct | `owfeed-manifest 2` |

* A reader **must** refuse an unknown `<name>`.
* A reader **must** refuse an `<integer>` higher than the one it understands. Refusing is the point
  of the line: misreading a shape is worse than not reading it.
* Readers parse positionally and **ignore surplus trailing tokens**.

## Reader obligations

These are not suggestions; each one has a failure it exists to prevent.

1. **Verify the signature before parsing.** The detached signature is `manifest.txt.sig`. Verify it
   against a key the reader pinned in advance, never a key fetched alongside the manifest.

   *Why:* every value in the manifest steers a download. Parsing first means acting on text nobody
   has vouched for.

2. **Check the first line.** A manifest in a shape you do not know must be refused by name, not
   discovered field by field.

   *Why:* a reader that skips this will accept a foreign six-field manifest and read an empty
   seventh field. It then fails somewhere far away with an error about a missing directory.

3. **Check `repo` and `tag` against what you expected.**

   *Why:* a signature proves who wrote something, never what it is about. One key often signs
   releases for several repositories. Without these two checks, a manifest lifted from another of
   the same author's releases verifies perfectly as this one.

4. **Check each downloaded file's size and hash against its `pkg` line.**

   *Why:* the manifest's signature already covers both. This check is what makes the signature mean
   anything about the bytes on disk.

## Signatures

`owfeed release` writes a detached usign signature beside the manifest and beside every package.

- The manifest's signature is the one a manifest-aware reader needs.
- The per-package signatures stay because a consumer already in the field may be fetching a single
  asset and know nothing about manifests.
- The signature comment is `<repo> <tag>`, which is what a verifying tool shows a human.

## Non-goals

* **Not a package index.** It describes one release of one repository. `owfeed index` builds a
  feed's index from the artifact tree, and that index has nothing to do with this file.
* **Not a trust root.** It says what a release contains, once you have already decided the signing
  key is one you trust. That decision is made elsewhere, deliberately, by a person.
* **Not reproducible-build metadata.** `owfeed release` records the release timestamp and
  deliberately does not honour `SOURCE_DATE_EPOCH`. `OWFEED_RELEASE_DATE` overrides it for tests.

  *Why:* a build pins `SOURCE_DATE_EPOCH` to a constant for reproducibility. Honouring it here would
  put that constant's date in a signed document describing a release made years later.
