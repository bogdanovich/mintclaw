#!/usr/bin/env bash
# Build a PRIVATE library-test probe, not an installable/admitted driver.
set -euo pipefail
source_dir=''
output_dir=''
candidate=window-keys
while [[ $# -gt 0 ]]; do
  [[ $# -ge 2 ]] || { echo 'Expected --source PATH --output PATH.' >&2; exit 2; }
  case "$1" in
    --source) source_dir=$2 ;;
    --output) output_dir=$2 ;;
    --candidate) candidate=$2 ;;
    *) echo 'Unknown probe option.' >&2; exit 2 ;;
  esac
  shift 2
done
case "$candidate" in window-keys|native-keyboard) ;; *) echo 'Unknown private candidate.' >&2; exit 2 ;; esac
[[ $(uname -s)/$(uname -m) == Linux/x86_64 ]] || exit 2
[[ "$source_dir" == /* && -d "$source_dir" && ! -L "$source_dir" && "$output_dir" == /* ]] || exit 2
[[ ! -e "$output_dir" && ! -L "$output_dir" ]] || exit 2
[[ $(git -C "$source_dir" rev-parse --show-toplevel) == "$source_dir" ]] || exit 2
[[ $(git -C "$source_dir" rev-parse HEAD) == 0c69d9a2c8abbb5051a2df4a23f0fa166d5385dc ]] || exit 2
git -C "$source_dir" diff --quiet HEAD --
[[ -z $(git -C "$source_dir" ls-files --others --exclude-standard) ]] || exit 2
helper_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
patch_path="$helper_dir/window-keys.patch"
module_path="$source_dir/libs/cua-driver/rust/crates/platform-linux/src/input/mintclaw_window_keys.rs"
[[ ! -e "$module_path" && ! -L "$module_path" ]] || exit 2
git -C "$source_dir" apply --unidiff-zero --whitespace=error-all --check "$patch_path"
git -C "$source_dir" apply --unidiff-zero --whitespace=error-all "$patch_path"
install -m 600 "$helper_dir/window_keys.rs" "$module_path"
module_sha=$(sha256sum "$module_path" | cut -d ' ' -f 1)
features=portal-input,mintclaw-window-keys
native_module_path="$source_dir/libs/cua-driver/rust/crates/platform-linux/src/input/mintclaw_native_x11_io.rs"
keyboard_module_path="$source_dir/libs/cua-driver/rust/crates/platform-linux/src/input/mintclaw_native_keyboard.rs"
qualification_module_path="$source_dir/libs/cua-driver/rust/crates/platform-linux/src/input/mintclaw_native_keyboard_qualification.rs"
if [[ "$candidate" == native-keyboard ]]; then
  [[ ! -e "$native_module_path" && ! -L "$native_module_path" ]] || exit 2
  [[ ! -e "$keyboard_module_path" && ! -L "$keyboard_module_path" ]] || exit 2
  [[ ! -e "$qualification_module_path" && ! -L "$qualification_module_path" ]] || exit 2
  git -C "$source_dir" apply --unidiff-zero --whitespace=error-all --check "$helper_dir/native-keyboard.patch"
  git -C "$source_dir" apply --unidiff-zero --whitespace=error-all "$helper_dir/native-keyboard.patch"
  install -m 600 "$helper_dir/native_x11_io.rs" "$native_module_path"
  install -m 600 "$helper_dir/native_keyboard.rs" "$keyboard_module_path"
  install -m 600 "$helper_dir/native_keyboard_qualification.rs" "$qualification_module_path"
  native_module_sha=$(sha256sum "$native_module_path" | cut -d ' ' -f 1)
  keyboard_module_sha=$(sha256sum "$keyboard_module_path" | cut -d ' ' -f 1)
  qualification_module_sha=$(sha256sum "$qualification_module_path" | cut -d ' ' -f 1)
  features+=,mintclaw-native-keyboard
fi
export CARGO_INCREMENTAL=0 CARGO_BUILD_JOBS=2 CARGO_PROFILE_RELEASE_DEBUG=0
export CUA_TELEMETRY=0 DO_NOT_TRACK=1
cd "$source_dir/libs/cua-driver/rust"
probe_path=$(cargo +1.97.1 test --locked --release -p platform-linux --lib \
  --target x86_64-unknown-linux-gnu --features "$features" --no-run --message-format=json |
  python3 -c 'import json, sys
paths = [item["executable"] for line in sys.stdin if (item := json.loads(line)).get("reason") == "compiler-artifact"
         and item.get("executable") and item.get("target", {}).get("name") == "platform_linux"]
if len(paths) != 1: raise SystemExit("Expected exactly one private library-test executable")
print(paths[0])')
[[ "$probe_path" == "$source_dir/libs/cua-driver/rust/target/x86_64-unknown-linux-gnu/release/deps/"platform_linux-* \
  && -f "$probe_path" && ! -L "$probe_path" ]] || exit 2
listing=$("$probe_path" --list)
[[ $(printf '%s\n' "$listing" | awk '/^input::mintclaw_window_keys::tests::.*: test$/ {n++} END {print n+0}') == 6 ]] || exit 1
[[ $(printf '%s\n' "$listing" | awk '/^input::mintclaw_window_keys::native::.*: test$/ {n++} END {print n+0}') == 4 ]] || exit 1
"$probe_path" input::mintclaw_window_keys::tests:: --test-threads=1
if [[ "$candidate" == native-keyboard ]]; then
  [[ $(printf '%s\n' "$listing" | awk '/^input::mintclaw_native_x11_io::tests::.*: test$/ {n++} END {print n+0}') == 9 ]] || exit 1
  [[ $(printf '%s\n' "$listing" | awk '/^input::mintclaw_native_keyboard::native::.*: test$/ {n++} END {print n+0}') == 10 ]] || exit 1
  [[ $(printf '%s\n' "$listing" | awk '/^input::mintclaw_native_keyboard::qualification::.*: test$/ {n++} END {print n+0}') == 1 ]] || exit 1
  "$probe_path" input::mintclaw_native_x11_io::tests:: --test-threads=1
  [[ $(sha256sum "$native_module_path" | cut -d ' ' -f 1) == "$native_module_sha" ]] || exit 1
  [[ $(sha256sum "$helper_dir/native_x11_io.rs" | cut -d ' ' -f 1) == "$native_module_sha" ]] || exit 1
  [[ $(sha256sum "$keyboard_module_path" | cut -d ' ' -f 1) == "$keyboard_module_sha" ]] || exit 1
  [[ $(sha256sum "$helper_dir/native_keyboard.rs" | cut -d ' ' -f 1) == "$keyboard_module_sha" ]] || exit 1
  [[ $(sha256sum "$qualification_module_path" | cut -d ' ' -f 1) == "$qualification_module_sha" ]] || exit 1
  [[ $(sha256sum "$helper_dir/native_keyboard_qualification.rs" | cut -d ' ' -f 1) == "$qualification_module_sha" ]] || exit 1
fi
[[ $(sha256sum "$module_path" | cut -d ' ' -f 1) == "$module_sha" ]] || exit 1
[[ $(sha256sum "$helper_dir/window_keys.rs" | cut -d ' ' -f 1) == "$module_sha" ]] || exit 1
git -C "$source_dir" diff --check
mkdir -m 700 "$output_dir"
install -m 700 "$probe_path" "$output_dir/window-keys.test"
install -m 600 "$source_dir/LICENSE.md" "$output_dir/LICENSE-UPSTREAM.md"
install -m 600 "$source_dir/libs/cua-driver/rust/THIRD_PARTY_NOTICES.md" "$output_dir/THIRD_PARTY_NOTICES.md"
# A build receipt only, never accepted by artifact.py or production admission.
(
  printf '%s\n' "mintclaw-${candidate}-probe-v1" 'source=0c69d9a2c8abbb5051a2df4a23f0fa166d5385dc' \
    "rust=1.97.1 target=x86_64-unknown-linux-gnu features=$features" 'NOT A QUALIFIED DRIVER'
  sha256sum "$patch_path" "$helper_dir/window_keys.rs" Cargo.lock "$output_dir/window-keys.test"
  if [[ "$candidate" == native-keyboard ]]; then
    sha256sum "$helper_dir/native-keyboard.patch" "$helper_dir/native_x11_io.rs" \
      "$helper_dir/native_keyboard.rs" "$helper_dir/native_keyboard_qualification.rs"
    sha256sum "$helper_dir/native-keyboard-fixture.py" "$helper_dir/native-keyboard-gtk-fixture.py"
  fi
) > "$output_dir/build-receipt.txt"
chmod 600 "$output_dir/build-receipt.txt"
