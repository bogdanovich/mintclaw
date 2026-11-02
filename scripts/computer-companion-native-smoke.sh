#!/usr/bin/env bash
# Real companion/WSS/native-window qualification. Only disposable fixture
# windows are selected. No install, permission changes or production enablement.
set -euo pipefail

usage() {
  echo 'Usage: computer-companion-native-smoke.sh --driver /absolute/cua-driver --node /absolute/mintclaw-node [--suite companion|gateway|input|ownership] [--app /absolute/Companion.app] [--test-binary /absolute/suite.test] [--capture-dir /absolute/private/directory]'
  echo 'The macOS-only input suite optionally accepts --input-test-binary /absolute/cua.test.'
  echo 'The macOS-only ownership suite optionally accepts --ownership-test-binary /absolute/desktop.test.'
}

driver_path=''
node_path=''
app_path=''
test_path=''
input_test_path=''
ownership_test_path=''
capture_dir=''
suite='companion'
test_timeout='3m'
while [[ $# -gt 0 ]]; do
  [[ $# -ge 2 ]] || { usage; exit 2; }
  case "$1" in
    --driver) driver_path=$2 ;;
    --node) node_path=$2 ;;
    --app) app_path=$2 ;;
    --test-binary) test_path=$2 ;;
    --input-test-binary) input_test_path=$2 ;;
    --ownership-test-binary) ownership_test_path=$2 ;;
    --capture-dir) capture_dir=$2 ;;
    --suite) suite=$2 ;;
    *) usage; exit 2 ;;
  esac
  shift 2
done
case "$suite" in
  companion) test_name=TestCompanionNativeComputerLifecycleOverWSS; test_package=./cmd/mintclaw-node ;;
  gateway)
    test_name=TestGatewayNativeComputerToolsOverWSS; test_package=./pkg/gateway
    [[ -z "$capture_dir" ]] || { echo '--capture-dir is companion-suite only.' >&2; exit 2; }
    ;;
  input)
    test_name=TestCompanionNativeInputMechanismsOverWSS; test_package=./cmd/mintclaw-node
    test_timeout='4m'
    [[ -z "$capture_dir" ]] || { echo '--capture-dir is companion-suite only.' >&2; exit 2; }
    ;;
  ownership)
    test_name=TestCompanionNativeInputOwnershipOverWSS; test_package=./cmd/mintclaw-node
    test_timeout='4m'
    [[ -z "$capture_dir" ]] || { echo '--capture-dir is companion-suite only.' >&2; exit 2; }
    ;;
  *) usage; exit 2 ;;
esac
[[ "$suite" == input || -z "$input_test_path" ]] || {
  echo '--input-test-binary is input-suite only.' >&2; exit 2;
}
[[ "$suite" == ownership || -z "$ownership_test_path" ]] || {
  echo '--ownership-test-binary is ownership-suite only.' >&2; exit 2;
}
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
qualification_os=$(uname -s)
case "$qualification_os/$(uname -m)" in
  Linux/x86_64)
    [[ "$suite" != input && "$suite" != ownership ]] || {
      echo 'Actual-app input/ownership suites are macOS-only.' >&2; exit 2;
    }
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
  test_path="$qualification_dir/$suite.test"
  (cd "$repository_root" && go test -c -tags goolm,stdjson,integration -o "$test_path" "$test_package")
