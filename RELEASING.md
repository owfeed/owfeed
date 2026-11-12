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
| `.github/workflows/feed.yml` and `.github/workflows/package.yml` | every `uses: owfeed/owfeed/setup@vX.Y.Z` line, one per job, and the tags in the usage comments at the top, which name the workflow's own ref as well |
| `README.md` and `README_ru.md` | the download example and the action snippets |
| `site/index.html` and `site/ru/index.html` | the download example on the landing page |
| `site/cookbook/release/index.html` and `site/cookbook/release/ru/index.html` | the `setup@vX.Y.Z` lines in the cookbook |
| `site/reference/index.html` and `site/reference/ru/index.html` | the owfeed version in the lead line, "Current for … owfeed vX.Y.Z" |
| `CHANGELOG.md` | the `## Unreleased` heading becomes the version and the date |

The site says which versions it is current for. A page whose example pins a release nobody can
install is the failure this project is least able to see.

## Steps

1. Count the workflow references: `grep -rc 'v<previous>' .github/workflows/`.
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

## Why exact tags rather than a floating `v1`

`actions/checkout@v4` is a moving target by design. For a checkout that is a reasonable trade.

This tool signs feeds. A consumer who pins `feed.yml@vX.Y.Z` should get exactly the `setup` that
was reviewed alongside it. Otherwise the code which fetches and verifies the signing tool can change
under a pin that looks exact.

The cost is this file. That is the right side of the trade.
