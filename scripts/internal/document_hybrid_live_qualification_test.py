import importlib.util
import json
import pathlib
import tempfile
import unittest


MODULE_PATH = pathlib.Path(__file__).with_name("document_hybrid_live_qualification.py")
SPEC = importlib.util.spec_from_file_location("document_hybrid_live_qualification", MODULE_PATH)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MODULE)


class DocumentHybridLiveQualificationTest(unittest.TestCase):
    def test_trace_requires_exact_document_sequence(self):
        records = []
        for index, action in enumerate(("inspect", "fields", "fill", "verify"), 1):
            records.extend(
                [
                    {
                        "kind": "tool.call",
                        "data": {
                            "tool": "document",
                            "arguments_preview": json.dumps({"action": action, "redacted": True}),
                        },
                    },
                    {
                        "kind": "tool.result",
                        "data": {
                            "tool": "document",
                            "result_preview": json.dumps(
                                {"operation": action, "state": "succeeded", "sequence": index}
                            )
                            + "\nStructured deliverable: omitted",
                        },
                    },
                ]
            )
        trace = {
            "schema_version": "mintclaw.diagnostic_trace.v1",
            "outcome": {"status": "completed"},
            "truncation": {},
            "records": records,
        }
        reports = MODULE.document_trace_reports(trace)
        self.assertEqual(list(reports), ["inspect", "fields", "fill", "verify"])

        trace["records"].insert(
            0,
            {
                "kind": "tool.call",
                "data": {"tool": "exec", "arguments_preview": json.dumps({"action": "run"})},
            },
        )
        with self.assertRaisesRegex(MODULE.QualificationError, "prohibited"):
            MODULE.document_trace_reports(trace)

    def test_trace_correlation_uses_session_hash(self):
        with tempfile.TemporaryDirectory() as root:
            trace_root = pathlib.Path(root)
            session_key = "sk_v1_test"
            expected_hash = MODULE.hashlib.sha256(session_key.encode()).hexdigest()
            wanted = trace_root / "trace-wanted.json"
            wanted.write_text(json.dumps({"metadata": {"session_hash": expected_hash}}))
            (trace_root / "trace-other.json").write_text(
                json.dumps({"metadata": {"session_hash": "different"}})
            )
            self.assertEqual(MODULE.trace_for_session(trace_root, session_key), wanted)

    def test_fields_preview_can_end_after_bounded_required_state(self):
        prefix = (
            '{"schema_version":"mintclaw.document_report.v1","operation":"fields",'
            '"state":"succeeded","form_eligibility":{"state":"eligible",'
            '"mode":"hybrid_print_ready"},"fields":'
        )
        report = MODULE.decode_document_preview(prefix)
        self.assertEqual(report["operation"], "fields")
        self.assertEqual(report["form_eligibility"]["mode"], "hybrid_print_ready")

    def test_private_scan_fails_closed(self):
        with tempfile.TemporaryDirectory() as root:
            evidence = pathlib.Path(root) / "trace.json"
            evidence.write_text('{"input_preview":"PRIVATE_CANARY"}')
            with self.assertRaisesRegex(MODULE.QualificationError, "private literal"):
                MODULE.assert_private_absent([evidence], ["PRIVATE_CANARY"])


if __name__ == "__main__":
    unittest.main()
