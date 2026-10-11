#!/usr/bin/env bash
set -euo pipefail

# Run public disposable fixtures as an ordinary user. No VM or host installation.
if [[ "$(uname -s)" != Darwin || "$#" -ne 0 || "$EUID" -eq 0 ]]; then
  echo "credential Mac VM input qualification requires ordinary-user macOS and no arguments" >&2
  exit 1
fi
qualification_source_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
qualification_package_dir="$(bash "$qualification_source_dir/build-credential-mac-vm.sh")"
cleanup() {
  local qualification_status=$?
  trap - EXIT
  rm -rf -- "$qualification_package_dir" || qualification_status=1
  exit "$qualification_status"
}
trap cleanup EXIT
xcrun swiftc -warnings-as-errors -target "$(uname -m)-apple-macosx13.0" \
  -module-cache-path "$qualification_package_dir/module-cache" \
  "$qualification_source_dir/internal/credential-mac-vm/protected-inputs.swift" \
  "$qualification_source_dir/internal/credential-mac-vm/qualification.swift" \
  -o "$qualification_package_dir/qualification"
"$qualification_package_dir/qualification"
if "$qualification_package_dir/mintclaw-credential-vm" caller-selected-path > /dev/null 2>&1; then
  echo "credential Mac VM caller arguments were unexpectedly accepted" >&2
  exit 1
fi
if "$qualification_package_dir/mintclaw-credential-vm" validate > /dev/null 2>&1; then
  echo "credential Mac VM ordinary-user startup was unexpectedly accepted" >&2
  exit 1
fi
echo "PASS: credential Mac VM signed package and ordinary-user denial; host boundary remains unqualified"
