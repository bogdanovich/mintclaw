"""Controller-only Calc seeds and bounded ODS checks; never a GUI executor."""

import argparse
from dataclasses import asdict, dataclass
from decimal import Decimal, InvalidOperation
from io import BytesIO
import json
import os
from pathlib import Path
import re
import sys
from types import MappingProxyType
import uuid
import xml.etree.ElementTree as ET
from xml.sax.saxutils import escape
import zipfile
import zlib

import text_fixtures as shared
import writer_fixtures as odf


NS = {**odf.NS, "number": "urn:oasis:names:tc:opendocument:xmlns:datastyle:1.0",
      "of": "urn:oasis:names:tc:opendocument:xmlns:of:1.2"}
MIME = b"application/vnd.oasis.opendocument.spreadsheet"
CANDIDATE = "document.ods"
MARKER = ".mintclaw-calc-fixture.json"
MAX_ROWS = 1048576
MAX_COLUMNS = 16384
MAX_DATA_ROWS = 16
MAX_DATA_COLUMNS = 16
MAX_NUMBER_CHARS = 128
MAX_FORMULA_CHARS = 256
MAX_STYLE_DEPTH = 16
SHEET = "Evaluation"


def tag(name):
    prefix, local = name.split(":")
    return "{" + NS[prefix] + "}" + local


@dataclass(frozen=True)
class Task:
    seed: tuple
    instruction: str
    expected: tuple
    currency_required: bool = False


TASKS = MappingProxyType({
    "CU-T13": Task(
        (),
        "Open the owned document.ods and use its existing Evaluation sheet. Enter the numeric values "
        "2, 3 and 5 in B2, B3 and B4. Enter a SUM formula for B2:B4 in B5, then save the same file. "
        "Do not type the result as a constant or add other cell data.",
        ((2, 2, "2", False), (3, 2, "3", False), (4, 2, "5", False), (5, 2, "10", True)),
    ),
    "CU-T14": Task(
        ((1, 1, "1000.25"),),
        "Open the owned document.ods and use its existing Evaluation sheet. Format A1 as currency "
        "without changing its numeric value 1000.25. Save the same file and add no other cell data.",
        ((1, 1, "1000.25", False),),
        currency_required=True,
    ),
})
FIXTURE_REVISION = shared.digest(json.dumps({
    "subset": "calc-v1", "tasks": {name: asdict(task) for name, task in TASKS.items()},
    "candidate": CANDIDATE, "sheet": SHEET, "mime": MIME.decode(), "namespaces": NS,
    "maximum_package_bytes": odf.MAX_ARTIFACT_BYTES, "maximum_xml_bytes": odf.MAX_XML_BYTES,
    "maximum_entries": odf.MAX_ENTRIES, "maximum_nodes": odf.MAX_NODES, "maximum_depth": odf.MAX_DEPTH,
    "maximum_rows": MAX_ROWS, "maximum_columns": MAX_COLUMNS,
    "data_rows": MAX_DATA_ROWS, "data_columns": MAX_DATA_COLUMNS,
    "maximum_number_characters": MAX_NUMBER_CHARS, "maximum_formula_characters": MAX_FORMULA_CHARS,
    "maximum_style_depth": MAX_STYLE_DEPTH,
    "rules": "numeric literals; OpenFormula SUM of B2:B4 with independent Decimal recalculation; "
             "finite cached result; unchanged numeric currency value and effective currency style; "
             "one named sheet; no extra data; ordered sparse repeats; no merges or multi-cell matrices; no native proof",
}, sort_keys=True, ensure_ascii=False, separators=(",", ":")).encode("utf-8"))


def xml_document(kind, body):
    declarations = " ".join(f'xmlns:{prefix}="{uri}"' for prefix, uri in NS.items())
    return (f'<?xml version="1.0" encoding="UTF-8"?><office:document-{kind} {declarations} '
            f'office:version="1.3">{body}</office:document-{kind}>').encode("utf-8")


