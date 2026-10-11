#!/usr/bin/env bash
set -euo pipefail

[[ $(uname -s) == Linux ]] || { echo 'credential runtime qualification requires Linux' >&2; exit 1; }
task_directory=$(mktemp -d /tmp/mintclaw-runtime-qual-XXXXXXXX)
task_output=$(mktemp /tmp/mintclaw-runtime-result-XXXXXXXX)
task_unit="mintclaw-runtime-qual-$(basename "$task_directory")"
cleanup() {
  sudo systemctl stop "$task_unit" >/dev/null 2>&1 || true
  sudo rm -rf -- "$task_directory"
  rm -f -- "$task_output"
}
trap cleanup EXIT

go test -race -tags goolm,stdjson -c -o "$task_directory/runtime.test" \
  ./cmd/mintclaw-credential/internal/runtimeimage
sudo chown -R root:root "$task_directory"
sudo chmod 0755 "$task_directory" "$task_directory/runtime.test"
sudo systemd-run --quiet --wait --pipe --unit="$task_unit" \
  --property=PrivateMounts=yes --property=TasksMax=64 \
  --property=MemoryMax=512M --property=RuntimeMaxSec=40 --property=LimitCORE=0 \
  /usr/bin/env -i PATH=/usr/bin:/bin LANG=C.UTF-8 \
  MINTCLAW_SYNTHETIC_RUNTIME_IMAGE_FIXTURE=1 \
  "$task_directory/runtime.test" -test.run='^TestRuntimeImageFixture$' -test.count=1 -test.v -test.timeout=30s \
  | tee "$task_output"
grep -q '^--- PASS: TestRuntimeImageFixture ' "$task_output"
