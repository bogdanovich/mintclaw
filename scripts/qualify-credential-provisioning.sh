#!/usr/bin/env bash
set -euo pipefail

# All privileged writes happen in the disposable container. Never run the
# credential command on the development host or accept a real token here.
repo_root="$(git rev-parse --show-toplevel)"
qualification_dir="$(mktemp -d)"
trap 'rm -rf "$qualification_dir"' EXIT
architecture="$(docker info --format '{{.Architecture}}')"
case "$architecture" in
  aarch64|arm64) target_arch=arm64 ;;
  x86_64|amd64) target_arch=amd64 ;;
  *) echo "unsupported Docker qualification architecture" >&2; exit 1 ;;
esac

env -u OP_SERVICE_ACCOUNT_TOKEN CGO_ENABLED=0 GOOS=linux GOARCH="$target_arch" \
  go build -tags goolm,stdjson -o "$qualification_dir/mintclaw-credential" "$repo_root/cmd/mintclaw-credential"
docker build --quiet -t mintclaw-credential-qualification:local \
  "$repo_root/runtime/qualification/credential-provisioning"
docker run --rm --network none \
  --mount "type=bind,src=$qualification_dir/mintclaw-credential,dst=/qual/mintclaw-credential,readonly" \
  -e MINTCLAW_DISPOSABLE_CREDENTIAL_QUALIFICATION=1 mintclaw-credential-qualification:local
