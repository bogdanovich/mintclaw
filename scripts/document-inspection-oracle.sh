#!/bin/sh

set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
fixture_root=$repo_root/pkg/document/testdata
expected_version=${PDF0B_POPPLER_VERSION:-}

for command in pdfinfo pdftotext pdfsig python3; do
	if ! command -v "$command" >/dev/null 2>&1; then
		echo "document inspection oracle: missing $command" >&2
		exit 1
	fi
done

actual_version=$(pdfinfo -v 2>&1 | sed -n '1s/.* version //p')
if [ -z "$actual_version" ]; then
	echo "document inspection oracle: could not resolve Poppler version" >&2
	exit 1
fi
if [ -n "$expected_version" ] && [ "$actual_version" != "$expected_version" ]; then
	echo "document inspection oracle: Poppler $actual_version, expected $expected_version" >&2
	exit 1
fi

python3 - "$fixture_root" "$actual_version" <<'PY'
import json
import pathlib
import subprocess
import sys

fixture_root = pathlib.Path(sys.argv[1])
poppler_version = sys.argv[2]
manifest = json.loads((fixture_root / "inspection-manifest.json").read_text(encoding="utf-8"))


def run(*arguments):
    return subprocess.run(arguments, text=True, capture_output=True, check=False)


def parse_info(output):
    result = {}
    for line in output.splitlines():
        if ":" not in line:
            continue
        key, value = line.split(":", 1)
        result[key.strip()] = value.strip()
    return result


def page_text_state(path, pages):
    states = []
    for page in range(1, pages + 1):
        result = run("pdftotext", "-f", str(page), "-l", str(page), str(path), "-")
        if result.returncode != 0:
            raise AssertionError(f"pdftotext failed for {path.name} page {page}: {result.stderr.strip()}")
        states.append("present" if result.stdout.strip("\x0c\r\n\t ") else "absent")
    if all(state == "present" for state in states):
        return "present"
    if all(state == "absent" for state in states):
        return "absent"
    return "mixed"


for fixture in manifest["fixtures"]:
    expected = fixture["expected"]
    path = fixture_root / fixture["file"]
    info_result = run("pdfinfo", str(path))
    differences = []
    if expected["state"] == "unsupported":
        combined = info_result.stdout + info_result.stderr
        assert info_result.returncode != 0 and "Incorrect password" in combined, fixture["id"]
        print(f'oracle_fixture={fixture["id"]} state=agreement class=password_required')
        continue
    if expected["state"] == "failed":
        if info_result.returncode == 0:
            print(
                f'oracle_fixture={fixture["id"]} state=declared_difference '
                "reason=poppler_repairs_backend_rejected_structure"
            )
        else:
            print(f'oracle_fixture={fixture["id"]} state=agreement class=malformed_pdf')
        continue

    assert info_result.returncode == 0, f'{fixture["id"]}: {info_result.stderr.strip()}'
    info = parse_info(info_result.stdout)
    assert info["PDF version"] == expected["pdf_version"], fixture["id"]
    assert int(info["Pages"]) == expected["page_count"], fixture["id"]
    assert info["Encrypted"] == "no", fixture["id"]

    expected_form = "none"
    if expected["xfa"] == "present":
        expected_form = "XFA"
        differences.append("xfa_subtype_not_reported_by_poppler")
    elif expected["acroform"] == "present":
        expected_form = "AcroForm"
    assert info["Form"] == expected_form, (fixture["id"], info["Form"], expected_form)
    oracle_text = page_text_state(path, expected["page_count"])
    if expected["text"] == "unknown":
        assert oracle_text == "absent", fixture["id"]
        differences.append("inline_image_text_signal_is_fail_closed")
    else:
        assert oracle_text == expected["text"], fixture["id"]

    signature_result = run("pdfsig", str(path))
    signature_output = signature_result.stdout + signature_result.stderr
    has_signed_field = "Signature #" in signature_output and "form field is not signed" not in signature_output
    if expected["signatures"] == "present" and expected.get("usage_rights") != "present":
        assert has_signed_field, fixture["id"]
    elif expected["signatures"] == "absent":
        assert not has_signed_field, fixture["id"]
    else:
        differences.append("usage_rights_signature_not_enumerated_by_pdfsig")
    if expected.get("certified") == "present":
        differences.append("certification_not_normalized_by_pdfsig")
    if expected.get("field_count") is not None:
        differences.append("field_count_not_reported_by_pdfinfo")

    suffix = ",".join(differences) if differences else "none"
    print(f'oracle_fixture={fixture["id"]} state=agreement declared_differences={suffix}')

print(f"oracle=poppler version={poppler_version} fixtures={len(manifest['fixtures'])} marker=MINTCLAW_PDF0B_ORACLE_OK")
PY
