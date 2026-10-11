"""Manual oracle controls, not target/model qualification or required CI."""

import hashlib
import io
import json
from pathlib import Path
import secrets
import subprocess
import sys
import unittest

from pypdf import PdfReader, PdfWriter
from pypdf.generic import NameObject, TextStringObject
from reportlab.pdfgen import canvas

REPOSITORY = Path(__file__).resolve().parents[2]
ORACLE = (REPOSITORY / "internal/eval/pdf_oracle.py").read_text()


class PDFOracleControls(unittest.TestCase):
    def setUp(self):
        self.source = (REPOSITORY / "evals/fixtures/pdf/loan.pdf").read_bytes()
        self.canary = "MC_EVAL_PRIVATE_" + secrets.token_hex(8)
        self.fields = [
            {"name": "borrower_name", "kind": "text", "preserve": True},
            {"name": "contact_reference", "kind": "text", "value": self.canary},
            {"name": "return_by_mail", "kind": "checkbox", "value": "false"},
            {"name": "delivery_note", "kind": "text"},
            {"name": "equipment", "kind": "text", "preserve": True},
        ]

    def filled(self, value=None):
        writer = PdfWriter(clone_from=io.BytesIO(self.source))
        writer.update_page_form_field_values(
            writer.pages[0], {"contact_reference": value or self.canary}, auto_regenerate=False
        )
        output = io.BytesIO()
        writer.write(output)
        return output.getvalue()

    def invoke(self, artifact, metadata=None):
        request = {
            "version": 1, "source_size": len(self.source), "artifact_size": len(artifact),
            "source_sha256": hashlib.sha256(self.source).hexdigest(),
            "pages": 2, "field_count": 5, "fields": self.fields,
        }
        header = json.dumps(request).encode() if metadata is None else metadata
        process = subprocess.run(
            [sys.executable, "-I", "-c", ORACLE], input=header + b"\n" + self.source + artifact,
            capture_output=True, timeout=60, check=False,
        )
        self.assertFalse(self.canary.encode() in process.stdout + process.stderr, "oracle leaked a private value")
        if process.returncode != 0:
            return None
        return json.loads(process.stdout)

    def test_correct_independent_fill(self):
        result = self.invoke(self.filled())
        self.assertIsNotNone(result, "oracle control unavailable")
        self.assertEqual(result["code"], "matched")
        self.assertEqual(result["checked_fields"], 5)
        self.assertEqual(result["rendered_pages"], 2)

    def test_fake_success_unfilled_file(self):
        result = self.invoke(self.source)
        self.assertEqual(result["code"], "mismatch")
        self.assertFalse(result["fields_matched"])
        self.assertFalse(result["appearance_matched"])

    def test_old_value_after_correction(self):
        result = self.invoke(self.filled("MC_EVAL_OLD_VALUE_" + secrets.token_hex(8)))
        self.assertEqual(result["code"], "mismatch")
        self.assertFalse(result["fields_matched"])
        self.assertFalse(result["appearance_matched"])

    def test_stale_appearance(self):
        writer = PdfWriter(clone_from=io.BytesIO(self.source))
        for reference in writer.pages[0]["/Annots"]:
            annotation = reference.get_object()
            if annotation.get("/T") == "contact_reference":
                annotation[NameObject("/V")] = TextStringObject(self.canary)
        output = io.BytesIO()
        writer.write(output)
        result = self.invoke(output.getvalue())
        self.assertEqual(result["code"], "mismatch")
        self.assertTrue(result["fields_matched"])
        self.assertFalse(result["appearance_matched"])

    def test_changed_background(self):
        writer = PdfWriter(clone_from=io.BytesIO(self.filled()))
        overlay = io.BytesIO()
        pdf = canvas.Canvas(overlay)
        pdf.drawString(48, 100, "Unexpected page content")
        pdf.save()
        writer.pages[0].merge_page(PdfReader(overlay).pages[0])
        output = io.BytesIO()
        writer.write(output)
        result = self.invoke(output.getvalue())
        self.assertEqual(result["code"], "mismatch")
        self.assertFalse(result["background_matched"])

    def test_invisible_value(self):
        writer = PdfWriter(clone_from=io.BytesIO(self.source))
        for reference in writer.pages[0]["/Annots"]:
            annotation = reference.get_object()
            if annotation.get("/T") == "contact_reference":
                annotation[NameObject("/DA")] = TextStringObject("/Helv 12 Tf 1 1 1 rg")
        writer.update_page_form_field_values(
            writer.pages[0], {"contact_reference": self.canary}, auto_regenerate=False
        )
        output = io.BytesIO()
        writer.write(output)
        result = self.invoke(output.getvalue())
        self.assertEqual(result["code"], "mismatch")
        self.assertTrue(result["fields_matched"])
        self.assertFalse(result["appearance_matched"])

    def test_serialized_empty_required_fixture(self):
        reader = PdfReader(REPOSITORY / "evals/fixtures/pdf/empty.pdf")
        for name in ("borrower_name", "contact_reference"):
            field = reader.get_fields()[name]
            self.assertTrue("/V" in field)
            self.assertTrue(field["/V"] == "", "fixture is not empty")
            self.assertTrue(int(field["/Ff"]) & 2)

    def test_wrong_artifact_schema(self):
        artifact = (REPOSITORY / "evals/fixtures/pdf/conditional.pdf").read_bytes()
        result = self.invoke(artifact)
        self.assertEqual(result["code"], "mismatch")
        self.assertFalse(result["structure_matched"])
        self.assertEqual(result["rendered_pages"], 0)

    def test_malformed_and_private_input_fail_closed(self):
        for metadata in (b"{}", b'{"version":1,"version":1}', self.canary.encode()):
            self.assertIsNone(self.invoke(self.source, metadata), "malformed input was admitted")
        self.assertIsNone(self.invoke(b"%PDF broken " + self.canary.encode()), "broken PDF was admitted")


if __name__ == "__main__":
    unittest.main()
