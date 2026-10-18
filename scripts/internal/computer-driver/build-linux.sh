#!/usr/bin/env bash
# Build one exact private dependency checkout. No driver install or desktop use.
set -euo pipefail

source_dir=''
output_dir=''
while [[ $# -gt 0 ]]; do
  [[ $# -ge 2 ]] || { echo 'Expected --source PATH --output PATH.' >&2; exit 2; }
  case "$1" in
    --source) source_dir=$2 ;;
    --output) output_dir=$2 ;;
    *) echo 'Unknown build option.' >&2; exit 2 ;;
  esac
  shift 2
done
[[ $(uname -s)/$(uname -m) == Linux/x86_64 ]] || {
  echo 'This private build qualifies Linux x86_64 only.' >&2; exit 2;
}
[[ "$source_dir" == /* && -d "$source_dir" && ! -L "$source_dir" && "$output_dir" == /* ]] || {
  echo 'Supply an explicit private source checkout and a new absolute output directory.' >&2; exit 2;
}
[[ ! -e "$output_dir" && ! -L "$output_dir" ]] || { echo 'Output must not already exist.' >&2; exit 2; }
[[ $(git -C "$source_dir" rev-parse --show-toplevel) == "$source_dir" ]] || exit 2
[[ $(git -C "$source_dir" rev-parse HEAD) == 0c69d9a2c8abbb5051a2df4a23f0fa166d5385dc ]] || {
  echo 'Pinned Cua source commit mismatch.' >&2; exit 2;
}
git -C "$source_dir" diff --quiet HEAD --
[[ -z $(git -C "$source_dir" ls-files --others --exclude-standard) ]] || {
  echo 'Build requires a clean dedicated source checkout.' >&2; exit 2;
}
helper_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
patch_path="$helper_dir/atspi-utf8-length.patch"
native_path="$source_dir/libs/cua-driver/rust/crates/platform-linux/src/atspi/native.rs"
git -C "$source_dir" apply --unidiff-zero --whitespace=error-all --check "$patch_path"
git -C "$source_dir" apply --unidiff-zero --whitespace=error-all "$patch_path"
native_sha=$(sha256sum "$native_path" | cut -d ' ' -f 1)

export CARGO_INCREMENTAL=0 CARGO_BUILD_JOBS=2
export CARGO_PROFILE_RELEASE_DEBUG=0 CUA_TELEMETRY=0 DO_NOT_TRACK=1
(
  cd "$source_dir/libs/cua-driver/rust"
  cargo +1.97.1 test --locked --release -p platform-linux --lib \
    --target x86_64-unknown-linux-gnu --features portal-input mintclaw_utf8_length_tests
  cargo +1.97.1 build --locked --release -p cua-driver \
    --target x86_64-unknown-linux-gnu --features cua-driver/portal-input
)
[[ $(sha256sum "$native_path" | cut -d ' ' -f 1) == "$native_sha" ]] || {
  echo 'Pinned patched source changed during build.' >&2; exit 1;
}
git -C "$source_dir" diff --check
mkdir -m 700 "$output_dir"
install -m 700 "$source_dir/libs/cua-driver/rust/target/x86_64-unknown-linux-gnu/release/cua-driver" \
  "$output_dir/cua-driver"
install -m 600 "$source_dir/libs/cua-driver/rust/THIRD_PARTY_NOTICES.md" "$output_dir/THIRD_PARTY_NOTICES.md"
install -m 600 "$source_dir/LICENSE.md" "$output_dir/LICENSE-UPSTREAM.md"
python3 "$helper_dir/artifact.py" emit --source "$source_dir" --patch "$patch_path" \
  --binary "$output_dir/cua-driver" --output "$output_dir/manifest.json"
python3 "$helper_dir/artifact.py" verify --manifest "$output_dir/manifest.json" \
  --patch "$patch_path" --binary "$output_dir/cua-driver"
"$output_dir/cua-driver" --version
