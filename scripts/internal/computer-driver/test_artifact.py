import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest


spec = importlib.util.spec_from_file_location("computer_driver_artifact", Path(__file__).with_name("artifact.py"))
artifact = importlib.util.module_from_spec(spec)
spec.loader.exec_module(artifact)


class ArtifactTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="mintclaw-driver-artifact-test-")
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.binary = self.directory / "driver"
        self.binary.write_bytes(b"synthetic artifact, never executed")
        self.binary.chmod(0o600)
        self.patch = self.directory / "patch"
        self.patch.write_text("synthetic source patch", encoding="utf-8")
        self.patch.chmod(0o600)
        self.manifest = self.directory / "manifest.json"
        self.metadata = artifact.expected(self.patch)
        self.metadata.update({
            "binary_sha256": artifact.digest(self.binary),
            "cargo_lock_sha256": "a" * 64,
            "patched_native_sha256": "b" * 64,
        })

    def write(self, value=None):
        self.manifest.write_text(json.dumps(self.metadata if value is None else value), encoding="utf-8")
        self.manifest.chmod(0o600)

    def verify(self):
        return artifact.verify(self.manifest, self.patch, self.binary)

    def test_exact_artifact_passes(self):
        self.write()
        self.assertEqual(self.verify(), artifact.digest(self.binary))

    def test_changed_binary_is_not_rebound(self):
        self.write()
        self.binary.write_bytes(b"replacement")
        with self.assertRaises(ValueError):
            self.verify()

    def test_fixed_provenance_fields_are_exact(self):
        for key in artifact.expected(self.patch):
            with self.subTest(key=key):
                changed = dict(self.metadata)
                changed[key] = "replacement"
                self.write(changed)
                with self.assertRaises(ValueError):
                    self.verify()

    def test_boolean_schema_version_is_not_integer_version(self):
        self.metadata["schema_version"] = True
        self.write()
        with self.assertRaises(ValueError):
            self.verify()

    def test_unknown_and_missing_fields_are_rejected(self):
        for field in ("source_commit", "binary_sha256"):
            value = dict(self.metadata)
            del value[field]
            self.write(value)
            with self.assertRaises(ValueError):
                self.verify()
        self.metadata["unexpected"] = "value"
        self.write()
        with self.assertRaises(ValueError):
            self.verify()

    def test_changed_patch_is_rejected(self):
        self.write()
        self.patch.write_text("different source patch", encoding="utf-8")
        with self.assertRaises(ValueError):
            self.verify()

    def test_invalid_digest_is_rejected(self):
        for value in (None, "ABC" * 22, "z" * 64, "a" * 63):
            with self.subTest(value=value):
                self.metadata["patched_native_sha256"] = value
                self.write()
                with self.assertRaises(ValueError):
                    self.verify()

    def test_metadata_size_duplicate_keys_and_nonobject_are_rejected(self):
        for raw in (" " * (16 * 1024 + 1), '{"schema_version":1,"schema_version":1}', "[]"):
            self.manifest.write_text(raw, encoding="utf-8")
            self.manifest.chmod(0o600)
            with self.assertRaises(ValueError):
                self.verify()

    def test_unsafe_files_and_symlinks_are_rejected(self):
        self.write()
        for path in (self.binary, self.patch, self.manifest):
            with self.subTest(path=path.name):
                path.chmod(0o622)
                with self.assertRaises(ValueError):
                    self.verify()
                path.chmod(0o600)
        target = self.directory / "original"
        self.binary.rename(target)
        os.symlink(target, self.binary)
        with self.assertRaises(ValueError):
            self.verify()


if __name__ == "__main__":
    unittest.main()
