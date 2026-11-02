"""Controller-only CU-T17 ODS projection; fresh native rendering is separate."""

import argparse
from dataclasses import asdict
from io import BytesIO
import copy
import json
import re
import sys
import xml.etree.ElementTree as ET
import zipfile

import calc_fixtures as calc


NS = {**calc.NS, "chart": "urn:oasis:names:tc:opendocument:xmlns:chart:1.0",
      "draw": "urn:oasis:names:tc:opendocument:xmlns:drawing:1.0",
      "svg": "urn:oasis:names:tc:opendocument:xmlns:svg-compatible:1.0",
      "xlink": "http://www.w3.org/1999/xlink",
      "loext": "urn:org:documentfoundation:names:experimental:office:xmlns:loext:1.0"}
TASK_ID = "CU-T17"
MARKER = ".mintclaw-chart-fixture.json"
CHART_MIME = "application/vnd.oasis.opendocument.chart"
REQUIRED_LIVE_EVIDENCE = ("native_gui_execution", "fresh_rendered_chart", "unchanged_guard", "release_disposition")
SOURCE = calc.Task(
    ((2, 2, "2"), (3, 2, "3"), (4, 2, "5")),
    "Open the owned document.ods and use its existing Evaluation sheet. Keep A1:B4 unchanged: "
    "the headers are Category and Value, followed by (alpha,2), (beta,3), (gamma,5). "
    "Create one bar chart from this range, using A2:A4 as categories, "
    "B2:B4 as its one value series and B1 as the series label. Save the same file. "
    "Do not replace the data with constants or add other cells or drawing objects.",
    ((2, 2, "2", False), (3, 2, "3", False), (4, 2, "5", False)),
    text_seed=((1, 1, "Category"), (1, 2, "Value"), (2, 1, "alpha"), (3, 1, "beta"), (4, 1, "gamma")),
    expected_text=((1, 1, "Category"), (1, 2, "Value"), (2, 1, "alpha"), (3, 1, "beta"), (4, 1, "gamma")),
)
CACHE = calc.Task((), "", SOURCE.expected, expected_text=SOURCE.expected_text[1:])
FIXTURE_REVISION = calc.shared.digest(json.dumps({
    "subset": "chart-v1", "task": TASK_ID, "source": asdict(SOURCE), "cache": asdict(CACHE),
    "namespaces": NS, "maximum_package_bytes": calc.odf.MAX_ARTIFACT_BYTES,
    "maximum_xml_bytes": calc.odf.MAX_XML_BYTES, "maximum_entries": calc.odf.MAX_ENTRIES,
    "maximum_nodes": calc.odf.MAX_NODES, "maximum_depth": calc.odf.MAX_DEPTH,
    "maximum_reference_bytes": 512, "maximum_range_characters": 128,
    "required_live_evidence": REQUIRED_LIVE_EVIDENCE,
    "rules": "one embedded bar chart; exact source data; actual series/category/label references; "
             "consistent local cache; internal package only; positive frame dimensions; "
             "no native or rendering proof from files or caller flags",
}, sort_keys=True, ensure_ascii=False, separators=(",", ":")).encode("utf-8"))


def tag(name):
    prefix, local = name.split(":")
    return "{" + NS[prefix] + "}" + local


def unavailable(reason):
    raise calc.shared.Unverifiable(reason)


def package_reference(value):
    if not value or len(value.encode("utf-8")) > 512:
        unavailable("unsupported_chart_reference")
    name = value.removeprefix("./")
    if name.startswith("/") or any(c in name for c in "\\:%?#\x00") or any(
            part in {"", ".", ".."} for part in name.split("/")):
        unavailable("unsupported_chart_reference")
    return name


def range_matches(value, column, first, last):
    if value is None or len(value) > 128:
        return False
    sheet = r"\$?(?:Evaluation|'Evaluation')"
    left = sheet + r"\.\$?" + column + r"\$?" + str(first)
    right = r"(?:(?:" + sheet + r")?\.)\$?" + column + r"\$?" + str(last)
    return bool(re.fullmatch(left + ":" + right, value) or
                first == last and re.fullmatch(left, value))


