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

poppler_render=/usr/bin/pdftoppm
poppler_text=/usr/bin/pdftotext
if [ ! -x "$poppler_render" ] || [ ! -x "$poppler_text" ] || \
	[ "$(sha256sum "$poppler_render" | cut -d ' ' -f 1)" != "207dcabcaeea0ce572aefc498d07d44d56a9ca06a85b3ae1fecd050476a34bf8" ] || \
	[ "$(sha256sum "$poppler_text" | cut -d ' ' -f 1)" != "0fb98ea179e19154a90202608c164f2a319b79f16576fa6534b2d601033565e7" ]; then
	echo "document form write oracle: pinned Poppler 24.02.0 is required" >&2
	exit 2
fi
if [ "$("$python_binary" -c 'import importlib.metadata; print(importlib.metadata.version("pypdf"))')" != "6.1.1" ]; then
	echo "document form write oracle: pypdf version must be 6.1.1" >&2
	exit 2
fi

oracle_root=$(mktemp -d "${TMPDIR:-/tmp}/mintclaw-document-form-write.XXXXXX")
trap 'rm -rf -- "$oracle_root"' EXIT HUP INT TERM
source_pdf=$repo_root/pkg/document/testdata/acroform-fields.pdf
visual_manifest=$repo_root/pkg/document/testdata/form-visual-manifest.json
candidate_pdf=$oracle_root/filled.pdf
source_before=$(sha256sum "$source_pdf" | cut -d ' ' -f 1)

if [ -n "${PDF2_DOCUMENT_TEST_BINARY:-}" ]; then
	(
		cd "$repo_root/pkg/document"
		HOME=$oracle_root/home \
		XDG_CONFIG_HOME=$oracle_root/config \
		MINTCLAW_PDF2_WRITE_ORACLE_OUTPUT=$candidate_pdf \
			"$PDF2_DOCUMENT_TEST_BINARY" \
			-test.run '^TestPDFCPUFormWriteBackendFillsSupportedMatrix$' -test.count=1
	)
else
	cd "$repo_root"
	HOME=$oracle_root/home \
	XDG_CONFIG_HOME=$oracle_root/config \
	MINTCLAW_PDF2_WRITE_ORACLE_OUTPUT=$candidate_pdf \
		go test ./pkg/document -run '^TestPDFCPUFormWriteBackendFillsSupportedMatrix$' -count=1
fi

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
assert acroform["/NeedAppearances"].value is False
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

visual_oracle=${PDF2_VISUAL_ORACLE_BINARY:-$oracle_root/document-form-visual-oracle}
if [ -z "${PDF2_VISUAL_ORACLE_BINARY:-}" ]; then
	go build -buildvcs=false -o "$visual_oracle" "$repo_root/scripts/internal/document-form-visual-oracle"
fi

for page in 1 2; do
	visible=$oracle_root/page-$page-visible
	background=$oracle_root/page-$page-background
	"$poppler_render" -f "$page" -l "$page" -singlefile -png -cropbox -r 144 \
		"$candidate_pdf" "$visible" >/dev/null 2>"$oracle_root/page-$page-visible.stderr"
	"$poppler_render" -f "$page" -l "$page" -singlefile -png -cropbox -r 144 -hide-annotations \
		"$candidate_pdf" "$background" >/dev/null 2>"$oracle_root/page-$page-background.stderr"
done
"$visual_oracle" compare "$visual_manifest" \
	"$oracle_root/page-1-visible.png" "$oracle_root/page-1-background.png" \
	"$oracle_root/page-2-visible.png" "$oracle_root/page-2-background.png"

echo "production=pdfcpu version=v0.15.0"
echo "visual=poppler version=24.02.0 dpi=144"
echo "oracle=pypdf version=6.1.1 golden=normalized-difference-grid tolerance=0.002"
echo "marker=MINTCLAW_PDF2_FORM_WRITE_ORACLE_OK"
