#!/bin/sh

set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
runtime=$(go env GOOS)/$(go env GOARCH)
if [ "$runtime" != "linux/amd64" ]; then
	echo "document read smoke: skipped on $runtime"
	exit 0
fi

smoke_root=$(mktemp -d "${TMPDIR:-/tmp}/mintclaw-document-read.XXXXXX")
trap 'rm -rf -- "$smoke_root"' EXIT HUP INT TERM

binary=${MINTCLAW_BINARY:-$smoke_root/mintclaw}
if [ -z "${MINTCLAW_BINARY:-}" ]; then
	cd "$repo_root"
	go build -o "$binary" ./cmd/mintclaw
fi

smoke_home=$smoke_root/home
text_report=$smoke_root/text-report.json
text_output=$smoke_root/text.jsonl
render_report=$smoke_root/render-report.json
render_output=$smoke_root/rendered
limit_report=$smoke_root/limit-report.json
existing_text_report=$smoke_root/existing-text-report.json
existing_render_report=$smoke_root/existing-render-report.json

MINTCLAW_HOME=$smoke_home "$binary" document extract \
	--input "$repo_root/pkg/document/testdata/unicode.pdf" \
	--pages 1 \
	--output "$text_output" \
	--json >"$text_report"
MINTCLAW_HOME=$smoke_home "$binary" document render \
	--input "$repo_root/pkg/document/testdata/rotated-crop.pdf" \
	--pages 1 \
	--output-dir "$render_output" \
	--json >"$render_report"

grep -Fq 'MintClaw Café résumé' "$text_output"
grep -Fq '"operation": "extract"' "$text_report"
grep -Fq '"state": "succeeded"' "$text_report"
grep -Fq '"source_sha256"' "$text_report"
grep -Fq '"operation": "render"' "$render_report"
grep -Fq '"width": 792' "$render_report"
grep -Fq '"height": 612' "$render_report"
test "$(od -An -tx1 -N8 "$render_output/page-0001.png" | tr -d ' \n')" = "89504e470d0a1a0a"

text_digest=$(sha256sum "$text_output" | cut -d ' ' -f 1)
render_digest=$(sha256sum "$render_output/page-0001.png" | cut -d ' ' -f 1)
set +e
MINTCLAW_HOME=$smoke_home "$binary" document extract \
	--input "$repo_root/pkg/document/testdata/unicode.pdf" \
	--pages 1 \
	--output "$text_output" \
	--json >"$existing_text_report"
existing_text_status=$?
MINTCLAW_HOME=$smoke_home "$binary" document render \
	--input "$repo_root/pkg/document/testdata/rotated-crop.pdf" \
	--pages 1 \
	--output-dir "$render_output" \
	--json >"$existing_render_report"
existing_render_status=$?
set -e
if [ "$existing_text_status" -ne 1 ] || [ "$existing_render_status" -ne 1 ]; then
	echo "document read smoke: existing output was not refused" >&2
	exit 1
fi
grep -Fq '"code": "artifact_registration_failed"' "$existing_text_report"
grep -Fq '"code": "artifact_registration_failed"' "$existing_render_report"
test "$(sha256sum "$text_output" | cut -d ' ' -f 1)" = "$text_digest"
test "$(sha256sum "$render_output/page-0001.png" | cut -d ' ' -f 1)" = "$render_digest"
if "$binary" document extract --help | grep -Fq -- --overwrite || \
	"$binary" document render --help | grep -Fq -- --overwrite; then
	echo "document read smoke: unsafe overwrite flag is exposed" >&2
	exit 1
fi

set +e
MINTCLAW_HOME=$smoke_home "$binary" document render \
	--input "$repo_root/pkg/document/testdata/extreme-dimensions.pdf" \
	--pages 1 \
	--output-dir "$smoke_root/must-not-exist" \
	--json >"$limit_report"
limit_status=$?
set -e
if [ "$limit_status" -ne 5 ]; then
	echo "document read smoke: render limit exit $limit_status, expected 5" >&2
	exit 1
fi
grep -Fq '"code": "render_limit"' "$limit_report"
test ! -e "$smoke_root/must-not-exist"

if grep -Fq "$repo_root" "$text_report" || grep -Fq "$repo_root" "$render_report" || \
	grep -Fq "$repo_root" "$existing_text_report" || \
	grep -Fq "$repo_root" "$existing_render_report" || grep -Fq "$repo_root" "$limit_report"; then
	echo "document read smoke: report leaked repository path" >&2
	exit 1
fi
if [ -d "$smoke_home/state/document-scratch" ] && \
	[ -n "$(find "$smoke_home/state/document-scratch" -mindepth 1 -print -quit)" ]; then
	echo "document read smoke: protected scratch was retained" >&2
	exit 1
fi

echo "document read smoke: MINTCLAW_PDF1A_CLI_OK"