def validate_classes(data):
    # parse_xml already bounded nodes/depth and refused DTD/entities. Preserve
    # lexical QName scope too; a chart-looking prefix is not its namespace.
    scopes, pending = [], []
    for event, node in ET.iterparse(BytesIO(data), events=("start-ns", "start", "end")):
        if event == "start-ns":
            pending.append(node)
        elif event == "start":
            scope = dict(scopes[-1]) if scopes else {}
            scope.update(pending)
            pending.clear()
            if node.tag in {tag("chart:chart"), tag("chart:series")}:
                value = node.get(tag("chart:class"))
                if value is not None:
                    prefix, separator, local = value.partition(":")
                    if separator and not prefix:
                        unavailable("unsupported_chart_class")
                    if not separator:
                        prefix, local = "", value
                    if scope.get(prefix) != NS["chart"] or local != "bar":
                        unavailable("unsupported_chart_class")
            scopes.append(scope)
        else:
            scopes.pop()


def positive_length(value):
    if value is None or len(value) > 64:
        return False
    found = re.fullmatch(r"([0-9]+(?:\.[0-9]+)?)(?:cm|mm|in|pt|pc|px)", value)
    return bool(found and calc.numeric(found[1]) > 0)


def reject_base_overrides(*roots):
    for root in roots:
        if any("{http://www.w3.org/XML/1998/namespace}base" in node.attrib for node in root.iter()):
            unavailable("unsupported_chart_base")


def cache_matches(table):
    table = copy.deepcopy(table)
    table.set(tag("table:name"), calc.SHEET)
    wrappers = {tag("table:" + name): tag("table:" + child) for name, child in [
        ("table-header-columns", "table-column"), ("table-columns", "table-column"),
        ("table-header-rows", "table-row"), ("table-rows", "table-row"),
    ]}
    flattened = []
    for child in calc.odf.structural_children(table):
        if child.tag in wrappers:
            members = calc.odf.structural_children(child)
            if any(node.tag != wrappers[child.tag] for node in members):
                unavailable("unsupported_chart_cache")
            flattened.extend(members)
        else:
            flattened.append(child)
    table[:] = flattened
    for cell in table.iter(tag("table:table-cell")):
        for group in list(cell.findall(tag("draw:g"))):
            members = calc.odf.structural_children(group)
            if len(members) != 1 or members[0].tag != tag("svg:desc") or len(members[0]):
                unavailable("unsupported_chart_cache")
            cell.remove(group)  # Non-rendering source descriptions are not data or authority.
    content = ET.Element(tag("office:document-content"))
    body = ET.SubElement(content, tag("office:body"))
    spreadsheet = ET.SubElement(body, tag("office:spreadsheet"))
    spreadsheet.append(table)
    styles = ET.Element(tag("office:document-styles"))
    values = calc.Sheet(content, styles).projection()
    header = values.pop((1, 1), None)
    if header is not None and (header[0] not in {"", "Category"} or header[1] is not None):
        return False
    expected = {(r, c): (calc.numeric(v), None) for r, c, v, _ in CACHE.expected}
    expected.update({(r, c): (v, None) for r, c, v in CACHE.expected_text})
    return {key: (value, formula) for key, (value, formula, _) in values.items()} == expected


