# The artifact contract

*The directory a build leaves behind, and which every later stage reads. For anyone writing a tool
that produces that directory or reads it.*

- The root is `dist/`, with one directory per architecture, spelled as downloads.openwrt.org spells it.
- An architecture-independent apk goes to `dist/noarch/`, an architecture-independent ipk to `dist/all/`.
- Architecture directories hold only `.apk` and `.ipk` files. Everything else goes at the root.
- Filenames carry no architecture: in a feed, the architecture is the directory.

It is the seam between compiling a package and publishing one. Because of it, those two jobs can
live in different tools without either importing the other.

Owned by owfeed, because owfeed has more than one consumer of it. Producers are free: `owlab build`,
`owfeed build`, `openwrt/gh-action-sdk`, or a hand-written SDK invocation all satisfy it the same
way.

## Shape

```
dist/
├── x86_64/
│   ├── luci-app-example-1.2.0-r1.apk
│   └── luci-app-example_1.2.0-r1_x86_64.ipk
├── aarch64_cortex-a53/
│   └── luci-app-example-1.2.0-r1.apk
├── noarch/                     ← architecture-independent apk
│   └── luci-theme-example-0.11.6-r1.apk
├── all/                        ← architecture-independent ipk
│   └── luci-theme-example_0.11.6-r1_all.ipk
├── manifest.txt                ← written by `owfeed release`, not by a build
├── manifest.txt.sig
└── notes.md
```

## Rules

1. **The root is `dist/`.** Producers may offer a flag; the default is `dist`.

2. **One directory per architecture, named as downloads.openwrt.org spells it:** `x86_64`,
   `aarch64_cortex-a53`, `mips_24kc`. Not an abbreviation, not a target triple, not a tool's
   internal shorthand. `owfeed.lock` records the authoritative set.

3. **The directory is named for the architecture the package itself declares.** For an
   architecture-independent package the two formats disagree on the spelling. The two artifacts
   land in different directories:

   | Format | Package metadata says | Directory |
   |---|---|---|
   | apk | `noarch` | `dist/noarch/` |
   | ipk | `all` | `dist/all/` |

   The translation is the producer's job, done once, at the point where the package's own
   architecture is known.

   <details>
   <summary>Why the split is not normalised away</summary>

   This is not an inconsistency to be normalised away. apk rejects `all` as uninstallable, which
   is why OpenWrt's own `package-pack.mk` translates it. opkg has never heard of `noarch`.

   Writing an `_all.ipk` into `noarch/` would leave the tree disagreeing with the package inside
   it. An index built from that tree would be wrong in a way nothing downstream can detect.

   </details>

4. **Filenames do not carry the architecture.** In a feed, the architecture is the directory.
   - An apk's name is derived from the package name and version alone.
   - `owfeed release` adds the `_<arch>` suffix, and only where flat release assets would otherwise
     collide. Ingest strips it back off.
   - A producer never adds it.

5. **Architecture directories hold nothing but `.apk` and `.ipk` files.** Readers ignore anything
   else, so anything else placed there is invisible and will be assumed absent. `manifest.txt`,
   `manifest.txt.sig`, `notes.md` and an installer script belong at the root of `dist/`. The installer has no fixed name;
   `owfeed release --sign-also <file>` signs it without adding it to the manifest.

6. **Both formats belong in the same tree when both release lines are served.** 25.12 installs the
   `.apk`, 24.10 the `.ipk`. Producing only one abandons half the users, and nothing downstream can
   recover the missing half.

7. **Signing does not move or rename anything.** `owfeed sign` rewrites artifacts in place;
   `owfeed index` fans them out into the published layout. A tree that has been signed is still a
   valid artifact tree.

## Producers and consumers

| Produced by | Consumed by |
|---|---|
| `owlab build` (SDK compilation) | `owfeed sign`: in-package signatures |
| `owfeed build` (mkpkg staging) | `owfeed release`: signed manifest for a GitHub release |
| `openwrt/gh-action-sdk`, hand-written SDK calls | `owfeed index`: the published feed layout |
| a feed's ingest scripts, unpacking an upstream release | a feed's tree checks |

A producer's entire obligation is this document. Nothing in owfeed inspects how the bytes were
made, and nothing should start to.

## Reader obligations

* Walk the immediate subdirectories of the root, skipping dotfiles. The directory name is the
  architecture, verbatim.
* Take `.apk` and `.ipk`; ignore everything else silently. A producer may leave logs or staging
  leftovers, and failing on them would make the contract brittle for no gain.
* Never infer the architecture from a filename. It is the directory, and only the directory.
* An empty tree is an error worth naming. Silence here reads downstream as "nothing to publish",
  which is indistinguishable from a build that failed quietly.
