---
title: Install
description: "Build WhatsCLI — WhatsApp for Agents from this fork with cgo and FTS5."
---

# Install

WhatsCLI ships as the `whatscli` executable. Build this fork to get its agent workflows; upstream WACLI packages do not include these additions.

## Homebrew (macOS, Linux)

A WhatsCLI Homebrew tap has not been configured. `brew install openclaw/tap/wacli` installs the original WACLI, not WhatsCLI.

## GitHub releases (raw binaries)

WhatsCLI artifacts use `whatscli_<version>_<os>_<arch>` archive names, with `whatscli` (`whatscli.exe` on Windows), `LICENSE`, and `README.md` inside. Check this fork's [releases](https://github.com/gabrielMalonso/whatscli/releases) for published WhatsCLI assets; older WACLI assets do not certify this rebrand or its capabilities. Publication and signing require separate setup under [Release](release.md).

## Build from source

Install Go 1.27.0 or newer, a C compiler, Node.js 24 or newer, and the pnpm version pinned in `package.json`. The preferred Go toolchain is 1.27.2; cgo and `sqlite_fts5` are required for the normal build.

- macOS: Xcode Command Line Tools.
- Debian / Ubuntu: `sudo apt install build-essential`.
- Fedora / RHEL: `sudo dnf groupinstall "Development Tools"`.

```bash
git clone https://github.com/gabrielMalonso/whatscli.git
cd whatscli
pnpm install --frozen-lockfile
pnpm build
./dist/whatscli --version
```

Add the checkout's `dist/` directory to your `PATH`. The build also creates `dist/wacli` as a compatibility alias, and `pnpm wacli -- <args>` forwards to `pnpm whatscli -- <args>`.

For a direct Go build:

```bash
CGO_ENABLED=1 CGO_CFLAGS="-Wno-error=missing-braces" \
  go build -tags sqlite_fts5 -o dist/whatscli ./cmd/whatscli
```

`make build` wraps the pnpm build; `make check` runs the complete local CI gate. After a WhatsCLI version is published, Go installation must use this fork's module and `cmd/whatscli`, not the upstream module.

## Verify the executable for agent work

```bash
command -v whatscli || exit "$?"
whatscli --version || exit "$?"
whatscli --help || exit "$?"
whatscli --read-only capabilities --agent || exit "$?"
```

These checks do not select an account or connect to WhatsApp. Inspect the resolved path and actual capabilities, not just the release version: local builds with the same version string can contain different commands. The daily workflow needs `-a`/`--for-account`, agent support for `auth status`, `doctor`, `sync status`, `changes list/watch` and the intended task commands; `changes watch` must advertise `encoding=ndjson`.

| Observation | Next step |
| --- | --- |
| `whatscli` not found | Use the intended fork binary's explicit path, or add its directory to this shell's PATH during authorized setup. |
| Unexpected executable path | Inspect shell aliases/functions and PATH order; use the reviewed executable consistently. |
| Unknown `capabilities` command or `-a` flag; required capability absent | Treat the executable as incompatible with this guide. Use a current fork release or rebuild the intended checkout during authorized setup; do not downgrade to legacy sends. |
| New CLI but an older managed follow owner | Coordinate its restart with its owner/supervisor before relying on new IPC features. Replacing a binary on disk does not update a running process. |

For an **authorized local build**, from the intended fork's checkout with the prerequisites above installed:

```bash
pnpm build || exit "$?"
./dist/whatscli --read-only capabilities --agent || exit "$?"
# Optional Bash setup for this shell; rebuilding alone does not install onto PATH.
export PATH="$PWD/dist:$PATH"
hash -r
command -v whatscli || exit "$?"
whatscli --read-only capabilities --agent || exit "$?"
```

Inspect any alias/function that still overrides the PATH entry, or invoke the reviewed executable by its explicit path. Use that same executable for the sync owner and task commands. Do not rebuild, replace binaries, restart shared processes, pair/relink or migrate an archive merely to make a readonly query succeed.

After executable verification, [Agent daily use](agent-daily-use.md) binds an existing account, checks local health with readonly `auth status`/`doctor`, and starts or reuses sync plus the managed change stream before live work. See [Doctor](doctor.md) for diagnostic scope.

## Updating

- **Homebrew tap**: unavailable for WhatsCLI until a fork-owned tap is configured.
- **GitHub release archives**: download the new tarball / ZIP and replace the binary.
- **Source builds**: `git pull && make build` (or `pnpm build`). Local builds use the version compiled into the source tree; release artifacts inject the tag during GoReleaser builds.

The local store format is forward-compatible across point releases; routine upgrades do not require re-pairing.

## Storage

- Default store directory: `~/.local/state/wacli` on Linux (XDG state dir), `~/.wacli` on macOS / Windows. Existing Linux `~/.wacli` directories keep working.
- Override with `--store DIR` or `WACLI_STORE_DIR`.
- The store contains `session.db` (whatsmeow keys), `wacli.db` (messages + FTS), `media/`, and a `LOCK` file. See [Spec](spec.md#storage-layout) for the layout.
- Permissions are owner-only (`0700` on the directory, `0600` on files). Do not relax these — they protect your WhatsApp session keys.

## Related pages

- [Quickstart](quickstart.md) — pair, sync, and send your first message.
- [Auth](auth.md) — `whatscli auth`, `auth status`, `auth logout`.
- [Sync](sync.md) — bootstrap and follow-mode sync, refresh flags.
- [Doctor](doctor.md) — self-checks and connectivity probe.
- [Release](release.md) — release workflow and artifact expectations.