fi
[[ "$test_path" == /* && -f "$test_path" && -x "$test_path" ]] || { usage; exit 2; }
[[ $("$test_path" -test.list "^$test_name$") == "$test_name" ]] || {
  echo 'The supplied test executable lacks the selected native qualification suite.' >&2; exit 2;
}
if [[ "$suite" == input ]]; then
  if [[ -z "$input_test_path" ]]; then
    input_test_path="$qualification_dir/cua-input.test"
    (cd "$repository_root" && go test -c -tags goolm,stdjson -o "$input_test_path" ./pkg/computer/cua)
  fi
  [[ "$input_test_path" == /* && -f "$input_test_path" && ! -L "$input_test_path" && -x "$input_test_path" ]] || {
    echo 'Input qualification requires an absolute regular helper executable.' >&2; exit 2;
  }
  [[ $("$input_test_path" -test.list '^TestNativeQualification$') == TestNativeQualification ]] || {
    echo 'The input helper lacks native fixture qualification.' >&2; exit 2;
  }
fi
if [[ "$suite" == ownership ]]; then
  if [[ -z "$ownership_test_path" ]]; then
    ownership_test_path="$qualification_dir/desktop-ownership.test"
    (cd "$repository_root" && go test -c -tags goolm,stdjson -o "$ownership_test_path" ./pkg/computer/desktop)
  fi
  [[ "$ownership_test_path" == /* && -f "$ownership_test_path" && ! -L "$ownership_test_path" && -x "$ownership_test_path" ]] || {
    echo 'Ownership qualification requires an absolute regular helper executable.' >&2; exit 2;
  }
  [[ $("$ownership_test_path" -test.list '^TestNativeMacInputOwnership$') == TestNativeMacInputOwnership ]] || {
    echo 'The ownership helper lacks native fixture qualification.' >&2; exit 2;
  }
fi
if [[ "$qualification_os" == Darwin ]]; then
  # The controller may belong to an old Background/audit session after a GUI
  # logout/login. Launch the test in the current GUI, like the actual companion.
  # This helper needs no TCC grant and never borrows one for the companion.
  qualification_app="$qualification_dir/MintClaw Native Qualification.app"
  mkdir -p "$qualification_app/Contents/MacOS"
  cp "$repository_root/scripts/internal/computer-native-qualification/launcher_macos.plist" \
    "$qualification_app/Contents/Info.plist"
  /usr/bin/swiftc "$repository_root/scripts/internal/computer-native-qualification/launcher_macos.swift" \
    -o "$qualification_app/Contents/MacOS/qualification-launcher"
  /usr/bin/codesign --sign - "$qualification_app"
  launch_status=0
  /usr/bin/open -n -W --stdout "$qualification_dir/test.stdout" --stderr "$qualification_dir/test.stderr" \
    --env "MINTCLAW_QUALIFICATION_EXECUTABLE=$test_path" \
    --env "MINTCLAW_QUALIFICATION_EXIT_FILE=$qualification_dir/test.exit" \
    --env "MINTCLAW_COMPUTER_NATIVE_DRIVER=$driver_path" \
    --env "MINTCLAW_COMPUTER_NATIVE_SHA256=$driver_sha256" \
    --env "MINTCLAW_COMPUTER_NATIVE_FIXTURE=$fixture_path" \
    --env "MINTCLAW_COMPUTER_NATIVE_INPUT_TEST_BINARY=$input_test_path" \
    --env "MINTCLAW_COMPUTER_NATIVE_OWNERSHIP_TEST_BINARY=$ownership_test_path" \
    --env "MINTCLAW_COMPUTER_COMPANION_APP=$app_path" \
    --env "MINTCLAW_COMPUTER_NATIVE_CAPTURE_DIR=$capture_dir" \
    --env MINTCLAW_COMPUTER_NATIVE_LOSS_CASE=1 \
    --env "MINTCLAW_NODE_TEST_BINARY=$node_path" \
    "$qualification_app" --args -test.run="^$test_name$" -test.v -test.count=1 -test.timeout="$test_timeout" \
    || launch_status=$?
  [[ ! -f "$qualification_dir/test.stdout" ]] || cat "$qualification_dir/test.stdout"
  [[ ! -f "$qualification_dir/test.stderr" ]] || cat "$qualification_dir/test.stderr" >&2
  if [[ "$launch_status" != 0 ]]; then
    echo 'macOS refused the qualification launch; no passing test result is established.' >&2
    exit "$launch_status"
  fi
  # open -W can return zero even when its application failed. Only the child's
  # private numeric receipt establishes the actual suite result.
  test_exit=''
  [[ ! -f "$qualification_dir/test.exit" ]] || test_exit=$(< "$qualification_dir/test.exit")
  if [[ ! "$test_exit" =~ ^(0|[1-9][0-9]{0,2})$ ]] || (( test_exit > 255 )); then
    echo 'GUI qualification produced no valid test exit receipt; refusing to report a pass.' >&2
    exit 1
  fi
  exit "$test_exit"
fi
env MINTCLAW_COMPUTER_NATIVE_DRIVER="$driver_path" \
  MINTCLAW_COMPUTER_NATIVE_SHA256="$driver_sha256" \
  MINTCLAW_COMPUTER_NATIVE_FIXTURE="$fixture_path" \
  MINTCLAW_COMPUTER_COMPANION_APP="$app_path" \
  MINTCLAW_COMPUTER_NATIVE_CAPTURE_DIR="$capture_dir" \
  MINTCLAW_COMPUTER_NATIVE_LOSS_CASE=1 \
  MINTCLAW_NODE_TEST_BINARY="$node_path" \
  "$test_path" -test.run="^$test_name$" -test.v -test.count=1 -test.timeout=3m
