# Verified package manager behaviour

Facts established by running the real thing, not by reading documentation. Every claim
here has a reproduction below it. Anything not reproduced is not in this file.

Most of it is apk, which 25.12 and later use. [opkg, 24.10 and earlier](#opkg-2410-and-earlier)
is at the end, where nearly every detail is different.

**Environment:** apk-tools 3.0.5 extracted from `openwrt-sdk-25.12.5-x86-64_gcc-14.3.0_musl.Linux-x86_64.tar.zst`,
run under `linux/amd64`. Date: 2026-07-27.

Throughout, `$APK` is the loader invocation from
[Extracting a host apk](#extracting-a-host-apk-from-the-sdk):

```sh
APK="/opt/apk/lib/ld-linux-x86-64.so.2 --library-path /opt/apk/lib /opt/apk/bin/.apk.bin"
```

---

## `mkndx` accepts repeated `--sign-key`

**This unblocks key rotation.** An index can carry several signatures, so during a
rotation window both the old and the new key can sign the same `packages.adb` and
subscribers with either key installed keep working. No two-index dance, no URL flip.

```sh
$APK mkndx --allow-untrusted --sign-key a.pem --sign-key b.pem --output two.adb ./h.apk
$APK adbdump two.adb | grep -c '^# sig'      # => 2
```

Dump excerpt:

```
# sig v00 h04 364b7138efaa7f7f862f62fd04099d96...: UNTRUSTED signature
# sig v00 h04 a4e7527837b5ee992d0619a1c3c27aff...: UNTRUSTED signature
```

(`UNTRUSTED` here only means neither key is in this container's trust directory; it is
the expected output when dumping a third-party index without installing its key.)

## `adbsign` reports failure on stdout and still exits 0

**Treat `adbsign`'s exit status as carrying no information.** It printed an error, did
nothing, and returned success:

```sh
$APK mkndx --allow-untrusted --sign-key a.pem --output x.adb ./h.apk
$APK adbsign --sign-key b.pem x.adb
#   ERROR: x.adb: UNTRUSTED signature
#   exit=0
#   md5 before=1499aca8 after=1499aca8  -> UNCHANGED
```

A pipeline written as `adbsign … || die` ships a silently unsigned or half-signed index.
Verify by re-reading the artifact and counting signature blocks, never by `$?`.

The cause is that `adbsign` reads the existing index through the normal trust path, so
signing an index whose own signatures are not trusted locally requires the flag:

```sh
$APK adbsign --allow-untrusted --sign-key b.pem y.adb
#   exit=0
#   md5 before=440c1a68 after=8c464210  -> CHANGED
#   signature blocks: 2
```

## An unsigned package is "untrusted", so signing one needs `--allow-untrusted`

There is no bootstrap from unsigned. Neither `adbsign` nor `mkndx` will touch a
package that carries no signature at all, and the diagnostic says `UNTRUSTED
signature` for a file that has none:

```sh
$APK mkpkg --info name:h --info version:1.0-r1 --info arch:noarch --files root --output h.apk
$APK adbsign --sign-key a.pem h.apk
#   ERROR: h.apk: UNTRUSTED signature
#   exit=0, signature count still 0

$APK mkndx --sign-key a.pem --output i.adb ./h.apk
#   ERROR: ./h.apk: UNTRUSTED signature
#   ERROR: 1 errors, not creating index
#   exit=99
```

This is why `--allow-untrusted` cannot be designed away entirely. It belongs on the
signing step and nowhere else, because that is the one place the input is a file
produced seconds earlier by the same run.

## `--keys-dir` is honoured only when the path is absolute

**A relative `--keys-dir` loads nothing and says nothing.** The failure looks
identical to a signature that genuinely does not verify:

```sh
$APK --keys-dir pub       mkndx --sign-key a.pem -o i.adb ./h.apk   # ERROR: UNTRUSTED signature, exit=99
$APK --keys-dir /work/pub mkndx --sign-key a.pem -o i.adb ./h.apk   # Index has 1 packages
```

With the absolute form, building an index is a real verification of every package
signature it ingests — which is what makes `adbsign`'s useless exit status
survivable: if signing silently did nothing, indexing fails.

## `adbsign` appends signatures rather than replacing them

Signing twice with different keys leaves both blocks in place, on packages as well
as on indexes:

```sh
$APK --allow-untrusted adbsign --sign-key a.pem h.apk
$APK --allow-untrusted adbsign --sign-key b.pem h.apk
$APK adbdump h.apk | grep -c '^# sig'    # => 2
```

## The index records each package's `file-size`, so sign before indexing

```
packages: # 1 items
  - name: h
    version: 1.0-r1
    hashes: 6cd348e1587b108cda4a141d8e35815ddb42cfdf3f11aff5b0bd9cf5b762b6f6
    arch: noarch
    installed-size: 2
    file-size: 265
```

Signing appends bytes to the `.apk`, so a package signed after it was indexed no
longer matches its own index entry. The order is not a preference.

The index carries no file *names*: apk derives the download name from the package
name and version, so the files have to sit flat beside `packages.adb` under exactly
`<name>-<version>.apk`.

## `mkndx` does fail loudly on an unusable key

Unlike `adbsign`, this one is trustworthy — exit 99, and no output file is left behind:

```sh
echo garbage > bad.pem
$APK mkndx --allow-untrusted --sign-key bad.pem --output z.adb ./h.apk
#   ERROR: Failed to load signing key: bad.pem: cryptographic key format not recognized
#   exit=99
#   z.adb not created
```

## Index magic is `ADBd`, and the SDK's apk cannot produce anything else

Default compression is deflate, which is the only thing OpenWrt's on-device apk can
read (it is built `-Dzstd=disabled`):

```sh
head -c 4 one.adb | od -c | head -1
#   0000000   A   D   B   d
```

The host apk extracted from the SDK is built the same way, so the zstd trap is not
merely avoidable there — it is unreachable:

```sh
$APK mkndx --allow-untrusted --compression zstd --output z.adb ./h.apk
#   ERROR: command line: invalid argument for option 'compression': 'zstd'
#   exit=1
$APK mkndx --allow-untrusted --compression deflate --output d.adb ./h.apk
#   Index has 1 packages (of which 1 are new)
```

This is a second payoff of insisting on the version-matched SDK toolchain rather
than whatever apk happens to be installed: Alpine's apk, or any build with zstd
compiled in, will happily write an index that parses on the build host and dies on
every router with "ADB compression not supported".

## The default deflate is already level 9, and naming a level changes the magic

`mkpkg` and `adbsign` both take `--compression ALGORITHM[:LEVEL]`. With no level the
package is written at deflate's best level already: the stream after the 4-byte `ADBd`
is byte for byte the one `--compression deflate:9` writes after its 6-byte header. Naming
any level switches the header to `ADBc` plus an algorithm byte and a level byte, so
`deflate:9` buys two bytes and nothing else. Payload: `luci-mod-network` from OpenWrt's
24.10.8 feed, unpacked; apk-tools 3.0.5 from the 25.12.5 SDK; date 2026-10-13.

```sh
$APK mkpkg --no-xattrs -I name:luci-mod-network -I version:26.283.50386-r1 -I arch:noarch -F payload -o u0.apk
$APK mkpkg --no-xattrs --compression deflate:9 -I name:luci-mod-network -I version:26.283.50386-r1 -I arch:noarch -F payload -o u9.apk
ls -l u0.apk u9.apk                                  # 66051, 66053
head -c 6 u9.apk | od -An -tu1                       #   65 68 66 99 1 9  ("ADBc", deflate, 9)
cmp <(tail -c +5 u0.apk) <(tail -c +7 u9.apk)       # identical
```

`adbsign` keeps whatever the file already had, and `--compression` on it re-encodes:

| `mkpkg` | `adbsign` | after signing | magic | `apk verify` |
|---|---|---|---|---|
| default | default | 66,178 | `ADBd` | OK |
| `deflate:9` | default | 66,172 | `ADBc 01 09` | OK |
| `deflate:6` | default | 66,320 | `ADBc 01 06` | OK |
| default | `deflate:9` | 66,178 | `ADBc 01 09` | OK |
| `deflate:9` | `deflate` | 66,176 | `ADBd` | OK |

Signed sizes move by a few bytes between runs, because each ECDSA signature is fresh. So
apk-tools' own option has no smaller setting to offer, and owfeed passes none. The short
spelling the help text lists does not parse:

```sh
$APK mkpkg -C deflate:9 ...
#   ERROR: command line: unrecognized option 'C'
#   exit=1
```

## apk signs the uncompressed ADB, so a signed package can be re-deflated

The signature covers the ADB inside the deflate stream, not the compressed bytes. A
signed package inflated and deflated again by another compressor still verifies; one
byte changed in the ADB does not. Same `luci-mod-network` package as above, signed with
a throwaway key whose public half is in `trust/`:

```sh
python3 - <<'PY'
import zlib
d = open('def.apk', 'rb').read()
adb = zlib.decompressobj(-15).decompress(d[4:])           # raw deflate after 'ADBd'
c = zlib.compressobj(1, zlib.DEFLATED, -15)               # any other encoder
open('re.apk', 'wb').write(b'ADBd' + c.compress(adb) + c.flush())
bad = bytearray(adb); bad[-40] ^= 1
c = zlib.compressobj(9, zlib.DEFLATED, -15)
open('bad.apk', 'wb').write(b'ADBd' + c.compress(bytes(bad)) + c.flush())
PY
$APK verify --keys-dir $PWD/trust re.apk               #   re.apk: OK
$APK verify --keys-dir $PWD/trust bad.apk              #   bad.apk: file integrity error
                                                         #   exit=1, on stdout; stderr empty
```

That is what makes `compression: zopfli` possible on the apk side, and also what limits
it: `adbsign` re-deflates the whole file at its own level, so the re-deflate has to come
after it. A second `adbsign` undoes it, which is what a feed adding its own signature
does:

```sh
$APK --allow-untrusted adbsign --sign-key feed.pem luci-mod-network-26.283.50386-r1.apk
#   64202 B before (zopfli, one signature), 66716 B after (two signatures)
```

## zopfli: 2-5% smaller, read unchanged by apk and opkg, tens of times slower

zopfli (the pure-Go port, `github.com/foobaz/go-zopfli`) writes a standard deflate
stream, smaller than the best zlib level and much slower to produce. owfeed uses it only
for a package whose config says `compression: zopfli`: the `.ipk` streams at build time,
the `.apk` in `owfeed sign` after `adbsign`, keeping `ADBd`, checking that the new
stream inflates to the identical ADB and that `apk verify` passes with the signing key
before the file is replaced.

`luci-mod-network` from OpenWrt's 24.10.8 feed, unpacked, `SOURCE_DATE_EPOCH=1791640786
owfeed build` then `owfeed sign --key file:k.pem dist`. Date: 2026-10-13, 20 cores.

| | `.ipk` | `.apk`, signed | `build` | `sign` |
|---|---|---|---|---|
| owfeed 0.5.4 | 69,923 | 66,605 | 0.02 s | 0.01 s |
| best standard level | 66,150 | 66,607 | 0.02 s | 0.01 s |
| `compression: zopfli` | 63,913 | 64,201 | 0.91 s | 0.68 s |

Signed `.apk` sizes move by a few bytes between runs with the ECDSA signature. Two
builds of each are byte-identical, zopfli included.

The cost grows with the payload. Two stripped x86-64 binaries, 10,656,711 B, packaged
as `example-daemon`, measured with `/usr/bin/time`:

| | `.ipk` | `.apk`, signed | `build` | `sign` |
|---|---|---|---|---|
| best standard level | 4,615,630 | 4,525,110 | 1.93 s, 75 MB | 1.19 s, 10 MB |
| `compression: zopfli` | 4,407,065 | 4,430,803 | 38.41 s, 634 MB | 32.86 s, 613 MB |

zopfli alone over that payload's 10,670,080 B tar: 4,430,163 B in 32.04 s and 605 MB,
against Go's `gzip.BestCompression` at 4,622,170 B in 0.73 s.

Installed with no `--allow-untrusted` anywhere: `TestIntegrationAcceptanceZopfli` builds,
signs (`zopfli noarch/luci-app-acceptance-1.0.0-r1.apk: 4935 -> 4723 B`), indexes and
runs `doctor` on a feed, then installs it on `openwrt/rootfs:x86-64-25.12.4` from the
index and again with `apk add ./file.apk`. `TestIntegrationInstallsOnOpkg/zopfli=true`
installs a zopfli `.ipk` with opkg on `openwrt/rootfs:x86-64-24.10.8`.

## `installed-size` and `file-size` are computed, not supplied

They appear in the index without being passed to `--info` (and `--info` rejects them):

```
    installed-size: 3
    file-size: 239
```

## A conflict is a negative dependency, and OpenWrt's apk build emits none

apk has no `conflicts` field. A conflict is written as a dependency with a leading
`!`, and `--info depends:` accepts it:

Package names in this reproduction are substituted for neutral ones; the behaviour
does not depend on them. Everything else is the output as it was produced.

```sh
$APK mkpkg --info name:p --info version:1.0-r1 --info arch:noarch \
  --info "depends:curl jq !othermon !luci-app-othermon" --files root --output p.apk
$APK adbdump p.apk
#   depends: # 4 items
#     - curl
#     - '!othermon'
#     - jq
#     - '!luci-app-othermon'
```

On the device it resolves as a real constraint:

```
ERROR: unable to select packages:
  othermon-2026.05.06-r1:
    breaks: netwatch-1.4.0-r1[!othermon]
```

**OpenWrt's own apk packages carry none of this.** In `package-pack.mk`, `CONFLICTS`
appears once, inside the `Package/$(1)/CONTROL` block that produces an ipk control
file; the `mkpkg` invocation a few hundred lines below never mentions it, and the
apk branch does not write `CONTROL` at all. So a Makefile that declares a conflict
does not enforce one on 25.12.

That is not academic. Real packages in the wild declare conflicts with three or four
others that write the same configuration, and on 25.12 nothing stops a user
installing two of them.

## `mkpkg` records unknown file owners as `nobody`, not as root

**This is the most consequential finding here, because it is silent.** `mkpkg` stores
each file's owner *by name*, resolving the numeric uid through the passwd file under
apk's root. An id with no entry there does not fall back to root:

```c
/* src/io.c, apk_id_cache_resolve_user */
if (ci) return APK_BLOB_PTR_LEN(ci->name, ci->len);
if (uid == 0) return APK_BLOB_STRLIT("root");
return APK_BLOB_STRLIT("nobody");
```

So a package built by an ordinary user records every file as `nobody:nobody`, and the
router chowns them that way on install. Nothing warns, at build time or at install time.

```sh
chown -R 1001:1001 root/
$APK mkpkg --info name:d --info version:1.0-r1 --info arch:noarch --files root --output plain.apk
$APK adbdump plain.apk | grep -E 'user|group' | sort -u
#   group: nobody
#   user: nobody
```

OpenWrt does not hit this because `package-pack.mk` runs `mkpkg` under `$(FAKEROOT)`.
`runas.so`, which the SDK's apk wrapper sets as `LD_PRELOAD`, is **not** a fakeroot: its
only exported behaviour is rewriting `argv[0]` from `RUNAS_ARG0`.

The fix needs no extra tooling. Point apk's `--root` at a directory whose `etc/passwd`
calls the building uid `root`:

```sh
mkdir -p idroot/etc
printf 'root:x:1001:1001:root:/root:/bin/sh\n' > idroot/etc/passwd
printf 'root:x:1001:\n'                        > idroot/etc/group
$APK --root idroot mkpkg --info name:d --info version:1.0-r1 --info arch:noarch \
     --files root --output rooted.apk
$APK adbdump rooted.apk | grep -E 'user|group' | sort -u
#   group: root
#   user: root
```

`--files` and `--output` stay relative to the working directory; `--root` affects only
id resolution here, and apk writes nothing into that directory.

## `mkpkg` records extended attributes by default

`--xattrs` defaults to on, which on macOS ships the build host's residue to routers:

```
  - name: etc
    acl:
      mode: 0755
      user: root
      group: root
      xattrs: # 1 items
        - com.apple.provenance=01020013ed3d264d9d6efa
```

`com.apple.provenance` is stamped by macOS on downloaded files. Besides being
meaningless on OpenWrt, it makes the same inputs produce different package bytes on
macOS and on Linux. `--no-xattrs` removes them.

## `mkpkg` does not stamp a build time

Nothing in the output varies between two runs over identical inputs — there is no
`build-time` field unless one is passed. The only source of non-determinism left is the
payload's mtimes, which `mkpkg` does record, so normalising those is enough to make a
build reproducible.

---

## On a real router image

Run against `openwrt/rootfs:x86-64-25.12.4`, against a feed built by owfeed and
mounted read-only. This settles three claims the design made from first principles.

### A signed index makes `apk add` work with no flag

```sh
cp /repo/demofeed.pem /etc/apk/keys/demofeed.pem
echo "/repo/releases/25.12/$(cat /etc/apk/arch)/packages.adb" > /etc/apk/repositories.d/demofeed.list
apk update
#   Demo feed [/repo/releases/25.12/x86_64/packages.adb]
#   OK: 11274 distinct packages available
apk add luci-app-demo
#   (1/1) Installing luci-app-demo (1.0.0-r1)
#     Executing luci-app-demo-1.0.0-r1.post-install
#   OK: 11.8 MiB in 137 packages
```

The installed files are owned `root root`, which is the point of the `--root`
passwd trick above, and the sidecars arrive where sysupgrade looks for them:
`/lib/apk/packages/luci-app-demo.{list,conffiles,conffiles_static}`.

### Signing each package fixes `apk add ./file.apk`, and therefore LuCI's upload

This is the claim the design derived rather than observed. It holds:

```sh
apk add /tmp/luci-app-demo-1.0.0-r1.apk        # key installed
#   (1/1) Installing luci-app-demo (1.0.0-r1)
#   exit=0

rm /etc/apk/keys/demofeed.pem                  # key removed
apk add /tmp/luci-app-demo-1.0.0-r1.apk
#   ERROR: /tmp/luci-app-demo-1.0.0-r1.apk: UNTRUSTED signature
#   exit=99
```

No `--allow-untrusted` in either case. OpenWrt's own 25.12 packages are unsigned
individually (commit `084697e`), so this path always needs the flag for them — and
LuCI's Upload Package flow cannot supply it, because `package-manager-call` drops
arguments it does not recognise (luci#8482). A trusted per-package signature is the
missing link for *this path*, and only for it — see the next entry for what the path
costs, and the one after for what happens without any package signature at all.

### A local install pins the package forever

```sh
apk add /tmp/luci-app-demo-1.0.0-r1.apk
grep demo /etc/apk/world
#   luci-app-demo><Q1tr1HVcrdHMqaRRH78PhUKaT5XPo=
```

The `><` operator pins by content hash, so the package will never again be upgraded
from the repository, and `/etc/apk/world` survives sysupgrade. Documentation must
therefore never offer local installation as the way to install from a feed. (The
hash is base64, not hex as the design assumed — the behaviour is what matters.)

### A keyring package cannot start a rotation on its own

The package that carries a feed's next public key is fetched from the feed, and the
feed's index is signed by the key it is rotating to. A subscriber holding only the old
key therefore cannot reach it:

```sh
# index signed by the NEW key only; router holds the OLD one
/usr/libexec/package-manager-call upgrade krfeed-keyring
#   { "code": 0, "stderr": "WARNING: opening .../packages.adb: UNTRUSTED signature" }
#   keys afterwards: unchanged
```

The upgrade reports success and changes nothing, which is the worst shape this failure
could take. With an overlap it works, and the same router needs no other help:

```sh
# index signed by BOTH keys
apk update                       # OK: 11157 distinct packages available
/usr/libexec/package-manager-call upgrade krfeed-keyring
#   { "code": 0, "stdout": "(1/1) Upgrading krfeed-keyring (1.3-r1 -> 1.4-r1)" }
#   keys afterwards: the new one, delivered by the package
```

Dropping the old key from the config afterwards leaves an index signed by the new key
alone, which that router now verifies. The old public key can then be removed from
`/etc/apk/keys` — measured: `apk update` still answers OK with only the new one
present.

So the keyring package and the overlap are one mechanism, not two features. Shipping
either alone leaves a feed that cannot rotate.

### An unsigned package installs, upgrades and removes from a signed index

The whole LuCI lifecycle works with no package signature at all, as long as the
index is signed. Run against a feed built by `owfeed` with
`signing.sign-packages: false`, on 25.12.5, driving LuCI's own wrapper rather than
`apk` directly:

```sh
# The package is in the list LuCI renders
/usr/libexec/package-manager-call list-available | grep luci-app-demo   # present

# Install button
/usr/libexec/package-manager-call install luci-app-demo
#   { "code": 0, "pkmcmd": "apk add luci-app-demo",
#     "stdout": "(1/1) Installing luci-app-demo (1.0.0-r1)" }

# Upgrade button, after publishing 1.0.1-r1
/usr/libexec/package-manager-call upgrade luci-app-demo
#   { "code": 0, "stdout": "(1/1) Upgrading luci-app-demo (1.0.0-r1 -> 1.0.1-r1)" }

# Remove button
/usr/libexec/package-manager-call remove luci-app-demo
#   { "code": 0, "stdout": "(1/1) Purging luci-app-demo (1.0.1-r1)" }
```

`/etc/apk/world` holds `luci-app-demo` with **no** `><` pin, which is why the upgrade
resolves at all — the contrast with the entry above is the whole difference between
installing by name and installing a file.

Trust comes from the index on this path: apk verifies it against the key in
`/etc/apk/keys` and then checks each package against the hash the index recorded. The
signature inside a package is never consulted. Removing the feed's key makes the
package vanish from the list entirely rather than install untrusted.

Two things this does **not** do. `apk add ./file.apk` and LuCI's Upload Package still
fail with `UNTRUSTED signature` and `code: 99` — there is no index to check against,
and the `--allow-untrusted` that would fix it is dropped by `package-manager-call`
(confirmed by reading it on the router: the apk branch shifts past any argument
starting with `-` except `--force-overwrite` and
`--force-removal-of-dependent-packages`).

Building the index needs `mkndx --allow-untrusted`, or it refuses the unsigned
package with `UNTRUSTED signature`, exit 99, and writes nothing.

---

## Extracting a host apk from the SDK

The SDK ships `staging_dir/host/bin/apk` as a **bash** wrapper around a hidden real
binary, running it under a bundled glibc loader:

```sh
$ cat staging_dir/host/bin/apk
#!/usr/bin/env bash
dir="$(dirname "$0")"
export RUNAS_ARG0="$0"
export LD_PRELOAD="${LD_PRELOAD:+$LD_PRELOAD:}$dir/../lib/runas.so"
exec "$dir/../lib/ld-linux-x86-64.so.2" --library-path "$dir/../lib/" "$dir/.apk.bin" "$@"

$ file staging_dir/host/bin/.apk.bin
ELF 64-bit LSB pie executable, x86-64, dynamically linked,
interpreter /lib64/ld-linux-x86-64.so.2, stripped
```

### You do not need all of `staging_dir/host/lib`

That directory is **143 MB**. The transitive `DT_NEEDED` closure of `.apk.bin` is three
libraries totalling 2.1 MB:

```
ld-linux-x86-64.so.2   173 KB
libc.so.6             1856 KB
libpthread.so.0        115 KB
```

So the complete extraction set is six files, **3.9 MB**:

```
staging_dir/host/bin/apk                    (wrapper, 224 B)
staging_dir/host/bin/.apk.bin               (1.9 MB)
staging_dir/host/lib/ld-linux-x86-64.so.2
staging_dir/host/lib/libc.so.6
staging_dir/host/lib/libpthread.so.0
staging_dir/host/lib/runas.so               (14 KB, LD_PRELOAD from the wrapper)
```

Extracted straight from the network without ever storing the 285 MB tarball:

```sh
curl -fsS "$BASE/$SDK" | zstd -dc | tar -x -C ext --strip-components=1 \
  '*/staging_dir/host/bin/apk'   '*/staging_dir/host/bin/.apk.bin' \
  '*/staging_dir/host/lib'
```

### Neither bash nor glibc is required to run it

Invoking the loader directly bypasses the wrapper, so `bash` is not needed. Because the
loader and libc are bundled, the host libc is irrelevant — it runs unmodified on a
musl-only Alpine image:

```sh
$ docker run --rm --platform linux/amd64 -v "$PWD/min":/opt/apk:ro alpine:3 \
    /opt/apk/lib/ld-linux-x86-64.so.2 --library-path /opt/apk/lib \
    /opt/apk/bin/.apk.bin --version
apk-tools 3.0.5, compiled for x86_64.
```

`LD_PRELOAD=runas.so` is also not required for `mkndx`/`mkpkg`/`adbdump` to run, and it
is not a fakeroot — its only exported symbols are `getenv`/`unsetenv` and the string
`RUNAS_ARG0`, i.e. it rewrites `argv[0]` and nothing else. Ownership is handled the way
described above, under `--root`. It is kept in the extraction set anyway, at 14 KB, so
the SDK's own wrapper script still works if anyone invokes it.

### macOS needs a container

`.apk.bin` is a Linux x86-64 ELF. On darwin there is nothing to run it with, so the
SDK-extraction path requires Docker there. It works under Docker Desktop's amd64
emulation on Apple silicon (slowly). This is the concrete form of the design's open
question about whether owfeed can be container-free on macOS: for the SDK route, no.

---

## Signature verification of the SDK itself

`sha256sums.sig` is a usign (signify-style ed25519) detached signature. The key id is in
the signature, and `github.com/openwrt/keyring/usign/` names its key files by that same
id, so the key is addressable directly from the signature:

```sh
$ base64 -d <<< "$(tail -1 sha256sums.sig)" | xxd | head -1
# magic "Ed", then 8-byte key id b5043e70f9a75cde, then a 64-byte signature
```

```
$ sha256sum b5043e70f9a75cde
d7ac10f9ed1b38033855f3d27c9327d558444fca804c685b17d9dcfb0648228f
```

Reading the key id out of the signature is a convenience for *locating* the key, never
for deciding whether to trust it — an attacker who can replace the signature can put any
key id in it. The trusted set of key ids and their hashes has to be pinned independently.

`internal/usign` verifies this natively, so no C toolchain is on the path; the test in
that package verifies a real 25.12.5 release artifact.

### A cached extraction can be re-checked by hand

Beside the six members, the cache directory keeps `sha256sums` and `sha256sums.sig` as
downloaded, the key they verified with as `sha256sums.pub`, and `members.sha256`, the
hash of each member as it came out of the tarball. owfeed checks all of it before each
use. A person can do the same in that directory:

```sh
$ cd ~/.cache/owfeed/sdk/25.12.5
$ usign -V -m sha256sums -p sha256sums.pub     # usign from openwrt/rootfs
OK
$ sha256sum -c members.sha256
staging_dir/host/bin/apk: OK
staging_dir/host/bin/.apk.bin: OK
...
```

That proves the members are the ones owfeed extracted, not that they came out of the
signed tarball: whoever can write the cache can rewrite `members.sha256` to match.
Date: 2026-10-26.

## downloads.openwrt.org resumes a download, and ignores If-Range

The 25.12.5 SDK is 284,842,599 bytes. A `Range` request is answered 206 from the byte
asked for. `If-Range` is not honoured: a validator that does not match the file still
gets 206 rather than the whole file.

```sh
U=https://downloads.openwrt.org/releases/25.12.5/targets/x86/64/openwrt-sdk-25.12.5-x86-64_gcc-14.3.0_musl.Linux-x86_64.tar.zst
curl -sS -o /dev/null -D - -r 0-0 "$U"
#   HTTP/2 206, etag: "6a4333e7-10fa5a67", content-range: bytes 0-0/284842599
curl -sS -o /dev/null -D - -r 284842590- -H 'If-Range: "00000000-00000000"' "$U"
#   HTTP/2 206, content-range: bytes 284842590-284842598/284842599
curl -sS -o /dev/null -D - -r 284842590- -H 'If-Range: Mon, 01 Jan 2024 00:00:00 GMT' "$U"
#   HTTP/2 206, content-range: bytes 284842590-284842598/284842599
curl -sS -o /dev/null -D - -r 284842599- "$U"
#   HTTP/2 416, content-range: bytes */284842599
```

So a resumed download cannot count on the server to say that the file changed between
two requests; the pieces of two files would arrive as one. What catches that is the
SHA-256 against the signed `sha256sums`, taken over every byte in order. Through
owfeed's own client, an SDK download cut at byte 41,943,040 was asked for again with
`Range: bytes=41943040-`, answered `206 Partial Content`, and the whole matched the
signed hash. Date: 2026-10-26.

---

# opkg, 24.10 and earlier

A different package manager, and the differences are the kind that produce a feed
which looks correct and works on nobody's router.

**Environment:** `openwrt/rootfs:x86-64-24.10.8`, and OpenWrt's own published
24.10.8 feed for the comparisons. Date: 2026-07-28.

## The index signature covers the uncompressed `Packages`

opkg downloads `Packages.gz`. The signature beside it is over `Packages`. Signing
the compressed copy produces a feed every router rejects, and nothing about the
filenames hints at which one is meant.

Checked against OpenWrt's own feed rather than assumed:

```sh
B=https://downloads.openwrt.org/releases/24.10.8/packages/x86_64/base
curl -sfS "$B/Packages" -o P; curl -sfS "$B/Packages.sig" -o P.sig; curl -sfS "$B/Packages.gz" -o P.gz
usign -V -m P    -p k24.pub -x P.sig   # OK
usign -V -m P.gz -p k24.pub -x P.sig   # fails
```

They are two separate files, so a publisher who regenerates one without the other
serves a feed whose signature is valid and whose contents are stale.

## The key's filename is its id

```sh
$ ls /etc/opkg/keys/
d310c6f2833e97f7
$ head -1 /etc/opkg/keys/d310c6f2833e97f7
untrusted comment: Public usign key for 24.10 release builds
```

opkg looks a key up by that name. apk is the opposite: it matches on the identity
inside the signature and ignores the filename entirely. The same public key
published under the wrong name breaks one manager or the other.

## The repository line names a directory

```
src/gz openwrt_base https://downloads.openwrt.org/releases/24.10.8/packages/x86_64/base
```

opkg appends `/Packages.gz` itself. apk's `ndx` line names the index *file*. Each
form is wrong for the other.

## Signature checking is on by default, and refusing is the default outcome

The stock `/etc/opkg.conf` carries `option check_signature`. Without the key
installed:

```
Updated list of available packages in /var/opkg-lists/dualfeed
Signature check failed.
Remove wrong Signature file.
 * opkg_install_cmd: Cannot install package luci-app-demo.
```

## Packages carry no signature at all

There is no per-package signature in the ipk container, and no equivalent of apk's
`adbsign`. opkg's trust rests entirely on the signed index, which is why the index
signature is the only thing worth checking there — and why the apk-side claim about
`apk add ./file.apk` has no counterpart.

One consequence in owfeed's favour: an unchanged ipk rebuilds byte for byte, where
an apk gets a fresh randomised ECDSA signature every time it is signed.

## The container is a gzipped tar, not an ar archive

From `scripts/ipkg-build` on the openwrt-24.10 branch:

```
<name>_<version>_<arch>.ipk = gzip(tar of ./debian-binary, ./data.tar.gz, ./control.tar.gz)
```

in that order, GNU format, numeric owner, sorted by name, fixed mtime.
`Installed-Size` in the control file is the *uncompressed* size of `data.tar.gz`.

## OpenWrt's packages are gzip level 6, and Go's level 6 is not gzip's

Measured on `luci-mod-network_26.283.50386~24db9bf_all.ipk` from OpenWrt's own 24.10.8 feed:
its three gzip streams all carry XFL 0, and its `data.tar.gz` is byte for byte what GNU
gzip writes at its default level, not at `-9`:

```sh
B=https://downloads.openwrt.org/releases/24.10.8/packages/x86_64/luci
curl -sfS "$B/luci-mod-network_26.283.50386~24db9bf_all.ipk" -o n.ipk
mkdir o && tar -xzf n.ipk -C o && zcat o/data.tar.gz > data.tar
od -An -tu1 -j8 -N1 o/data.tar.gz                        #   0  (XFL; -9 writes 2)
gzip -6 -n -c data.tar | tail -c +11 | cmp - <(tail -c +11 o/data.tar.gz)   # identical, 65561 B
gzip -9 -n -c data.tar | wc -c                           #   65311
```

Go's `compress/flate` at its default level is not zlib's level 6. owfeed 0.5.4 wrote its
streams with `gzip.NewWriter` and came out larger than the SDK. The same package rebuilt
by owfeed from that payload, `SOURCE_DATE_EPOCH=1791640786 owfeed build`, one 274,432 B
`data.tar` in both builds:

| | `data.tar.gz` | `.ipk` |
|---|---|---|
| OpenWrt 24.10.8 (GNU gzip -6, its own tar) | 65,561 | 66,541 |
| GNU gzip -6 over owfeed's tar | 65,555 | — |
| GNU gzip -9 over owfeed's tar | 65,323 | — |
| owfeed 0.5.4, `gzip.NewWriter` | 67,762 | 69,923 |
| owfeed, `gzip.BestCompression` | 65,382 | 66,150 |

The outer stream compresses a tar that is almost entirely the already-compressed
`data.tar.gz`, and at the default level it added 1,750 B over its members where the best
level adds 376 B. Every stream is now written at `gzip.BestCompression`, XFL 2, which
`TestBuildCompressesEveryStreamAtBestLevel` holds.

## `all`, never `noarch`

opkg calls an architecture-independent package `all`. apk rejects `all` as
uninstallable and requires `noarch`. The same package therefore carries a different
architecture name in each of its two artifacts, which is what OpenWrt's own
`package-pack.mk` translates when it builds for apk.

## `opkg` needs `/var/lock` to exist

Not a format detail, but it costs an afternoon if you meet it in a container:

```
opkg_conf_load: Could not create lock file /var/lock/opkg.lock: No such file or directory
```

The rootfs images ship without it.

## All four lifecycle scripts run, and only two have a `default_` helper

Measured in `openwrt/rootfs:x86-64-24.10.8` with a package whose `preinst`, `postinst`,
`prerm` and `postrm` each appended their arguments to a file:

| operation | scripts, in order |
|---|---|
| install | `preinst install`, `postinst configure` |
| upgrade | `prerm remove`, `postrm remove`, `preinst install`, `postinst configure` |
| remove | `prerm remove`, `postrm remove` |

Two consequences, both of which owfeed got wrong at first.

**A package that ships only `postinst` and `prerm` loses its cleanup on removal.** That is
the pair OpenWrt's own `package-pack.mk` generates, so it is easy to assume it is the whole
set — but `postrm` is where a package undoes what it did, and opkg runs it. owfeed emitted
neither `postrm` nor `preinst` until this was measured; the apk side had carried
`post-deinstall` all along, so the same package cleaned up on 25.12 and did not on 24.10.

**There is no `default_postrm`.** `/lib/functions.sh` on 24.10 defines exactly two:

```
default_postinst
default_prerm
```

So `postrm` and `preinst` carry the author's body and nothing else. Sourcing
`functions.sh` to call a helper that does not exist would fail on every removal.

**opkg has no upgrade hook.** An upgrade is a removal followed by an install, which is why
`prerm`/`postrm` appear in the upgrade row. apk's `pre-upgrade` and `post-upgrade` have no
counterpart, so owfeed says so at build time rather than dropping them silently.

# GitHub Actions, as it actually behaves

Not package-manager behaviour, but the same kind of fact: measured against the live
services in July 2026, and each one is a way a correct feed reaches subscribers broken.

## `upload-pages-artifact` drops `.nojekyll` from v4

From v4 the action excludes hidden files by default. `.nojekyll` is a hidden file,
and it is the file that stops Pages running Jekyll over a tree of binaries — Jekyll
then drops every path beginning with a dot or an underscore.

So a feed can be correct at every point owfeed can inspect and be deployed without
the thing that keeps it correct: `owfeed publish` refuses a tree that has no
`.nojekyll`, but what it inspects is the tree, and what subscribers fetch is whatever
the upload step put in the artifact.

```yaml
- uses: actions/upload-pages-artifact@v5
  with:
    path: out
    include-hidden-files: true
```

`owfeed verify` fetches `<feed>/.nojekyll` from the live site and reports OWF514 when
it is not there, because outside the deploy is the only place that answer exists.

## `deploy-pages` needs `actions: read` from v4

It resolves the artifact by id through the Actions API. Declaring a `permissions:`
block sets every scope you did not name to `none`, so the natural-looking

```yaml
permissions: { contents: read, pages: write, id-token: write }
```

is a deploy that cannot read what the job before it uploaded.

## An attestation over several files is one attestation, not several

`actions/attest-build-provenance` with a glob produces a single statement referencing
each subject's digest, not one statement per file. Verification is still per-file —
a verifier looks up its own file's digest among the subjects. Measured: a binary with
one byte appended is rejected, and so is an untampered binary checked against a
different repository. Both fail as `HTTP 404` from the attestations API, which is the
honest answer — there is no attestation for that digest under that repository.

## `--repo` alone is a weaker check than it looks

`gh attestation verify --repo <owner>/<repo>` accepts an attestation produced by *any*
workflow in that repository that holds `attestations: write`. Adding one workflow via
a merged pull request is enough to mint a valid attestation for arbitrary bytes.

`--signer-workflow <owner>/<repo>/.github/workflows/release.yml` is what makes the
check an assertion about how the artifact was built rather than about who owns the
repository it came from.

## `download-artifact` fails on a digest mismatch from v8

Earlier versions logged a warning. This is the right default and worth having.

## A reusable workflow cannot grant itself permissions

`GITHUB_TOKEN` permissions can only be narrowed down a call chain, never widened. A
called workflow that declares `pages: write` on one of its jobs gets it only if the
*calling* job already had it — and the recommended hardening setting is a default
token that is read-only, so the natural-looking

```yaml
jobs:
  publish:
    uses: someone/repo/.github/workflows/feed.yml@v1
```

is a publish job with no permission to publish. The caller has to say so:

```yaml
jobs:
  publish:
    permissions: { contents: read, pages: write, id-token: write, actions: read }
    uses: ...
```

## An environment secret cannot be passed into a reusable workflow

`workflow_call` does not support the `environment` keyword, so the calling job has no
environment and cannot read an environment-scoped secret in order to pass it on. A
reusable workflow that takes its signing key as a declared secret therefore requires
that key to be a repository or organization secret.

The `environment:` on the called job still does the thing worth having — it is what
puts the run behind a required reviewer. The scoping of the secret and the gating of
the job are separate mechanisms, and it is easy to assume one implies the other.
