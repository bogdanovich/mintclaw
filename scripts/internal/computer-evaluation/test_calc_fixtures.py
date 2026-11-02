from io import BytesIO
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock
import warnings
import zipfile

import calc_fixtures as fixtures


SUM_BODY = '''<table:table table:name="Evaluation">
<table:table-column table:number-columns-repeated="2"/>
<table:table-row><table:table-cell table:number-columns-repeated="2"/></table:table-row>
<table:table-row><table:table-cell/><table:table-cell office:value-type="float" office:value="2"/></table:table-row>
<table:table-row><table:table-cell/><table:table-cell office:value-type="float" office:value="3"/></table:table-row>
<table:table-row><table:table-cell/><table:table-cell office:value-type="float" office:value="5"/></table:table-row>
<table:table-row><table:table-cell/><table:table-cell office:value-type="float" office:value="10"
table:formula="of:=SUM([.B2:.B4])"/></table:table-row></table:table>'''
CURRENCY_BODY = '''<table:table table:name="Evaluation"><table:table-row>
<table:table-cell table:style-name="Price" office:value-type="currency" office:currency="USD"
office:value="1000.25"><text:p>$1,000.25</text:p></table:table-cell></table:table-row></table:table>'''
CURRENCY_STYLES = '''<number:currency-style style:name="Money"><number:currency-symbol>$</number:currency-symbol>
<number:number number:decimal-places="2"/></number:currency-style>
<style:style style:name="Price" style:family="table-cell" style:data-style-name="Money"/>'''


def package(body, styles="", extra_content="", namespace_override=None):
    """Independent compressed ODS examples, not task completion through prepare."""
    namespaces = dict(fixtures.NS)
    if namespace_override:
        namespaces.update(namespace_override)
    declarations = " ".join(f'xmlns:{key}="{value}"' for key, value in namespaces.items())
    content = (f'<office:document-content {declarations} office:version="1.3">'
               '<office:automatic-styles>' + styles + '</office:automatic-styles><office:body>'
               '<office:spreadsheet>' + body + extra_content
               + '</office:spreadsheet></office:body></office:document-content>').encode()
    document_styles = (f'<office:document-styles {declarations} office:version="1.3">'
                       '<office:styles/></office:document-styles>').encode()
    manifest = (f'<manifest:manifest xmlns:manifest="{namespaces["manifest"]}">'
                '<manifest:file-entry manifest:full-path="/" '
                'manifest:media-type="application/vnd.oasis.opendocument.spreadsheet"/>'
                '<manifest:file-entry manifest:full-path="content.xml" manifest:media-type="text/xml"/>'
                '<manifest:file-entry manifest:full-path="styles.xml" manifest:media-type="text/xml"/>'
                '</manifest:manifest>').encode()
    output = BytesIO()
    with zipfile.ZipFile(output, "w") as archive:
        archive.writestr("mimetype", b"application/vnd.oasis.opendocument.spreadsheet", compress_type=zipfile.ZIP_STORED)
        archive.writestr("content.xml", content, compress_type=zipfile.ZIP_DEFLATED)
        archive.writestr("styles.xml", document_styles, compress_type=zipfile.ZIP_DEFLATED)
        archive.writestr("META-INF/manifest.xml", manifest, compress_type=zipfile.ZIP_DEFLATED)
    return output.getvalue()


class CalcFixtureTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.counter = 0

    def prepare(self, task):
        self.counter += 1
        path = self.root / str(self.counter)
        fixtures.prepare(task, path)
        return path

    def check(self, task, data):
        path = self.prepare(task)
        (path / fixtures.CANDIDATE).write_bytes(data)
        result = fixtures.check_artifact(task, path)
        self.assertIs(result["native_task_qualified"], False)
        return result

    def test_seeds_are_deterministic_and_never_qualify_a_task(self):
        self.assertEqual(fixtures.FIXTURE_REVISION, "c014b91f509c0674ac14847a40a07349e6064208db13bd1384aafb149b89ef9c")
        self.assertEqual(fixtures.NS["of"], "urn:oasis:names:tc:opendocument:xmlns:of:1.2")
        self.assertEqual(tuple(fixtures.TASKS), ("CU-T13", "CU-T14"))
        self.assertEqual(fixtures.seed_ods(fixtures.TASKS["CU-T14"].seed),
                         fixtures.seed_ods(fixtures.TASKS["CU-T14"].seed))
        for task in fixtures.TASKS:
            path = self.prepare(task)
            self.assertIs(fixtures.check_artifact(task, path)["artifact_matches"], False)
            with self.assertRaises(FileExistsError):
                fixtures.prepare(task, path)

    def test_independent_sum_document_and_absolute_formula_references(self):
        self.assertIs(self.check("CU-T13", package(SUM_BODY))["artifact_matches"], True)
        body = SUM_BODY.replace("SUM([.B2:.B4])", "sum([.$B$2:.$B$4])")
        self.assertIs(self.check("CU-T13", package(body))["artifact_matches"], True)

    def test_typed_result_wrong_formula_cache_inputs_and_extra_data_do_not_match(self):
        variants = [SUM_BODY.replace('table:formula="of:=SUM([.B2:.B4])"', ""),
                    SUM_BODY.replace("SUM([.B2:.B4])", "SUM(2;3;5)"),
                    SUM_BODY.replace("SUM([.B2:.B4])", "SUM([.B2:.B5])"),
                    SUM_BODY.replace('office:value="10"', 'office:value="11"'),
                    SUM_BODY.replace('office:value="5"', 'office:value="6"'),
                    SUM_BODY.replace('<table:table-cell/>', '<table:table-cell office:value-type="float" office:value="9"/>', 1)]
        for body in variants:
            with self.subTest(body=body):
                self.assertIs(self.check("CU-T13", package(body))["artifact_matches"], False)

    def test_formula_namespace_must_be_real_and_lexically_in_scope(self):
        self.assertIsNone(self.check("CU-T13", package(SUM_BODY, namespace_override={"of": "urn:fake"}))["artifact_matches"])
        rebound = SUM_BODY.replace('table:formula="of:', 'xmlns:of="urn:fake" table:formula="of:')
        self.assertIsNone(self.check("CU-T13", package(rebound))["artifact_matches"])
        alias = SUM_BODY.replace('table:formula="of:', 'table:formula="formula:')
        self.assertIs(self.check("CU-T13", package(alias, namespace_override={"formula": fixtures.NS["of"]}))["artifact_matches"], True)

    def test_currency_requires_effective_metadata_and_unchanged_numeric_value(self):
        self.assertIs(self.check("CU-T14", package(CURRENCY_BODY, CURRENCY_STYLES))["artifact_matches"], True)
        plain = CURRENCY_BODY.replace('table:style-name="Price"', "")
        self.assertIs(self.check("CU-T14", package(plain, CURRENCY_STYLES))["artifact_matches"], False)
        changed = CURRENCY_BODY.replace('office:value="1000.25"', 'office:value="1000.26"')
        self.assertIs(self.check("CU-T14", package(changed, CURRENCY_STYLES))["artifact_matches"], False)
        text = CURRENCY_BODY.replace('office:value-type="currency"', 'office:value-type="string"')
        self.assertIsNone(self.check("CU-T14", package(text, CURRENCY_STYLES))["artifact_matches"])

    def test_currency_style_inheritance_defaults_and_conditional_branches(self):
        inherited = CURRENCY_STYLES.replace('style:name="Price" style:family="table-cell"',
                                            'style:name="Base" style:family="table-cell"')
        inherited += '<style:style style:name="Price" style:family="table-cell" style:parent-style-name="Base"/>'
        self.assertIs(self.check("CU-T14", package(CURRENCY_BODY, inherited))["artifact_matches"], True)
        for location in ("row", "column", "default"):
            body = CURRENCY_BODY.replace('table:style-name="Price"', "")
            styles = CURRENCY_STYLES
            if location == "row":
                body = body.replace('<table:table-row>', '<table:table-row table:default-cell-style-name="Price">')
            elif location == "column":
                body = body.replace('<table:table-row>', '<table:table-column table:default-cell-style-name="Price"/><table:table-row>')
            else:
                styles += '<style:default-style style:family="table-cell" style:data-style-name="Money"/>'
            self.assertIs(self.check("CU-T14", package(body, styles))["artifact_matches"], True)
        cyclic = CURRENCY_STYLES.replace('<number:number number:decimal-places="2"/>',
                                        '<number:number/><style:map style:apply-style-name="Money"/>')
        self.assertIsNone(self.check("CU-T14", package(CURRENCY_BODY, cyclic))["artifact_matches"])
        fake = CURRENCY_STYLES.replace('<number:number number:decimal-places="2"/>',
                                      '<number:number/><style:map style:apply-style-name="Text"/>')
        fake += '<number:text-style style:name="Text"><number:text>currency</number:text></number:text-style>'
        self.assertIs(self.check("CU-T14", package(CURRENCY_BODY, fake))["artifact_matches"], False)

    def test_huge_empty_tail_is_sparse_but_meaningful_repeats_and_merges_are_bounded(self):
        tail = '<table:table-row table:number-rows-repeated="1048571"><table:table-cell/></table:table-row>'
        body = SUM_BODY.replace('</table:table>', tail + '</table:table>')
        body = body.replace('table:number-columns-repeated="2"', 'table:number-columns-repeated="16384"')
        self.assertIs(self.check("CU-T13", package(body))["artifact_matches"], True)
        for body in [SUM_BODY.replace('table:number-columns-repeated="2"', 'table:number-columns-repeated="9999999999"'),
                     SUM_BODY.replace('<table:table-row>', '<table:table-row table:number-rows-repeated="17">', 2),
                     SUM_BODY.replace('office:value="2"', 'table:number-columns-spanned="2" office:value="2"'),
                     SUM_BODY.replace('office:value="10"', 'table:number-matrix-columns-spanned="2" office:value="10"')]:
            self.assertIsNone(self.check("CU-T13", package(body))["artifact_matches"])

    def test_non_xml_numeric_lexemes_nonfinite_values_and_extra_sheets_are_unverifiable(self):
        for value in ["NaN", "INF", "1_000", "١٠", " 2 ", "1" * 129]:
            body = SUM_BODY.replace('office:value="2"', 'office:value="' + value + '"')
            self.assertIsNone(self.check("CU-T13", package(body))["artifact_matches"])
        extra = '<table:table table:name="Other"><table:table-row><table:table-cell/></table:table-row></table:table>'
        self.assertIsNone(self.check("CU-T13", package(SUM_BODY, extra_content=extra))["artifact_matches"])

    def test_encoded_whitespace_and_untyped_values_are_not_silent_empty_cells(self):
        for content in ["<text:s/>", '<text:s text:c="1000000000"/>', "<text:tab/>",
                        "<text:line-break/>", "<text:span/>"]:
            body = SUM_BODY.replace('<table:table-cell/>',
                                    '<table:table-cell><text:p>' + content + '</text:p></table:table-cell>', 1)
            self.assertIsNone(self.check("CU-T13", package(body))["artifact_matches"])
        for attribute in ['office:value="9"', 'office:string-value="extra"',
                          'office:boolean-value="false"', 'office:date-value="2026-11-01"',
                          'office:time-value="PT1H"']:
            body = SUM_BODY.replace('<table:table-cell/>', '<table:table-cell ' + attribute + '/>', 1)
            self.assertIsNone(self.check("CU-T13", package(body))["artifact_matches"])

    def test_shared_package_limits_and_expected_mime_are_enforced(self):
        ods = package(SUM_BODY)
        with self.assertRaises(fixtures.shared.Unverifiable):
            fixtures.odf.read_package(ods)
        duplicate = BytesIO(ods)
        with warnings.catch_warnings():
            warnings.simplefilter("ignore", UserWarning)
            with zipfile.ZipFile(duplicate, "a") as archive:
                archive.writestr("content.xml", b"<fake/>")
        self.assertIsNone(self.check("CU-T13", duplicate.getvalue())["artifact_matches"])
        expanded = BytesIO()
        with zipfile.ZipFile(BytesIO(ods)) as before, zipfile.ZipFile(expanded, "w") as after:
            for entry in before.infolist():
                data = before.read(entry.filename)
                if entry.filename == "content.xml":
                    data = b'<!DOCTYPE x [<!ENTITY bad "x">]>' + data
                after.writestr(entry, data)
        self.assertIsNone(self.check("CU-T13", expanded.getvalue())["artifact_matches"])

    def test_original_file_marker_and_stable_read_guards_remain_required(self):
        for kind in ("symlink", "hardlink", "record", "mutation"):
            path = self.prepare("CU-T13")
            candidate = path / fixtures.CANDIDATE
            candidate.write_bytes(package(SUM_BODY))
            if kind == "symlink":
                candidate.rename(path / "source.ods")
                candidate.symlink_to("source.ods")
            elif kind == "hardlink":
                os.link(candidate, path / "linked.ods")
            elif kind == "record":
                marker = path / fixtures.MARKER
                data = json.loads(marker.read_text())
                data["fixture_revision"] = "0" * 64
                marker.write_text(json.dumps(data))
            else:
                original = fixtures.shared.os.read
                changed = False

                def mutate(descriptor, size):
                    nonlocal changed
                    data = original(descriptor, size)
                    if data.startswith(b"PK") and not changed:
                        changed = True
                        with candidate.open("ab") as output:
                            output.write(b"x")
                    return data

                with mock.patch.object(fixtures.shared.os, "read", side_effect=mutate):
                    self.assertIsNone(fixtures.check_artifact("CU-T13", path)["artifact_matches"])
                continue
            self.assertIsNone(fixtures.check_artifact("CU-T13", path)["artifact_matches"])

    def test_cli_reports_only_protected_status_and_exit_codes(self):
        script = Path(fixtures.__file__)
        path = self.root / "cli"
        command = [sys.executable, str(script), "prepare", "--task", "CU-T13", "--run-directory", str(path)]
        prepared = subprocess.run(command, capture_output=True, text=True, check=True)
        self.assertIs(json.loads(prepared.stdout)["native_task_qualified"], False)
        command[2] = "check-artifact"
        failed = subprocess.run(command, capture_output=True, text=True)
        self.assertEqual(failed.returncode, 1)
        (path / fixtures.CANDIDATE).write_bytes(package(SUM_BODY))
        passed = subprocess.run(command, capture_output=True, text=True, check=True)
        self.assertIs(json.loads(passed.stdout)["artifact_matches"], True)
        self.assertIs(json.loads(passed.stdout)["native_task_qualified"], False)
        (path / fixtures.CANDIDATE).write_bytes(b"do-not-report-me")
        broken = subprocess.run(command, capture_output=True, text=True)
        self.assertEqual(broken.returncode, 2)
        self.assertNotIn("do-not-report-me", broken.stdout)


if __name__ == "__main__":
    unittest.main()
