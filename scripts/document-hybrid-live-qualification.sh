#!/bin/sh

set -eu
umask 077

usage() {
	cat >&2 <<'EOF'
usage: document-hybrid-live-qualification.sh \
  --input PDF --fields FILL_MAP --case-prompt FILE --private-values FILE \
  --output PDF --config CONFIG --expected-sha256 HEX --expected-pages N \
  --expected-fields N --expected-assigned-fields N --evidence-dir DIR [--timeout DURATION]

The case prompt supplies semantic fill instructions but no host path. The harness
adds the exact input path and the document-only execution contract itself.
PRIVATE_VALUES contains one sensitive literal per line for the evidence scan.
EOF
	exit 2
}

input=
fill_map=
case_prompt=
private_values=
output=
config=
expected_sha256=
expected_pages=
expected_fields=
expected_assigned_fields=
evidence_dir=
timeout=6m
while [ "$#" -gt 0 ]; do
	case "$1" in
	--input) input=${2:-}; shift 2 ;;
	--fields) fill_map=${2:-}; shift 2 ;;
	--case-prompt) case_prompt=${2:-}; shift 2 ;;
	--private-values) private_values=${2:-}; shift 2 ;;
	--output) output=${2:-}; shift 2 ;;
	--config) config=${2:-}; shift 2 ;;
	--expected-sha256) expected_sha256=${2:-}; shift 2 ;;
	--expected-pages) expected_pages=${2:-}; shift 2 ;;
	--expected-fields) expected_fields=${2:-}; shift 2 ;;
	--expected-assigned-fields) expected_assigned_fields=${2:-}; shift 2 ;;
	--evidence-dir) evidence_dir=${2:-}; shift 2 ;;
	--timeout) timeout=${2:-}; shift 2 ;;
	-h|--help) usage ;;
	*) usage ;;
	esac
done

[ -n "$input" ] && [ -n "$fill_map" ] && [ -n "$case_prompt" ] && \
	[ -n "$private_values" ] && [ -n "$output" ] && [ -n "$config" ] && \
	[ -n "$expected_sha256" ] && [ -n "$expected_pages" ] && \
	[ -n "$expected_fields" ] && [ -n "$expected_assigned_fields" ] && \
	[ -n "$evidence_dir" ] || usage
for path in "$input" "$fill_map" "$case_prompt" "$private_values" "$config"; do
	[ -f "$path" ] || { echo "required qualification input is unavailable" >&2; exit 2; }
done
awk 'NF { found = 1 } END { exit !found }' "$private_values" || {
	echo "private-values must contain at least one non-empty literal" >&2
	exit 2
}
[ ! -e "$output" ] || { echo "live qualification output already exists" >&2; exit 2; }
case "$expected_sha256" in
*[!a-f0-9]*|'') echo "expected SHA-256 must be lowercase hexadecimal" >&2; exit 2 ;;
esac
[ "${#expected_sha256}" -eq 64 ] || { echo "expected SHA-256 must contain 64 characters" >&2; exit 2; }
case "$expected_pages:$expected_fields:$expected_assigned_fields" in
*[!0-9:]*|0:*|*:0:*|*:*:0) echo "expected counts must be positive integers" >&2; exit 2 ;;
esac

runtime=$(go env GOOS)/$(go env GOARCH)
[ "$runtime" = "linux/amd64" ] || { echo "live hybrid qualification is admitted only on linux/amd64" >&2; exit 2; }
for command in sha256sum python3 pdfinfo pdfsig pdftoppm; do
	command -v "$command" >/dev/null 2>&1 || { echo "live hybrid qualification requires $command" >&2; exit 2; }
done

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
binary=${MINTCLAW_BINARY:-$(command -v mintclaw || true)}
[ -n "$binary" ] && [ -x "$binary" ] || { echo "set MINTCLAW_BINARY to the deployed MintClaw binary" >&2; exit 2; }
if [ -e "$evidence_dir" ] && [ -n "$(find "$evidence_dir" -mindepth 1 -print -quit 2>/dev/null)" ]; then
	echo "evidence directory must be absent or empty" >&2
	exit 2
