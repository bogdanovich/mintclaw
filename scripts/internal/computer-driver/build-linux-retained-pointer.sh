#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 || $1 != --output || $2 != /* ]]; then
  echo "usage: $0 --output /absolute/artifact-directory" >&2
  exit 2
fi
output_dir=$2
source_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/retained-pointer" && pwd)
mkdir -p -- "$output_dir"
build_dir=$(mktemp -d "${TMPDIR:-/tmp}/mintclaw-retained-pointer.XXXXXXXX")
trap 'rm -rf -- "$build_dir"' EXIT
export CARGO_TARGET_DIR="$build_dir"

cd -- "$source_dir"
cargo fmt --all --check
cargo test --locked
cargo test --locked --features fixture-fault-injection
cargo build --locked --release
install -m 0500 -- "$build_dir/release/mintclaw-retained-x11-pointer" \
  "$output_dir/mintclaw-retained-x11-pointer"
sha256sum -- Cargo.toml Cargo.lock src/*.rs > "$output_dir/source.sha256"
(
  cd -- "$output_dir"
  sha256sum -- mintclaw-retained-x11-pointer > executable.sha256
)
