#!/usr/bin/env bash
set -euo pipefail

# Private-console qualification is confined to this disposable, synthetic OS.
repo_root="$(git rev-parse --show-toplevel)"
qualification_dir="$(mktemp -d)"
trap 'rm -rf "$qualification_dir"' EXIT
fixture_image="debian:bookworm-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587"
architecture="$(docker info --format '{{.Architecture}}')"
case "$architecture" in
  aarch64|arm64) target_arch=arm64 ;;
  x86_64|amd64) target_arch=amd64 ;;
  *) echo "unsupported Docker qualification architecture" >&2; exit 1 ;;
esac
env -u OP_SERVICE_ACCOUNT_TOKEN CGO_ENABLED=0 GOOS=linux GOARCH="$target_arch" \
  go test -c -tags goolm,stdjson -o "$qualification_dir/approval.test" \
  "$repo_root/cmd/mintclaw-credential/internal/approval"
fixture_status=0
docker run --rm --network none --cap-drop ALL --cap-add CHOWN --cap-add SETUID --cap-add SETGID \
  --cap-add DAC_OVERRIDE --cap-add KILL --cap-add FOWNER \
  --security-opt no-new-privileges=true --ulimit core=0:0 --pids-limit 128 --memory 256m \
  --mount "type=bind,src=$qualification_dir/approval.test,dst=/qual/approval.test,readonly" \
  -e MINTCLAW_SYNTHETIC_PHONE_CONSOLE_FIXTURE=1 --entrypoint /bin/sh "$fixture_image" -ec '
    test ! -e /run/mintclaw-credential-owner
    mkdir -m 755 /qual-phone
    printf "mintclaw-synthetic-phone-v1\n" >/qual-phone/synthetic-only
    chmod 444 /qual-phone/synthetic-only
    printf "mintclaw-credential:x:65532:65532:synthetic fixture:/nonexistent:/usr/sbin/nologin\n" >>/etc/passwd
    mkdir -m 700 /run/mintclaw-credential-owner
    chown 65532:65532 /run/mintclaw-credential-owner
    exec /qual/approval.test -test.run "^TestPrivateConsoleFixture$" -test.timeout 30s -test.v
  ' >"$qualification_dir/console-output" 2>&1 || fixture_status=$?
if [[ "$fixture_status" == 0 ]]; then
  case "$(cat "$qualification_dir/console-output")" in
    *'--- PASS: TestPrivateConsoleFixture ('*) ;;
    *) echo "private console fixture did not execute its expected tests" >&2; exit 1 ;;
  esac
fi
cat "$qualification_dir/console-output"
if [[ "$fixture_status" != 0 ]]; then exit "$fixture_status"; fi

# Install the repository's pinned Node dependencies and Chromium before running.
env -u OP_SERVICE_ACCOUNT_TOKEN MINTCLAW_SYNTHETIC_PHONE_BROWSER_FIXTURE=1 \
  go test -tags goolm,stdjson -run '^TestPhoneBrowserFixture$' -count=1 -timeout=110s \
  "$repo_root/cmd/mintclaw-credential/internal/approval"
