#!/bin/sh

set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
runtime=$(go env GOOS)/$(go env GOARCH)
if [ "$runtime" != "linux/amd64" ]; then
	echo "document fields oracle: skipped on $runtime"
	exit 0
fi

python_binary=${PDF2_PYTHON:-python3}
if ! "$python_binary" -c 'import pypdf' >/dev/null 2>&1; then
	echo "document fields oracle: PDF2_PYTHON must provide pypdf 6.1.1" >&2
	exit 2
fi
if [ "$("$python_binary" -c 'import importlib.metadata; print(importlib.metadata.version("pypdf"))')" != "6.1.1" ]; then
	echo "document fields oracle: pypdf version must be 6.1.1" >&2
	exit 2
fi

oracle_root=$(mktemp -d "${TMPDIR:-/tmp}/mintclaw-document-fields-oracle.XXXXXX")
trap 'rm -rf -- "$oracle_root"' EXIT HUP INT TERM
binary=${MINTCLAW_BINARY:-$oracle_root/mintclaw}
if [ -z "${MINTCLAW_BINARY:-}" ]; then
	cd "$repo_root"
	go build -o "$binary" ./cmd/mintclaw
fi

fixture=$repo_root/pkg/document/testdata/acroform-fields.pdf
manifest=$repo_root/pkg/document/testdata/form-fields-manifest.json
report=$oracle_root/fields.json
MINTCLAW_HOME=$oracle_root/home "$binary" document fields --input "$fixture" --json >"$report"

"$python_binary" - "$fixture" "$report" <<'PY'
import json
import pathlib
import re
import sys

from pypdf import PdfReader

fixture_path = pathlib.Path(sys.argv[1])
report = json.loads(pathlib.Path(sys.argv[2]).read_text(encoding="utf-8"))
fields = report["fields"]["fields"]
assert report["state"] == "succeeded", report
assert report["fields"]["source_sha256"] == report["input"]["sha256"]
assert len(fields) == 8, len(fields)
assert sum(len(field["widgets"]) for field in fields) == 10
assert all(re.fullmatch(r"field_[a-f0-9]{64}", field["id"]) for field in fields)
assert all(
    re.fullmatch(r"widget_[a-f0-9]{64}", widget["id"])
    for field in fields
    for widget in field["widgets"]
)

by_name = {field["name"]: field for field in fields}
assert set(by_name) == {
    "agree", "color", "country", "full_name", "notes", "repeated", "start_date", "tags"
}
assert by_name["full_name"]["required"] is True
assert by_name["full_name"]["has_value"] is True
assert by_name["notes"]["multiline"] is True
assert by_name["tags"]["multi_select"] is True
assert by_name["start_date"]["max_length"] == 10
assert [widget["page"] for widget in by_name["repeated"]["widgets"]] == [1, 2]
assert by_name["country"]["options"] == [
    {"export": "US", "display": "United States"},
    {"export": "CA", "display": "Canada"},
    {"export": " EU ", "display": " Europe "},
]

reader = PdfReader(str(fixture_path))
oracle = reader.get_fields()
assert oracle is not None
assert set(oracle) == set(by_name), set(oracle)
assert oracle["full_name"]["/FT"] == "/Tx"
assert oracle["agree"]["/FT"] == "/Btn"
assert oracle["country"]["/FT"] == "/Ch"
assert oracle["full_name"]["/V"] == "Existing User"
date_root = next(
    ref.get_object()
    for ref in reader.trailer["/Root"]["/AcroForm"]["/Fields"]
    if ref.get_object().get("/T") == "start_date"
)
date_widget = date_root["/Kids"][0].get_object()
assert date_root["/MaxLen"] == 10
assert "/MaxLen" not in date_widget
assert date_widget.raw_get("/Parent").idnum == date_root.indirect_reference.idnum
assert report["fields"]["backend"]["name"] == "pdfcpu"
assert report["fields"]["backend"]["version"] == "v0.15.0"
PY

hybrid_fixture=$repo_root/pkg/document/testdata/hybrid-xfa-packet-array.pdf
hybrid_report=$oracle_root/hybrid-xfa-packet-array.json
MINTCLAW_HOME=$oracle_root/home "$binary" document fields --input "$hybrid_fixture" --json >"$hybrid_report"
"$python_binary" - "$hybrid_fixture" "$hybrid_report" <<'PY'
import json
import pathlib
import sys

from pypdf import PdfReader

fixture = pathlib.Path(sys.argv[1])
report = json.loads(pathlib.Path(sys.argv[2]).read_text(encoding="utf-8"))
assert report["state"] == "succeeded", report
assert report["form_eligibility"] == {
    "state": "eligible",
    "mode": "hybrid_discovery_only",
}, report
assert len(report["fields"]["fields"]) == 1, report
oracle = PdfReader(str(fixture)).get_fields()
assert oracle is not None and set(oracle) == {"hybrid-name"}, oracle
PY

for fixture_name in text hybrid-xfa-dynamic signed-certified unsigned-signature calculated-field encrypted-password-required; do
	set +e
	MINTCLAW_HOME=$oracle_root/home "$binary" document fields \
		--input "$repo_root/pkg/document/testdata/$fixture_name.pdf" --json \
		>"$oracle_root/$fixture_name.json"
	status=$?
	set -e
	if [ "$status" -eq 0 ]; then
		echo "document fields oracle: refusal fixture $fixture_name succeeded" >&2
		exit 1
	fi
	"$python_binary" - "$manifest" "$oracle_root/$fixture_name.json" "$fixture_name.pdf" <<'PY'
import json
import pathlib
import sys

manifest = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
report = json.loads(pathlib.Path(sys.argv[2]).read_text(encoding="utf-8"))
fixture_name = sys.argv[3]
fixture = next(item for item in manifest["fixtures"] if item["file"] == fixture_name)
expected = fixture["expected"]
assert report["state"] == expected["state"], (fixture_name, report)
assert report["failure"]["code"] == expected["failure_code"], (fixture_name, report)
assert "fields" not in report, (fixture_name, report)
PY
done

echo "production=pdfcpu version=v0.15.0"
echo "oracle=pypdf version=6.1.1"
echo "marker=MINTCLAW_PDF2_FIELDS_ORACLE_OK"
