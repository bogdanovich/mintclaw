#!/bin/sh

set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
runtime=$(go env GOOS)/$(go env GOARCH)
if [ "$runtime" != "linux/amd64" ]; then
	echo "document form write oracle: skipped on $runtime"
	exit 0
fi

python_binary=${PDF2_PYTHON:-python3}
if ! "$python_binary" -c 'import pypdf' >/dev/null 2>&1; then
	echo "document form write oracle: PDF2_PYTHON must provide pypdf 6.1.1" >&2
	exit 2
fi
if [ "$("$python_binary" -c 'import importlib.metadata; print(importlib.metadata.version("pypdf"))')" != "6.1.1" ]; then
	echo "document form write oracle: pypdf version must be 6.1.1" >&2
	exit 2
fi

oracle_root=$(mktemp -d "${TMPDIR:-/tmp}/mintclaw-document-form-write.XXXXXX")
trap 'rm -rf -- "$oracle_root"' EXIT HUP INT TERM
source_pdf=$repo_root/pkg/document/testdata/acroform-fields.pdf
candidate_pdf=$oracle_root/filled.pdf
source_before=$(sha256sum "$source_pdf" | cut -d ' ' -f 1)

cd "$repo_root"
HOME=$oracle_root/home \
XDG_CONFIG_HOME=$oracle_root/config \
MINTCLAW_PDF2_WRITE_ORACLE_OUTPUT=$candidate_pdf \
	go test ./pkg/document -run '^TestPDFCPUFormWriteBackendFillsSupportedMatrix$' -count=1

source_after=$(sha256sum "$source_pdf" | cut -d ' ' -f 1)
if [ "$source_before" != "$source_after" ]; then
	echo "document form write oracle: source fixture changed" >&2
	exit 1
fi

"$python_binary" - "$source_pdf" "$candidate_pdf" <<'PY'
import hashlib
import pathlib
import sys

from pypdf import PdfReader

source_path = pathlib.Path(sys.argv[1])
candidate_path = pathlib.Path(sys.argv[2])
source = source_path.read_bytes()
candidate = candidate_path.read_bytes()
assert candidate.startswith(b"%PDF-")
assert candidate != source
assert len(candidate) <= 64 * 1024 * 1024
assert hashlib.sha256(candidate).hexdigest() != hashlib.sha256(source).hexdigest()

reader = PdfReader(candidate_path)
assert not reader.is_encrypted
assert len(reader.pages) == 2
acroform = reader.trailer["/Root"]["/AcroForm"]
assert "/XFA" not in acroform
fields = reader.get_fields()
assert fields is not None
assert set(fields) == {
    "agree", "color", "country", "full_name", "notes", "repeated", "start_date", "tags"
}
assert fields["full_name"]["/V"] == "MintClaw Updated"
assert fields["notes"]["/V"] == "first line\nsecond line"
assert fields["agree"]["/V"] == "/Off"
assert fields["color"]["/V"] == "/Blue"
assert fields["country"]["/V"] == " EU "
assert fields["tags"]["/V"] == ["two", "three"]
assert fields["repeated"]["/V"] == "same on both pages"

roots = {ref.get_object().get("/T"): ref.get_object() for ref in acroform["/Fields"]}
assert roots["start_date"]["/Kids"][0].get_object()["/V"] == "09/14/2026"
assert roots["agree"]["/AS"] == "/Off"
assert [kid.get_object()["/AS"] for kid in roots["color"]["/Kids"]] == ["/Off", "/Blue"]

widget_count = 0
for field in roots.values():
    widgets = [field] if field.get("/Subtype") == "/Widget" else [
        ref.get_object() for ref in field.get("/Kids", []) if ref.get_object().get("/Subtype") == "/Widget"
    ]
    assert widgets
    for widget in widgets:
        assert "/AP" in widget and "/N" in widget["/AP"]
        widget_count += 1
assert widget_count == 10
PY

echo "production=pdfcpu version=v0.15.0"
echo "oracle=pypdf version=6.1.1"
echo "marker=MINTCLAW_PDF2_FORM_WRITE_ORACLE_OK"
