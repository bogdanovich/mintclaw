#!/bin/sh

set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
runtime=$(go env GOOS)/$(go env GOARCH)
if [ "$runtime" != "linux/amd64" ]; then
	echo "document worker smoke: skipped on $runtime"
	exit 0
fi

smoke_root=$(mktemp -d "${TMPDIR:-/tmp}/mintclaw-document-worker.XXXXXX")
trap 'rm -rf -- "$smoke_root"' EXIT HUP INT TERM

binary="$smoke_root/mintclaw"
report="$smoke_root/report.json"
mintclaw_home="$smoke_root/home"

cd "$repo_root"
go build -o "$binary" ./cmd/mintclaw
MINTCLAW_HOME="$mintclaw_home" "$binary" document acquire \
	--input pkg/document/testdata/acquisition-fixture.pdf \
	--json >"$report"

grep -Fq '"schema_version": "mintclaw.document_report.v1"' "$report"
grep -Fq '"operation": "acquire"' "$report"
grep -Fq '"state": "succeeded"' "$report"
grep -Fq '"content_type": "application/pdf"' "$report"

if grep -Fq "$repo_root" "$report"; then
	echo "document worker smoke: report leaked repository path" >&2
	exit 1
fi
if [ -d "$mintclaw_home/state/document-scratch" ] && \
	[ -n "$(find "$mintclaw_home/state/document-scratch" -mindepth 1 -print -quit)" ]; then
	echo "document worker smoke: protected scratch was retained" >&2
	exit 1
fi

echo "document worker smoke: OK"
