#!/usr/bin/env bash
set -euo pipefail

# Three disposable volumes contain only generated synthetic service material.
repo_root="$(git rev-parse --show-toplevel)"
qualification_dir="$(mktemp -d)"
fixture_volumes=()
fixture_image="debian:bookworm-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587"
cleanup() {
  local cleanup_status=$?
  trap - EXIT
  for fixture_volume in "${fixture_volumes[@]}"; do
    docker volume rm "$fixture_volume" >/dev/null || cleanup_status=1
  done
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
  go test -c -tags goolm,stdjson -o "$qualification_dir/startup.test" \
  "$repo_root/cmd/mintclaw-credential/internal/startup"
for _ in 1 2 3; do fixture_volumes+=("$(docker volume create)"); done
data_volume="${fixture_volumes[0]}"
config_volume="${fixture_volumes[1]}"
console_volume="${fixture_volumes[2]}"
fixture_status=0
docker run --rm --network none --cap-drop ALL --cap-add CHOWN \
  --security-opt no-new-privileges=true --ulimit core=0:0 --pids-limit 128 --memory 256m \
  --mount "type=bind,src=$qualification_dir/startup.test,dst=/qual/startup.test,readonly" \
  --mount "type=volume,src=$data_volume,dst=/qual-approval-service" \
  --mount "type=volume,src=$config_volume,dst=/etc" \
  --mount "type=volume,src=$console_volume,dst=/run/mintclaw-credential-owner" \
  -e MINTCLAW_SYNTHETIC_APPROVAL_SERVICE_FIXTURE=setup \
  --entrypoint /bin/sh "$fixture_image" -ec '
    test ! -e /qual-approval-service/synthetic-only
    mkdir -m 700 /qual-approval-service/state
    chmod 755 /qual-approval-service /etc
    chmod 700 /run/mintclaw-credential-owner
    chown 65532:65532 /qual-approval-service/state /run/mintclaw-credential-owner
    mkdir -m 750 /etc/mintclaw-credential
    chown 0:65532 /etc/mintclaw-credential
    printf "root:x:0:0:Root:/root:/bin/sh\nmintclaw-credential:x:65532:65532:synthetic fixture:/nonexistent:/usr/sbin/nologin\n" >/etc/passwd
    printf "root:x:0:\nmintclaw-credential:x:65532:\n" >/etc/group
    chmod 644 /etc/passwd /etc/group
    printf "mintclaw-approval-service-fixture-v1\n" >/qual-approval-service/synthetic-only
    chmod 444 /qual-approval-service/synthetic-only
    exec /qual/startup.test -test.run "^TestApprovalServiceFixture$" -test.timeout 30s -test.v
  ' >"$qualification_dir/setup-output" 2>&1 || fixture_status=$?
if [[ "$fixture_status" != 0 ]]; then cat "$qualification_dir/setup-output"; exit "$fixture_status"; fi
case "$(cat "$qualification_dir/setup-output")" in
  *'--- PASS: TestApprovalServiceFixture ('*) ;;
  *) echo "synthetic service setup did not execute its expected tests" >&2; exit 1 ;;
esac
cat "$qualification_dir/setup-output"

docker run --rm --network none --cap-drop ALL --user 65532:65532 \
  --security-opt no-new-privileges=true --ulimit core=0:0 --pids-limit 128 --memory 256m \
  --mount "type=bind,src=$qualification_dir/startup.test,dst=/qual/startup.test,readonly" \
  --mount "type=volume,src=$data_volume,dst=/qual-approval-service" \
  --mount "type=volume,src=$config_volume,dst=/etc,readonly" \
  --mount "type=volume,src=$console_volume,dst=/run/mintclaw-credential-owner" \
  -e MINTCLAW_SYNTHETIC_APPROVAL_SERVICE_FIXTURE=serve \
  --entrypoint /qual/startup.test "$fixture_image" \
  -test.run '^TestApprovalServiceFixture$' -test.timeout 60s -test.v \
  >"$qualification_dir/service-output" 2>&1 || fixture_status=$?
if [[ "$fixture_status" == 0 ]]; then
  case "$(cat "$qualification_dir/service-output")" in
    *'--- PASS: TestApprovalServiceFixture ('*) ;;
    *) echo "protected service fixture did not execute its expected tests" >&2; exit 1 ;;
  esac
fi
cat "$qualification_dir/service-output"
exit "$fixture_status"
