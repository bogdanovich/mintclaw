#!/usr/bin/env bash
# Opt-in native proof using only disposable fixture windows. No installation,
# permission changes, ambient application discovery, or production enablement.
set -euo pipefail

usage() {
  echo 'Usage: computer-native-qualification.sh --driver /absolute/pinned/cua-driver [--case all|ObservationLifecycle|UnicodeInsertion|UnicodeReplacement|ButtonClick|ScopeDenial]'
  echo 'Optional: MINTCLAW_COMPUTER_NATIVE_TEST_BINARY=/absolute/prebuilt/cua.test'
}

driver_path=''
qualification_case=all
while [[ $# -gt 0 ]]; do
  case "$1" in
    --driver) [[ $# -ge 2 ]] || { usage; exit 2; }; driver_path=$2; shift 2 ;;
    --case) [[ $# -ge 2 ]] || { usage; exit 2; }; qualification_case=$2; shift 2 ;;
    --help|-h) usage; exit 0 ;;
    *) usage; exit 2 ;;
  esac
done

case "$qualification_case" in
  all) test_pattern='^TestNativeQualification$' ;;
  ObservationLifecycle)
    test_pattern="^TestNativeQualification$/^${qualification_case}$" ;;
  UnicodeInsertion|UnicodeReplacement|ButtonClick|ScopeDenial)
    test_pattern="^TestNativeQualification$/^Input$/^${qualification_case}$" ;;
  *) usage; exit 2 ;;
esac
[[ "$driver_path" == /* && -f "$driver_path" && ! -L "$driver_path" && -x "$driver_path" ]] || {
  echo 'Supply an absolute, regular, executable pinned driver path.' >&2
  exit 2
}

case "$(uname -s)/$(uname -m)" in
  Linux/x86_64)
    driver_sha256=fd922e708099aae311873a6667beb48eaabb8c15c3abec744492db8a96b544ee
    driver_actual=$(sha256sum "$driver_path" | cut -d ' ' -f 1)
    [[ -n "${DISPLAY:-}" && -n "${XAUTHORITY:-}" ]] || {
      echo 'Select the intended X11 GUI session explicitly with DISPLAY and XAUTHORITY.' >&2
      exit 2
    }
    /usr/bin/python3 -c 'import gi; gi.require_version("Gtk", "3.0"); gi.require_version("GdkX11", "3.0"); from gi.repository import Gtk, GdkX11' ;;
  Darwin/arm64|Darwin/x86_64)
    driver_sha256=56bc881744bfb956d44386e6f6ccb2f6fc7f5cef47be25cc056e08265cdf4c7d
    driver_actual=$(shasum -a 256 "$driver_path" | cut -d ' ' -f 1) ;;
  *) echo 'Pinned qualification supports Linux x86_64 X11 and macOS only.' >&2; exit 2 ;;
esac
[[ "$driver_actual" == "$driver_sha256" ]] || { echo 'Pinned driver checksum mismatch.' >&2; exit 2; }

repository_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
qualification_dir=$(mktemp -d "${TMPDIR:-/tmp}/mintclaw-native-smoke.XXXXXX")
cleanup() {
  # This exact path was newly created by mktemp, never supplied by the caller.
  [[ -n "$qualification_dir" && -d "$qualification_dir" ]] && rm -rf -- "$qualification_dir"
}
trap cleanup EXIT

if [[ $(uname -s) == Darwin ]]; then
  fixture_path="$qualification_dir/fixture-macos"
  /usr/bin/swiftc "$repository_root/scripts/internal/computer-native-qualification/fixture_macos.swift" \
    -o "$fixture_path"
else
  fixture_path="$repository_root/scripts/internal/computer-native-qualification/fixture_linux.py"
fi

test_binary=${MINTCLAW_COMPUTER_NATIVE_TEST_BINARY:-}
if [[ -z "$test_binary" ]]; then
  test_binary="$qualification_dir/cua.test"
  (cd "$repository_root" && go test -c -o "$test_binary" ./pkg/computer/cua)
fi
[[ "$test_binary" == /* && -f "$test_binary" && -x "$test_binary" ]] || {
  echo 'Qualification test binary must be an absolute executable file.' >&2
  exit 2
}
[[ $("$test_binary" -test.list '^TestNativeQualification$') == TestNativeQualification ]] || {
  echo 'The supplied test binary does not contain native qualification.' >&2
  exit 2
}

printf 'Pinned Cua Driver 0.34.1; native qualification case: %s\n' "$qualification_case"
if [[ "$qualification_case" != all ]]; then
  echo 'This selected-case run does not qualify omitted capabilities.'
fi
env MINTCLAW_COMPUTER_NATIVE_DRIVER="$driver_path" \
  MINTCLAW_COMPUTER_NATIVE_SHA256="$driver_sha256" \
  MINTCLAW_COMPUTER_NATIVE_FIXTURE="$fixture_path" \
  "$test_binary" -test.run="$test_pattern" -test.v -test.count=1 -test.timeout=2m
