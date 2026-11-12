# Examples

*[Русская версия](examples_ru.md)*

Six recipes for `owfeed.yml`, smallest first. They are for package authors who have files to ship and
want a signed OpenWrt feed.

- Each recipe is a directory you already have, a config, and five commands: `owfeed lock --update`,
  then `build`, `sign`, `index` and `doctor`.
- Every field the configs use is listed in the [owfeed.yml reference](https://owfeed.org/reference/#owfeed-yml)
  and in [schema/v1.json](../schema/v1.json).
- owfeed packages a directory. Your own build produces that directory.
- If the shape of a feed is not yet clear, read [How it works](../README.md#how-it-works). It is twenty
  lines.
- To run a recipe in GitHub Actions, with the keys kept as secrets, follow the
  [cookbook](https://owfeed.org/cookbook/).

## Before the first recipe

Three things every recipe relies on.

- **`owfeed.lock`.** `owfeed lock --update` writes it beside `owfeed.yml`. It records what owfeed
  derived: the point release, the architectures, the toolchain and the keyring version. Commit it at
  first use. An upstream change then arrives as a diff in a pull request.
- **The signing key.** `owfeed keygen -o ~/keys/myfeed.pem` writes the private key and its `.pub.pem`.
  It refuses a path inside a git working tree. owfeed reads the key from `$OWFEED_SIGN_KEY` unless
  `signing.key` names another `env:VAR` or `file:PATH`. A 24.10 feed also needs a usign key:
  `owfeed keygen --usign`, read from the variable `signing.usign-key` names. For keys in CI, see
  [CI in the README](../README.md#ci) and the [cookbook](https://owfeed.org/cookbook/).
- **`publish: - target: github-pages`.** It is the only target this version implements. `owfeed publish`
  runs the doctor checks on `out/` and refuses a tree that fails them. The upload itself is done by
  the workflow, with `actions/upload-pages-artifact` and `actions/deploy-pages`.

## Find it fast

| You want to | Recipe | It also shows |
|---|---|---|
| Ship a directory of files | [Package a plain directory](#package-a-plain-directory) | the smallest config |
| Ship a LuCI theme | [Package a LuCI theme: luci-theme-footstrap](#package-a-luci-theme-luci-theme-footstrap) | translations, `conffiles`, `install-if` |
| Ship a service and its settings page | [Package a service and its LuCI app from one repository](#package-a-service-and-its-luci-app-from-one-repository) | conflicts, real dependencies |
| Ship a compiled binary | [Package a static Go binary for several architectures](#package-a-static-go-binary-for-several-architectures) | several architectures |
| Serve apk and opkg routers | [Publish to both release lines from one config](#publish-to-both-release-lines-from-one-config) | apk and opkg |
| Make downloads smaller | [Trade build time for smaller packages](#trade-build-time-for-smaller-packages) | `compression: zopfli` |

---

## Package a plain directory

1. Lay out the files the way they should install:

   ```
   pkg/root/
     www/luci-static/demo/style.css
     etc/config/demo
   ```

2. Write `owfeed.yml`:

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

3. Lock the architectures, then build, sign, index and check the feed:

   ```sh
   owfeed lock --update
   owfeed build && owfeed sign && owfeed index && owfeed doctor
   ```

---

## Package a LuCI theme: luci-theme-footstrap

[luci-theme-footstrap](https://github.com/VizzleTF/luci-theme-footstrap) is a noarch LuCI theme: CSS,
templates and translations, no compiled code. The status quo asks for 35 SDK builds to produce the
same bytes 35 times.

`owfeed build` packages a directory; it does not build one. So the work splits in two: your build
produces the rootfs, and owfeed turns it into a signed feed.

### 1. Stage the rootfs

The source layout is LuCI's, and the mapping is `luci.mk`'s:

| in the repo | installs as |
|---|---|
| `htdocs/` | `/www` |
| `ucode/` | `/usr/share/ucode/luci` |
| `luasrc/` | `/usr/lib/lua/luci` |
| `root/` | `/` |
| `i18n/<lang>/*.po` | `/usr/lib/lua/luci/i18n/<name>.<lang>.lmo` |

The script below skips translations. owfeed compiles those itself, in step 2.

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

Keep sources out of the payload. If `files:` points at a source tree, owfeed refuses it and names
what it found: `.po`, `.scss`, `node_modules`, `.DS_Store`. Without that check, the package would
install cleanly and miss what the tree implied.

<details>
<summary>Why there is no <code>po2lmo</code> call</summary>

`po2lmo` is a host tool built from `luci-base`. Requiring it would put a C build of the LuCI feed in
front of anyone packaging a theme. owfeed has its own compiler, byte-identical to that tool's output.

</details>

### 2. Write owfeed.yml

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

Check two entries before you copy this config.

**`conffiles`.** Declare every config file the package ships. The theme ships `/etc/config/footstrap`.
If you leave it undeclared, sysupgrade silently replaces the user's settings with the package
defaults on every firmware upgrade. `owfeed doctor` reports it as OWF207.

**`i18n.basename`.** If your package never shipped a `luci-i18n-*` variant, the default is fine. The
default is the `.po` file's own name, which is what `luci.mk` does. Here that would be
`footstrap.<lang>.lmo`. Footstrap sets `footstrap-theme` instead.

<details>
<summary>Why footstrap changed the basename</summary>

LuCI's loader globs `*.<lang>.lmo`, so any basename is found. But this theme used to ship its
translations through separate `luci-i18n-footstrap-<lang>` packages. A router upgrading from that
release still owns `footstrap.ru.lmo`. Reusing the path is a file conflict, and apk refuses the
upgrade: the one it was supposed to deliver.

</details>

### 3. If the translations are their own packages, add `install-if`

Splitting catalogues out the way `luci.mk` does, one `luci-i18n-<app>-<lang>` each, leaves a gap
nothing else in this file closes. `depends` points from the catalogue to the application. So
installing or upgrading the APPLICATION pulls in no catalogue at all.

A router that had the language then upgrades and quietly loses it. That happened to footstrap between
0.14.3 and 0.14.4.

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
Russian".

- apk wants at least two entries, with one pinned by `=` (apk-package(5)).
- `{version}` expands to the version being built, so a release never hand-edits the pin.
- The pin moves, so the rule re-fires on every upgrade of the application.
- The trigger is a package, not a setting. A package manager cannot read `uci luci.main.lang`. So
  `luci-i18n-base-<lang>`, the LuCI base catalogue a translated router already carries, is what
  "this router speaks that language" looks like from here.

**opkg has no conditional form of this.**

- `Recommends:` is parsed and acted on by OpenWrt's opkg, but it is unconditional. It would put
  Russian on every 24.10 router.
- A postinst cannot install the package either: opkg holds an exclusive lock for the whole run.
- So the ipk leg of a split-out translation needs an installer script or a documented package name.
  The apk leg carries the behaviour on its own.

That asymmetry is the format's, not owfeed's.

### 4. Build the feed

```sh
./stage.sh
owfeed lock --update
owfeed build && owfeed sign && owfeed index && owfeed doctor
```

The theme's `/etc/uci-defaults/30_luci-theme-footstrap` runs on install without any extra
configuration.

<details>
<summary>Why uci-defaults run without extra configuration</summary>

owfeed wraps `post-install` the way `package-pack.mk` does, so `default_postinst` applies uci-defaults
and enables init scripts. A bare post-install script would install the files and do none of that.

</details>

### 5. Give your users the install snippet

```sh
owfeed install-snippet
```

Paste the output into the README verbatim. `doctor` checks it has not drifted.

---

## Package a service and its LuCI app from one repository

This is the common shape for anything with a settings page: a shell-script service and the LuCI app
that drives it, in one repository. Both are architecture-independent with an empty `Build/Compile`,
so neither needs a toolchain.

1. Write `owfeed.yml` with both packages:

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

2. Stage both packages. Staging is a straight copy plus the version substitution the Makefiles do:

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

3. Build the feed. The commented lines show the expected output:

   ```sh
   owfeed lock --update
   owfeed build && owfeed sign && owfeed index && owfeed doctor
   #   built dist/noarch/netwatch-1.4.0-r1.apk
   #   built dist/noarch/luci-app-netwatch-1.4.0-r1.apk
   #     note: compiled 1 translation catalogue(s): /usr/lib/lua/luci/i18n/netwatch.ru.lmo
   #   25.12: 2 package(s) across 35 architecture(s)
   #   390 checks passed
   ```

On a router, `apk add luci-app-netwatch` pulls the whole chain from the official feeds: `curl`, `jq`,
`coreutils-base64`, `bind-dig`. It installs with no `--allow-untrusted`.

### Declare `conflicts:`, which OpenWrt's own build drops

Two packages that rewrite the same configuration cannot both be installed. So a Makefile declares
`CONFLICTS:=othermon luci-app-othermon`.

On 25.12 that declaration has no effect. `package-pack.mk` emits `Conflicts:` only into the ipk
control file and never passes it to `mkpkg`. The built apk package carries nothing.

apk does support it: a conflict is a dependency with a leading `!`. owfeed emits one. Installing
both then fails:

```
ERROR: unable to select packages:
  othermon-2026.05.06-r1:
    breaks: netwatch-1.4.0-r1[!othermon]
```

### Choose `i18n.basename` if the app shipped language packages before

A package shipping `LUCI_LANGUAGES:=en ru` makes `luci.mk` emit separate `luci-i18n-netwatch-<lang>`
packages. The config above folds the catalogues into `luci-app-netwatch` instead.

A router that installed the language package from an earlier release then already owns
`/usr/lib/lua/luci/i18n/netwatch.ru.lmo`. Pick one:

- Keep shipping the language packages.
- Pick a basename that does not collide, as `luci-theme-footstrap` does.

owfeed will not guess for you, and `doctor` cannot see the other package either.

---

## Package a static Go binary for several architectures

A static Go binary needs no OpenWrt SDK, only a build for the right target. So the SDK-less path is not
restricted to `noarch`. One upstream artifact usually serves several OpenWrt architectures that share
a GOARCH: one `arm64` build covers all four `aarch64_*`.

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

- `{arch}` is required whenever more than one architecture is named. Leaving it out is an error.
- Builds land in `dist/<arch>/`.
- Indexing places a `noarch` package into every architecture's directory. An architecture-specific one
  goes only into its own.
- The mapping from GOARCH to OpenWrt architectures belongs in your fetch script, not in owfeed. A
  worked one is in [owfeed/owfeed-packages](https://github.com/owfeed/owfeed-packages), a live feed
  built this way.

<details>
<summary>Why these rules</summary>

- `{arch}`: two architectures cannot share a payload. If they could, the package would be `noarch`. So
  a missing `{arch}` is an error rather than a silent mistake.
- `dist/<arch>/`: apk derives a package's filename from its name and version alone. Two architectures
  of one package would collide in a flat directory.
- The GOARCH mapping is a property of your toolchain, not of packaging.

</details>

---

## Publish to both release lines from one config

25.12 is apk and 24.10 is opkg. A package that runs on both goes to both. A package that does not
says so with `releases:`.

1. Declare both release lines, one signing key per package manager, and the lines each package
   belongs to:

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

2. Build the feed. The commented lines show the expected output:

   ```sh
   owfeed build && owfeed sign && owfeed index && owfeed doctor
   #   built dist/noarch/luci-app-mine-1.0.0-r1.apk (25.12)
   #   built dist/noarch/luci-app-mine-next-1.0.0-r1.apk (25.12)
   #   built dist/all/luci-app-mine_1.0.0-r1_all.ipk (24.10)
   #   24.10: signed by usign key 3af054550a655062
   #   25.12: ... signed by key 05353a8e456d078a46325b13310c9b96
   ```

The result is one tree with two feeds under one URL. A 24.10 router never sees `luci-app-mine-next`:
it is not in that line's index at all.

This was verified on real routers with [owlab](https://github.com/owfeed/owlab), one per manager.

<details>
<summary>Why each package names its release lines</summary>

Saying which lines a package belongs to keeps it out of the other line's index. The alternative is
publishing everything everywhere and hoping dependencies sort it out.

</details>

---

## Trade build time for smaller packages

Every router that installs or upgrades a package downloads it. If a package ships often and
compresses in a second or two, set `compression: zopfli`:

```yaml
- name: luci-app-example
  build: mkpkg
  arch: noarch
  version: 1.0.0-r1
  files: ./dist/root
  compression: zopfli
```

- **Size:** zopfli writes the streams instead of the best standard deflate level. They were 2-5%
  smaller in the measurements.
- **Readers:** opkg and apk read the packages exactly as before.
- **Cost:** a 250 KB LuCI payload takes about a second more to build and to sign. A 10 MB binary takes
  30-40 s and about 600 MB of memory for each.
- **Default:** leave it unset and nothing changes.

### Where the saving survives

- The `.ipk` is compressed this way when it is built.
- The `.apk` is re-deflated by `owfeed sign`, after apk's own signing step. That step re-compresses the
  whole file.
- A downstream feed that adds its own signature to your `.apk` re-compresses it, and the saving is
  gone.
- A downstream feed with `sign-packages: false` serves your bytes as they are.

The numbers are in
[apk-behaviour.md](apk-behaviour.md#zopfli-2-5-smaller-read-unchanged-by-apk-and-opkg-tens-of-times-slower).
