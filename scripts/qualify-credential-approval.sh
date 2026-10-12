#!/usr/bin/env bash
set -euo pipefail

# Only synthetic passkeys and a disposable owner-store volume are admitted.
repo_root="$(git rev-parse --show-toplevel)"
qualification_dir="$(mktemp -d)"
fixture_volume=""
fixture_image="debian:bookworm-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587"
cleanup() {
  local cleanup_status=$?
  trap - EXIT
  if [[ -n "$fixture_volume" ]]; then
    docker volume rm "$fixture_volume" >/dev/null || cleanup_status=1
  fi
  rm -rf "$qualification_dir" || cleanup_status=1
  exit "$cleanup_status"
}
trap cleanup EXIT

architecture="$(docker info --format '{{.Architecture}}')"
case "$architecture" in
  aarch64|arm64) target_arch=arm64 ;;
  x86_64|amd64) target_arch=amd64 ;;
  *) echo "unsupported Docker qualification architecture" >&2; exit 1 ;;
esac
env -u OP_SERVICE_ACCOUNT_TOKEN CGO_ENABLED=0 GOOS=linux GOARCH="$target_arch" \
  go test -c -tags goolm,stdjson -o "$qualification_dir/approval.test" \
  "$repo_root/internal/credentials/approval"
fixture_volume="$(docker volume create)"
docker run --rm --network none --cap-drop ALL --cap-add CHOWN \
  --security-opt no-new-privileges=true --entrypoint /bin/sh \
  --mount "type=volume,src=$fixture_volume,dst=/qual-approval" \
  "$fixture_image" -ec '
    test ! -e /qual-approval/synthetic-only
    mkdir /qual-approval/state /qual-approval/rejected
    chmod 700 /qual-approval/state /qual-approval/rejected
    chown 65532:65532 /qual-approval/state /qual-approval/rejected
    chmod 755 /qual-approval
    printf "mintclaw-synthetic-approval-v1\n" >/qual-approval/synthetic-only
    chmod 444 /qual-approval/synthetic-only
  '
fixture_status=0
docker run --rm --network none --cap-drop ALL --user 65532:65532 \
  --security-opt no-new-privileges=true --ulimit core=0:0 --pids-limit 128 --memory 256m \
  --mount "type=bind,src=$qualification_dir/approval.test,dst=/qual/approval.test,readonly" \
  --mount "type=volume,src=$fixture_volume,dst=/qual-approval" \
  -e MINTCLAW_SYNTHETIC_APPROVAL_FIXTURE=1 \
  --entrypoint /qual/approval.test "$fixture_image" \
  -test.run '^TestProtectedOwnerStoreFixture$' -test.timeout 30s -test.v \
  >"$qualification_dir/output" 2>&1 || fixture_status=$?
if [[ "$fixture_status" == 0 ]]; then
  case "$(cat "$qualification_dir/output")" in
    *'--- PASS: TestProtectedOwnerStoreFixture ('*) ;;
    *) echo "protected owner fixture did not execute its expected tests" >&2; exit 1 ;;
  esac
fi
cat "$qualification_dir/output"
exit "$fixture_status"
