# Release

Read when: preparing WhatsCLI artifacts or reviewing inherited release infrastructure.

WhatsCLI — WhatsApp for Agents is maintained in [gabrielMalonso/whatscli](https://github.com/gabrielMalonso/whatscli). The repository was renamed from `gabrielMalonso/wacli`; this rename does not publish a release automatically.

## Local builds

```bash
pnpm build
./dist/whatscli --version
```

The primary executable is `whatscli`. Local builds additionally create `dist/wacli` for existing scripts. Both use the same account/store configuration and databases; no rebrand data migration is needed.

## Artifact contract

- `.goreleaser.yaml` builds macOS `whatscli` binaries; `.goreleaser-linux-windows.yaml` builds Linux and Windows binaries.
- Archives are named `whatscli_<version>_<os>_<arch>.tar.gz`, or `.zip` on Windows, and contain `whatscli` (`whatscli.exe` on Windows), `LICENSE`, and `README.md`.
- `checksums.txt` remains the checksum asset. The original MIT notice and WhatsCLI modification copyright travel with every archive.
- GoReleaser build IDs and the existing release linker marker remain unchanged so internal verification and CI selectors stay compatible.
- Source version stays in `cmd/whatscli/root.go`; release tooling reads it from that path.
- The preferred build toolchain remains Go 1.27.1, with the Go 1.27.0 source minimum declared in `go.mod`.

A credential-free macOS snapshot can be built with `make snapshot` on macOS. For Linux and Windows, use the split config with the matching C cross-compilers:

```bash
goreleaser release --snapshot --clean --skip=publish --config .goreleaser-linux-windows.yaml
```

## Publication status

The inherited `.github/workflows/release.yml` relies on OpenClaw’s shared release workflow, Developer ID identity, and Homebrew tap. It is restricted to the upstream repository and cannot publish this fork. The legacy local publisher is also unavailable for WhatsCLI; its signing checks are retained as upstream reference code, not as a claim that WhatsCLI is signed by OpenClaw.

Before enabling publication, configure a fork-owned release workflow, macOS signing/notarization identity and any desired Homebrew tap, then update the corresponding verifiers. No WhatsCLI tap, hosted documentation domain, or signing identity is asserted by this rebrand. Those are separate maintainer decisions, not dependencies for local use.

Release/tag/deploy actions require separate explicit authorization. Never dispatch upstream publication or update `openclaw/homebrew-tap` from this fork. Follow [fork maintenance](fork-maintenance.md) for scoped PRs and upstream updates.
