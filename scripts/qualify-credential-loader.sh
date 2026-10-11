#!/usr/bin/env bash
set -euo pipefail

# Credentials are synthetic and created only in a disposable Docker volume.
# Never run the fixture on the development host or forward a vault token.
repo_root="$(git rev-parse --show-toplevel)"
qualification_dir="$(mktemp -d)"
fixture_volume=""
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
  go test -c -tags goolm,stdjson -o "$qualification_dir/startup.test" \
  "$repo_root/cmd/mintclaw-credential/internal/startup"
docker build --quiet -t mintclaw-credential-loader-qualification:local \
  "$repo_root/runtime/qualification/credential-loader"
fixture_volume="$(docker volume create)"
docker run --rm --network none --cap-drop ALL --cap-add CHOWN \
  --security-opt no-new-privileges=true --entrypoint python3 \
  --mount "type=volume,src=$fixture_volume,dst=/qual-credentials" \
  -e MINTCLAW_SYNTHETIC_LOADER_SETUP=1 \
  mintclaw-credential-loader-qualification:local /qual/setup.py

run_fixture() {
  local mode="$1" mount_access="$2" identity="$3" no_new_privileges="$4"
  local fixture_status=0 fixture_output="$qualification_dir/output-$mode"
  # Bash 3.2 treats an empty array as unset under nounset. Always name the
  # intended primary group; the loader permits that redundant membership.
  local group_options=(--group-add "${identity#*:}")
  if [[ "$mode" == supplementary ]]; then
    group_options=(--group-add 1)
  fi
  docker run --rm --network none --cap-drop ALL --user "$identity" \
    "${group_options[@]}" \
    --security-opt "no-new-privileges=$no_new_privileges" --ulimit core=0:0 \
    --pids-limit 128 --memory 256m \
    --mount "type=bind,src=$qualification_dir/startup.test,dst=/qual/startup.test,readonly" \
    --mount "type=volume,src=$fixture_volume,dst=/qual-credentials$mount_access" \
    -e "MINTCLAW_CREDENTIAL_DELIVERY_FIXTURE=$mode" \
    -e OP_SERVICE_ACCOUNT_TOKEN=MINTCLAW_SYNTHETIC_ENV_TOKEN_v1 \
    mintclaw-credential-loader-qualification:local \
    -test.run '^TestCredentialDeliveryFixture$' -test.timeout 30s -test.v \
    >"$fixture_output" 2>&1 || fixture_status=$?
  python3 - "$fixture_output" "$fixture_status" <<'PY'
import base64
import pathlib
import sys

data = pathlib.Path(sys.argv[1]).read_bytes()
for canary in (b"MINTCLAW_SYNTHETIC_LOADER_TOKEN_v1", b"MINTCLAW_SYNTHETIC_ENV_TOKEN_v1"):
    encoded = (canary, base64.b64encode(canary), base64.urlsafe_b64encode(canary))
    encoded += tuple(value.rstrip(b"=") for value in encoded[1:])
    encoded += (canary.hex().encode(), canary.hex().upper().encode())
    if any(value in data for value in encoded):
        raise SystemExit("credential loader fixture disclosed a synthetic credential")
if int(sys.argv[2]) == 0 and b"--- PASS: TestCredentialDeliveryFixture (" not in data:
    raise SystemExit("credential loader fixture did not execute its expected tests")
sys.stdout.buffer.write(data)
PY
  return "$fixture_status"
}

run_fixture readonly ',readonly' 65532:65532 true
run_fixture writable '' 65532:65532 true
run_fixture unhardened ',readonly' 65532:65532 false
run_fixture root ',readonly' 0:0 true
run_fixture root-group ',readonly' 65532:0 true
run_fixture supplementary ',readonly' 65532:65532 true
echo "all six synthetic credential loader modes passed"
