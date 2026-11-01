"""Controller-only Writer fixtures and bounded saved-ODT checks; never a GUI executor."""

import argparse
from dataclasses import asdict, dataclass
from io import BytesIO
import json
import os
from pathlib import Path
import sys
from types import MappingProxyType
from typing import Optional
import uuid
import xml.etree.ElementTree as ET
from xml.sax.saxutils import escape
import zipfile
import zlib

import text_fixtures as shared


NS = {
    "office": "urn:oasis:names:tc:opendocument:xmlns:office:1.0",
    "text": "urn:oasis:names:tc:opendocument:xmlns:text:1.0",
    "style": "urn:oasis:names:tc:opendocument:xmlns:style:1.0",
    "table": "urn:oasis:names:tc:opendocument:xmlns:table:1.0",
    "fo": "urn:oasis:names:tc:opendocument:xmlns:xsl-fo-compatible:1.0",
    "manifest": "urn:oasis:names:tc:opendocument:xmlns:manifest:1.0",
}
MIME = b"application/vnd.oasis.opendocument.text"
CANDIDATE = "document.odt"
MARKER = ".mintclaw-writer-fixture.json"
MAX_ARTIFACT_BYTES = 2 * 1024 * 1024
MAX_XML_BYTES = 256 * 1024
MAX_ENTRIES = 64
MAX_NODES = 4096
MAX_DEPTH = 64
MAX_DIRECTORY_ENTRIES = 16

# The earlier marker is a deliberate navigation distractor. Exact paragraph
# order, text and the final insertion are checked in the saved ODT.
LONG_PARAGRAPHS = tuple(
    f"Paragraph {index:03d}: {shared.U} — preserve this seeded paragraph exactly."
    + (" END-MARKER is not the final marker." if index == 64 else "")
    for index in range(1, 129)
) + ("END-MARKER",)

@dataclass(frozen=True)
class Task:
    seed: Optional[str]
    instruction: str
    expected: tuple
    candidate: str = CANDIDATE
    exact_directory: bool = False
    unchanged_documents: tuple = ()
    intermediate_states: tuple = ()


TASKS = MappingProxyType({
    "CU-T06": Task(
        None,
        "Create a Writer document with a heading containing exactly " + shared.U
                       + " and a separate bold paragraph containing exactly " + shared.R
                       + ". Save as document.odt in the admitted task directory.",
        (("heading", shared.U), ("paragraph", shared.R, True)),
    ),
    "CU-T07": Task(
        "<text:p>" + escape(shared.U) + "</text:p>",
        "Open the owned document.odt. Preserve its existing paragraph exactly. "
                       "Below it add one ordered list with exactly three items: alpha, Привет, 世界. Save the same file.",
        (("paragraph", shared.U, False), ("ordered_list", ("alpha", "Привет", "世界"))),
    ),
    "CU-T08": Task(
        "<text:p/>",
        "Open the owned blank document.odt. Insert one real 3-by-3 table. "
                       "Fill rows left-to-right with r1c1, r1c2, r1c3; r2c1, r2c2, r2c3; r3c1, r3c2, r3c3. "
                       "Save the same file without adding other text.",
        (("table", tuple(tuple(f"r{row}c{column}" for column in range(1, 4)) for row in range(1, 4))),),
    ),
    "CU-T09": Task(
        "".join("<text:p>" + escape(value) + "</text:p>" for value in LONG_PARAGRAPHS),
        "Open the owned document.odt. Navigate the long document to the final END-MARKER. "
        "In that same paragraph append one space followed by exactly " + shared.R
        + ". Preserve all preceding paragraphs, including the earlier marker, exactly. Save the same file.",
        tuple(("paragraph", value, False) for value in LONG_PARAGRAPHS[:-1])
        + (("paragraph", "END-MARKER " + shared.R, False),),
    ),
    "CU-T10": Task(
        None,
        "Create a Writer document containing exactly " + shared.U
        + ". Use the application's Save As UI to save as Крем-世界.odt in the admitted task directory. "
        "Do not create another document there. Close the saved document to release its temporary lock file.",
        (("paragraph", shared.U, False),),
        candidate="Крем-世界.odt",
        exact_directory=True,
    ),
    "CU-T11": Task(
        "<text:p>" + escape(shared.U) + "</text:p>",
        "Use Writer's admitted Open dialog to open the owned document.odt in the task directory. "
        "Replace its paragraph with exactly " + shared.R
        + ". Save the same file. Do not open or change unrelated.odt or the guard window.",
        (("paragraph", shared.R, False),),
        exact_directory=True,
        unchanged_documents=(("unrelated.odt", "<text:p>GUARD_UNCHANGED</text:p>"),),
    ),
    "CU-T12": Task(
        "<text:p>" + escape(shared.U) + "</text:p>",
        "Open the owned document.odt. Replace its paragraph with exactly " + shared.R
        + ". Observe that edit, undo it and observe the original paragraph, then redo it and observe "
        "the replacement again. Save the same file. The native controller must independently observe all three states.",
        (("paragraph", shared.R, False),),
        intermediate_states=(("after_edit", shared.R), ("after_undo", shared.U), ("after_redo", shared.R)),
    ),
})

