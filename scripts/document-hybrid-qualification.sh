#!/bin/sh

set -eu

usage() {
	echo "usage: $0 --input PDF --expected-sha256 HEX --expected-pages N --expected-fields N --evidence-dir DIR" >&2
	exit 2
}

input=
expected_sha256=
expected_pages=
expected_fields=
evidence_dir=
while [ "$#" -gt 0 ]; do
	case "$1" in
	--input) input=${2:-}; shift 2 ;;
	--expected-sha256) expected_sha256=${2:-}; shift 2 ;;
	--expected-pages) expected_pages=${2:-}; shift 2 ;;
	--expected-fields) expected_fields=${2:-}; shift 2 ;;
	--evidence-dir) evidence_dir=${2:-}; shift 2 ;;
	*) usage ;;
	esac
done

[ -n "$input" ] && [ -n "$expected_sha256" ] && [ -n "$expected_pages" ] && \
	[ -n "$expected_fields" ] && [ -n "$evidence_dir" ] || usage
[ -f "$input" ] || { echo "hybrid qualification input is unavailable" >&2; exit 2; }
case "$expected_sha256" in
*[!a-f0-9]*|'') echo "expected SHA-256 must be lowercase hexadecimal" >&2; exit 2 ;;
esac
[ "${#expected_sha256}" -eq 64 ] || { echo "expected SHA-256 must contain 64 characters" >&2; exit 2; }
case "$expected_pages:$expected_fields" in
*[!0-9:]*|0:*|*:0) echo "expected counts must be positive integers" >&2; exit 2 ;;
esac

runtime=$(go env GOOS)/$(go env GOARCH)
[ "$runtime" = "linux/amd64" ] || { echo "hybrid qualification is admitted only on linux/amd64" >&2; exit 2; }
for command in sha256sum python3 pdfinfo pdfsig; do
	command -v "$command" >/dev/null 2>&1 || { echo "hybrid qualification requires $command" >&2; exit 2; }
done

if [ -e "$evidence_dir" ] && [ -n "$(find "$evidence_dir" -mindepth 1 -print -quit 2>/dev/null)" ]; then
	echo "evidence directory must be absent or empty" >&2
	exit 2
fi
mkdir -p "$evidence_dir"
chmod 700 "$evidence_dir"

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
scratch=$(mktemp -d "${TMPDIR:-/tmp}/mintclaw-hybrid-qualification.XXXXXX")
trap 'rm -rf -- "$scratch"' EXIT HUP INT TERM
binary=${MINTCLAW_BINARY:-$scratch/mintclaw}
if [ -z "${MINTCLAW_BINARY:-}" ]; then
	(cd "$repo_root" && go build -buildvcs=false -o "$binary" ./cmd/mintclaw)
fi
[ -x "$binary" ] || { echo "MintClaw binary is unavailable" >&2; exit 2; }

source_before=$(sha256sum "$input" | awk '{print $1}')
[ "$source_before" = "$expected_sha256" ] || { echo "qualification input digest mismatch" >&2; exit 2; }

home=$scratch/home
inspection=$evidence_dir/inspection.json
fields=$evidence_dir/fields.json
fill_map=$scratch/fill-map.json
fill_refusal=$evidence_dir/fill-refusal.json
output=$scratch/forbidden-output.pdf
MINTCLAW_HOME=$home "$binary" document inspect --input "$input" --json >"$inspection"
MINTCLAW_HOME=$home "$binary" document fields --input "$input" --json >"$fields"

python3 - "$fields" "$fill_map" <<'PY'
import json
import pathlib
import sys

report = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
field = next(
    item for item in report["fields"]["fields"]
    if item["kind"] == "text" and not item["read_only"]
)
fill_map = {
    "schema_version": "mintclaw.document_fill_map.v1",
    "assignments": [{
        "field_id": field["id"],
        "value": {"type": "text", "text": "MINTCLAW_HYBRID_NO_WRITE"},
    }],
}
pathlib.Path(sys.argv[2]).write_text(json.dumps(fill_map), encoding="utf-8")
PY

set +e
MINTCLAW_HOME=$home "$binary" document fill \
	--input "$input" --fields "$fill_map" --output "$output" --json >"$fill_refusal"
fill_status=$?
set -e
[ "$fill_status" -eq 4 ] || { echo "hybrid write gate returned exit $fill_status, expected 4" >&2; exit 1; }
[ ! -e "$output" ] || { echo "hybrid write gate created an output" >&2; exit 1; }

