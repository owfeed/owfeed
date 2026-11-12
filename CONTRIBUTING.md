# Contributing

The rules a pull request to owfeed is reviewed against. For anyone changing the code, a check or the
documentation.

MUST and MUST NOT mean a reviewer blocks the change.

## The short version

```sh
go test ./...
gofmt -l .
golangci-lint run ./...
```

All three run in CI on every pull request.

## What you need

- **Go 1.26.9**, the version in `go.mod`. CI reads it from there.
- **`bash` and `sh`** on `PATH`. A few tests run the workflow and setup scripts.
- **For the integration tests only:** network access and Docker. They are the `*_integration_test.go`
  files.

## Unit tests and integration tests

`go test ./...` alone runs the unit tests. It needs no Docker.

The integration tests skip themselves unless you opt in, as CI does:

```sh
OWFEED_INTEGRATION=1 go test ./...
```

They pull real container images and a real apk toolchain: the failures worth catching are the ones
that only appear against the real thing.

- They download an OpenWrt SDK and router images, and talk to downloads.openwrt.org.
- They need Docker. The apk tests need it only off linux/amd64.
- `OWFEED_TEST_CACHE=<dir>` keeps the extracted toolchain between runs.
- The usign interop test runs only when `OWFEED_USIGN` names a usign binary.

## Pull request checklist

- [ ] `go test ./...`, `gofmt -l .` and `golangci-lint run ./...` are clean.
- [ ] A change users can see has an entry under `## Unreleased` in `CHANGELOG.md`. Add the heading if
      it is not there. Documentation and tests need no entry.
- [ ] A claim about what apk or opkg does is measured, with the command and the result, in
      [docs/apk-behaviour.md](docs/apk-behaviour.md) (C-1).
- [ ] A new check has the next free ID in its range and tests in both directions (C-5, C-7).
- [ ] No key is committed (C-9).

## Find it fast

| Rule | Area |
|---|---|
| [C-1](#evidence-checks-and-errors) Every claim is measured, not reasoned | evidence |
| [C-2](#evidence-checks-and-errors) A check that cannot run counts as failed | evidence |
| [C-3](#evidence-checks-and-errors) Errors say four things | evidence |
| [C-4](#evidence-checks-and-errors) Comments explain why, not what | evidence |
| [C-5](#adding-a-check) A check gets a stable ID in the existing ranges | checks |
| [C-6](#adding-a-check) A check's doc comment says what breaks in the field | checks |
| [C-7](#adding-a-check) A check is tested in both directions | checks |
| [C-8](#things-that-need-care) Documentation describes packaging, not what packages are for | care |
| [C-9](#things-that-need-care) Never commit a key | care |
| [C-10](#things-that-need-care) Do not rename a released artifact unchecked | care |
| [C-11](#things-that-need-care) Config changes are compatibility changes | care |

## Evidence, checks and errors

**C-1. Every claim MUST be measured, not reasoned.** [`docs/apk-behaviour.md`](docs/apk-behaviour.md) is the record: what
apk and opkg do, established by running them, with the command and the result. A change that
asserts new behaviour belongs there with evidence beside it.

*Why:* a plausible mechanism that nobody observed is a guess, and this project has been wrong about
several.

**C-2. A check that cannot run MUST count as failed.** Not skipped, not warned about.

*Why:* otherwise a green report can mean "nothing was looked at", which is worse than no report.

**C-3. Errors MUST say four things:** what failed, the exact command that failed, how to fix it,
and the check ID.

*Why:* a message that only reports that something is wrong has done half the job.

**C-4. Comments MUST explain why, not what.** The code says what.

*Why:* the reason a line exists is usually a bug it prevents, often one that shipped somewhere.
That reason cannot be recovered by reading the code.

## Adding a check

**C-5. A check MUST get a stable ID in the existing ranges.**

| Range | Area |
|---|---|
| 1xx | config |
| 2xx | metadata |
| 3xx | signing |
| 4xx | index |
| 5xx | transport |
| 6xx | on-device |
| 7xx | documentation |

The IDs are string literals beside each check, in `internal/doctor/*.go` and
`internal/verify/verify.go`. To see the ones already taken:

```sh
grep -rhoE 'OWF[0-9]{3}' internal cmd --include='*.go' | sort -u
```

**C-6. Its doc comment MUST say what breaks in the field when it is not caught.**

*Why:* that tells the next person whether the check is worth its false positives.

**C-7. Tests MUST cover both directions:** the thing the check catches, and a healthy feed it must
stay quiet about.

*Why:* a check that fires on a correct feed is worse than no check. It teaches people to ignore the
output.

## Things that need care

**C-8. Documentation MUST describe packaging, not what packages are for.** Examples name a generic
package, such as `luci-app-example` or `example-daemon`. They describe a mechanism: two packages
from one repository, a conflict, a translation catalogue. They do not describe what any particular
software is used for. They do not link to a third-party project as an endorsement.

*Why:* a tool that stays neutral is judged as a tool.

<details>
<summary>The reason behind C-8</summary>

This is a build and distribution tool, in the same category as `tar` or a web server. Its
documentation stays on that side of the line.

The reason is narrow and worth stating. A tool that stays neutral is judged as a tool. One whose
documentation reads as advocacy for a particular use invites the question of whether the tool exists
for that use. Nothing is gained by answering it.

</details>

**C-9. You MUST NOT commit a key.** `.gitignore` covers `*.pem`, `*.key` and `*.sec`.
`owfeed keygen` refuses to write inside a git working tree without `--force`.

*Why:* a published signing key cannot be revoked.

**C-10. You MUST NOT change what a released artifact is named without checking who reads it.**

*Why:* an installer already on a router looks its asset up by name and cannot be fixed remotely.

**C-11. You MUST treat a config change as a compatibility change.**

*Why:* an unknown key is an error by design, so a renamed field breaks every existing `owfeed.yml`
at load time.

## Releasing

See [RELEASING.md](RELEASING.md).
