#!/usr/bin/env bash
set -euo pipefail

# Only a disposable Linux qualification host: no token, browser profile or vault.
test "$(uname -s)" = Linux
qualification_dir="$(mktemp -d)"
qualification_result="$(mktemp)"
qualification_unit="mintclaw-credential-forest-qualification-$(date +%s)-$$"
cleanup() {
  local cleanup_status=$?
  trap - EXIT
  sudo systemctl stop "$qualification_unit.service" >/dev/null 2>&1 || true
  sudo rm -rf -- "$qualification_dir" || cleanup_status=1
  rm -f -- "$qualification_result" || cleanup_status=1
  exit "$cleanup_status"
}
trap cleanup EXIT
env -u OP_SERVICE_ACCOUNT_TOKEN go test -c -race -tags goolm,stdjson \
  -o "$qualification_dir/supervision.test" ./cmd/mintclaw-credential/internal/supervision
sudo chown root:root "$qualification_dir" "$qualification_dir/supervision.test"
sudo chmod 755 "$qualification_dir" "$qualification_dir/supervision.test"
sudo systemd-run --quiet --wait --pipe --collect --service-type=exec \
  --unit "$qualification_unit" --property=Delegate=yes --property=PrivateNetwork=yes --property=PrivateMounts=yes \
  --property=TasksMax=256 \
  --property=MemoryMax=512M --property=RuntimeMaxSec=40s --property=LimitCORE=0 \
  /usr/bin/env -i PATH=/usr/bin:/bin LANG=C.UTF-8 \
  MINTCLAW_SYNTHETIC_WORKER_FOREST_FIXTURE=1 \
  "$qualification_dir/supervision.test" -test.run '^Test(Forest|Network|Launcher)Fixture$' -test.timeout 30s -test.v \
  | tee "$qualification_result"
grep -Fq -- '--- PASS: TestForestFixture (' "$qualification_result"
grep -Fq -- '--- PASS: TestNetworkFixture (' "$qualification_result"
grep -Fq -- '--- PASS: TestLauncherFixture (' "$qualification_result"
