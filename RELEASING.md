# Cutting a release

Steps for a maintainer tagging a new owfeed version. The repository references its own release
tag, so you move those references before the tag exists. The tag then contains its own correct
self-reference.

- Every file in the table below names the current version. Move all of them, then commit and tag.
- Do not count the references from memory: grep for the previous version.
- When you are done, the previous version matches nothing outside `CHANGELOG.md`.

## Files whose version moves

| File | What moves |
|---|---|
| `.github/workflows/feed.yml` and `.github/workflows/package.yml` | every `uses: owfeed/owfeed/setup@vX.Y.Z` line, one per job, and the usage comments at the top that name the workflow's own ref: `.github/workflows/feed.yml@<version>` (two lines in `feed.yml`) and `.github/workflows/package.yml@<version>` (one line in `package.yml`) |
| `README.md` and `README_ru.md` | the download example and the action snippets |
| `site/index.html` and `site/ru/index.html` | the download example on the landing page |
| `site/cookbook/release/index.html` and `site/cookbook/release/ru/index.html` | the `setup@vX.Y.Z` lines in the cookbook |
| `site/reference/index.html` and `site/reference/ru/index.html` | the owfeed version in the lead line, "Current for … owfeed vX.Y.Z" |
| `CHANGELOG.md` | the `## Unreleased` heading becomes the version and the date |

No other file type pins the version. A grep of the whole repository for the current version, run
when this was written, matched only the files in this table: nothing in `schema/`, `setup/`,
`site/llms.txt` or any `.json` or `.txt` file. So the step-4 grep covers every pin.

The site says which versions it is current for. A page whose example pins a release nobody can
install is the failure this project is least able to see.

## Before you start

- The `CHANGELOG.md` has a `## Unreleased` section with the changes going out.
- `go test ./...` passes. The release workflow runs it first and publishes nothing if it fails.
- `THIRD_PARTY_LICENSES` is current. `TestThirdPartyLicenses` checks it against what `cmd/owfeed`
  links, and the workflow ships the file with the binaries.

## Steps

1. Count the workflow references: `grep -rc 'v<previous>' .github/workflows/`. Write down the count
   per file. Expect 5 in `feed.yml` (three `setup@` lines, two usage comments) and 2 in `package.yml`
   (one of each). After step 3, `grep -rc 'vX.Y.Z' .github/workflows/` must show the same counts.
2. List the site pins: `grep -rn 'setup@v\|release download v' site`. This grep does not match the
   lead line of the reference pages; step 4 does.
3. Edit every file in the table.
4. Check the whole repository:

   ```sh
   grep -rn 'v<previous>' --include='*.md' --include='*.yml' --include='*.html' .
   ```

   It should match nothing outside `CHANGELOG.md`. There the old version is history rather than a
   pin.
5. Commit.
6. Tag: `git tag -a vX.Y.Z`.
7. Push the tag: `git push origin vX.Y.Z`.

## What the release workflow does

- It runs the tests before it publishes anything.
- It builds for linux and darwin on amd64 and arm64.
- It attests each binary.

If the attestation step failed, nobody can install that release. `owfeed/setup` refuses to install
a binary that does not verify against its attestation. This is the intended outcome, not a bug to
work around.

## If the release workflow fails

The workflow runs its steps in this order: `go test ./...`, build, attest, then
`gh release create`. Nothing is published before the last step.

- **A step before `gh release create` failed.** No release exists for the tag. Fix the cause. A fix in
  the code is a new commit, which the tag does not point at yet.
- **The cause is outside the code** (a network error). Re-run the failed workflow run. The build is
  reproducible: it sets `SOURCE_DATE_EPOCH` from the tagged commit, so the same tag gives the same
  bytes.
- **The release is already published and something is wrong with it.** Cut a new version. Releases
  are immutable: the tag is locked to its commit and assets cannot be replaced (see
  [SECURITY.md](SECURITY.md#supply-chain)).

## Why exact tags rather than a floating `v1`

`actions/checkout@v4` is a moving target by design. For a checkout that is a reasonable trade.

This tool signs feeds. A consumer who pins `feed.yml@vX.Y.Z` should get exactly the `setup` that
was reviewed alongside it. Otherwise the code which fetches and verifies the signing tool can change
under a pin that looks exact.

The cost is this file. That is the right side of the trade.