pdfinfo "$input" >"$evidence_dir/pdfinfo.txt"
pdfinfo -js "$input" >"$evidence_dir/pdfinfo-js.txt" 2>&1
set +e
pdfsig "$input" >"$evidence_dir/pdfsig.txt" 2>&1
pdfsig_status=$?
set -e
"$binary" version --no-color >"$evidence_dir/mintclaw-version.txt"

source_after=$(sha256sum "$input" | awk '{print $1}')
[ "$source_after" = "$source_before" ] || { echo "qualification source changed" >&2; exit 1; }

python3 - \
	"$inspection" "$fields" "$fill_refusal" "$evidence_dir/pdfinfo.txt" \
	"$evidence_dir/pdfinfo-js.txt" "$evidence_dir/pdfsig.txt" "$evidence_dir/result.json" \
	"$expected_sha256" "$expected_pages" "$expected_fields" "$fill_status" "$pdfsig_status" <<'PY'
import json
import pathlib
import sys

(
    inspection_path,
    fields_path,
    fill_path,
    pdfinfo_path,
    javascript_path,
    pdfsig_path,
    result_path,
    expected_sha256,
    expected_pages,
    expected_fields,
    fill_status,
    pdfsig_status,
) = sys.argv[1:]
inspection = json.loads(pathlib.Path(inspection_path).read_text(encoding="utf-8"))
fields = json.loads(pathlib.Path(fields_path).read_text(encoding="utf-8"))
fill = json.loads(pathlib.Path(fill_path).read_text(encoding="utf-8"))
info_text = pathlib.Path(pdfinfo_path).read_text(encoding="utf-8")
javascript_text = pathlib.Path(javascript_path).read_text(encoding="utf-8")
signature_text = pathlib.Path(pdfsig_path).read_text(encoding="utf-8")


def parse_info(text):
    result = {}
    for line in text.splitlines():
        if ":" in line:
            key, value = line.split(":", 1)
            result[key.strip()] = value.strip()
    return result


facts = inspection["inspection"]
assert inspection["state"] == "succeeded", inspection
assert inspection["input"]["sha256"] == expected_sha256
assert facts["page_count"]["value"] == int(expected_pages), facts
assert facts["acroform"]["field_count"]["value"] == int(expected_fields), facts
assert facts["xfa"]["state"] == "present", facts
assert facts["hybrid_form"]["authority"] == {
    "state": "present",
    "value": "acroform_fixed_pages",
}, facts
assert facts["hybrid_form"]["xml_parsed"] == "present", facts
assert facts["encryption"]["operation_permissions"]["print"] == "allowed", facts
assert facts["encryption"]["operation_permissions"]["form_fill"] == "allowed", facts
assert facts["signatures"]["content"]["state"] == "absent", facts
assert facts["signatures"]["usage_rights"]["state"] == "present", facts
assert fields["state"] == "succeeded", fields
assert fields["form_eligibility"] == {
    "state": "eligible",
    "mode": "hybrid_discovery_only",
}, fields
assert len(fields["fields"]["fields"]) == int(expected_fields), fields
assert fill["state"] == "unsupported" and fill["failure"]["code"] == "form_unsupported", fill
assert int(fill_status) == 4

info = parse_info(info_text)
assert int(info["Pages"]) == int(expected_pages), info
assert info["Form"] == "XFA", info
assert info["Encrypted"].startswith("yes"), info
assert javascript_text.strip(), "Poppler did not expose the document JavaScript inventory"
assert "Signature #" not in signature_text, signature_text

result = {
    "schema_version": "mintclaw.document_hybrid_qualification.v1",
    "state": "discovery_only_admitted",
    "source_sha256": expected_sha256,
    "source_unchanged": True,
    "pages": int(expected_pages),
    "fields": int(expected_fields),
    "authority": "acroform_fixed_pages",
    "permissions": {"print": "allowed", "form_fill": "allowed"},
    "content_signature": "absent",
    "usage_rights_signature": "present",
    "write_state": "unsupported",
    "write_failure": "form_unsupported",
    "write_output_created": False,
    "oracle": {
        "pdfinfo_form": info["Form"],
        "pdfinfo_encrypted": info["Encrypted"],
        "pdfsig_exit": int(pdfsig_status),
        "ordinary_signature_enumerated": False,
    },
    "marker": "MINTCLAW_PDF4H2_QUALIFICATION_OK",
}
pathlib.Path(result_path).write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
PY

chmod 600 "$evidence_dir"/*
echo "evidence=$evidence_dir"
echo "marker=MINTCLAW_PDF4H2_QUALIFICATION_OK"