# This is a subset/input revision, not a native-runner revision or task score.
FIXTURE_REVISION = shared.digest(json.dumps({
    "subset": "writer-v3", "tasks": {key: asdict(task) for key, task in TASKS.items()}, "candidate": CANDIDATE,
    "maximum_bytes": MAX_ARTIFACT_BYTES, "maximum_xml_bytes": MAX_XML_BYTES,
    "maximum_entries": MAX_ENTRIES, "maximum_nodes": MAX_NODES, "maximum_depth": MAX_DEPTH,
    "maximum_directory_entries": MAX_DIRECTORY_ENTRIES,
    "semantic_rules": "heading; all nonempty paragraph runs bold; numbered list level 1; exact unmerged table; "
                      "ignore empty paragraphs; no Unicode normalization; unsupported structures unverifiable; "
                      "unchanged document bytes; selected exact census; ordered intermediate requirements are not receipts",
}, sort_keys=True, ensure_ascii=False, separators=(",", ":")).encode("utf-8"))


def tag(name):
    prefix, local = name.split(":")
    return "{" + NS[prefix] + "}" + local


def structural_children(node):
    if node.text and node.text.strip() or any(child.tail and child.tail.strip() for child in node):
        raise shared.Unverifiable("unsupported_structure")
    return list(node)


def xml_document(kind, body):
    declarations = " ".join(f'xmlns:{prefix}="{uri}"' for prefix, uri in NS.items())
    return (f'<?xml version="1.0" encoding="UTF-8"?><office:document-{kind} {declarations} '
            f'office:version="1.3">{body}</office:document-{kind}>').encode("utf-8")


def seed_odt(body):
    content = xml_document("content", "<office:automatic-styles/><office:body><office:text>"
                           + body + "</office:text></office:body>")
    styles = xml_document("styles", "<office:styles/>")
    manifest = (f'<manifest:manifest xmlns:manifest="{NS["manifest"]}" manifest:version="1.3">'
                f'<manifest:file-entry manifest:full-path="/" manifest:media-type="{MIME.decode()}"/>'
                '<manifest:file-entry manifest:full-path="content.xml" manifest:media-type="text/xml"/>'
                '<manifest:file-entry manifest:full-path="styles.xml" manifest:media-type="text/xml"/>'
                '</manifest:manifest>').encode("utf-8")
    output = BytesIO()
    with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_STORED) as archive:
        for name, data in [("mimetype", MIME), ("content.xml", content), ("styles.xml", styles),
                           ("META-INF/manifest.xml", manifest)]:
            entry = zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0))
            entry.external_attr = 0o100600 << 16
            archive.writestr(entry, data)
    return output.getvalue()


