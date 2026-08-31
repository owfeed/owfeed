# Examples

*[Русская версия](examples_ru.md)*

Five worked examples, smallest first. Each one is a directory you already have, a config, and four
commands. If the shape of a feed is not yet clear, [How it works](../README.md#how-it-works) is
twenty lines.

- [The simplest thing that works](#the-simplest-thing-that-works)
- [A LuCI theme: luci-theme-footstrap](#a-luci-theme-luci-theme-footstrap) — translations
- [A service and its LuCI app from one repository](#a-service-and-its-luci-app-from-one-repository) — conflicts,
  real dependencies
- [A compiled binary: a static Go daemon](#a-compiled-binary-a-static-go-daemon) — several architectures
- [Both release lines from one config](#both-release-lines-from-one-config) — apk and opkg

---

## The simplest thing that works

A directory of files, laid out the way it should install:

```
pkg/root/
  www/luci-static/demo/style.css
  etc/config/demo
```

```yaml
version: 1
feed:
  name: demofeed
  url: https://feed.example.org
publish:
  - target: github-pages
packages:
  - name: luci-app-demo
    build: mkpkg
    arch: noarch
    version: 1.0.0-r1
    files: ./pkg/root
    depends: [luci-base]
    conffiles: ["/etc/config/demo"]
```

```sh
owfeed lock --update
owfeed build && owfeed sign && owfeed index && owfeed doctor
```

---

## A LuCI theme: luci-theme-footstrap

[luci-theme-footstrap](https://github.com/VizzleTF/luci-theme-footstrap) is a noarch LuCI theme —
CSS, templates and translations, no compiled code. Exactly the case where the status quo asks for 35
SDK builds to produce the same bytes 35 times.

`owfeed build` packages a directory; it does not build one. So the work splits in two: your build
produces the rootfs, owfeed turns it into a signed feed.

### 1. Stage the rootfs

The source layout is LuCI's, and the mapping is `luci.mk`'s:

| in the repo | installs as |
|---|---|
| `htdocs/` | `/www` |
| `ucode/` | `/usr/share/ucode/luci` |
| `luasrc/` | `/usr/lib/lua/luci` |
| `root/` | `/` |
| `i18n/<lang>/*.po` | `/usr/lib/lua/luci/i18n/<name>.<lang>.lmo` |

Translations are not in the table because owfeed compiles those itself — see step 2.

```sh
#!/bin/sh
# stage.sh — produce dist/root, the directory owfeed packages.
set -e
SRC=luci-theme-footstrap
DIST=dist/root
rm -rf dist && mkdir -p "$DIST"

./"$SRC"/build-css.sh                       # minified CSS into htdocs/

mkdir -p "$DIST/www" "$DIST/usr/share/ucode/luci"
cp -a "$SRC"/htdocs/. "$DIST/www/"
cp -a "$SRC"/ucode/.  "$DIST/usr/share/ucode/luci/"
cp -a "$SRC"/root/.   "$DIST/"

git describe --tags --abbrev=0 | sed 's/^v//;s/$/-r1/' > dist/VERSION
```

No `po2lmo` call, and no need for one: it is a host tool built from `luci-base`, so requiring it
would put a C build of the LuCI feed in front of anyone packaging a theme. owfeed has its own
compiler, byte-identical to that tool's output.

Sources never go in the payload. Point `files:` at a source tree and owfeed refuses it by name —
`.po`, `.scss`, `node_modules`, `.DS_Store` — rather than shipping a package that installs cleanly
and is missing what the tree implied.

### 2. owfeed.yml

```yaml
version: 1

feed:
  name: footstrap
  url: https://feed.footstrap.dev
  title: Footstrap
  maintainer: "VizzleTF <vizzletf47@gmail.com>"
  license: Apache-2.0
  homepage: https://github.com/VizzleTF/luci-theme-footstrap

publish:
  - target: github-pages

packages:
  - name: luci-theme-footstrap
    build: mkpkg
    arch: noarch                          # never "all" — apk rejects it
    version-from: file:./dist/VERSION
    files: ./dist/root
    description: "A modern, fast LuCI theme."
    depends: [luci-base]
    conffiles: ["/etc/config/footstrap"]
    i18n:
      from: ./luci-theme-footstrap/i18n     # a directory of <lang>/*.po
      basename: footstrap-theme             # -> footstrap-theme.<lang>.lmo
```

Two entries are worth reading twice.

**`conffiles`.** The theme ships `/etc/config/footstrap`; leaving it undeclared means sysupgrade
replaces the user's settings with the package defaults on every firmware upgrade, silently.
`owfeed doctor` reports it as OWF207.

**`i18n.basename`.** It defaults to the `.po` file's own name, which is what `luci.mk` does — here
that would be `footstrap.<lang>.lmo`. Footstrap sets it to `footstrap-theme` instead, and the reason
is worth knowing before you pick your own. LuCI's loader globs `*.<lang>.lmo`, so any basename is
found. But this theme used to ship its translations through separate `luci-i18n-footstrap-<lang>`
packages, and a router upgrading from that release still owns `footstrap.ru.lmo`. Reusing the path
is a file conflict, and apk refuses the upgrade — the one it was supposed to deliver. If your
package never shipped a `luci-i18n-*` variant, the default is fine.

**`install-if`, when the translations are their own packages.** Splitting catalogues out the way
`luci.mk` does — one `luci-i18n-<app>-<lang>` each — leaves a gap nothing else in this file closes:
`depends` points from the catalogue to the application, so installing or upgrading the APPLICATION
pulls in no catalogue at all. A router that had the language then upgrades and quietly loses it,
which is what happened to footstrap between 0.14.3 and 0.14.4.

apk has a field for exactly this, and owfeed passes it through:

```yaml
  - name: luci-i18n-footstrap-ru
    build: mkpkg
    arch: noarch
    version-from: file:./dist/VERSION
    files: ./dist/i18n-ru
    depends: [luci-theme-footstrap]
    install-if: [ "luci-theme-footstrap={version}", "luci-i18n-base-ru" ]
```

Read it as "install this by yourself once the theme is at that version AND this router is already
Russian". apk wants at least two entries with one pinned by `=` (apk-package(5)); `{version}`
expands to the version being built, so a release never hand-edits the pin, and because the pin moves
the rule re-fires on every upgrade of the application.

The trigger is a package, not a setting: a package manager cannot read `uci luci.main.lang`, so
`luci-i18n-base-<lang>` — the LuCI base catalogue a translated router already carries — is what
"this router speaks that language" looks like from here.

**opkg has no conditional form of this.** `Recommends:` is parsed and acted on by OpenWrt's opkg,
but it is unconditional: it would put Russian on every 24.10 router. A postinst cannot install the
package either, opkg holding an exclusive lock for the whole run. So the ipk leg of a split-out
translation needs an installer script or a documented package name, and the apk leg carries the
behaviour on its own. That asymmetry is the format's, not owfeed's.

### 3. Build the feed

```sh
./stage.sh
owfeed lock --update
owfeed build && owfeed sign && owfeed index && owfeed doctor
```

The theme's `/etc/uci-defaults/30_luci-theme-footstrap` runs on install without any extra
configuration: owfeed wraps `post-install` the way `package-pack.mk` does, so `default_postinst`
applies uci-defaults and enables init scripts. A bare post-install script would install the files and
do none of that.

### 4. What your users run

```sh
owfeed install-snippet
```

Paste the output into the README verbatim. `doctor` checks it has not drifted.

---

## A service and its LuCI app from one repository

The common shape for anything with a settings page: a shell-script service and the LuCI app that
drives it, in one repository. Both are architecture-independent with an empty `Build/Compile`, so
neither needs a toolchain.

```yaml
version: 1

feed:
  name: netwatch
  url: https://feed.example.org
  title: netwatch
  maintainer: "You <you@example.org>"
  license: GPL-2.0-or-later
  homepage: https://example.org/netwatch

publish:
  - target: github-pages

packages:
  - name: netwatch
    build: mkpkg
    arch: noarch
    version-from: file:./VERSION
    files: ./staging/netwatch
    description: "Link monitoring daemon"
    depends: [curl, jq, coreutils-base64, bind-dig]
    conflicts: [othermon, luci-app-othermon]
    conffiles: ["/etc/config/netwatch"]

  - name: luci-app-netwatch
    build: mkpkg
    arch: noarch
    version-from: file:./VERSION
    files: ./staging/luci-app-netwatch
    description: "LuCI netwatch app"
    depends: [luci-base, netwatch]
    i18n:
      from: ./luci-app-netwatch/po
      basename: netwatch
```

Staging is a straight copy plus the version substitution the Makefiles do:

```sh
#!/bin/sh
set -e
VER="1.4.0"; echo "$VER-r1" > VERSION

# luci-app-netwatch: htdocs -> /www, root -> /
mkdir -p staging/luci-app-netwatch/www
cp -a luci-app-netwatch/htdocs/. staging/luci-app-netwatch/www/
cp -a luci-app-netwatch/root/.   staging/luci-app-netwatch/

# netwatch: files/ maps 1:1, except usr/lib/* -> /usr/lib/netwatch/
mkdir -p staging/netwatch/usr/lib/netwatch
cp -a netwatch/files/etc netwatch/files/usr/bin staging/netwatch/
cp -a netwatch/files/usr/lib/.                  staging/netwatch/usr/lib/netwatch/

grep -rl __COMPILED_VERSION_VARIABLE__ staging | xargs sed -i "s/__COMPILED_VERSION_VARIABLE__/$VER/g"
```

```sh
owfeed lock --update
owfeed build && owfeed sign && owfeed index && owfeed doctor
#   built dist/noarch/netwatch-1.4.0-r1.apk
#   built dist/noarch/luci-app-netwatch-1.4.0-r1.apk
#     note: compiled 1 translation catalogue(s): /usr/lib/lua/luci/i18n/netwatch.ru.lmo
#   25.12: 2 package(s) across 35 architecture(s)
#   390 checks passed
```

On a router, `apk add luci-app-netwatch` pulls the whole chain — `curl`, `jq`,
`coreutils-base64`, `bind-dig` — from the official feeds, and installs with no
`--allow-untrusted`.

### `conflicts:` is the entry that does something OpenWrt's own build cannot

Two packages that rewrite the same configuration cannot both be installed, so a Makefile declares
`CONFLICTS:=othermon luci-app-othermon`. On 25.12 that declaration has no effect:
`package-pack.mk` emits `Conflicts:` only into the ipk control file and never passes it to
`mkpkg`, so the built apk package carries nothing.

apk does support it — a conflict is a dependency with a leading `!` — and owfeed emits one:

```
ERROR: unable to select packages:
  othermon-2026.05.06-r1:
    breaks: netwatch-1.4.0-r1[!othermon]
```

### A note on `i18n.basename`

A package shipping `LUCI_LANGUAGES:=en ru` makes `luci.mk` emit separate
`luci-i18n-netwatch-<lang>` packages. Folding the catalogues into `luci-app-netwatch` instead —
which is what the config above does — means a router that installed the language package from an
earlier release already owns `/usr/lib/lua/luci/i18n/netwatch.ru.lmo`. Either keep shipping the
language packages, or pick a basename that does not collide, as `luci-theme-footstrap` does.
owfeed will not guess for you; `doctor` cannot see the other package either.

---

## A compiled binary: a static Go daemon

A static Go binary needs no OpenWrt SDK — only a build for the right target — so the SDK-less path
is not restricted to `noarch`. One upstream artifact usually serves several OpenWrt architectures
that share a GOARCH: one `arm64` build covers all four `aarch64_*`.

```yaml
- name: example-daemon
  build: mkpkg
  arch:
    - x86_64                 # GOARCH=amd64
    - aarch64_cortex-a53     # GOARCH=arm64, all four of these
    - aarch64_cortex-a72
    - aarch64_cortex-a76
    - aarch64_generic
    - mipsel_24kc            # GOARCH=mipsle, GOMIPS=softfloat
    - mipsel_74kc
  version-from: file:./staging/example-daemon.version
  files: ./staging/example-daemon/{arch}
  description: "One line. LuCI truncates past 512 bytes."
```

`{arch}` is required whenever more than one architecture is named. Two architectures cannot share a
payload — if they could, the package would be `noarch` — so leaving it out is an error rather than a
silent mistake.

Builds land in `dist/<arch>/`, because apk derives a package's filename from its name and version
alone: two architectures of one package would collide in a flat directory. Indexing then places a
`noarch` package into every architecture's directory and an architecture-specific one only into its
own.

The mapping from GOARCH to OpenWrt architectures belongs in your fetch script, not in owfeed — it is
a property of your toolchain, not of packaging. A worked one is in
[owfeed/owfeed-packages](https://github.com/owfeed/owfeed-packages), a live feed built this way.

---

## Both release lines from one config

25.12 is apk and 24.10 is opkg. A package that runs on both goes to both; one that does not says so.

```yaml
releases:
  - line: "25.12"
    default: true
    format: apk
  - line: "24.10"
    format: ipk

signing:
  key: env:OWFEED_SIGN_KEY        # EC, for apk
  usign-key: env:OWFEED_USIGN_KEY # usign, for opkg — each manager verifies only its own

packages:
  - name: luci-app-mine           # no `releases:` — published on both
    build: mkpkg
    arch: noarch
    version: 1.0.0-r1
    files: ./dist/root
    url: https://github.com/you/mine

  - name: luci-app-mine-next
    releases: ["25.12"]           # 25.12 only
    build: mkpkg
    arch: noarch
    version: 1.0.0-r1
    files: ./next/root
    url: https://github.com/you/mine
```

```sh
owfeed build && owfeed sign && owfeed index && owfeed doctor
#   built dist/noarch/luci-app-mine-1.0.0-r1.apk (25.12)
#   built dist/noarch/luci-app-mine-next-1.0.0-r1.apk (25.12)
#   built dist/all/luci-app-mine_1.0.0-r1_all.ipk (24.10)
#   24.10: signed by usign key 3af054550a655062
#   25.12: ... signed by key 05353a8e456d078a46325b13310c9b96
```

One tree, two feeds under one URL. A 24.10 router never sees `luci-app-mine-next`: it is not in that
line's index at all, which is the point of saying which lines a package belongs to rather than
publishing everything everywhere and hoping dependencies sort it out.

Verified on real routers with [owlab](https://github.com/owfeed/owlab), one per manager.