def seed_ods(cells, automatic_styles=""):
    """Deterministic fixture packaging only; never used to complete an agent task."""
    values = {(row, column): value for row, column, value in cells}
    rows = []
    for row in range(1, 6):
        columns = []
        for column in range(1, 3):
            value = values.get((row, column))
            columns.append('<table:table-cell/>' if value is None else
                           f'<table:table-cell office:value-type="float" office:value="{escape(value)}">'
                           f'<text:p>{escape(value)}</text:p></table:table-cell>')
        rows.append('<table:table-row>' + "".join(columns) + '</table:table-row>')
    body = ('<office:automatic-styles>' + automatic_styles + '</office:automatic-styles>'
            '<office:body><office:spreadsheet><table:table table:name="' + SHEET + '">'
            '<table:table-column table:number-columns-repeated="2"/>' + "".join(rows)
            + '</table:table></office:spreadsheet></office:body>')
    parts = [("mimetype", MIME), ("content.xml", xml_document("content", body)),
             ("styles.xml", xml_document("styles", "<office:styles/>"))]
    manifest = (f'<manifest:manifest xmlns:manifest="{NS["manifest"]}" manifest:version="1.3">'
                f'<manifest:file-entry manifest:full-path="/" manifest:media-type="{MIME.decode()}"/>'
                '<manifest:file-entry manifest:full-path="content.xml" manifest:media-type="text/xml"/>'
                '<manifest:file-entry manifest:full-path="styles.xml" manifest:media-type="text/xml"/>'
                '</manifest:manifest>').encode("utf-8")
    parts.append(("META-INF/manifest.xml", manifest))
    output = BytesIO()
    with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_STORED) as archive:
        for name, data in parts:
            entry = zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0))
            entry.external_attr = 0o100600 << 16
            archive.writestr(entry, data)
    return output.getvalue()


def validate_formula_namespaces(data):
    # The package reader has already bounded and validated this XML. Retain
    # lexical namespace scopes too: a fake/rebound 'of' prefix is not a formula.
    scopes, pending = [], []
    for event, value in ET.iterparse(BytesIO(data), events=("start-ns", "start", "end")):
        if event == "start-ns":
            pending.append(value)
        elif event == "start":
            scope = dict(scopes[-1]) if scopes else {}
            scope.update(pending)
            pending.clear()
            formula = value.get(tag("table:formula"))
            if formula is not None:
                prefix, separator, _ = formula.partition(":=")
                if len(formula) > MAX_FORMULA_CHARS or not separator or scope.get(prefix) != NS["of"]:
                    raise shared.Unverifiable("unsupported_formula_namespace")
            scopes.append(scope)
        else:
            scopes.pop()


def repeat(node, name, maximum):
    value = node.get(tag(name), "1")
    if len(value) > 10 or not value.isascii() or not value.isdecimal() or not 0 < int(value) <= maximum:
        raise shared.Unverifiable("repeat_bounds")
    return int(value)


def numeric(value):
    if value is None or len(value) > MAX_NUMBER_CHARS or not re.fullmatch(
            r"[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?", value):
        raise shared.Unverifiable("unsupported_number")
    try:
        number = Decimal(value)
    except InvalidOperation as error:
        raise shared.Unverifiable("unsupported_number") from error
    if not number.is_finite():
        raise shared.Unverifiable("unsupported_number")
    return number


