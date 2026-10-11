"""Generate the unrelated medium PDFI4 AcroForm; synthetic test data only.

Run: uv run --with reportlab==4.4.10 python docs/operations/pdfi4-fixtures.py /tmp/pdfi4-fixtures
Small, conditional and large fixtures remain in pdfi3-fixtures.py.
"""

import argparse
import hashlib
import importlib.util
import json
import sys
from pathlib import Path

from reportlab.lib.pagesizes import letter
from reportlab.pdfgen import canvas


def load_template():
    path = Path(__file__).with_name("pdfi3-fixtures.py")
    spec = importlib.util.spec_from_file_location("pdfi3_fixture_template", path)
    template = importlib.util.module_from_spec(spec)
    previous = sys.dont_write_bytecode
    try:
        sys.dont_write_bytecode = True
        spec.loader.exec_module(template)
    finally:
        sys.dont_write_bytecode = previous
    return template


def medium(path):
    template = load_template()
    pdf = canvas.Canvas(str(path), pagesize=letter, invariant=True)
    labels = [
        ("name", "Full legal name"),
        ("birth_date", "Date of birth (YYYY-MM-DD)"),
        ("email", "Email address"),
        ("phone", "Telephone number"),
        ("street", "Street address"),
        ("city", "City or town"),
        ("postal", "Postal code"),
        ("country", "Country"),
    ]
    roles = [("borrower", "Borrower"), ("contact", "Emergency contact")]
    for number, (role, title) in enumerate(roles, 1):
        template.page(pdf, "Community Equipment Loan", [
            "The borrower requests the loan; the emergency contact is a different person.",
            "Never reuse one person's information for the other unless explicitly supplied.",
            f"This page records the {title.lower()}. Ask only missing or ambiguous facts.",
            "Preserve existing data; deferred fields may remain editable in a requested draft.",
        ], number, 3)
        for row, (key, label) in enumerate(labels):
            template.text(pdf, f"{role}_{key}", f"{title}: {label}", 570 - row * 51)
        pdf.showPage()
    template.page(pdf, "Loan Arrangements", [
        "Preserve the pre-entered equipment unless an explicit correction is requested.",
        "The delivery note is optional. Checked means return by mail, unchecked means in person.",
        "Do not fill the handwritten signature. A partial draft is not a completed application.",
    ], 3, 3)
    template.text(pdf, "equipment", "Equipment description", 555, "Synthetic Mint Camera")
    template.text(pdf, "delivery_note", "Delivery note (optional)", 465)
    pdf.acroForm.checkbox(
        name="return_by_mail", tooltip="Return equipment by mail", x=48, y=405,
        size=16, checked=False, buttonStyle="check", forceBorder=True,
    )
    pdf.setFont("Helvetica", 11)
    pdf.drawString(74, 409, "Return equipment by mail")
    pdf.drawString(48, 340, "Handwritten signature (leave untouched)")
    pdf.acroForm.textfield(
        name="signature", tooltip="Handwritten signature; do not fill electronically",
        x=48, y=310, width=480, height=22, value="", maxlen=80,
        fieldFlags="readOnly", forceBorder=True,
    )
    pdf.save()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output", type=Path)
    output = parser.parse_args().output
    output.mkdir(parents=True, exist_ok=True)
    path = output / "pdfi4-medium.pdf"
    if path.exists():
        parser.error("refusing to overwrite an existing fixture")
    medium(path)
    print(json.dumps({
        "name": path.name, "pages": 3, "fields": 20, "writable_fields": 19,
        "sha256": hashlib.sha256(path.read_bytes()).hexdigest(), "privacy": "synthetic_only",
    }, indent=2))


if __name__ == "__main__":
    main()