def parse_xml(data):
    if len(data) > MAX_XML_BYTES:
        raise shared.Unverifiable("xml_too_large")
    value = data.decode("utf-8-sig")
    if "<!doctype" in value.lower() or "<!entity" in value.lower() or "\x00" in value:
        raise shared.Unverifiable("unsupported_xml")
    parser = ET.XMLPullParser(events=("start", "end"))
    root, count, depth = None, 0, 0
    for offset in range(0, len(value), 4096):
        parser.feed(value[offset:offset + 4096])
        for event, element in parser.read_events():
            if event == "start":
                count += 1
                depth += 1
                if root is None:
                    root = element
                if count > MAX_NODES or depth > MAX_DEPTH:
                    raise shared.Unverifiable("xml_complexity")
            else:
                depth -= 1
    parser.close()
    if root is None or depth != 0:
        raise shared.Unverifiable("invalid_xml")
    return root


def read_package(data):
    with zipfile.ZipFile(BytesIO(data)) as archive:
        entries = archive.infolist()
        names = [entry.filename for entry in entries]
        if not entries or len(entries) > MAX_ENTRIES or len(set(names)) != len(names) or \
                sum(entry.file_size for entry in entries) > MAX_ARTIFACT_BYTES:
            raise shared.Unverifiable("package_bounds")
        for entry in entries:
            parts = entry.filename.split("/")
            if entry.flag_bits & 1 or entry.filename.startswith("/") or "\\" in entry.filename or \
                    any(part in {".", ".."} for part in parts) or entry.file_size < 0:
                raise shared.Unverifiable("unsupported_package")
        if names[0] != "mimetype" or entries[0].compress_type != zipfile.ZIP_STORED or \
                archive.read("mimetype") != MIME:
            raise shared.Unverifiable("not_odt")
        content = parse_xml(archive.read("content.xml"))
        styles = parse_xml(archive.read("styles.xml"))
        manifest = parse_xml(archive.read("META-INF/manifest.xml"))
        roots = [entry for entry in manifest if entry.get(tag("manifest:full-path")) == "/"]
        if manifest.tag != tag("manifest:manifest") or len(roots) != 1 or \
                roots[0].get(tag("manifest:media-type")) != MIME.decode():
            raise shared.Unverifiable("not_odt")
        for name in ("content.xml", "styles.xml"):
            found = [entry for entry in manifest if entry.get(tag("manifest:full-path")) == name]
            if len(found) != 1 or found[0].get(tag("manifest:media-type")) != "text/xml" or len(found[0]):
                raise shared.Unverifiable("unsupported_package")
    if content.tag != tag("office:document-content") or styles.tag != tag("office:document-styles"):
        raise shared.Unverifiable("unsupported_document")
    return content, styles


