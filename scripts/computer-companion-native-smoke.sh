#!/usr/bin/env bash
# Real companion/WSS/native-window qualification. Only disposable fixture
# windows are selected. No install, permission changes or production enablement.
set -euo pipefail

usage() {
  echo 'Usage: computer-companion-native-smoke.sh --driver /absolute/cua-driver --node /absolute/mintclaw-node [--app /absolute/Companion.app] [--test-binary /absolute/node.test] [--capture-dir /absolute/private/directory]'
}

driver_path=''
node_path=''
app_path=''
test_path=''
capture_dir=''
while [[ $# -gt 0 ]]; do
  [[ $# -ge 2 ]] || { usage; exit 2; }
  case "$1" in
    --driver) driver_path=$2 ;;
    --node) node_path=$2 ;;
    --app) app_path=$2 ;;
    --test-binary) test_path=$2 ;;
    --capture-dir) capture_dir=$2 ;;
    *) usage; exit 2 ;;
  esac
  shift 2
done
for executable in "$driver_path" "$node_path"; do
  [[ "$executable" == /* && -f "$executable" && ! -L "$executable" && -x "$executable" ]] || {
    echo 'Supply absolute regular pinned-driver and companion executable paths.' >&2; exit 2;
  }
done
repository_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
qualification_dir=$(mktemp -d "${TMPDIR:-/tmp}/mintclaw-companion-native.XXXXXX")
cleanup() {
  [[ -n "$qualification_dir" && -d "$qualification_dir" ]] && rm -rf -- "$qualification_dir"
}
trap cleanup EXIT
case "$(uname -s)/$(uname -m)" in
  Linux/x86_64)
    driver_sha256=fd922e708099aae311873a6667beb48eaabb8c15c3abec744492db8a96b544ee
    actual_sha256=$(sha256sum "$driver_path" | cut -d ' ' -f 1)
    [[ -n "${DISPLAY:-}" && -n "${XAUTHORITY:-}" && -z "$app_path" ]] || {
      echo 'Select the X11 GUI explicitly with DISPLAY/XAUTHORITY; --app is macOS-only.' >&2; exit 2;
    }
    fixture_path="$repository_root/scripts/internal/computer-native-qualification/fixture_linux.py"
    ;;
  Darwin/x86_64|Darwin/arm64)
    driver_sha256=56bc881744bfb956d44386e6f6ccb2f6fc7f5cef47be25cc056e08265cdf4c7d
    actual_sha256=$(shasum -a 256 "$driver_path" | cut -d ' ' -f 1)
    [[ "$app_path" == /*.app && -d "$app_path" && ! -L "$app_path" ]] || {
      echo 'The actual companion app and its OS grants are required on macOS.' >&2; exit 2;
    }
    cmp -s "$node_path" "$app_path/Contents/MacOS/mintclaw-node" || {
      echo 'App executable differs from the selected companion binary.' >&2; exit 2;
    }
    fixture_path="$qualification_dir/fixture-macos"
    /usr/bin/swiftc "$repository_root/scripts/internal/computer-native-qualification/fixture_macos.swift" -o "$fixture_path"
    ;;
  *) echo 'Qualified drivers currently cover Linux x86_64 X11 and macOS.' >&2; exit 2 ;;
esac
[[ "$actual_sha256" == "$driver_sha256" ]] || { echo 'Pinned driver checksum mismatch.' >&2; exit 2; }
if [[ -z "$test_path" ]]; then
  test_path="$qualification_dir/node.test"
  (cd "$repository_root" && go test -c -tags integration -o "$test_path" ./cmd/mintclaw-node)
fi
[[ "$test_path" == /* && -f "$test_path" && -x "$test_path" ]] || { usage; exit 2; }
[[ $("$test_path" -test.list '^TestCompanionNativeComputerLifecycleOverWSS$') == TestCompanionNativeComputerLifecycleOverWSS ]] || {
  echo 'The supplied test executable lacks native companion qualification.' >&2; exit 2;
}
env MINTCLAW_COMPUTER_NATIVE_DRIVER="$driver_path" \
  MINTCLAW_COMPUTER_NATIVE_SHA256="$driver_sha256" \
  MINTCLAW_COMPUTER_NATIVE_FIXTURE="$fixture_path" \
  MINTCLAW_COMPUTER_COMPANION_APP="$app_path" \
  MINTCLAW_COMPUTER_NATIVE_CAPTURE_DIR="$capture_dir" \
  MINTCLAW_COMPUTER_NATIVE_LOSS_CASE=1 \
  MINTCLAW_NODE_TEST_BINARY="$node_path" \
  "$test_path" -test.run='^TestCompanionNativeComputerLifecycleOverWSS$' -test.v -test.count=1 -test.timeout=3m
