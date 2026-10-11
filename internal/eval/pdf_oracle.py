"""Independent synthetic AcroForm oracle. One JSON line, then two bounded PDFs.

Private expectations are read only from stdin. Emit predicates/counts/digests,
never submitted values, paths, parser messages, field names or a traceback.
This is not an approval, delivery, full OCR or hostile-PDF sandbox certificate.
"""

import contextlib
import hashlib
import importlib.metadata
import io
import json
import math
import sys

VERSION = "mintclaw.eval.pdf_oracle.v1"
MAX_PDF = 64 * 1024 * 1024
MAX_PIXELS = 16 * 1024 * 1024


def object_pairs(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError()
        result[key] = value
    return result


def read_request(stream):
    line = stream.readline(65538)
    if len(line) > 65537 or not line.endswith(b"\n"):
        raise ValueError()
    request = json.loads(line, object_pairs_hook=object_pairs)
    if set(request) != {
        "version", "source_size", "artifact_size", "source_sha256", "pages", "field_count", "fields"
    } or type(request["version"]) is not int or request["version"] != 1:
        raise ValueError()
    for key, maximum in (("source_size", MAX_PDF), ("artifact_size", MAX_PDF), ("pages", 32), ("field_count", 256)):
        if type(request[key]) is not int or not 0 < request[key] <= maximum:
            raise ValueError()
    if not isinstance(request["fields"], list) or len(request["fields"]) != request["field_count"]:
        raise ValueError()
    names = set()
    for field in request["fields"]:
        if not isinstance(field, dict) or not set(field) <= {"name", "kind", "value", "preserve"}:
            raise ValueError()
        name = field.get("name")
        if not isinstance(name, str) or not 0 < len(name.encode()) <= 128 or name in names:
            raise ValueError()
        if field.get("kind") not in ("text", "checkbox") or type(field.get("preserve", False)) is not bool:
            raise ValueError()
        value = field.get("value", "")
        if not isinstance(value, str) or len(value.encode()) > 4096 or "\x00" in value:
            raise ValueError()
        if field.get("preserve") and value:
            raise ValueError()
        if field["kind"] == "checkbox" and not field.get("preserve") and value not in ("true", "false"):
            raise ValueError()
        names.add(name)
    source = stream.read(request["source_size"])
    artifact = stream.read(request["artifact_size"])
    if len(source) != request["source_size"] or len(artifact) != request["artifact_size"] or stream.read(1):
        raise ValueError()
    if hashlib.sha256(source).hexdigest() != request["source_sha256"]:
        raise ValueError()
    return request, source, artifact


def field_value(field, kind):
    value = field.get("/V", "")
    if kind == "checkbox":
        if field.get("/FT") != "/Btn" or int(field.get("/Ff", 0)) & ((1 << 15) | (1 << 16)):
            raise ValueError()
        return str(value) not in ("", "/Off")
    if field.get("/FT") != "/Tx":
        raise ValueError()
    return str(value)


def widgets(document, pymupdf):
    result = {}
    for number, page in enumerate(document):
        for widget in page.widgets() or ():
            if widget.field_name in result or widget.rect.is_empty or widget.rect.is_infinite:
                raise ValueError()
            rectangle = tuple(widget.rect)
            if not all(math.isfinite(value) for value in rectangle) or not page.rect.contains(widget.rect):
                raise ValueError()
            result[widget.field_name] = (number, rectangle, widget.field_type)
            if len(result) > 256:
                raise ValueError()
    return result


def render(page, rectangle, pymupdf):
    return page.get_pixmap(clip=rectangle, colorspace=pymupdf.csRGB, alpha=False, annots=True)


def masked_page(page, rectangles, pymupdf):
    pixmap = render(page, page.rect, pymupdf)
    if pixmap.width * pixmap.height > MAX_PIXELS:
        raise ValueError()
    data = bytearray(pixmap.samples)
    for rect in rectangles:
        area = pymupdf.Rect(rect) + (-2, -2, 2, 2)
        left, top = max(0, math.floor(area.x0)), max(0, math.floor(area.y0))
        right, bottom = min(pixmap.width, math.ceil(area.x1)), min(pixmap.height, math.ceil(area.y1))
        for row in range(top, bottom):
            start, end = (row * pixmap.width + left) * 3, (row * pixmap.width + right) * 3
            data[start:end] = b"\xff" * (end - start)
    return data


def inspect(request, source, artifact):
    from pypdf import PdfReader
    import pymupdf

    if importlib.metadata.version("pypdf") != "6.20.0" or importlib.metadata.version("pymupdf") != "1.28.2":
        raise ValueError()
    source_reader = PdfReader(io.BytesIO(source), strict=True)
    actual_reader = PdfReader(io.BytesIO(artifact), strict=True)
    for reader in (source_reader, actual_reader):
        acroform = reader.trailer["/Root"].get("/AcroForm")
        if acroform is not None and "/XFA" in acroform.get_object():
            raise ValueError()
    source_fields, actual_fields = source_reader.get_fields(), actual_reader.get_fields()
    expected_names = {field["name"] for field in request["fields"]}
    structure = (
        not source_reader.is_encrypted and not actual_reader.is_encrypted
        and len(source_reader.pages) == len(actual_reader.pages) == request["pages"]
        and source_fields is not None and actual_fields is not None
        and set(source_fields) == set(actual_fields) == expected_names
    )
    with pymupdf.open(stream=source, filetype="pdf") as original, pymupdf.open(
        stream=artifact, filetype="pdf"
    ) as actual, pymupdf.open(stream=artifact, filetype="pdf") as blank:
        if len(original) != request["pages"] or len(actual) != request["pages"]:
            return result(request, source, artifact, False, False, False, False, 0, 0)
        pixels = 0
        for document in (original, actual):
            for page in document:
                if page.rotation != 0 or page.rect.width > 2048 or page.rect.height > 2048:
                    raise ValueError()
                pixels += math.ceil(page.rect.width) * math.ceil(page.rect.height)
                if pixels > MAX_PIXELS:
                    raise ValueError()
        original_widgets, actual_widgets = widgets(original, pymupdf), widgets(actual, pymupdf)
        structure = structure and original_widgets == actual_widgets and set(actual_widgets) == expected_names
        if not structure:
            return result(request, source, artifact, False, False, False, False, 0, 0)
        matched, appearance = True, True
        expected = {}
        for field in request["fields"]:
            name, kind = field["name"], field["kind"]
            value = field_value(source_fields[name], kind) if field.get("preserve") else field.get("value", "")
            if kind == "checkbox" and not field.get("preserve"):
                value = value == "true"
            expected[name] = value
            observed = field_value(actual_fields[name], kind)
            matched = observed == value and matched
            structure = structure and int(source_fields[name].get("/Ff", 0)) == int(
                actual_fields[name].get("/Ff", 0)
            ) and not int(actual_fields[name].get("/Ff", 0)) & 1
        # Compare the observed widget raster with its independently cleared
        # appearance, excluding the border. This detects invisible/stale values;
        # it is not OCR and does not claim exact glyph or font equivalence.
        for number in range(len(blank)):
            page = blank[number]
            actual_page = actual[number]
            references = [widget.xref for widget in page.widgets() or ()]
            for reference in references:
                widget = page.load_widget(reference)
                name = widget.field_name
                rectangle = widget.rect + (2, 2, -2, -2)
                if rectangle.is_empty:
                    raise ValueError()
                if widget.field_type == pymupdf.PDF_WIDGET_TYPE_TEXT:
                    text = actual_page.get_text(
                        "text", clip=widget.rect
                    ).rstrip("\n")
                    appearance = text == expected[name] and appearance
                observed = render(actual_page, rectangle, pymupdf).samples
                # A nonempty whitespace value forces appearance regeneration
                # even when a library treats an empty setter as "unchanged".
                widget.field_value = "Off" if widget.field_type == pymupdf.PDF_WIDGET_TYPE_CHECKBOX else " "
                widget.update()
                page = blank.reload_page(page)
                if widget.field_type == pymupdf.PDF_WIDGET_TYPE_TEXT and page.get_text(
                    "text", clip=rectangle
                ).strip():
                    raise ValueError()
                cleared = render(page, rectangle, pymupdf).samples
                appearance = appearance and ((observed != cleared) == bool(expected[name]))
        background = True
        for number, (original_page, actual_page) in enumerate(zip(original, actual)):
            if original_page.rect != actual_page.rect or original_page.rotation != 0 or actual_page.rotation != 0:
                raise ValueError()
            rectangles = [rect for page, rect, _ in actual_widgets.values() if page == number]
            original_pixels = masked_page(original_page, rectangles, pymupdf)
            actual_pixels = masked_page(actual_page, rectangles, pymupdf)
            background = original_pixels == actual_pixels and background
        return result(request, source, artifact, matched, structure, appearance, background,
                      request["field_count"], request["pages"])


def result(request, source, artifact, fields, structure, appearance, background, checked, rendered):
    import pymupdf

    return {
        "version": 1, "schema": VERSION,
        "code": "matched" if fields and structure and appearance and background else "mismatch",
        "pypdf": "6.20.0", "pymupdf": "1.28.2", "mupdf": pymupdf.VersionFitz,
        "source_sha256": hashlib.sha256(source).hexdigest(),
        "artifact_sha256": hashlib.sha256(artifact).hexdigest(),
        "fields_matched": fields, "structure_matched": structure,
        "appearance_matched": appearance, "background_matched": background,
        "checked_fields": checked, "rendered_pages": rendered,
    }


def main():
    # Both libraries may emit diagnostics containing submitted PDF data. Never
    # forward them to the parent; the result vocabulary is deliberately fixed.
    try:
        with contextlib.redirect_stdout(Discard()), contextlib.redirect_stderr(Discard()):
            request, source, artifact = read_request(sys.stdin.buffer)
            outcome = inspect(request, source, artifact)
    except Exception:
        return 2
    print(json.dumps(outcome, separators=(",", ":")))
    return 0


class Discard(io.TextIOBase):
    def write(self, value):
        return len(value)


if __name__ == "__main__":
    raise SystemExit(main())