class Sheet:
    """A bounded sparse cell/style projection, not a spreadsheet engine."""

    def __init__(self, content, styles):
        self.cell_styles, self.formats = {}, {}
        self.default_data_style = None
        default_seen = False
        for root in (content, styles):
            for kind in ("office:styles", "office:automatic-styles"):
                containers = root.findall(tag(kind))
                if len(containers) > 1:
                    raise shared.Unverifiable("ambiguous_styles")
                for container in containers:
                    for node in odf.structural_children(container):
                        name = node.get(tag("style:name"))
                        if node.tag == tag("style:default-style") and node.get(tag("style:family")) == "table-cell":
                            if default_seen:
                                raise shared.Unverifiable("ambiguous_styles")
                            default_seen = True
                            self.default_data_style = node.get(tag("style:data-style-name"))
                        elif node.tag == tag("style:style") and node.get(tag("style:family")) == "table-cell":
                            if not name or name in self.cell_styles:
                                raise shared.Unverifiable("ambiguous_styles")
                            self.cell_styles[name] = node
                        elif node.tag.startswith("{" + NS["number"] + "}") and name:
                            if name in self.formats:
                                raise shared.Unverifiable("ambiguous_styles")
                            self.formats[name] = node
        bodies = content.findall(tag("office:body"))
        if len(bodies) != 1 or len(odf.structural_children(bodies[0])) != 1:
            raise shared.Unverifiable("unsupported_workbook")
        spreadsheet = bodies[0][0]
        if spreadsheet.tag != tag("office:spreadsheet"):
            raise shared.Unverifiable("unsupported_workbook")
        children = odf.structural_children(spreadsheet)
        tables = [node for node in children if node.tag == tag("table:table")]
        if len(tables) != 1 or tables[0].get(tag("table:name")) != SHEET:
            raise shared.Unverifiable("unsupported_workbook")
        for node in children:
            if node.tag not in {tag("table:table"), tag("table:calculation-settings")} and not (
                    node.tag == tag("table:named-expressions") and not len(node)):
                raise shared.Unverifiable("unsupported_workbook")
        self.table = tables[0]

    def currency_format(self, name, seen=()):
        if not name:
            return False
        if name in seen or len(seen) >= MAX_STYLE_DEPTH:
            raise shared.Unverifiable("style_cycle")
        node = self.formats.get(name)
        if node is None:
            raise shared.Unverifiable("missing_number_style")
        if node.tag != tag("number:currency-style"):
            return False
        if len(node.findall(tag("number:number"))) != 1 or not any(
                "".join(symbol.itertext()).strip() for symbol in node.findall(tag("number:currency-symbol"))):
            return False
        # All conditional branches must retain currency semantics. Unsupported
        # condition languages cannot turn a currency-looking label into proof.
        return all(self.currency_format(mapping.get(tag("style:apply-style-name")), seen + (name,))
                   for mapping in node.findall(tag("style:map")))

    def currency_cell(self, name, seen=()):
        if not name:
            return self.currency_format(self.default_data_style)
        if name in seen or len(seen) >= MAX_STYLE_DEPTH:
            raise shared.Unverifiable("style_cycle")
        node = self.cell_styles.get(name)
        if node is None:
            raise shared.Unverifiable("missing_cell_style")
        data_style = node.get(tag("style:data-style-name"))
        if data_style:
            return self.currency_format(data_style)
        return self.currency_cell(node.get(tag("style:parent-style-name")), seen + (name,))

    def projection(self):
        values, defaults = {}, {}
        column, row = 1, 1
        for node in odf.structural_children(self.table):
            if node.tag == tag("table:table-column"):
                if row != 1:
                    raise shared.Unverifiable("unsupported_table")
                count = repeat(node, "table:number-columns-repeated", MAX_COLUMNS)
                if column + count - 1 > MAX_COLUMNS:
                    raise shared.Unverifiable("repeat_bounds")
                for index in range(column, min(column + count, MAX_DATA_COLUMNS + 1)):
                    defaults[index] = node.get(tag("table:default-cell-style-name"))
                column += count
            elif node.tag == tag("table:table-row"):
                copies = repeat(node, "table:number-rows-repeated", MAX_ROWS)
                if row + copies - 1 > MAX_ROWS:
                    raise shared.Unverifiable("repeat_bounds")
                cells, position = [], 1
                for cell in odf.structural_children(node):
                    if cell.tag != tag("table:table-cell") or any(
                            repeat(cell, name, MAX_COLUMNS) != 1 for name in
                            ("table:number-columns-spanned", "table:number-rows-spanned",
                             "table:number-matrix-columns-spanned", "table:number-matrix-rows-spanned")):
                        raise shared.Unverifiable("unsupported_cell")
                    if any(child.tag != tag("text:p") for child in odf.structural_children(cell)):
                        raise shared.Unverifiable("unsupported_cell")
                    count = repeat(cell, "table:number-columns-repeated", MAX_COLUMNS)
                    if position + count - 1 > MAX_COLUMNS:
                        raise shared.Unverifiable("repeat_bounds")
                    meaningful = cell.get(tag("office:value-type")) is not None or \
                        cell.get(tag("table:formula")) is not None or any(
                            "".join(p.itertext()) for p in cell.findall(tag("text:p")))
                    if meaningful:
                        if position + count - 1 > MAX_DATA_COLUMNS or row + copies - 1 > MAX_DATA_ROWS:
                            raise shared.Unverifiable("data_bounds")
                        if cell.get(tag("office:value-type")) not in {"float", "currency"}:
                            raise shared.Unverifiable("unsupported_cell")
                        value = numeric(cell.get(tag("office:value")))
                        for index in range(position, position + count):
                            style = (cell.get(tag("table:style-name"))
                                     or node.get(tag("table:default-cell-style-name")) or defaults.get(index))
                            cells.append((index, value, cell.get(tag("table:formula")), style))
                    position += count
                for offset in range(copies if cells else 0):
                    for index, value, formula, style in cells:
                        values[(row + offset, index)] = (value, formula, style)
                row += copies
            else:
                raise shared.Unverifiable("unsupported_table")
        return values

    def matches(self, task):
        values = self.projection()
        if set(values) != {(r, c) for r, c, _, _ in task.expected}:
            return False
        for row, column, expected, sum_formula in task.expected:
            value, formula, style = values[(row, column)]
            if value != Decimal(expected):
                return False
            if sum_formula:
                expression = formula.partition(":=")[2] if formula else ""
                if not re.fullmatch(r"\s*SUM\s*\(\s*\[\.\$?B\$?2:\.\$?B\$?4\]\s*\)\s*",
                                    expression, re.IGNORECASE | re.ASCII):
                    return False
                if value != sum((values[(r, 2)][0] for r in (2, 3, 4)), Decimal(0)):
                    return False
            elif formula is not None:
                return False
            if task.currency_required and not self.currency_cell(style):
                return False
        return True