class Document:
    """A bounded structural subset, not an ODF renderer or a GUI state oracle."""

    def __init__(self, content, styles):
        self.styles, self.lists = {}, {}
        self.default_weight = None
        for root in (styles, content):
            for container_name in ("office:styles", "office:automatic-styles"):
                container = root.find(tag(container_name))
                if container is None:
                    continue
                for node in container:
                    if node.tag == tag("text:list-style"):
                        name = node.get(tag("style:name"))
                        if not name or name in self.lists:
                            raise shared.Unverifiable("ambiguous_style")
                        self.lists[name] = node
                    elif node.tag == tag("style:style"):
                        key = (node.get(tag("style:family")), node.get(tag("style:name")))
                        if not key[1] or key in self.styles:
                            raise shared.Unverifiable("ambiguous_style")
                        self.styles[key] = node
                    elif node.tag == tag("style:default-style") and \
                            node.get(tag("style:family")) == "paragraph":
                        if self.default_weight is not None:
                            raise shared.Unverifiable("ambiguous_style")
                        self.default_weight = self.weight(node, "normal")
                    elif node.tag == tag("style:default-style") and \
                            node.get(tag("style:family")) == "text" and self.weight(node, None) is not None:
                        raise shared.Unverifiable("unsupported_style")
        self.body = content.find("office:body/office:text", NS)
        if self.body is None:
            raise shared.Unverifiable("unsupported_document")

    @staticmethod
    def weight(node, inherited):
        properties = node.find(tag("style:text-properties"))
        return inherited if properties is None else properties.get(tag("fo:font-weight"), inherited)

    def styled_weight(self, family, name, inherited, visited=()):
        if name is None:
            return inherited
        key = (family, name)
        if key in visited or len(visited) >= 16 or key not in self.styles:
            raise shared.Unverifiable("unsupported_style")
        node = self.styles[key]
        if node.find(tag("style:map")) is not None:
            raise shared.Unverifiable("unsupported_style")
        parent = self.styled_weight(family, node.get(tag("style:parent-style-name")), inherited, visited + (key,))
        return self.weight(node, parent)

    def runs(self, node, weight):
        if any(key in node.attrib for key in (tag("text:cond-style-name"), tag("text:class-names"))):
            raise shared.Unverifiable("unsupported_style")
        weight = self.styled_weight("text", node.get(tag("text:style-name")), weight) \
            if node.tag == tag("text:span") else weight
        result = [(node.text, weight)] if node.text else []
        for child in node:
            if child.tag == tag("text:span"):
                result.extend(self.runs(child, weight))
            elif child.tag == tag("text:s"):
                if len(child) or child.text:
                    raise shared.Unverifiable("unsupported_inline")
                count = int(child.get(tag("text:c"), "1"))
                if not 1 <= count <= 4096:
                    raise shared.Unverifiable("text_bounds")
                result.append((" " * count, weight))
            elif child.tag in {tag("text:tab"), tag("text:line-break")}:
                result.append(("\t" if child.tag == tag("text:tab") else "\n", weight))
            elif child.tag not in {tag("text:bookmark-start"), tag("text:bookmark-end"), tag("text:soft-page-break")}:
                raise shared.Unverifiable("unsupported_inline")
            if child.tail:
                result.append((child.tail, weight))
        return result

    def paragraph(self, node):
        inherited = self.styled_weight("paragraph", node.get(tag("text:style-name")), self.default_weight or "normal")
        runs = self.runs(node, inherited)
        text = "".join(value for value, _ in runs)
        weights = {weight for value, weight in runs if value}
        if not weights.issubset({"normal", "bold"} | {str(value) for value in range(100, 1000, 100)}):
            raise shared.Unverifiable("unsupported_weight")
        bold = bool(text) and weights.issubset({"bold", "700", "800", "900"})
        return text, bold

    def ordered_list(self, node):
        style = self.lists.get(node.get(tag("text:style-name")))
        levels = [] if style is None else [child for child in style if child.get(tag("text:level")) == "1"]
        if len(levels) != 1 or levels[0].tag != tag("text:list-level-style-number") or \
                levels[0].get(tag("style:num-format")) not in {"1", "a", "A", "i", "I"}:
            raise shared.Unverifiable("unsupported_list")
        items = []
        for item in structural_children(node):
            if item.tag != tag("text:list-item") or len(structural_children(item)) != 1 or item[0].tag != tag("text:p"):
                raise shared.Unverifiable("unsupported_list")
            items.append(self.paragraph(item[0])[0])
        return items

    def table(self, node):
        rows, columns = [], 0
        for child in structural_children(node):
            if child.tag == tag("table:table-column"):
                count = int(child.get(tag("table:number-columns-repeated"), "1"))
                if not 1 <= count <= 3:
                    raise shared.Unverifiable("unsupported_table")
                columns += count
                continue
            if child.tag != tag("table:table-row") or child.get(tag("table:number-rows-repeated"), "1") != "1":
                raise shared.Unverifiable("unsupported_table")
            row = []
            for cell in structural_children(child):
                if cell.tag != tag("table:table-cell") or any(cell.get(tag(key), "1") != "1" for key in (
                    "table:number-columns-repeated", "table:number-columns-spanned", "table:number-rows-spanned",
                )) or len(structural_children(cell)) != 1 or cell[0].tag != tag("text:p"):
                    raise shared.Unverifiable("unsupported_table")
                row.append(self.paragraph(cell[0])[0])
            rows.append(row)
        if columns and columns != 3:
            raise shared.Unverifiable("unsupported_table")
        return rows

    def projection(self):
        result = []
        for node in structural_children(self.body):
            if node.tag in {tag("text:p"), tag("text:h")}:
                text, bold = self.paragraph(node)
                if not text:
                    continue
                if node.tag == tag("text:h"):
                    if not 1 <= int(node.get(tag("text:outline-level"), "0")) <= 10:
                        raise shared.Unverifiable("unsupported_heading")
                    result.append(["heading", text])
                else:
                    result.append(["paragraph", text, bold])
            elif node.tag == tag("text:list"):
                result.append(["ordered_list", self.ordered_list(node)])
            elif node.tag == tag("table:table"):
                result.append(["table", self.table(node)])
            elif node.tag != tag("text:sequence-decls"):
                raise shared.Unverifiable("unsupported_body")
        return result


