import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

import text_fixtures as fixtures


class TextFixtureTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="mintclaw-text-oracle-")
        self.addCleanup(self.temporary.cleanup)
        self.parent = Path(self.temporary.name)

    def prepare(self, task_id):
        path = self.parent / task_id
        result = fixtures.prepare(task_id, path)
        self.assertFalse(result["native_task_qualified"])
        self.assertEqual(result["fixture_revision"], fixtures.FIXTURE_REVISION)
        return path

    def test_subset_revision_is_frozen_before_native_model_runs(self):
        self.assertEqual(
            fixtures.FIXTURE_REVISION,
            "8f79387473e9664aec89256d4f2b2fcd1e076bc8737562d8a961d8dc02810ceb",
        )

    def test_frozen_task_intents_and_literal_saved_bytes(self):
        # Independent literals: do not generate expected results through the
        # implementation's fixture/expected-value helper.
        self.assertEqual(tuple(fixtures.TASKS), ("CU-T01", "CU-T02", "CU-T03", "CU-T04", "CU-T05"))
        outputs = {
            "CU-T01": "MintClaw — Привет 世界 café 👩‍💻\n",
            "CU-T02": "ALPHA\nОбновлено — naïve 🌿MintClaw — Привет 世界 café 👩‍💻\nOMEGA\n",
            "CU-T03": "ALPHA\nОбновлено — naïve 🌿\nOMEGA\n",
            "CU-T04": "ALPHA\nMintClaw — Привет 世界 café 👩‍💻\nOMEGA\n"
                       "FIRST\tMintClaw — Привет 世界 café 👩‍💻\nSECOND e\u0301\nTHIRD Обновлено — naïve 🌿\n",
            "CU-T05": "needle A\nkeep\nedited B\n",
        }
        for task_id, expected in outputs.items():
            with self.subTest(task=task_id):
                path = self.prepare(task_id)
                initial = fixtures.check_artifact(task_id, path)
                self.assertIsNot(initial["artifact_matches"], True)
                candidate = path / fixtures.CANDIDATE
                candidate.write_bytes(expected.encode("utf-8"))
                before = candidate.stat()
                result = fixtures.check_artifact(task_id, path)
                self.assertTrue(result["artifact_matches"])
                self.assertFalse(result["native_task_qualified"])
                self.assertEqual(fixtures.file_identity(before), fixtures.file_identity(candidate.stat()))
                self.assertNotIn(expected, json.dumps(result, ensure_ascii=False))
                self.assertEqual(result["artifact_bytes"], len(expected.encode("utf-8")))
        self.assertFalse(fixtures.TASKS["CU-T01"].seed_file)
        self.assertEqual(fixtures.TASKS["CU-T02"].caret_scalars, 6)

    def test_exact_initial_data_and_private_new_directory(self):
        initial = "ALPHA\nMintClaw — Привет 世界 café 👩‍💻\nOMEGA\n".encode("utf-8")
        for task_id in ("CU-T02", "CU-T03", "CU-T04"):
            path = self.prepare(task_id)
            self.assertEqual((path / fixtures.CANDIDATE).read_bytes(), initial)
            self.assertEqual(path.stat().st_mode & 0o777, 0o700)
        path = self.prepare("CU-T01")
        self.assertFalse((path / fixtures.CANDIDATE).exists())
        (path / "user-file").write_bytes(b"preserve")
        with self.assertRaises(FileExistsError):
            fixtures.prepare("CU-T01", path)
        self.assertEqual((path / "user-file").read_bytes(), b"preserve")

    def test_truncation_normalization_newline_and_wrong_occurrence_do_not_match(self):
        path = self.prepare("CU-T04")
        expected = fixtures.TASKS["CU-T04"].expected
        for changed in (
            expected[:-1], expected.replace("e\u0301", "é"), expected.replace("\n", "\r\n"),
            expected.replace("\t", "\\t"), expected + "extra", expected.replace("OMEGA", "changed"),
        ):
            (path / fixtures.CANDIDATE).write_bytes(changed.encode("utf-8"))
            self.assertFalse(fixtures.check_artifact("CU-T04", path)["artifact_matches"])
        other = self.prepare("CU-T05")
        (other / fixtures.CANDIDATE).write_bytes(b"edited A\nkeep\nneedle B\n")
        self.assertFalse(fixtures.check_artifact("CU-T05", other)["artifact_matches"])

    def test_unavailable_unsafe_and_oversized_candidates_are_unverifiable(self):
        for kind in ("missing", "symlink", "hardlink", "directory", "fifo", "oversized", "invalid-utf8"):
            with self.subTest(kind=kind):
                path = self.parent / kind
                fixtures.prepare("CU-T01", path)
                candidate = path / fixtures.CANDIDATE
                private = path / "private-fixture"
                private.write_bytes(b"private error content canary")
                if kind == "symlink":
                    candidate.symlink_to(private)
                elif kind == "hardlink":
                    os.link(private, candidate)
                elif kind == "directory":
                    candidate.mkdir()
                elif kind == "fifo":
                    os.mkfifo(candidate)
                elif kind == "oversized":
                    candidate.write_bytes(b"x" * (fixtures.MAX_ARTIFACT_BYTES + 1))
                elif kind == "invalid-utf8":
                    candidate.write_bytes(b"\xff")
                result = fixtures.check_artifact("CU-T01", path)
                self.assertIsNone(result["artifact_matches"])
                self.assertFalse(result["native_task_qualified"])
                self.assertIn("reason", result)
                self.assertNotIn("private error content canary", json.dumps(result))

    def test_wrong_task_revision_root_and_duplicate_marker_are_unverifiable(self):
        path = self.prepare("CU-T03")
        marker = path / fixtures.MARKER
        original = marker.read_bytes()
        self.assertIsNone(fixtures.check_artifact("CU-T02", path)["artifact_matches"])
        for key, value in (("task_id", "CU-T02"), ("fixture_revision", "0" * 64),
                           ("root_inode", 0), ("run_id", "not-a-run"), ("extra", True)):
            record = json.loads(original)
            record[key] = value
            marker.write_text(json.dumps(record))
            self.assertIsNone(fixtures.check_artifact("CU-T03", path)["artifact_matches"])
        marker.write_bytes(original[:-1] + b',"task_id":"CU-T03"}')
        self.assertIsNone(fixtures.check_artifact("CU-T03", path)["artifact_matches"])
        marker.write_bytes(original)
        alias = self.parent / "symlink-root"
        alias.symlink_to(path, target_is_directory=True)
        self.assertIsNone(fixtures.check_artifact("CU-T03", alias)["artifact_matches"])

    def test_mutation_during_read_is_unverifiable(self):
        path = self.prepare("CU-T03")
        original_read = os.read

        def mutate(descriptor, maximum):
            data = original_read(descriptor, maximum)
            if data.startswith(b"ALPHA\n"):
                (path / fixtures.CANDIDATE).write_bytes(b"changed during read")
            return data

        with mock.patch.object(fixtures.os, "read", side_effect=mutate):
            result = fixtures.check_artifact("CU-T03", path)
        self.assertIsNone(result["artifact_matches"])
        self.assertEqual(result["reason"], "artifact_changed")

    def test_atomic_replacement_during_read_is_unverifiable(self):
        path = self.prepare("CU-T03")
        original_read = os.read

        def replace(descriptor, maximum):
            data = original_read(descriptor, maximum)
            if data.startswith(b"ALPHA\n"):
                replacement = path / "replacement.txt"
                replacement.write_bytes(data)
                os.replace(replacement, path / fixtures.CANDIDATE)
            return data

        with mock.patch.object(fixtures.os, "read", side_effect=replace):
            result = fixtures.check_artifact("CU-T03", path)
        self.assertIsNone(result["artifact_matches"])
        self.assertEqual(result["reason"], "artifact_changed")

    def test_marker_bounds_and_directory_permissions_fail_closed(self):
        path = self.prepare("CU-T03")
        marker = path / fixtures.MARKER
        original = marker.read_bytes()
        for malformed in (b"null", b"[]", b"{" + b"x" * 4096, b"[" * 1500 + b"]" * 1500):
            marker.write_bytes(malformed)
            result = fixtures.check_artifact("CU-T03", path)
            self.assertIsNone(result["artifact_matches"])
            self.assertFalse(result["native_task_qualified"])
        marker.write_bytes(original)
        path.chmod(0o750)
        try:
            self.assertIsNone(fixtures.check_artifact("CU-T03", path)["artifact_matches"])
        finally:
            path.chmod(0o700)

    def test_cli_prepare_check_refusal_and_exit_status(self):
        script = str(Path(fixtures.__file__).resolve())
        path = self.parent / "cli"

        def invoke(operation):
            return subprocess.run(
                [sys.executable, script, operation, "--task", "CU-T01", "--run-directory", str(path)],
                capture_output=True, text=True, timeout=5, check=False,
            )

        prepared = invoke("prepare")
        self.assertEqual(prepared.returncode, 0, prepared.stderr)
        self.assertEqual(invoke("prepare").returncode, 2)
        self.assertEqual(invoke("check-artifact").returncode, 2)
        (path / fixtures.CANDIDATE).write_bytes(b"wrong")
        self.assertEqual(invoke("check-artifact").returncode, 1)
        (path / fixtures.CANDIDATE).write_bytes("MintClaw — Привет 世界 café 👩‍💻\n".encode("utf-8"))
        checked = invoke("check-artifact")
        self.assertEqual(checked.returncode, 0, checked.stderr)
        self.assertTrue(json.loads(checked.stdout)["artifact_matches"])
        self.assertFalse(json.loads(checked.stdout)["native_task_qualified"])
        self.assertEqual(checked.stderr, "")


if __name__ == "__main__":
    unittest.main()
