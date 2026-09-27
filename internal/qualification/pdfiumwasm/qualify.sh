#!/usr/bin/env bash
set -euo pipefail

module_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repository_root=$(cd "$module_dir/../../.." && pwd)
temporary_dir=$(mktemp -d "${TMPDIR:-/tmp}/mintclaw-pdfium-qualification.XXXXXX")
trap 'rm -rf "$temporary_dir"' EXIT

cd "$module_dir"
CGO_ENABLED=0 go test -buildvcs=false -c -o "$temporary_dir/baseline.test" ./baseline
CGO_ENABLED=0 go test -buildvcs=false -c -o "$temporary_dir/candidate.test" .

baseline_size=$(wc -c < "$temporary_dir/baseline.test" | tr -d ' ')
candidate_size=$(wc -c < "$temporary_dir/candidate.test" | tr -d ' ')
binary_growth=$((candidate_size - baseline_size))
maximum_binary_growth=$((64 * 1024 * 1024))
if ((binary_growth <= 0 || binary_growth > maximum_binary_growth)); then
  printf 'candidate binary growth %d is outside 1..%d bytes\n' "$binary_growth" "$maximum_binary_growth" >&2
  exit 1
fi
printf 'pdfium wasm binary evidence: baseline=%d candidate=%d growth=%d bytes\n' \
  "$baseline_size" "$candidate_size" "$binary_growth"

dependency_graph=$(CGO_ENABLED=0 go list -buildvcs=false -deps -test .)
for forbidden in \
  github.com/klippa-app/go-pdfium/internal/implementation_cgo \
  github.com/klippa-app/go-pdfium/multi_threaded \
  github.com/klippa-app/go-pdfium/single_threaded \
  github.com/hashicorp/go-plugin \
  google.golang.org/grpc \
  runtime/cgo; do
  if grep -Fxq "$forbidden" <<<"$dependency_graph"; then
    printf 'forbidden reachable qualification dependency: %s\n' "$forbidden" >&2
    exit 1
  fi
done

production_graph=$(cd "$repository_root" && go list -buildvcs=false -tags goolm,stdjson -deps ./cmd/mintclaw)
for required in github.com/klippa-app/go-pdfium/webassembly github.com/tetratelabs/wazero; do
	if ! grep -Fxq "$required" <<<"$production_graph"; then
		printf 'admitted portable dependency is absent from production graph: %s\n' "$required" >&2
		exit 1
	fi
done
portable_graph=$(cd "$repository_root" && CGO_ENABLED=0 go list -buildvcs=false -deps ./pkg/document)
for forbidden in \
	github.com/klippa-app/go-pdfium/internal/implementation_cgo \
	github.com/klippa-app/go-pdfium/multi_threaded \
	github.com/klippa-app/go-pdfium/single_threaded \
	github.com/hashicorp/go-plugin \
	google.golang.org/grpc \
	runtime/cgo; do
	if grep -Fxq "$forbidden" <<<"$portable_graph"; then
		printf 'forbidden production PDFium dependency: %s\n' "$forbidden" >&2
		exit 1
	fi
done

CGO_ENABLED=0 go test -buildvcs=false -count=1 -v ./...