def prepare(task_id, path):
    Path(path).mkdir(mode=0o700, parents=False, exist_ok=False)
    directory, info = shared.open_directory(path)
    try:
        record = {"task_id": task_id, "fixture_revision": FIXTURE_REVISION, "run_id": uuid.uuid4().hex,
                  "root_device": info.st_dev, "root_inode": info.st_ino}
        shared.write_new(directory, MARKER, json.dumps(record, sort_keys=True).encode("utf-8"))
        seed = TASKS[task_id].seed
        if seed is not None:
            shared.write_new(directory, TASKS[task_id].candidate, seed_odt(seed))
        for name, body in TASKS[task_id].unchanged_documents:
            shared.write_new(directory, name, seed_odt(body))
        result = {**record, "candidate": TASKS[task_id].candidate, "seeded": seed is not None,
                  "instruction": TASKS[task_id].instruction, "native_task_qualified": False}
        if TASKS[task_id].intermediate_states:
            # Requirements for a future independent native controller. These
            # are not receipts, and the artifact checker cannot attest them.
            result["required_intermediate_states"] = [
                {"phase": phase, "text": text} for phase, text in TASKS[task_id].intermediate_states
            ]
        return result
    finally:
        os.close(directory)


def directory_names(directory):
    # Iterate the already-open original directory; never traverse extra files
    # or allocate an unbounded list before enforcing the census limit.
    names = set()
    with os.scandir(directory) as entries:
        for index, entry in enumerate(entries):
            if index >= MAX_DIRECTORY_ENTRIES:
                raise shared.Unverifiable("directory_bounds")
            if entry.name in names:
                raise shared.Unverifiable("directory_changed")
            names.add(entry.name)
    return names


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
            task = TASKS[task_id]
            names = directory_names(directory) if task.exact_directory else None
            data = shared.read_owned_file(directory, task.candidate, MAX_ARTIFACT_BYTES)
            content, styles = read_package(data)
            projection = Document(content, styles).projection()
            matches = (json.dumps(projection, ensure_ascii=False)
                       == json.dumps(task.expected, ensure_ascii=False))
            if task.unchanged_documents:
                guard_matches = True
                for name, body in task.unchanged_documents:
                    guard = shared.read_owned_file(directory, name, MAX_ARTIFACT_BYTES)
                    guard_matches = (shared.digest(guard) == shared.digest(seed_odt(body))) and guard_matches
                result["unchanged_document_bytes_match"] = guard_matches
                matches = matches and guard_matches
            if task.exact_directory:
                if names != directory_names(directory) or shared.file_identity(root) != \
                        shared.file_identity(os.fstat(directory)):
                    raise shared.Unverifiable("directory_changed")
                expected_names = {MARKER, task.candidate} | {name for name, _ in task.unchanged_documents}
                directory_matches = names == expected_names
                result["directory_matches"] = directory_matches
                matches = matches and directory_matches
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
    except (OSError, ValueError, TypeError, KeyError, RecursionError, RuntimeError,
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
