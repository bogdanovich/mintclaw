from io import BytesIO
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock
import zipfile

import calc_fixtures as calc
import chart_fixtures as fixtures


DECLARATIONS = " ".join(f'xmlns:{name}="{uri}"' for name, uri in fixtures.NS.items())
FRAME = ('<draw:frame svg:width="16cm" svg:height="9cm"><draw:object xlink:href="./Chart" '
         'xlink:type="simple" xlink:show="embed" xlink:actuate="onLoad"/></draw:frame>')
CACHE = ('<table:table table:name="local-table"><table:table-row><table:table-cell/>'
         '<table:table-cell office:value-type="string"><text:p>Value</text:p></table:table-cell>'
         '</table:table-row>' + "".join('<table:table-row><table:table-cell office:value-type="string">'
                                      '<text:p>' + label + '</text:p></table:table-cell><table:table-cell '
                                      'office:value-type="float" office:value="' + str(value)
                                      + '"/></table:table-row>' for label, value in
                                      [("alpha", 2), ("beta", 3), ("gamma", 5)]) + '</table:table>')
CHART = ('<chart:chart chart:class="chart:bar" xlink:href=".."><chart:plot-area '
         'table:cell-range-address="Evaluation.A1:Evaluation.B4"><chart:axis chart:dimension="x">'
         '<chart:categories table:cell-range-address="Evaluation.A2:Evaluation.A4"/></chart:axis>'
         '<chart:series chart:class="chart:bar" chart:values-cell-range-address="Evaluation.B2:Evaluation.B4" '
         'chart:label-cell-address="Evaluation.B1:Evaluation.B1"/></chart:plot-area>' + CACHE + '</chart:chart>')


def example(chart=CHART, frame=FRAME, source=None, manifest_mime=fixtures.CHART_MIME, extra_parts=None):
    """Independent minimal ODF examples; never native-task completion."""
    if source is None:
        source = calc.seed_ods(((2, 2, "2"), (3, 2, "3"), (4, 2, "5")), text_cells=(
            (1, 1, "Category"), (1, 2, "Value"), (2, 1, "alpha"), (3, 1, "beta"), (4, 1, "gamma")))
    with zipfile.ZipFile(BytesIO(source)) as archive:
        parts = {name: archive.read(name) for name in archive.namelist()}
    content = parts["content.xml"].decode().replace('<office:document-content ',
                                                   '<office:document-content xmlns:draw="' + fixtures.NS["draw"]
                                                   + '" xmlns:svg="' + fixtures.NS["svg"]
                                                   + '" xmlns:xlink="' + fixtures.NS["xlink"] + '" ')
    content = content.replace('<table:table table:name="Evaluation">',
                              '<table:table table:name="Evaluation"><table:shapes>' + frame + '</table:shapes>')
    parts["content.xml"] = content.encode()
    parts["Chart/content.xml"] = (f'<office:document-content {DECLARATIONS}><office:body><office:chart>'
                                  + chart + '</office:chart></office:body></office:document-content>').encode()
    parts["Chart/styles.xml"] = (f'<office:document-styles {DECLARATIONS}>'
                                 '<office:styles/></office:document-styles>').encode()
    manifest = parts["META-INF/manifest.xml"].decode().replace('</manifest:manifest>',
        '<manifest:file-entry manifest:full-path="Chart/" manifest:media-type="' + manifest_mime + '"/>'
        '<manifest:file-entry manifest:full-path="Chart/content.xml" manifest:media-type="text/xml"/>'
        '<manifest:file-entry manifest:full-path="Chart/styles.xml" manifest:media-type="text/xml"/>'
        '</manifest:manifest>')
    parts["META-INF/manifest.xml"] = manifest.encode()
    parts.update(extra_parts or {})
    output = BytesIO()
    with zipfile.ZipFile(output, "w") as archive:
        for name, data in parts.items():
            archive.writestr(name, data, compress_type=zipfile.ZIP_STORED if name == "mimetype" else zipfile.ZIP_DEFLATED)
    return output.getvalue()


class ChartFixtureTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.counter = 0

    def prepare(self):
        self.counter += 1
        path = self.root / str(self.counter)
        value = fixtures.prepare("CU-T17", path)
        self.assertIs(value["native_task_qualified"], False)
        return path

    def check(self, data):
        path = self.prepare()
        (path / calc.CANDIDATE).write_bytes(data)
        value = fixtures.check_artifact("CU-T17", path)
        self.assertIs(value["native_task_qualified"], False)
        self.assertIs(value["fresh_render_required"], True)
        return value

    def test_frozen_recipe_exact_seed_and_live_evidence_requirements(self):
        self.assertEqual(fixtures.FIXTURE_REVISION, "9a8a679f46e1c9396ee4c36fab0e39dd34bad0c0e6a82eb180d111e4ac9e8037")
        self.assertEqual(calc.FIXTURE_REVISION, "1fb357eb69587ef9abdaab43a1252dff259c77fb3d5515b82c598ede1f3470f9")
        path = self.prepare()
        content, styles = calc.odf.read_package((path / calc.CANDIDATE).read_bytes(), calc.MIME)
        self.assertTrue(calc.Sheet(content, styles).matches(fixtures.SOURCE))
        self.assertIs(fixtures.check_artifact("CU-T17", path)["artifact_matches"], False)
        with self.assertRaises(FileExistsError):
            fixtures.prepare("CU-T17", path)
        self.assertIn("fresh_rendered_chart", fixtures.REQUIRED_LIVE_EVIDENCE)

    def test_embedded_chart_matches_without_qualifying_native_or_rendered_effects(self):
        path = self.prepare()
        (path / calc.CANDIDATE).write_bytes(example())
        (path / "native-result.json").write_text('{"native_task_qualified":true,"rendered":true}')
        result = fixtures.check_artifact("CU-T17", path)
        self.assertIs(result["artifact_matches"], True)
        self.assertIs(result["native_task_qualified"], False)
        self.assertIs(result["fresh_render_required"], True)

    def test_actual_series_categories_and_label_refs_override_range_looking_metadata(self):
        for old, new in [("Evaluation.B2:Evaluation.B4", "Evaluation.B2:Evaluation.B5"),
                         ("Evaluation.A2:Evaluation.A4", "Other.A2:Other.A4"),
                         ("Evaluation.B1:Evaluation.B1", "Evaluation.A1:Evaluation.A1")]:
            with self.subTest(reference=new):
                self.assertIs(self.check(example(chart=CHART.replace(old, new)))["artifact_matches"], False)
        # ODF gives actual series refs priority over this deprecated plot hint.
        hint = CHART.replace('table:cell-range-address="Evaluation.A1:Evaluation.B4"',
                             'table:cell-range-address="Other.A1:Other.B4"')
        self.assertIs(self.check(example(chart=hint))["artifact_matches"], True)
        absolute = CHART.replace('Evaluation.B2:Evaluation.B4', "$Evaluation.$B$2:.$B$4")
        self.assertIs(self.check(example(chart=absolute))["artifact_matches"], True)

    def test_unchanged_source_and_independent_cached_values_are_both_required(self):
        changed = calc.seed_ods(((2, 2, "9"), (3, 2, "3"), (4, 2, "5")), text_cells=(
            (1, 1, "Category"), (1, 2, "Value"), (2, 1, "alpha"), (3, 1, "beta"), (4, 1, "gamma")))
        self.assertIs(self.check(example(source=changed))["artifact_matches"], False)
        for old, new in [('office:value="2"', 'office:value="9"'),
                         ('<text:p>beta</text:p>', '<text:p>other</text:p>'),
                         ('office:value="3"', 'office:value="3" table:formula="of:=3"')]:
            self.assertIs(self.check(example(chart=CHART.replace(old, new)))["artifact_matches"], False)

    def test_chart_class_qnames_must_resolve_in_their_actual_lexical_scope(self):
        alias = CHART.replace('chart:class="chart:bar"', 'chart:class="kind:bar"')
        alias = alias.replace('<chart:chart ', '<chart:chart xmlns:kind="' + fixtures.NS["chart"] + '" ', 1)
        self.assertIs(self.check(example(chart=alias))["artifact_matches"], True)
        fake = alias.replace('xmlns:kind="' + fixtures.NS["chart"] + '"', 'xmlns:kind="urn:fake"')
        self.assertIsNone(self.check(example(chart=fake))["artifact_matches"])
        rebound = alias.replace('<chart:series ', '<chart:series xmlns:kind="urn:fake" ', 1)
        self.assertIsNone(self.check(example(chart=rebound))["artifact_matches"])
        invalid = CHART.replace('chart:class="chart:bar"', 'chart:class=":bar"').replace(
            '<chart:chart ', '<chart:chart xmlns="' + fixtures.NS["chart"] + '" ', 1)
        self.assertIsNone(self.check(example(chart=invalid))["artifact_matches"])

    def test_external_traversing_and_missing_chart_parts_are_unverifiable(self):
        for href in ['https://example.invalid/chart', '../Chart', '/Chart', './%43hart', './Chart#data']:
            frame = FRAME.replace('./Chart', href)
            self.assertIsNone(self.check(example(frame=frame))["artifact_matches"])
        self.assertIsNone(self.check(example(extra_parts={'Chart/content.xml': b'<fake/>'}))["artifact_matches"])
        self.assertIsNone(self.check(example(manifest_mime='application/vnd.oasis.opendocument.text'))["artifact_matches"])
        based = FRAME.replace('<draw:frame ', '<draw:frame xml:base="https://example.invalid/" ', 1)
        self.assertIsNone(self.check(example(frame=based))["artifact_matches"])
        mode = FRAME.replace('xlink:type="simple"', 'xlink:type="extended"')
        self.assertIsNone(self.check(example(frame=mode))["artifact_matches"])

    def test_image_only_extra_frames_zero_size_and_inline_objects_cannot_pass(self):
        image = '<draw:frame svg:width="16cm" svg:height="9cm"><draw:image xlink:href="./preview"/></draw:frame>'
        self.assertIsNone(self.check(example(frame=image, extra_parts={'preview': b'fake'}))["artifact_matches"])
        self.assertIsNone(self.check(example(frame=FRAME + FRAME))["artifact_matches"])
        self.assertIs(self.check(example(frame=FRAME.replace('16cm', '0cm')))["artifact_matches"], False)
        inline = FRAME.replace('xlink:actuate="onLoad"/>',
                               'xlink:actuate="onLoad"><office:document/></draw:object>')
        self.assertIsNone(self.check(example(frame=inline))["artifact_matches"])

    def test_cache_metadata_does_not_hide_drawing_or_extra_cell_data(self):
        group = '<draw:g><svg:desc>untrusted description</svg:desc></draw:g>'
        cache = CHART.replace('<text:p>alpha</text:p>', '<text:p>alpha</text:p>' + group)
        self.assertIs(self.check(example(chart=cache))["artifact_matches"], True)
        hidden = cache.replace('<svg:desc>untrusted description</svg:desc>', '<draw:rect/>')
        self.assertIsNone(self.check(example(chart=hidden))["artifact_matches"])
        repeated = CHART.replace('<table:table-row>', '<table:table-row table:number-rows-repeated="17">', 1)
        self.assertIsNone(self.check(example(chart=repeated))["artifact_matches"])

    def test_original_file_marker_and_observed_mutation_guards_are_reused(self):
        for kind in ('symlink', 'hardlink', 'record', 'mutation'):
            path = self.prepare()
            candidate = path / calc.CANDIDATE
            candidate.write_bytes(example())
            if kind == 'symlink':
                candidate.rename(path / 'source.ods')
                candidate.symlink_to('source.ods')
            elif kind == 'hardlink':
                os.link(candidate, path / 'linked.ods')
            elif kind == 'record':
                marker = path / fixtures.MARKER
                value = json.loads(marker.read_text())
                value['fixture_revision'] = '0' * 64
                marker.write_text(json.dumps(value))
            else:
                original = calc.shared.os.read
                changed = False

                def mutate(descriptor, size):
                    nonlocal changed
                    data = original(descriptor, size)
                    if data.startswith(b'PK') and not changed:
                        changed = True
                        with candidate.open('ab') as output:
                            output.write(b'x')
                    return data

                with mock.patch.object(calc.shared.os, 'read', side_effect=mutate):
                    self.assertIsNone(fixtures.check_artifact('CU-T17', path)['artifact_matches'])
                continue
            self.assertIsNone(fixtures.check_artifact('CU-T17', path)['artifact_matches'])
        self.assertEqual(fixtures.check_artifact('CU-T13', self.root)['reason'], 'unsupported_task')

    def test_cli_reports_only_bounded_status_and_keeps_fresh_render_required(self):
        script = Path(fixtures.__file__)
        path = self.root / 'cli'
        command = [sys.executable, str(script), 'prepare', '--task', 'CU-T17', '--run-directory', str(path)]
        subprocess.run(command, capture_output=True, check=True)
        command[2] = 'check-artifact'
        self.assertEqual(subprocess.run(command, capture_output=True).returncode, 1)
        (path / calc.CANDIDATE).write_bytes(example())
        result = subprocess.run(command, capture_output=True, check=True)
        value = json.loads(result.stdout)
        self.assertIs(value['artifact_matches'], True)
        self.assertIs(value['native_task_qualified'], False)
        self.assertIs(value['fresh_render_required'], True)
        (path / calc.CANDIDATE).write_bytes(b'private-candidate-do-not-echo')
        result = subprocess.run(command, capture_output=True)
        self.assertEqual(result.returncode, 2)
        self.assertNotIn(b'private-candidate-do-not-echo', result.stdout)


if __name__ == '__main__':
    unittest.main()