def prepare(task_id, path):
    task = TASKS[task_id]
    Path(path).mkdir(mode=0o700, parents=False, exist_ok=False)
    directory, info = shared.open_directory(path)
    try:
        record = {"task_id": task_id, "fixture_revision": FIXTURE_REVISION, "run_id": uuid.uuid4().hex,
                  "root_device": info.st_dev, "root_inode": info.st_ino}
        shared.write_new(directory, MARKER, json.dumps(record, sort_keys=True).encode("utf-8"))
        shared.write_new(directory, CANDIDATE, seed_ods(task.seed))
        return {**record, "candidate": CANDIDATE, "instruction": task.instruction,
                "seeded": True, "native_task_qualified": False}
    finally:
        os.close(directory)


def check_artifact(task_id, path):
    result = {"task_id": task_id, "fixture_revision": FIXTURE_REVISION,
              "artifact_matches": None, "native_task_qualified": False}
    try:
        directory, root = shared.open_directory(path)
        try:
            record = json.loads(shared.read_owned_file(directory, MARKER, 4096), object_pairs_hook=shared.unique_object)
            if not isinstance(record, dict) or set(record) != {
                "task_id", "fixture_revision", "run_id", "root_device", "root_inode",
            } or record["task_id"] != task_id or record["fixture_revision"] != FIXTURE_REVISION or \
                    record["root_device"] != root.st_dev or record["root_inode"] != root.st_ino or \
                    not isinstance(record["run_id"], str) or len(record["run_id"]) != 32 or \
                    any(char not in "0123456789abcdef" for char in record["run_id"]):
                raise shared.Unverifiable("invalid_fixture_record")
            data = shared.read_owned_file(directory, CANDIDATE, odf.MAX_ARTIFACT_BYTES)
            content, styles = odf.read_package(data, MIME)
            with zipfile.ZipFile(BytesIO(data)) as archive:
                validate_formula_namespaces(archive.read("content.xml"))
            matches = Sheet(content, styles).matches(TASKS[task_id])
            named = os.stat(path, follow_symlinks=False)
            if (root.st_dev, root.st_ino, root.st_uid, root.st_mode) != (
                named.st_dev, named.st_ino, named.st_uid, named.st_mode,
            ):
                raise shared.Unverifiable("directory_changed")
            result.update(run_id=record["run_id"], artifact_matches=matches,
                          artifact_sha256=shared.digest(data), artifact_bytes=len(data))
        finally:
            os.close(directory)
    except shared.Unverifiable as error:
        result["reason"] = str(error)
    except (OSError, ValueError, TypeError, KeyError, RecursionError, RuntimeError, ArithmeticError,
            ET.ParseError, zipfile.BadZipFile, zlib.error, NotImplementedError):
        result["reason"] = "artifact_unavailable"
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("prepare", "check-artifact"))
    parser.add_argument("--task", choices=tuple(TASKS), required=True)
    parser.add_argument("--run-directory", required=True)
    args = parser.parse_args()
    if args.operation == "prepare":
        try:
            result = prepare(args.task, args.run_directory)
        except (OSError, shared.Unverifiable):
            print(json.dumps({"reason": "setup_refused", "native_task_qualified": False}))
            return 2
    else:
        result = check_artifact(args.task, args.run_directory)
    print(json.dumps(result, sort_keys=True, ensure_ascii=False))
    if result.get("artifact_matches") is False:
        return 1
    return 2 if result.get("reason") else 0


if __name__ == "__main__":
    sys.exit(main())
