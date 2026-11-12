# Security

How to report a vulnerability in owfeed, what owfeed protects and what it does not, and how to
verify a release binary. For feed maintainers and anyone auditing the tool.

- **Report privately** through a [private advisory](https://github.com/owfeed/owfeed/security/advisories/new).
- The signing key never enters a build job.
- A feed's key is trusted for every package name, and apk cannot revoke it.
- Release binaries are checked against a build attestation. A checksum from the same release is not
  a check.

## Reporting

Report every vulnerability through a
[private advisory](https://github.com/owfeed/owfeed/security/advisories/new). Please do not open a
public issue for one, least of all for anything that would let someone publish under a feed's key.

## What owfeed protects, and what it does not

owfeed is a build and publish tool, not a runtime. Its security value is in what it refuses to
publish and in what it can prove about what it did publish.

| Claim | Kind |
|---|---|
| [The signing key never enters a build job](#the-signing-key-never-enters-a-build-job) | protection |
| [A feed's key is trusted for every package name](#a-feeds-key-is-trusted-for-every-package-name) | limit |
| [apk has no revocation](#apk-has-no-revocation) | limit |

### The signing key never enters a build job

The reusable workflow splits build from publish. Whatever a feed's own scripts do happens before
the key exists. For a feed carrying other people's packages, those scripts arrive by pull request.

`owfeed publish` gates the upload, and there is no flag to skip it.

### A feed's key is trusted for every package name

A key in `/etc/apk/keys` validates an index claiming *any* name, not only yours. A compromised feed
can offer a higher version of `dropbear` or `base-files` and win the resolution.

This is a property of apk, not of owfeed, and no amount of care here changes it. For this reason
the README section [Do you need a feed at all?](README.md#do-you-need-a-feed-at-all) talks people
out of running a feed where signed release artifacts would do.

### apk has no revocation

No CRL, no expiry, no signal that a key is dead.

- A compromised feed key plus control of the feed's URL is a full compromise of every subscriber.
- A device that is offline when you find out has no recovery path.
- owfeed makes rotation cheap enough to do. APKv3 matches keys by identity, so keys coexist. A
  keyring package carries a new key to routers already installed.
- owfeed does not claim revocation exists.

## Supply chain

- Release binaries carry [build provenance
  attestations](https://docs.github.com/en/actions/security-for-github-actions/using-artifact-attestations).
- `owfeed/setup` verifies a binary against the attestation from this repository's release workflow.
  It does this before the binary is executed or put on `PATH`, and refuses to install it otherwise.
- `owfeed/setup` passes `--signer-workflow`, not only `--repo`.
- Releases are immutable: the tag is locked to a commit and assets cannot be replaced.
- Every action used here is pinned by commit. Dependabot proposes updates as diffs.

<details>
<summary>Why <code>--signer-workflow</code> and not only <code>--repo</code></summary>

Checking the repository alone would accept an attestation produced by any workflow in it holding
`attestations: write`. A single merged pull request adding a workflow would then be enough to mint
a valid attestation for arbitrary bytes.

</details>

## Verifying a download by hand

Download the binary first: the `gh release download` command is in [Install](README.md#install). Then
run:

```sh
gh attestation verify owfeed-linux-amd64 -R owfeed/owfeed \
  --signer-workflow owfeed/owfeed/.github/workflows/release.yml
```

A `SHA256SUMS` file is published too. On its own it is not a check: the same host serves it, from
the same release, as the binary it describes. Whoever can replace one can replace the other.
