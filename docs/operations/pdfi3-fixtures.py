"""Generate synthetic PDFI3 qualification forms, never operator documents.

Run: uv run --with reportlab==4.4.10 python docs/operations/pdfi3-fixtures.py /tmp/pdfi3-fixtures
"""

import argparse
import hashlib
import json
from pathlib import Path

from reportlab.lib import colors
from reportlab.lib.pagesizes import letter
from reportlab.pdfgen import canvas


def page(pdf, title, lines, number, total):
    pdf.setFont("Helvetica-Bold", 16)
    pdf.drawString(48, 744, title)
    pdf.setFont("Helvetica", 10)
    for index, line in enumerate(lines):
        pdf.drawString(48, 718 - index * 15, line)
    pdf.setFont("Helvetica", 9)
    pdf.drawString(48, 28, f"SYNTHETIC TEST ONLY - DO NOT SUBMIT | Page {number} of {total}")


def text(pdf, name, label, y, value="", required=False):
    pdf.setFont("Helvetica", 11)
    pdf.drawString(48, y + 29, label)
    pdf.acroForm.textfield(
        name=name,
        tooltip=label,
        x=48,
        y=y,
        width=480,
        height=22,
        value=value,
        maxlen=80,
        fieldFlags="required" if required else "",
        borderColor=colors.grey,
        fillColor=colors.white,
        textColor=colors.black,
        forceBorder=True,
    )


def small(path):
    pdf = canvas.Canvas(str(path), pagesize=letter, invariant=True)
    page(pdf, "Equipment Loan Application", [
        "Record the borrower, contact, and return arrangements for one equipment loan.",
        "Preserve existing data unless a correction is requested. The delivery note is optional.",
        "The return-by-mail checkbox means checked = mail; unchecked = in-person return.",
        "Review all entries and obtain confirmation before producing a completed copy.",
    ], 1, 2)
    text(pdf, "borrower_name", "Borrower name (required)", 560, "Synthetic Borrower", True)
    text(pdf, "contact_reference", "Contact reference (required)", 480, required=True)
    pdf.acroForm.checkbox(
        name="return_by_mail", tooltip="Return equipment by mail", x=48, y=414,
        size=16, checked=False, buttonStyle="check", forceBorder=True,
    )
    pdf.setFont("Helvetica", 11)
    pdf.drawString(74, 418, "Return equipment by mail")
    pdf.showPage()
    page(pdf, "Equipment Loan - Delivery", [
        "The delivery note may be left blank. Do not invent a note.",
        "The existing equipment description should be preserved unless explicitly corrected.",
    ], 2, 2)
    text(pdf, "delivery_note", "Delivery note (optional)", 550)
    text(pdf, "equipment", "Equipment description", 470, "Test Laptop")
    pdf.save()


def conditional(path):
    pdf = canvas.Canvas(str(path), pagesize=letter, invariant=True)
    page(pdf, "Conditional Borrower Intake", [
        "This form supports an own request or a representative completing it for another person.",
        "For an own request, complete the borrower section and leave representative fields blank.",
        "For another person's request, complete both sections. Never copy one person's facts to another.",
        "Establish which request applies before asking for personal details.",
    ], 1, 2)
    text(pdf, "borrower", "Borrower name", 540)
    pdf.showPage()
    page(pdf, "Representative And Routing", [
        "Representative name is applicable only when completing a request for another person.",
        "Routing code is an office-use field; no code-to-purpose mapping is provided in this form.",
        "Do not guess an office routing code. Ask whether separate office instructions exist.",
    ], 2, 2)
    text(pdf, "representative", "Representative name (conditional)", 540)
    text(pdf, "routing", "Office routing code (office use only)", 450)
    pdf.save()


def large(path):
    pdf = canvas.Canvas(str(path), pagesize=letter, invariant=True)
    sections = ["Inventory"] * 3 + ["Condition"] * 3 + ["Safety"] * 2 + ["Handover"] * 2
    for page_index, section in enumerate(sections, 1):
        page(pdf, "Batch Equipment Inspection", [
            "Ten pages record inventory (1-3), condition (4-6), safety (7-8), and handover (9-10).",
            "Choose the requested section before collection. Do not treat every field as applicable.",
            f"This page belongs to {section}. Each finding is a short synthetic inspection reference.",
            "Only use supplied facts; ambiguous findings need clarification, not guessed codes.",
        ], page_index, 10)
        for row in range(10):
            number = (page_index - 1) * 10 + row + 1
            text(pdf, f"finding_{number:03}", f"{section} finding {number}", 580 - row * 50)
        pdf.showPage()
    pdf.save()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output", type=Path)
    output = parser.parse_args().output
    output.mkdir(parents=True, exist_ok=True)
    expected = {"small": (2, 5), "conditional": (2, 3), "large": (10, 100)}
    manifest = []
    for name, generate in (("small", small), ("conditional", conditional), ("large", large)):
        path = output / f"pdfi3-{name}.pdf"
        generate(path)
        pages, fields = expected[name]
        manifest.append({
            "name": path.name, "pages": pages, "fields": fields,
            "sha256": hashlib.sha256(path.read_bytes()).hexdigest(), "privacy": "synthetic_only",
        })
    print(json.dumps(manifest, indent=2))


if __name__ == "__main__":
    main()
