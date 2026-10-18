#!/usr/bin/env bash
set -euo pipefail

: "${COMPILE_TARGETS:?set the space-separated GOOS/GOARCH[/GOARM] targets}"
failed=0
for target in $COMPILE_TARGETS; do
  IFS=/ read -r target_goos target_goarch target_goarm <<< "$target"
  echo "::group::Compile $target (CGO_ENABLED=0)"
  if ! CGO_ENABLED=0 GOOS="$target_goos" GOARCH="$target_goarch" GOARM="${target_goarm:-}" \
    go test -exec=true -run '^$' -tags goolm,stdjson ./...; then
    failed=1
    echo "::error::Compilation failed for $target"
  fi
  echo "::endgroup::"
done
exit "$failed"