def chart_matches(data):
    content, styles = calc.odf.read_package(data, calc.MIME)
    reject_base_overrides(content, styles)
    sheet = calc.Sheet(copy.deepcopy(content), styles)
    shapes = sheet.table.findall(tag("table:shapes"))
    if not shapes:
        return False
    if len(shapes) != 1:
        unavailable("unsupported_chart_shapes")
    frames = calc.odf.structural_children(shapes[0])
    if len(frames) != 1 or frames[0].tag != tag("draw:frame"):
        unavailable("unsupported_chart_shapes")
    frame = frames[0]
    sheet.table.remove(shapes[0])
    if not sheet.matches(SOURCE):
        return False
    if not all(positive_length(frame.get(tag("svg:" + name))) for name in ("width", "height")):
        return False
    children = calc.odf.structural_children(frame)
    if any(child.tag not in {tag("draw:object"), tag("draw:image"), tag("svg:title"), tag("svg:desc")}
           for child in children):
        unavailable("unsupported_chart_frame")
    objects, previews = frame.findall(tag("draw:object")), frame.findall(tag("draw:image"))
    if len(objects) != 1 or len(previews) > 1:
        unavailable("unsupported_chart_frame")
    embedded = objects[0]
    for element in objects + previews:
        if any(element.get(tag("xlink:" + name), value) != value for name, value in
               [("type", "simple"), ("show", "embed"), ("actuate", "onLoad")]):
            unavailable("unsupported_chart_link_mode")
    if any(child.tag != tag("loext:p") or len(child) or child.attrib or child.text
           for child in calc.odf.structural_children(embedded)):
        unavailable("unsupported_inline_chart")
    prefix = package_reference(embedded.get(tag("xlink:href")))
    if "/" in prefix:
        unavailable("unsupported_chart_reference")
    with zipfile.ZipFile(BytesIO(data)) as archive:
        for preview in previews:
            archive.getinfo(package_reference(preview.get(tag("xlink:href"))))
        manifest = calc.odf.parse_xml(archive.read("META-INF/manifest.xml"))
        reject_base_overrides(manifest)
        for name, mime in [(prefix + "/", CHART_MIME), (prefix + "/content.xml", "text/xml"),
                           (prefix + "/styles.xml", "text/xml")]:
            entries = [node for node in manifest if node.get(tag("manifest:full-path")) == name]
            if len(entries) != 1 or entries[0].get(tag("manifest:media-type")) != mime or len(entries[0]):
                unavailable("unsupported_chart_manifest")
        raw = archive.read(prefix + "/content.xml")
        root = calc.odf.parse_xml(raw)
        chart_styles = calc.odf.parse_xml(archive.read(prefix + "/styles.xml"))
        reject_base_overrides(root, chart_styles)
        if root.tag != tag("office:document-content") or chart_styles.tag != tag("office:document-styles"):
            unavailable("unsupported_chart_document")
        validate_classes(raw)
    bodies = root.findall(tag("office:body"))
    if len(bodies) != 1 or len(calc.odf.structural_children(bodies[0])) != 1:
        unavailable("unsupported_chart_document")
    container = bodies[0][0]
    charts = calc.odf.structural_children(container)
    if container.tag != tag("office:chart") or len(charts) != 1 or charts[0].tag != tag("chart:chart"):
        unavailable("unsupported_chart_document")
    chart = charts[0]
    if chart.get(tag("chart:class")) is None or chart.get(tag("xlink:href")) != "..":
        unavailable("unsupported_chart_document")
    plots, tables = chart.findall(tag("chart:plot-area")), chart.findall(tag("table:table"))
    if any(node.tag not in {tag("chart:" + name) for name in ("title", "subtitle", "legend", "plot-area")}
           | {tag("table:table")} for node in calc.odf.structural_children(chart)):
        unavailable("unsupported_chart_structure")
    if len(plots) != 1 or len(tables) != 1:
        unavailable("unsupported_chart_structure")
    if any(node.tag not in {tag("chart:" + name) for name in
                           ("coordinate-region", "axis", "series", "wall", "floor")}
           for node in calc.odf.structural_children(plots[0])):
        unavailable("unsupported_chart_structure")
    series = plots[0].findall(tag("chart:series"))
    categories = [category for axis in plots[0].findall(tag("chart:axis"))
                  if axis.get(tag("chart:dimension")) == "x" for category in axis.findall(tag("chart:categories"))]
    if len(series) != 1 or len(categories) != 1:
        return False
    return (range_matches(series[0].get(tag("chart:values-cell-range-address")), "B", 2, 4)
            and range_matches(series[0].get(tag("chart:label-cell-address")), "B", 1, 1)
            and range_matches(categories[0].get(tag("table:cell-range-address")), "A", 2, 4)
            and cache_matches(tables[0]))


def prepare(task_id, path):
    if task_id != TASK_ID:
        raise ValueError("unsupported_task")
    result = calc.prepare_ods_fixture(task_id, path, SOURCE, FIXTURE_REVISION, MARKER)
    return {**result, "required_live_evidence": REQUIRED_LIVE_EVIDENCE}


def check_artifact(task_id, path):
    if task_id != TASK_ID:
        return {"task_id": task_id, "fixture_revision": FIXTURE_REVISION, "artifact_matches": None,
                "native_task_qualified": False, "fresh_render_required": True, "reason": "unsupported_task"}
    result = calc.check_ods_fixture(task_id, path, chart_matches, FIXTURE_REVISION, MARKER)
    return {**result, "fresh_render_required": True}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("prepare", "check-artifact"))
    parser.add_argument("--task", choices=(TASK_ID,), required=True)
    parser.add_argument("--run-directory", required=True)
    args = parser.parse_args()
    try:
        result = prepare(args.task, args.run_directory) if args.operation == "prepare" else \
            check_artifact(args.task, args.run_directory)
    except (OSError, calc.shared.Unverifiable):
        result = {"reason": "setup_refused", "native_task_qualified": False}
    print(json.dumps(result, sort_keys=True))
    return 2 if result.get("reason") else 1 if result.get("artifact_matches") is False else 0


if __name__ == "__main__":
    sys.exit(main())
