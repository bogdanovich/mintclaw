import base64
import copy
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

import interactive_fixtures as fixtures


class InteractiveFixtureTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="mintclaw-interactive-oracle-")
        self.addCleanup(self.temporary.cleanup)
        self.parent = Path(self.temporary.name)

    def prepare(self, task):
        path = self.parent / task
        result = fixtures.prepare(task, path)
        self.assertIs(result["native_task_qualified"], False)
        self.assertIs(result["native_source_verified"], False)
        return path

    def candidate(self, task, path):
        record, _ = fixtures.load_fixture(task, path)
        state = {
            "cards": [
                {"id": "alpha", "label": "Card Alpha", "x": 40, "y": 260, "width": 120, "height": 70},
                {"id": "beta", "label": "Card Beta", "x": 260, "y": 60, "width": 120, "height": 70},
            ],
            "drag": {"card": "alpha", "from": [40, 60], "to": [40, 260], "updates": 3, "completed": True},
        } if task == "CU-T18" else {
            "fields": {"name": "MintClaw — Привет 世界 café 👩‍💻", "choice": "beta",
                       "confirmed": True, "period": "monthly"},
            "decoys": {"name_preview": "KEEP", "choice_preview": "gamma", "confirmed_preview": False},
        }
        return {key: record[key] for key in ("task_id", "run_id", "fixture_revision")} | {
            "state": state, "guard": {"text": "GUARD_UNCHANGED", "count": 0, "visible": True},
        }

    def check(self, task, path, value):
        result = fixtures.check_state(task, path, fixtures.encoded(value))
        self.assertIs(result["native_task_qualified"], False)
        self.assertIs(result["native_source_verified"], False)
        self.assertNotIn("GUARD_UNCHANGED", json.dumps(result))
        self.assertNotIn("Привет", json.dumps(result, ensure_ascii=False))
        return result

    def test_revision_specs_and_mac_pins_frozen_before_native_runs(self):
        self.assertEqual(fixtures.FIXTURE_REVISION,
                         "f23ca5033bb936f52ec7c879b710e9f9af76365b71b2f09af7625d2294b04ef0")
        hashes = {"CU-T18": "40f62ed6adf3959b55fdbcb8b11665568bbfa439390910c147b9ec05e80c6f47",
                  "CU-T19": "ba6a45c2b8ec767b54f450084e2e0d029202870cac5d9c630d72014b53c2b22d"}
        swift = Path(fixtures.__file__).with_name("interactive_fixture_macos.swift").read_text()
        self.assertIn(fixtures.FIXTURE_REVISION, swift)
        for task, digest in hashes.items():
            self.assertEqual(fixtures.shared.digest(fixtures.SPECS[task]), digest)
            self.assertIn(digest, swift)
        with self.assertRaises(TypeError):
            fixtures.SPECS["CU-T18"] = b"changed"
        changed = fixtures.seed("CU-T18")
        changed["cards"][0]["x"] = 100
        self.assertEqual(fixtures.seed("CU-T18")["cards"][0]["x"], 40)

    def test_private_new_directory_seed_and_launch_round_trip(self):
        for task in fixtures.SPECS:
            path = self.prepare(task)
            self.assertEqual(path.stat().st_mode & 0o777, 0o700)
            self.assertEqual((path / fixtures.SPEC_FILE).stat().st_mode & 0o777, 0o600)
            config = fixtures.launch_config(task, path)
            self.assertEqual(base64.b64decode(config["spec_base64"]), fixtures.SPECS[task])
            record, spec = fixtures.parse_launch(task, fixtures.encoded(config))
            self.assertEqual(record, config["record"])
            self.assertEqual(spec, fixtures.seed(task))
            with self.assertRaises(FileExistsError):
                fixtures.prepare(task, path)
            self.assertEqual((path / fixtures.SPEC_FILE).read_bytes(), fixtures.SPECS[task])

    def test_exact_states_match_without_qualifying_native_execution(self):
        for task in fixtures.SPECS:
            path = self.prepare(task)
            value = self.candidate(task, path)
            result = self.check(task, path, value)
            self.assertIs(result["state_matches"], True)
            self.assertEqual(result["snapshot_sha256"], fixtures.shared.digest(fixtures.encoded(value)))
            self.assertEqual(result["snapshot_bytes"], len(fixtures.encoded(value)))
            for forged in ("native_task_qualified", "native_source_verified", "fresh", "pid", "windows"):
                fake = copy.deepcopy(value)
                fake[forged] = True
                self.assertIsNone(self.check(task, path, fake)["state_matches"])

    def test_wrong_guard_form_decoys_and_exact_unicode_do_not_match(self):
        task = "CU-T19"
        path = self.prepare(task)
        good = self.candidate(task, path)
        mutations = [
            ("guard", "text", "changed"), ("guard", "count", 1), ("guard", "visible", False),
            ("guard", "visible", 1),
        ]
        for section, key, value in mutations:
            changed = copy.deepcopy(good)
            changed[section][key] = value
            self.assertIs(self.check(task, path, changed)["state_matches"], False)
        for section, key, value in (
            ("fields", "name", "MintClaw — Привет 世界 cafe\u0301 👩‍💻"),
            ("fields", "name", "MintClaw — Привет 世界 café 👩‍💻 "),
            ("fields", "choice", "gamma"), ("fields", "confirmed", 1),
            ("fields", "period", "yearly"), ("decoys", "name_preview", "changed"),
            ("decoys", "choice_preview", "beta"), ("decoys", "confirmed_preview", 0),
        ):
            changed = copy.deepcopy(good)
            changed["state"][section][key] = value
            self.assertIs(self.check(task, path, changed)["state_matches"], False)

    def test_canvas_requires_correct_card_containment_and_completed_motion(self):
        task = "CU-T18"
        path = self.prepare(task)
        good = self.candidate(task, path)
        for key, value in (("card", "beta"), ("from", [40, 61]), ("to", [40, 259]),
                           ("updates", 0), ("updates", 100001), ("completed", False), ("completed", 1)):
            changed = copy.deepcopy(good)
            changed["state"]["drag"][key] = value
            self.assertIs(self.check(task, path, changed)["state_matches"], False)
        for index, key, value in ((1, "x", 261), (0, "x", 31.9), (0, "x", 48.1),
                                  (0, "y", 251.9), (0, "y", 268.1), (0, "label", "other")):
            changed = copy.deepcopy(good)
            changed["state"]["cards"][index][key] = value
            self.assertIs(self.check(task, path, changed)["state_matches"], False)
        for x, y in ((32, 252), (48, 268)):
            changed = copy.deepcopy(good)
            changed["state"]["cards"][0].update(x=x, y=y)
            changed["state"]["drag"]["to"] = [x, y]
            self.assertIs(self.check(task, path, changed)["state_matches"], True)

    def test_canvas_callback_model_rejects_unchanged_cancelled_and_no_motion(self):
        spec = fixtures.seed("CU-T18")
        model = fixtures.CanvasModel(spec)
        self.assertFalse(fixtures.canvas_matches(spec, model.snapshot()))
        self.assertFalse(model.begin(1, 1))
        self.assertFalse(model.move(70, 290))
        self.assertTrue(model.begin(70, 90))
        self.assertTrue(model.move(70, 290))
        unfinished = model.snapshot()
        self.assertFalse(fixtures.canvas_matches(spec, unfinished))
        model.cancel()
        self.assertFalse(model.finish(70, 290))
        self.assertFalse(fixtures.canvas_matches(spec, model.snapshot()))
        model = fixtures.CanvasModel(spec)
        self.assertTrue(model.begin(70, 90))
        self.assertTrue(model.finish(70, 290))
        self.assertFalse(fixtures.canvas_matches(spec, model.snapshot()))
        model = fixtures.CanvasModel(spec)
        model.begin(70, 90)
        model.move(70, 190)
        model.move(70, 290)
        model.finish(70, 290)
        self.assertTrue(fixtures.canvas_matches(spec, model.snapshot()))
        self.assertEqual(model.snapshot()["drag"]["updates"], 2)
        model.snapshot()["cards"][0]["x"] = 0
        self.assertEqual(model.cards[0]["x"], 40)

    def test_snapshot_schemas_bounds_and_binding_refused(self):
        task = "CU-T18"
        path = self.prepare(task)
        good = self.candidate(task, path)
        changed_values = []
        for key, value in (("task_id", "CU-T19"), ("run_id", "0" * 32), ("fixture_revision", "0" * 64),
                           ("state", None), ("guard", [])):
            changed = copy.deepcopy(good)
            changed[key] = value
            changed_values.append(changed)
        for location, key, value in (("guard", "count", False), ("state", "cards", []),
                                      ("state", "extra", True)):
            changed = copy.deepcopy(good)
            changed[location][key] = value
            changed_values.append(changed)
        for value in (True, "40", None, 10 ** 1000):
            changed = copy.deepcopy(good)
            changed["state"]["cards"][0]["x"] = value
            changed_values.append(changed)
        changed = copy.deepcopy(good)
        changed["state"]["drag"]["updates"] = True
        changed_values.append(changed)
        for changed in changed_values:
            self.assertIsNone(self.check(task, path, changed)["state_matches"])
        for data in (None, b"x" * (fixtures.MAX_SNAPSHOT_BYTES + 1), b"null", b"\xff",
                     b"[" * 1500 + b"]" * 1500, fixtures.encoded(good)[:-1] + b',"task_id":"CU-T18"}'):
            result = fixtures.check_state(task, path, data)
            self.assertIsNone(result["state_matches"])
            self.assertIs(result["native_source_verified"], False)
        for number in (float("nan"), float("inf"), float("-inf"), True, 10 ** 1000):
            self.assertFalse(fixtures.finite(number))

    def test_changed_spec_unsafe_files_and_root_binding_refused(self):
        for kind in ("changed", "symlink", "hardlink", "directory", "fifo", "oversized", "root", "mode"):
            with self.subTest(kind=kind):
                path = self.parent / kind
                fixtures.prepare("CU-T19", path)
                good = self.candidate("CU-T19", path)
                spec = path / fixtures.SPEC_FILE
                if kind == "changed":
                    spec.write_bytes(spec.read_bytes() + b" ")
                elif kind in ("symlink", "hardlink", "directory", "fifo"):
                    contents = spec.read_bytes()
                    spec.unlink()
                    if kind in ("symlink", "hardlink"):
                        sibling = path / "other"
                        sibling.write_bytes(contents)
                        spec.symlink_to(sibling) if kind == "symlink" else os.link(sibling, spec)
                    elif kind == "directory":
                        spec.mkdir()
                    else:
                        os.mkfifo(spec)
                elif kind == "oversized":
                    spec.write_bytes(b"x" * 8193)
                elif kind == "root":
                    alias = self.parent / "alias"
                    alias.symlink_to(path, target_is_directory=True)
                    path = alias
                else:
                    path.chmod(0o750)
                    self.addCleanup(path.chmod, 0o700)
                self.assertIsNone(self.check("CU-T19", path, good)["state_matches"])

    def test_marker_and_launch_reject_wrong_binding_shape_spec_and_duplicates(self):
        task = "CU-T19"
        path = self.prepare(task)
        good = self.candidate(task, path)
        marker = path / fixtures.MARKER
        original = marker.read_bytes()
        config = fixtures.launch_config(task, path)
        for key, value in (("task_id", "CU-T18"), ("run_id", "bad"), ("root_inode", -1),
                           ("fixture_revision", "0" * 64), ("extra", True)):
            record = json.loads(original)
            record[key] = value
            marker.write_bytes(fixtures.encoded(record))
            self.assertIsNone(self.check(task, path, good)["state_matches"])
        marker.write_bytes(original)
        for key, value in (("task_id", "CU-T18"), ("run_id", "bad"), ("root_inode", True),
                           ("root_device", -1), ("fixture_revision", "0" * 64), ("extra", True)):
            changed = copy.deepcopy(config)
            changed["record"][key] = value
            with self.assertRaises(fixtures.shared.Unverifiable):
                fixtures.parse_launch(task, fixtures.encoded(changed))
        for raw in (b"null", b"[]", b"{}", b"x" * (fixtures.MAX_LAUNCH_BYTES + 1),
                    fixtures.encoded(config)[:-1] + b',"record":{}}'):
            with self.assertRaises((ValueError, fixtures.shared.Unverifiable)):
                fixtures.parse_launch(task, raw)
        for value in ("not base64", base64.b64encode(b"{}").decode(), True):
            changed = copy.deepcopy(config)
            changed["spec_base64"] = value
            with self.assertRaises((ValueError, fixtures.shared.Unverifiable)):
                fixtures.parse_launch(task, fixtures.encoded(changed))

    def test_spec_replacement_during_read_is_unverifiable(self):
        task = "CU-T18"
        path = self.prepare(task)
        good = self.candidate(task, path)
        original_read = os.read

        def replace(descriptor, maximum):
            data = original_read(descriptor, maximum)
            if data == fixtures.SPECS[task]:
                replacement = path / "replacement"
                replacement.write_bytes(data)
                os.replace(replacement, path / fixtures.SPEC_FILE)
            return data

        with mock.patch.object(fixtures.os, "read", side_effect=replace):
            result = self.check(task, path, good)
        self.assertIsNone(result["state_matches"])
        self.assertEqual(result["reason"], "artifact_changed")

    def test_cli_exit_status_metadata_only_and_no_source_claim(self):
        script = str(Path(fixtures.__file__).resolve())
        task = "CU-T19"
        path = self.parent / "cli"

        def invoke(operation, data=b""):
            return subprocess.run([sys.executable, script, operation, "--task", task, "--run-directory", str(path)],
                                  input=data, capture_output=True, timeout=5, check=False)

        self.assertEqual(invoke("prepare").returncode, 0)
        config = invoke("launch-config")
        self.assertEqual(config.returncode, 0)
        self.assertEqual(fixtures.parse_launch(task, config.stdout)[0]["task_id"], task)
        good = self.candidate(task, path)
        accepted = invoke("check-state", fixtures.encoded(good))
        self.assertEqual(accepted.returncode, 0)
        self.assertIs(json.loads(accepted.stdout)["native_source_verified"], False)
        self.assertNotIn("Привет", accepted.stdout.decode())
        good["state"]["fields"]["choice"] = "alpha"
        self.assertEqual(invoke("check-state", fixtures.encoded(good)).returncode, 1)
        self.assertEqual(invoke("check-state", b"invalid").returncode, 2)
        self.assertEqual(invoke("prepare").returncode, 2)


if __name__ == "__main__":
    unittest.main()
