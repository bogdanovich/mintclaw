import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

import handoff_fixtures as fixtures


class HandoffFixtureTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="mintclaw-handoff-oracle-")
        self.addCleanup(self.temporary.cleanup)
        self.parent = Path(self.temporary.name)
        self.path = self.parent / "task"
        self.prepared = fixtures.prepare(self.path)

    def check(self, phase="agent_resume"):
        result = fixtures.check_artifact(self.path, phase)
        self.assertIs(result["native_task_qualified"], False)
        self.assertIs(result["native_source_verified"], False)
        self.assertNotIn("Привет", json.dumps(result, ensure_ascii=False))
        self.assertNotIn("HUMAN", json.dumps(result))
        return result

    def test_revision_and_exact_stage_bytes_frozen_before_native_runs(self):
        self.assertEqual(fixtures.FIXTURE_REVISION,
                         "840e8bdc5df270ca1593e75dec89016235862f738dab1baabc652d5aa03980c1")
        expected = {
            "agent_edit": "ALPHA\nОбновлено — naïve 🌿\nOMEGA\n",
            "human_edit": "ALPHA\nОбновлено — naïve 🌿\nHUMAN — e\u0301 世界\nOMEGA\n",
            "agent_resume": "ALPHA\nОбновлено — naïve 🌿\nHUMAN — e\u0301 世界\nACK — принято\nOMEGA\n",
        }
        self.assertEqual(dict(fixtures.EXPECTED), {key: value.encode() for key, value in expected.items()})
        self.assertEqual((self.path / fixtures.shared.CANDIDATE).read_bytes(),
                         "ALPHA\nMintClaw — Привет 世界 café 👩‍💻\nOMEGA\n".encode())
        for phase, value in expected.items():
            (self.path / fixtures.shared.CANDIDATE).write_bytes(value.encode())
            result = self.check(phase)
            self.assertIs(result["artifact_matches"], True)
            self.assertEqual(result["artifact_bytes"], len(value.encode()))
            self.assertEqual(result["artifact_sha256"], fixtures.shared.digest(value.encode()))

    def test_localized_plan_declares_independent_interlocks_not_receipts(self):
        plan = self.prepared["plan"]
        self.assertEqual(plan["language"], "ru")
        self.assertIn("передайте мне ручное", plan["instruction"])
        self.assertIn("именно эту же сессию", plan["resume_message"])
        self.assertIn("Заверши задачу", plan["finish_message"])
        self.assertEqual(len(plan["required_live_evidence"]), 9)
        self.assertFalse(self.prepared["native_task_qualified"])
        self.assertFalse(self.prepared["native_source_verified"])
        plan["required_live_evidence"].clear()
        self.assertEqual(len(json.loads(fixtures.PLAN)["required_live_evidence"]), 9)
        with self.assertRaises(TypeError):
            fixtures.EXPECTED["agent_resume"] = b"changed"

    def test_initial_and_wrong_stage_never_match(self):
        for phase in fixtures.EXPECTED:
            self.assertIs(self.check(phase)["artifact_matches"], False)
        for written in fixtures.EXPECTED:
            (self.path / fixtures.shared.CANDIDATE).write_bytes(fixtures.EXPECTED[written])
            for requested in fixtures.EXPECTED:
                self.assertIs(self.check(requested)["artifact_matches"], written == requested)

    def test_final_bytes_do_not_prove_prior_phases_or_lifecycle(self):
        # Intentionally write the final artifact without a handoff, human GUI edit,
        # resume, native input, localized report or release. Equality alone is not proof.
        candidate = self.path / fixtures.shared.CANDIDATE
        candidate.write_bytes(fixtures.EXPECTED["agent_resume"])
        before = fixtures.shared.file_identity(candidate.stat())
        result = self.check()
        self.assertIs(result["artifact_matches"], True)
        self.assertEqual(fixtures.shared.file_identity(candidate.stat()), before)
        self.assertNotIn("completed_phases", result)
        for field in ("native_task_qualified", "native_source_verified", "released", "fresh_observation"):
            marker = self.path / fixtures.MARKER
            original = marker.read_bytes()
            record = json.loads(original)
            record[field] = True
            marker.write_bytes(fixtures.encoded(record))
            self.assertIsNone(self.check()["artifact_matches"])
            marker.write_bytes(original)

    def test_normalized_missing_duplicated_or_misordered_human_edits_fail(self):
        original = fixtures.EXPECTED["agent_resume"].decode()
        for changed in (original.replace("e\u0301", "é"), original.replace("\n", "\r\n"), original[:-1],
                        original.replace("HUMAN — e\u0301 世界\n", ""), original + "extra\n",
                        original.replace("ACK — принято\n", "ACK — принято\nACK — принято\n"),
                        original.replace("HUMAN — e\u0301 世界\nACK — принято", "ACK — принято\nHUMAN — e\u0301 世界"),
                        original.replace("Обновлено — naïve 🌿", "changed"), original.replace("OMEGA", "changed")):
            (self.path / fixtures.shared.CANDIDATE).write_text(changed)
            self.assertIs(self.check()["artifact_matches"], False)

    def test_private_setup_does_not_adopt_or_overwrite_existing_directory(self):
        self.assertEqual(self.path.stat().st_mode & 0o777, 0o700)
        for name in (fixtures.MARKER, fixtures.shared.CANDIDATE):
            self.assertEqual((self.path / name).stat().st_mode & 0o777, 0o600)
        candidate = self.path / fixtures.shared.CANDIDATE
        candidate.write_bytes(b"preserve existing file")
        with self.assertRaises(FileExistsError):
            fixtures.prepare(self.path)
        self.assertEqual(candidate.read_bytes(), b"preserve existing file")

    def test_unsafe_missing_invalid_and_oversized_candidates_unverifiable(self):
        for kind in ("missing", "symlink", "hardlink", "fifo", "directory", "utf8", "size"):
            with self.subTest(kind=kind):
                path = self.parent / kind
                fixtures.prepare(path)
                candidate = path / fixtures.shared.CANDIDATE
                candidate.unlink()
                if kind in ("symlink", "hardlink"):
                    other = path / "canary"
                    other.write_bytes(b"private canary")
                    candidate.symlink_to(other) if kind == "symlink" else os.link(other, candidate)
                elif kind == "fifo":
                    os.mkfifo(candidate)
                elif kind == "directory":
                    candidate.mkdir()
                elif kind == "utf8":
                    candidate.write_bytes(b"\xff")
                elif kind == "size":
                    candidate.write_bytes(b"x" * (fixtures.shared.MAX_ARTIFACT_BYTES + 1))
                result = fixtures.check_artifact(path, "agent_resume")
                self.assertIsNone(result["artifact_matches"])
                self.assertIs(result["native_task_qualified"], False)
                self.assertNotIn("private canary", json.dumps(result))

    def test_wrong_root_marker_duplicate_fields_and_bounds_unverifiable(self):
        marker = self.path / fixtures.MARKER
        original = marker.read_bytes()
        for key, value in (("task_id", "CU-T03"), ("run_id", "bad"), ("root_inode", True),
                           ("root_device", -1), ("fixture_revision", "0" * 64), ("extra", True)):
            record = json.loads(original)
            record[key] = value
            marker.write_bytes(fixtures.encoded(record))
            self.assertIsNone(self.check()["artifact_matches"])
        for data in (b"null", b"[]", b"x" * 4097, b"[" * 1500 + b"]" * 1500,
                     original[:-1] + b',"task_id":"CU-T20"}'):
            marker.write_bytes(data)
            self.assertIsNone(self.check()["artifact_matches"])
        marker.write_bytes(original)
        alias = self.parent / "alias"
        alias.symlink_to(self.path, target_is_directory=True)
        self.assertIsNone(fixtures.check_artifact(alias, "agent_resume")["artifact_matches"])
        self.path.chmod(0o750)
        try:
            self.assertIsNone(self.check()["artifact_matches"])
        finally:
            self.path.chmod(0o700)

    def test_candidate_replacement_during_read_is_unverifiable(self):
        original_read = os.read

        def replace(descriptor, maximum):
            data = original_read(descriptor, maximum)
            if data == fixtures.shared.INITIAL.encode():
                replacement = self.path / "replacement"
                replacement.write_bytes(data)
                os.replace(replacement, self.path / fixtures.shared.CANDIDATE)
            return data

        with mock.patch.object(fixtures.os, "read", side_effect=replace):
            result = self.check()
        self.assertIsNone(result["artifact_matches"])
        self.assertEqual(result["reason"], "artifact_changed")

    def test_unknown_phase_is_not_echoed_or_treated_as_a_lifecycle_receipt(self):
        for phase in ("private phase canary", None, [], {"released": True}, True):
            result = fixtures.check_artifact(self.path, phase)
            self.assertIsNone(result["phase"])
            self.assertIsNone(result["artifact_matches"])
            self.assertEqual(result["reason"], "unsupported_phase")
            self.assertNotIn("private phase canary", json.dumps(result))

    def test_cli_requires_explicit_phase_and_reports_only_projection(self):
        script = str(Path(fixtures.__file__).resolve())

        def invoke(*args):
            return subprocess.run([sys.executable, script, *args, '--run-directory', str(self.path)],
                                  capture_output=True, timeout=5, check=False)

        self.assertEqual(invoke('prepare').returncode, 2)
        self.assertEqual(invoke('check-artifact').returncode, 2)
        self.assertEqual(invoke('check-artifact', '--phase', 'agent_edit').returncode, 1)
        (self.path / fixtures.shared.CANDIDATE).write_bytes(fixtures.EXPECTED['human_edit'])
        result = invoke('check-artifact', '--phase', 'human_edit')
        self.assertEqual(result.returncode, 0)
        projected = json.loads(result.stdout)
        self.assertIs(projected['artifact_matches'], True)
        self.assertIs(projected['native_task_qualified'], False)
        self.assertIs(projected['native_source_verified'], False)
        self.assertNotIn('HUMAN', result.stdout.decode())

    def test_unknown_phase_cli_is_structured_and_never_echoes_supplied_value(self):
        script = str(Path(fixtures.__file__).resolve())
        for path in (self.path, self.parent / 'absent'):
            for phase in ('private phase canary', '', '-private-phase-canary'):
                with self.subTest(path_exists=path.exists(), phase=phase):
                    result = subprocess.run(
                        [sys.executable, script, 'check-artifact', '--run-directory', str(path), '--phase=' + phase],
                        capture_output=True, timeout=5, check=False,
                    )
                    self.assertEqual(result.returncode, 2)
                    self.assertEqual(result.stderr, b'')
                    value = json.loads(result.stdout)
                    self.assertEqual(value['reason'], 'unsupported_phase')
                    self.assertIsNone(value['phase'])
                    self.assertIsNone(value['artifact_matches'])
                    self.assertIs(value['native_task_qualified'], False)
                    self.assertIs(value['native_source_verified'], False)
                    if phase:
                        self.assertNotIn(phase.encode(), result.stdout)


if __name__ == '__main__':
    unittest.main()
