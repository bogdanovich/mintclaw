#!/bin/sh

set -eu
umask 077

usage() {
	echo "usage: $0 --staged-output PDF --output PDF --evidence-dir DIR --scratch DIR" >&2
	exit 2
}

staged_output=
output=
evidence_dir=
scratch=
while [ "$#" -gt 0 ]; do
	case "$1" in
	--staged-output) staged_output=${2:-}; shift 2 ;;
	--output) output=${2:-}; shift 2 ;;
	--evidence-dir) evidence_dir=${2:-}; shift 2 ;;
	--scratch) scratch=${2:-}; shift 2 ;;
	*) usage ;;
	esac
done

[ -n "$staged_output" ] && [ -n "$output" ] && [ -n "$evidence_dir" ] && [ -n "$scratch" ] || usage
case "$(basename -- "$scratch")" in
.mintclaw-hybrid-live-qualification.*) ;;
*) echo "refusing unsafe live qualification scratch path" >&2; exit 2 ;;
esac
[ "$staged_output" = "$scratch/verified-output.pdf" ] || {
	echo "verified staged output is outside live qualification scratch" >&2
	exit 2
}
[ "$(dirname -- "$scratch")" = "$(dirname -- "$output")" ] || {
	echo "live qualification scratch and output must share a directory" >&2
	exit 2
}
completed=false
cleanup() {
	status=$?
	trap - EXIT HUP INT TERM
	if [ "$completed" != true ] && [ -e "$staged_output" ] && [ -e "$output" ] && \
		[ "$staged_output" -ef "$output" ]; then
		rm -f -- "$output"
	fi
	rm -rf -- "$scratch"
	exit "$status"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

[ -f "$staged_output" ] || { echo "verified staged output is unavailable" >&2; exit 2; }
[ -d "$evidence_dir" ] || { echo "live evidence directory is unavailable" >&2; exit 2; }
[ -d "$scratch" ] || { echo "live qualification scratch is unavailable" >&2; exit 2; }
[ ! -e "$output" ] || { echo "live qualification output already exists" >&2; exit 2; }

ln "$staged_output" "$output"
echo "output=$output"
echo "evidence=$evidence_dir"
echo "marker=MINTCLAW_PDF4H4_LIVE_QUALIFICATION_OK"
completed=true
