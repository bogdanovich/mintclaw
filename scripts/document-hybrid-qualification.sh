#!/bin/sh

set -eu

usage() {
	echo "usage: $0 --input PDF --fields FILL_MAP --output PDF --expected-sha256 HEX --expected-pages N --expected-fields N --evidence-dir DIR" >&2
	exit 2
}

input=
fill_map=
output=
expected_sha256=
expected_pages=
expected_fields=
evidence_dir=
while [ "$#" -gt 0 ]; do
	case "$1" in
	--input) input=${2:-}; shift 2 ;;
	--fields) fill_map=${2:-}; shift 2 ;;
	--output) output=${2:-}; shift 2 ;;
	--expected-sha256) expected_sha256=${2:-}; shift 2 ;;
	--expected-pages) expected_pages=${2:-}; shift 2 ;;
	--expected-fields) expected_fields=${2:-}; shift 2 ;;
	--evidence-dir) evidence_dir=${2:-}; shift 2 ;;
	*) usage ;;
	esac
done

[ -n "$input" ] && [ -n "$fill_map" ] && [ -n "$output" ] && [ -n "$expected_sha256" ] && \
	[ -n "$expected_pages" ] && [ -n "$expected_fields" ] && [ -n "$evidence_dir" ] || usage
[ -f "$input" ] || { echo "hybrid qualification input is unavailable" >&2; exit 2; }
[ -f "$fill_map" ] || { echo "hybrid qualification fill map is unavailable" >&2; exit 2; }
[ ! -e "$output" ] || { echo "hybrid qualification output already exists" >&2; exit 2; }
[ "$input" != "$output" ] || { echo "hybrid qualification output must differ from input" >&2; exit 2; }
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
inspection=$evidence_dir/source-inspection.json
fields=$evidence_dir/source-fields.json
fill_report=$evidence_dir/fill-report.json
verify_report=$evidence_dir/verify-report.json
output_inspection=$evidence_dir/output-inspection.json
MINTCLAW_HOME=$home "$binary" document inspect --input "$input" --json >"$inspection"
MINTCLAW_HOME=$home "$binary" document fields --input "$input" --json >"$fields"
MINTCLAW_HOME=$home "$binary" document fill \
	--input "$input" --fields "$fill_map" --output "$output" --json >"$fill_report"
MINTCLAW_HOME=$home "$binary" document verify \
	--input "$output" --expect "$fill_report" --json >"$verify_report"
MINTCLAW_HOME=$scratch/output-inspection-home "$binary" document inspect \
	--input "$output" --json >"$output_inspection"

pdfinfo "$input" >"$evidence_dir/source-pdfinfo.txt"
pdfinfo -js "$input" >"$evidence_dir/source-pdfinfo-js.txt" 2>&1
pdfinfo "$output" >"$evidence_dir/output-pdfinfo.txt"
source_pdfsig_raw=$scratch/source-pdfsig.txt
output_pdfsig_raw=$scratch/output-pdfsig.txt
set +e
pdfsig "$input" >"$source_pdfsig_raw" 2>&1
source_pdfsig_status=$?
pdfsig "$output" >"$output_pdfsig_raw" 2>&1
output_pdfsig_status=$?
set -e
python3 - \
	"$input" "$source_pdfsig_raw" "$evidence_dir/source-pdfsig.txt" \
	"$output" "$output_pdfsig_raw" "$evidence_dir/output-pdfsig.txt" <<'PY'
import pathlib
import re
import sys


for selector, raw_path, evidence_path in zip(sys.argv[1::3], sys.argv[2::3], sys.argv[3::3]):
    text = pathlib.Path(raw_path).read_text(encoding="utf-8", errors="replace")
    text = text.replace(selector, "[document path omitted]")
    text = re.sub(r"(?m)^(File )'[^']*'(.*)$", r"\1'[document path omitted]'\2", text)
    pathlib.Path(evidence_path).write_text(text, encoding="utf-8")
PY
"$binary" version --no-color >"$evidence_dir/mintclaw-version.txt"

source_after=$(sha256sum "$input" | awk '{print $1}')
output_sha256=$(sha256sum "$output" | awk '{print $1}')
[ "$source_after" = "$source_before" ] || { echo "qualification source changed" >&2; exit 1; }

python3 - \
	"$inspection" "$fields" "$fill_map" "$fill_report" "$verify_report" "$output_inspection" \
	"$evidence_dir/source-pdfinfo.txt" "$evidence_dir/source-pdfinfo-js.txt" \
	"$evidence_dir/source-pdfsig.txt" "$evidence_dir/output-pdfinfo.txt" \
	"$evidence_dir/output-pdfsig.txt" "$evidence_dir/result.json" \
	"$expected_sha256" "$expected_pages" "$expected_fields" "$output_sha256" \
	"$source_pdfsig_status" "$output_pdfsig_status" <<'PY'
import json
import pathlib
import sys

