#!/bin/sh

set -eu

host=""
fixture="pkg/document/testdata/text.pdf"
while [ "$#" -gt 0 ]; do
	case "$1" in
	--host)
		host=${2:?--host requires a value}
		shift 2
		;;
	--fixture)
		fixture=${2:?--fixture requires a value}
		shift 2
		;;
	*)
		echo "usage: $0 [--host server@oc] [--fixture repo-relative-path]" >&2
		exit 2
		;;
	esac
done

case "$fixture" in
/*|*..*|*[!A-Za-z0-9_./-]*)
	echo "fixture must be a safe repository-relative path" >&2
	exit 2
	;;
esac

ssh_options="-o BatchMode=yes -o ConnectTimeout=5"
if [ -z "$host" ]; then
	for candidate in server@oc server@oc-ts; do
		if ssh $ssh_options "$candidate" true >/dev/null 2>&1; then
			host=$candidate
			break
		fi
	done
fi
if [ -z "$host" ]; then
	echo "no deployed MintClaw host is reachable" >&2
	exit 1
fi
case "$host" in
server@oc|server@oc-ts) ;;
*)
	echo "host must be server@oc or server@oc-ts" >&2
	exit 2
	;;
esac

echo "host=$host"
ssh $ssh_options "$host" sh -s -- "/home/server/src/mintclaw" "$fixture" <<'REMOTE'
set -eu

repo=$1
fixture=$2
binary=$repo/build/mintclaw
input=$repo/$fixture

if [ ! -x "$binary" ] || [ ! -f "$input" ]; then
	echo "deployed binary or checked-in fixture is unavailable" >&2
	exit 1
fi

smoke_root=$(mktemp -d "${TMPDIR:-/tmp}/mintclaw-document-deployed.XXXXXX")
trap 'rm -rf -- "$smoke_root"' EXIT HUP INT TERM
capabilities=$smoke_root/capabilities.json
report=$smoke_root/report.json
inspection=$smoke_root/inspection.json
smoke_home=$smoke_root/home

"$binary" document capabilities --json >"$capabilities"
MINTCLAW_HOME=$smoke_home "$binary" document acquire --input "$input" --json >"$report"
MINTCLAW_HOME=$smoke_home "$binary" document inspect --input "$input" --json >"$inspection"

expected_digest=$(sha256sum "$input" | awk '{print $1}')
python3 - "$capabilities" "$report" "$inspection" "$expected_digest" "$repo" <<'PY'
import json
import pathlib
import sys

capabilities_path, report_path, inspection_path, expected_digest, forbidden_path = sys.argv[1:]
capabilities = json.loads(pathlib.Path(capabilities_path).read_text(encoding="utf-8"))
report = json.loads(pathlib.Path(report_path).read_text(encoding="utf-8"))
inspection = json.loads(pathlib.Path(inspection_path).read_text(encoding="utf-8"))
assert capabilities["platform"] == "linux"
assert capabilities["architecture"] == "amd64"
assert capabilities["operations"]["acquire"]["state"] == "supported"
assert capabilities["operations"]["inspect"]["state"] == "supported"
assert report["schema_version"] == "mintclaw.document_report.v1"
assert report["operation"] == "acquire"
assert report["state"] == "succeeded"
assert report["input"]["sha256"] == expected_digest
assert forbidden_path not in json.dumps(report, sort_keys=True)
assert inspection["schema_version"] == "mintclaw.document_report.v1"
assert inspection["operation"] == "inspect"
assert inspection["state"] == "succeeded"
assert inspection["input"]["sha256"] == expected_digest
assert inspection["inspection"]["backend"] == {
    "name": "pdfcpu", "version": "v0.15.0", "role": "production"
}
assert inspection["inspection"]["page_count"]["value"] == 1
assert inspection["inspection"]["extractable_text"]["state"] == "present"
assert forbidden_path not in json.dumps(inspection, sort_keys=True)
PY
if [ -d "$smoke_home/state/document-scratch" ] && \
	[ -n "$(find "$smoke_home/state/document-scratch" -mindepth 1 -print -quit)" ]; then
	echo "deployed document smoke retained protected scratch" >&2
	exit 1
fi

echo "core_sha=$(git -C "$repo" rev-parse HEAD)"
echo "fixture=$fixture"
echo "sha256=$expected_digest"
echo "state=succeeded"
echo "scratch=clean"
echo "marker=MINTCLAW_DOCUMENT_INSPECT_OK"
REMOTE