fi
mkdir -p "$evidence_dir"
chmod 700 "$evidence_dir"

output_parent=$(dirname -- "$output")
[ -d "$output_parent" ] || { echo "live qualification output directory is unavailable" >&2; exit 2; }
scratch=$(mktemp -d "$output_parent/.mintclaw-hybrid-live-qualification.XXXXXX")
staged_output=$scratch/verified-output.pdf
trap 'rm -rf -- "$scratch"' EXIT HUP INT TERM
baseline_output=$scratch/cli-baseline.pdf
baseline_evidence=$evidence_dir/cli
MINTCLAW_BINARY="$binary" "$repo_root/scripts/document-hybrid-qualification.sh" \
	--input "$input" \
	--fields "$fill_map" \
	--output "$baseline_output" \
	--expected-sha256 "$expected_sha256" \
	--expected-pages "$expected_pages" \
	--expected-fields "$expected_fields" \
	--evidence-dir "$baseline_evidence" >"$evidence_dir/cli.stdout"

scratch_before=$scratch/document-scratch-before.txt
if [ -d "${TMPDIR:-/tmp}/mintclaw_document_agent" ]; then
	find "${TMPDIR:-/tmp}/mintclaw_document_agent" -mindepth 1 -maxdepth 1 -print | sort >"$scratch_before"
else
	: >"$scratch_before"
fi

session="pdf4h-live-$(date -u +%Y%m%dT%H%M%SZ)-$$"
prompt=$(cat <<EOF
This is a bounded technical PDF qualification using synthetic data. The PDF will not be submitted.
Use tool_search_tool_bm25 exactly once as the first tool call to discover the hidden document tool. After that, work only through document. Do not use shell, exec, browser, Python, external PDF libraries, manual drawing, the protected form workflow, or send_file.

Use the local PDF at $input. Perform exactly this sequence in this turn:
1. discover document without including the local path or any form value in the search query;
2. inspect the local path and retain the exact returned source ref;
3. call fields on that source;
4. map exactly $expected_assigned_fields semantic values to exactly $expected_assigned_fields unambiguous discovered stable field IDs, then call fill exactly once;
5. call verify exactly once using both the exact artifact ref and the exact operation_id returned by fill.

$(cat "$case_prompt")

Never guess a field ID or choice export value. Do not retry fill or cause a second delivery. If the mapping is not exactly one-to-one or an operation is unavailable, stop without a workaround and return the exact typed state, failure code, and message. Produce exactly one PDF artifact only after fill and verify succeed.
EOF
)

set +e
"$binary" agent live --json --config "$config" --session "$session" \
	--timeout "$timeout" --message "$prompt" >"$evidence_dir/live.json" 2>"$evidence_dir/live.stderr"
live_status=$?
set -e
[ "$live_status" -eq 0 ] || { echo "agent live failed; see live.json and live.stderr" >&2; exit 1; }

python3 "$repo_root/scripts/internal/document_hybrid_live_qualification.py" \
	--live-json "$evidence_dir/live.json" \
	--live-stderr "$evidence_dir/live.stderr" \
	--config "$config" \
	--input "$input" \
	--baseline "$baseline_output" \
	--private-values "$private_values" \
	--scratch-before "$scratch_before" \
	--staged-output "$staged_output" \
	--evidence-dir "$evidence_dir" \
	--binary "$binary" \
	--expected-sha256 "$expected_sha256" \
	--expected-pages "$expected_pages" \
	--expected-assigned-fields "$expected_assigned_fields"

exec "$repo_root/scripts/internal/document-hybrid-live-publish.sh" \
	--staged-output "$staged_output" \
	--output "$output" \
	--evidence-dir "$evidence_dir" \
	--scratch "$scratch"
