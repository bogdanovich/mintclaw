#!/usr/bin/env bash
set -euo pipefail

# Public software only. No installer, account creation, VM boot or key access.
if [[ "$(uname -s)" != Darwin || "$#" -ne 0 ]]; then
  echo "credential Mac VM build requires macOS and accepts no arguments" >&2
  exit 1
fi
umask 077
package_source_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/internal/credential-mac-vm" && pwd)"
package_output_dir="$(mktemp -d "${TMPDIR:-/tmp}/mintclaw-credential-mac-vm-package.XXXXXXXX")"
cleanup_failure() {
  local package_status=$?
  trap - EXIT
  if [[ "$package_status" -ne 0 ]]; then
    rm -rf -- "$package_output_dir"
  fi
  exit "$package_status"
}
trap cleanup_failure EXIT
package_architecture="$(uname -m)"
case "$package_architecture" in x86_64|arm64) ;; *) exit 1 ;; esac
xcrun swiftc -warnings-as-errors -O -target "$package_architecture-apple-macosx13.0" \
  -module-cache-path "$package_output_dir/module-cache" \
  "$package_source_dir/protected-inputs.swift" "$package_source_dir/launcher.swift" \
  -o "$package_output_dir/mintclaw-credential-vm"
/usr/bin/codesign --force --sign - --options runtime --timestamp=none \
  --entitlements "$package_source_dir/virtualization.entitlements.plist" \
  "$package_output_dir/mintclaw-credential-vm"
/usr/bin/codesign --verify --strict "$package_output_dir/mintclaw-credential-vm"
cp "$package_source_dir/org.mintclaw.credential-vm.plist" "$package_output_dir/"
cp "$package_source_dir/virtualization.entitlements.plist" "$package_output_dir/"
/usr/bin/plutil -lint "$package_output_dir/org.mintclaw.credential-vm.plist" \
  "$package_output_dir/virtualization.entitlements.plist" >/dev/null
rm -rf -- "$package_output_dir/module-cache"
(
  cd -- "$package_output_dir"
  /usr/bin/shasum -a 256 mintclaw-credential-vm org.mintclaw.credential-vm.plist \
    virtualization.entitlements.plist > SHA256SUMS
)
printf '%s\n' "$package_output_dir"
