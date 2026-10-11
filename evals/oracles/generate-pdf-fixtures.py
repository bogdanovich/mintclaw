"""Regenerate only the repository's synthetic PDF eval fixtures."""

import hashlib
import importlib.util
import json
import sys
from pathlib import Path

from pypdf import PdfReader, PdfWriter


def main():
    sys.dont_write_bytecode = True
    repository = Path(__file__).resolve().parents[2]
    spec = importlib.util.spec_from_file_location(
        "pdfi3_fixtures", repository / "docs/operations/pdfi3-fixtures.py"
    )
    generator = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(generator)
    directory = repository / "evals/fixtures/pdf"
    directory.mkdir(parents=True, exist_ok=True)
    for name, generate in (
        ("loan", generator.small),
        ("conditional", generator.conditional),
        ("large", generator.large),
    ):
        generate(directory / f"{name}.pdf")
    writer = PdfWriter(clone_from=directory / "loan.pdf")
    writer.update_page_form_field_values(
        writer.pages[0], {"borrower_name": ""}, auto_regenerate=False
    )
    writer.write(directory / "empty.pdf")
    records = []
    for path in sorted(directory.glob("*.pdf")):
        reader = PdfReader(path, strict=True)
        records.append({
            "path": str(path.relative_to(repository / "evals")),
            "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
            "pages": len(reader.pages),
            "fields": len(reader.get_fields()),
        })
    print(json.dumps(records, indent=2))


if __name__ == "__main__":
    main()
