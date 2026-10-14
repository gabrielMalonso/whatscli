#!/usr/bin/env bash
set -euo pipefail

# Replace the alias inode so a running legacy sync owner can keep using it.
alias_tmp="$(mktemp dist/.wacli.XXXXXX)"
trap 'rm -f "$alias_tmp"' EXIT
cp -p dist/whatscli "$alias_tmp"
mv -f "$alias_tmp" dist/wacli
