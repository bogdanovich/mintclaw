#!/bin/sh

set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
runtime=$(go env GOOS)/$(go env GOARCH)
if [ "$runtime" != "linux/amd64" ]; then
	echo "document form CLI smoke: skipped on $runtime"
	exit 0
fi

smoke_root=$(mktemp -d "${TMPDIR:-/tmp}/mintclaw-document-form-cli.XXXXXX")
trap 'rm -rf -- "$smoke_root"' EXIT HUP INT TERM

binary=${MINTCLAW_BINARY:-$smoke_root/mintclaw}
if [ -z "${MINTCLAW_BINARY:-}" ]; then
	cd "$repo_root"
	go build -buildvcs=false -o "$binary" ./cmd/mintclaw
fi

smoke_home=$smoke_root/home
source_pdf=$repo_root/pkg/document/testdata/acroform-fields.pdf
fields_report=$smoke_root/fields.json
fill_map=$smoke_root/fill-map.json
fill_report=$smoke_root/fill-report.json
retry_report=$smoke_root/retry-report.json
verify_report=$smoke_root/verify-report.json
overwrite_report=$smoke_root/overwrite-report.json
human_report=$smoke_root/human-report.txt
filled_pdf=$smoke_root/filled.pdf
retry_pdf=$smoke_root/retry.pdf
human_pdf=$smoke_root/distinctive-private-output-path.pdf
synthetic_value='MintClaw CLI smoke'
source_before=$(sha256sum "$source_pdf" | cut -d ' ' -f 1)

MINTCLAW_HOME=$smoke_home "$binary" document fields --input "$source_pdf" --json >"$fields_report"
python3 - "$fields_report" "$fill_map" "$synthetic_value" <<'PY'
import json
import pathlib
import sys

report = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
field = next(item for item in report["fields"]["fields"] if item.get("name") == "full_name")
fill_map = {
    "schema_version": "mintclaw.document_fill_map.v1",
    "assignments": [{
        "field_id": field["id"],
        "value": {"type": "text", "text": sys.argv[3]},
    }],
}
pathlib.Path(sys.argv[2]).write_text(json.dumps(fill_map), encoding="utf-8")
PY

MINTCLAW_HOME=$smoke_home "$binary" document fill \
	--input "$source_pdf" \
	--fields "$fill_map" \
	--output "$filled_pdf" \
	--json >"$fill_report"

operation_id=$(python3 - "$fill_report" <<'PY'
import json
import pathlib
import sys

report = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
assert report["operation"] == "fill"
assert report["state"] == "succeeded"
assert report["write"]["structural_assertions"] > 0
assert report["write"]["visual_assertions"] > 0
assert len(report["artifacts"]) == 1
print(report["operation_id"])
PY
)

MINTCLAW_HOME=$smoke_home "$binary" document verify \
	--input "$filled_pdf" \
	--expect "$fill_report" \
	--json >"$verify_report"

MINTCLAW_HOME=$smoke_home "$binary" document fill \
	--input "$source_pdf" \
	--fields "$fill_map" \
	--operation-id "$operation_id" \
	--output "$retry_pdf" \
	--json >"$retry_report"

MINTCLAW_HOME=$smoke_home "$binary" document fill \
	--input "$source_pdf" \
	--fields "$fill_map" \
	--operation-id "$operation_id" \
	--output "$human_pdf" >"$human_report"

filled_digest=$(sha256sum "$filled_pdf" | cut -d ' ' -f 1)
retry_digest=$(sha256sum "$retry_pdf" | cut -d ' ' -f 1)
test "$filled_digest" = "$retry_digest"
test "$(sha256sum "$source_pdf" | cut -d ' ' -f 1)" = "$source_before"

python3 - "$fill_report" "$retry_report" "$verify_report" "$filled_digest" <<'PY'
import json
import pathlib
import sys

fill = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
retry = json.loads(pathlib.Path(sys.argv[2]).read_text(encoding="utf-8"))
verify = json.loads(pathlib.Path(sys.argv[3]).read_text(encoding="utf-8"))
digest = sys.argv[4]
assert retry["state"] == "succeeded"
assert verify["state"] == "succeeded"
assert fill["operation_id"] == retry["operation_id"] == verify["operation_id"]
assert fill["write"]["output_sha256"] == retry["write"]["output_sha256"] == digest
assert verify["write"]["output_sha256"] == digest
PY

set +e
MINTCLAW_HOME=$smoke_home "$binary" document fill \
	--input "$source_pdf" \
	--fields "$fill_map" \
	--operation-id "$operation_id" \
	--output "$filled_pdf" \
	--json >"$overwrite_report"
overwrite_status=$?
set -e
if [ "$overwrite_status" -ne 1 ]; then
	echo "document form CLI smoke: existing output was not refused" >&2
	exit 1
fi
grep -Fq '"code": "artifact_registration_failed"' "$overwrite_report"
test "$(sha256sum "$filled_pdf" | cut -d ' ' -f 1)" = "$filled_digest"

if "$binary" document fill --help | grep -Fq -- --overwrite; then
	echo "document form CLI smoke: unsafe overwrite flag is exposed" >&2
	exit 1
fi
if grep -Fq "$synthetic_value" "$fill_report" || grep -Fq "$synthetic_value" "$retry_report" || \
	grep -Fq "$synthetic_value" "$verify_report" || grep -Fq "$repo_root" "$fill_report" || \
	grep -Fq "$repo_root" "$retry_report" || grep -Fq "$repo_root" "$verify_report"; then
	echo "document form CLI smoke: report leaked a submitted value or host path" >&2
	exit 1
fi
if grep -Fq "$human_pdf" "$human_report" || grep -Fq "$smoke_root" "$human_report"; then
	echo "document form CLI smoke: human report leaked an output path" >&2
	exit 1
fi
if find "$smoke_home/state/document-writes" -name '*.json' -type f -exec grep -Fl "$synthetic_value" {} + | grep -q .; then
	echo "document form CLI smoke: durable metadata leaked a submitted value" >&2
	exit 1
fi
if [ -d "$smoke_home/state/document-scratch" ] && \
	[ -n "$(find "$smoke_home/state/document-scratch" -mindepth 1 -print -quit)" ]; then
	echo "document form CLI smoke: protected scratch was retained" >&2
	exit 1
fi

echo "source_unchanged=true"
echo "operation_id=$operation_id"
echo "output_sha256=$filled_digest"
echo "marker=MINTCLAW_PDF2_FORM_FILL_OK"
echo "marker=MINTCLAW_PDF2_FORM_VERIFY_OK"
echo "marker=MINTCLAW_PDF2_FORM_RECOVERY_OK"
echo "marker=MINTCLAW_PDF2_FORM_CLI_OK"