(
    inspection_path,
    fields_path,
    fill_map_path,
    fill_path,
    verify_path,
    output_inspection_path,
    source_pdfinfo_path,
    javascript_path,
    source_pdfsig_path,
    output_pdfinfo_path,
    output_pdfsig_path,
    result_path,
    expected_sha256,
    expected_pages,
    expected_fields,
    output_sha256,
    source_pdfsig_status,
    output_pdfsig_status,
) = sys.argv[1:]
inspection = json.loads(pathlib.Path(inspection_path).read_text(encoding="utf-8"))
fields = json.loads(pathlib.Path(fields_path).read_text(encoding="utf-8"))
fill_map = json.loads(pathlib.Path(fill_map_path).read_text(encoding="utf-8"))
fill = json.loads(pathlib.Path(fill_path).read_text(encoding="utf-8"))
verify = json.loads(pathlib.Path(verify_path).read_text(encoding="utf-8"))
output_inspection = json.loads(pathlib.Path(output_inspection_path).read_text(encoding="utf-8"))
source_info_text = pathlib.Path(source_pdfinfo_path).read_text(encoding="utf-8")
javascript_text = pathlib.Path(javascript_path).read_text(encoding="utf-8")
source_signature_text = pathlib.Path(source_pdfsig_path).read_text(encoding="utf-8")
output_info_text = pathlib.Path(output_pdfinfo_path).read_text(encoding="utf-8")
output_signature_text = pathlib.Path(output_pdfsig_path).read_text(encoding="utf-8")


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
    "mode": "hybrid_print_ready",
}, fields
assert len(fields["fields"]["fields"]) == int(expected_fields), fields

assignment_count = len(fill_map["assignments"])
assert assignment_count > 0, fill_map
assert fill["state"] == "succeeded", fill
assert len(fill["artifacts"]) == 1, fill
write = fill["write"]
assert write["source_sha256"] == expected_sha256, write
assert write["output_sha256"] == output_sha256, write
assert write["checked_fields"] == assignment_count, write
assert write["checked_widgets"] >= assignment_count, write
assert write["appearance_widgets"] == write["checked_widgets"], write
assert write["structural_assertions"] > 0, write
assert write["visual_assertions"] > 0, write
assert write["rendered_pages"] == int(expected_pages), write
assert write["independent_visual_assertions"] > 0, write
assert write["independent_rendered_pages"] == int(expected_pages), write
assert write["output"] == {
    "mode": "flattened_print_ready",
    "page_count": int(expected_pages),
    "acroform": "absent",
    "xfa": "absent",
    "encryption": "present",
    "operation_permissions": {
        "print": "allowed",
        "form_fill": "allowed",
        "modify": "denied",
        "assemble": "denied",
    },
    "content_signatures": "absent",
    "usage_rights": "absent",
    "actions": "absent",
    "normalizations": [
        "xfa_removed",
        "acroform_flattened",
        "xfa_scripts_removed",
        "javascript_name_tree_removed",
        "usage_rights_removed",
        "encryption_preserved",
    ],
}, write

assert verify["state"] == "succeeded", verify
assert verify["operation"] == "verify", verify
assert verify["operation_id"] == fill["operation_id"], verify
assert verify["write"] == write, verify
output_facts = output_inspection["inspection"]
assert output_inspection["state"] == "succeeded", output_inspection
assert output_inspection["input"]["sha256"] == output_sha256, output_inspection
assert output_facts["page_count"]["value"] == int(expected_pages), output_facts
assert output_facts["acroform"]["state"] == "absent", output_facts
assert output_facts["xfa"]["state"] == "absent", output_facts
assert output_facts["signatures"]["state"] == "absent", output_facts
assert output_facts["actions"]["state"] == "absent", output_facts
assert output_facts["encryption"]["operation_permissions"] == write["output"]["operation_permissions"], output_facts

source_info = parse_info(source_info_text)
output_info = parse_info(output_info_text)
assert int(source_info["Pages"]) == int(expected_pages), source_info
assert source_info["Form"] == "XFA", source_info
assert source_info["Encrypted"].startswith("yes"), source_info
assert int(output_info["Pages"]) == int(expected_pages), output_info
assert output_info["Form"] == "none", output_info
assert output_info["Encrypted"].startswith("yes"), output_info
assert javascript_text.strip(), "Poppler did not expose the source JavaScript inventory"
assert "Signature #" not in source_signature_text, source_signature_text
assert "Signature #" not in output_signature_text, output_signature_text

result = {
    "schema_version": "mintclaw.document_hybrid_qualification.v1",
    "state": "flattened_print_ready_verified",
    "source_sha256": expected_sha256,
    "source_unchanged": True,
    "output_sha256": output_sha256,
    "pages": int(expected_pages),
    "fields": int(expected_fields),
    "assigned_fields": assignment_count,
    "authority": "acroform_fixed_pages",
    "permissions": {"print": "allowed", "form_fill": "allowed"},
    "source_content_signature": "absent",
    "source_usage_rights_signature": "present",
    "output_acroform": "absent",
    "output_xfa": "absent",
    "output_signatures": "absent",
    "output_actions": "absent",
    "output_mode": "flattened_print_ready",
    "oracle": {
        "source_pdfinfo_form": source_info["Form"],
        "output_pdfinfo_form": output_info["Form"],
        "source_pdfinfo_encrypted": source_info["Encrypted"],
        "output_pdfinfo_encrypted": output_info["Encrypted"],
        "source_pdfsig_exit": int(source_pdfsig_status),
        "output_pdfsig_exit": int(output_pdfsig_status),
        "ordinary_signature_enumerated": False,
    },
    "marker": "MINTCLAW_PDF4H3_QUALIFICATION_OK",
}
pathlib.Path(result_path).write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
PY

chmod 600 "$evidence_dir"/*
echo "output=$output"
echo "evidence=$evidence_dir"
echo "marker=MINTCLAW_PDF4H3_QUALIFICATION_OK"
