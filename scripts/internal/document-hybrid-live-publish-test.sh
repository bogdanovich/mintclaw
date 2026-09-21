#!/bin/sh

set -eu

publisher=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/document-hybrid-live-publish.sh
test_root=$(mktemp -d "${TMPDIR:-/tmp}/mintclaw-live-publish-test.XXXXXX")
trap 'rm -rf -- "$test_root"' EXIT HUP INT TERM

new_case() {
	case_root=$test_root/$1
	mkdir -p "$case_root/.mintclaw-hybrid-live-qualification.test" "$case_root/evidence"
	scratch=$case_root/.mintclaw-hybrid-live-qualification.test
	staged_output=$scratch/verified-output.pdf
	output=$case_root/output.pdf
	printf 'verified-pdf' >"$staged_output"
}

new_case success
result=$(
	"$publisher" --staged-output "$staged_output" --output "$output" \
		--evidence-dir "$case_root/evidence" --scratch "$scratch"
)
[ -f "$output" ] && [ ! -e "$scratch" ]
[ "$(cat "$output")" = verified-pdf ]
printf '%s' "$result" | grep -q 'marker=MINTCLAW_PDF4H4_LIVE_QUALIFICATION_OK'

new_case closed-stdout
set +e
"$publisher" --staged-output "$staged_output" --output "$output" \
	--evidence-dir "$case_root/evidence" --scratch "$scratch" >&- 2>/dev/null
status=$?
set -e
[ "$status" -ne 0 ] && [ ! -e "$output" ] && [ ! -e "$scratch" ]

new_case no-clobber
printf 'preexisting' >"$output"
set +e
"$publisher" --staged-output "$staged_output" --output "$output" \
	--evidence-dir "$case_root/evidence" --scratch "$scratch" >/dev/null 2>&1
status=$?
set -e
[ "$status" -ne 0 ] && [ "$(cat "$output")" = preexisting ] && [ ! -e "$scratch" ]

unsafe=$test_root/unsafe
mkdir -p "$unsafe"
printf 'keep' >"$unsafe/sentinel"
set +e
"$publisher" --staged-output "$unsafe/verified-output.pdf" --output "$test_root/unsafe.pdf" \
	--evidence-dir "$test_root" --scratch "$unsafe" >/dev/null 2>&1
status=$?
set -e
[ "$status" -ne 0 ] && [ "$(cat "$unsafe/sentinel")" = keep ]

new_case signal-during-publication
linked=$case_root/linked
PATH="$(CDPATH= cd -- "$(dirname -- "$0")/testdata/document-hybrid-live-publish" && pwd):$PATH" \
	MINTCLAW_LIVE_PUBLISH_TEST_LINKED="$linked" \
	"$publisher" --staged-output "$staged_output" --output "$output" \
	--evidence-dir "$case_root/evidence" --scratch "$scratch" >"$case_root/result" &
publisher_pid=$!
attempt=0
while [ ! -e "$linked" ] && [ "$attempt" -lt 200 ]; do
	sleep 0.01
	attempt=$((attempt + 1))
done
[ -e "$linked" ]
kill -TERM "$publisher_pid"
wait "$publisher_pid"
[ -f "$output" ] && [ ! -e "$scratch" ]
grep -q 'marker=MINTCLAW_PDF4H4_LIVE_QUALIFICATION_OK' "$case_root/result"

echo "MINTCLAW_DOCUMENT_HYBRID_LIVE_PUBLISH_TEST_OK"
