#!/usr/bin/env bash
set -euo pipefail

# No sudo, downloads, account/VM creation, config reads, or secret access.
if [[ "$(uname -s)" != Darwin || "$#" -ne 0 ]]; then
  echo "credential Mac preflight requires macOS and accepts no arguments" >&2
  exit 1
fi
preflight_source_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
preflight_build_dir="$(mktemp -d)"
cleanup() {
  local cleanup_status=$?
  trap - EXIT
  rm -rf -- "$preflight_build_dir" || cleanup_status=1
  exit "$cleanup_status"
}
trap cleanup EXIT
# Compile only this public metadata probe against the installed Apple SDK.
xcrun swiftc "$preflight_source_dir/internal/credential-mac-vm/host-preflight.swift" \
  -o "$preflight_build_dir/host-preflight"
"$preflight_build_dir/host-preflight"
