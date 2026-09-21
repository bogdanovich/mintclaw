import importlib.util
import json
import os
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
        operation_id = "document_write_test"
        artifact_ref = "media://node-transfer-test"
        records = [
            {
                "kind": "tool.call",
                "correlation": {"tool_call_id": "call_discovery"},
                "data": {
                    "tool": "tool_search_tool_bm25",
                    "arguments_preview": json.dumps({"query": "document PDF inspect fields fill verify"}),
                },
            },
            {
                "kind": "tool.result",
                "correlation": {"tool_call_id": "call_discovery"},
                "data": {
                    "tool": "tool_search_tool_bm25",
                    "result_preview": 'Found tools: [{"name":"document"}]\nSUCCESS: unlocked',
                    "executed": True,
                    "status": "completed",
                },
            },
        ]
        for index, action in enumerate(("inspect", "fields", "fill", "verify"), 1):
            arguments = {"action": action, "redacted": True}
            if action == "verify":
                arguments = {"action": action, "source": artifact_ref, "operation_id": operation_id}
            report = {"operation": action, "state": "succeeded", "sequence": index}
            if action == "fill":
                report.update({"operation_id": operation_id, "artifacts": [{"ref": artifact_ref}]})
            records.extend(
                [
                    {
                        "kind": "tool.call",
                        "correlation": {"tool_call_id": f"call_{action}"},
                        "data": {
                            "tool": "document",
                            "arguments_preview": json.dumps(arguments),
                        },
                    },
                    {
                        "kind": "tool.result",
                        "correlation": {"tool_call_id": f"call_{action}"},
                        "data": {
                            "tool": "document",
                            "executed": True,
                            "status": "completed",
                            "result_preview": json.dumps(report) + "\nStructured deliverable: omitted",
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

        for missing, message in (
            ("source", "exact fill artifact ref"),
            ("operation_id", "exact fill operation ID"),
        ):
            with self.subTest(missing=missing):
                incomplete_trace = json.loads(json.dumps(trace))
                verify_call = next(
                    record
                    for record in incomplete_trace["records"]
                    if record.get("correlation", {}).get("tool_call_id") == "call_verify"
                    and record.get("kind") == "tool.call"
                )
                verify_arguments = json.loads(verify_call["data"]["arguments_preview"])
                verify_arguments.pop(missing)
                verify_call["data"]["arguments_preview"] = json.dumps(verify_arguments)
                with self.assertRaisesRegex(MODULE.QualificationError, message):
                    MODULE.document_trace_reports(incomplete_trace)

        reordered_trace = dict(trace)
        reordered_records = list(records)
        discovery_result = reordered_records.pop(1)
        reordered_records.append(discovery_result)
        reordered_trace["records"] = reordered_records
        with self.assertRaisesRegex(MODULE.QualificationError, "before successful discovery"):
            MODULE.document_trace_reports(reordered_trace)

        inverse_trace = dict(trace)
        inverse_records = list(records)
        discovery_result = inverse_records.pop(1)
        inverse_records.insert(0, discovery_result)
        inverse_trace["records"] = inverse_records
        with self.assertRaisesRegex(MODULE.QualificationError, "result appeared before its call"):
            MODULE.document_trace_reports(inverse_trace)

        unsupported_result_trace = dict(trace)
        unsupported_records = list(records)
        unsupported_records.insert(
            2,
            {
                "kind": "tool.result",
                "correlation": {"tool_call_id": "call_exec"},
                "data": {"tool": "exec", "executed": True, "status": "completed"},
            },
        )
        unsupported_result_trace["records"] = unsupported_records
        with self.assertRaisesRegex(MODULE.QualificationError, "prohibited model-visible tool result"):
            MODULE.document_trace_reports(unsupported_result_trace)

        trace["records"].insert(
            0,
            {
                "kind": "tool.call",
                "data": {"tool": "exec", "arguments_preview": json.dumps({"action": "run"})},
            },
        )
        with self.assertRaisesRegex(MODULE.QualificationError, "prohibited"):
            MODULE.document_trace_reports(trace)

    def test_trace_rejects_discovery_that_does_not_unlock_document(self):
        trace = {
            "schema_version": "mintclaw.diagnostic_trace.v1",
            "outcome": {"status": "completed"},
            "truncation": {},
            "records": [
                {
                    "kind": "tool.call",
                    "correlation": {"tool_call_id": "call_discovery"},
                    "data": {
                        "tool": "tool_search_tool_bm25",
                        "arguments_preview": json.dumps({"query": "document PDF"}),
                    },
                },
                {
                    "kind": "tool.result",
                    "correlation": {"tool_call_id": "call_discovery"},
                    "data": {
                        "tool": "tool_search_tool_bm25",
                        "result_preview": "Found 0 tools",
                        "executed": True,
                        "status": "completed",
                    },
                },
            ],
        }
        with self.assertRaisesRegex(MODULE.QualificationError, "unlock"):
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

    def test_private_scan_decodes_json_escaping(self):
        with tempfile.TemporaryDirectory() as root:
            evidence = pathlib.Path(root) / "trace.json"
            evidence.write_text(r'{"input_preview":"A\u0026B"}')
            self.assertNotIn("A&B", evidence.read_text())
            with self.assertRaisesRegex(MODULE.QualificationError, "private literal"):
                MODULE.assert_private_absent([evidence], ["A&B"])

    def test_atomic_copy_is_private_and_never_overwrites(self):
        with tempfile.TemporaryDirectory() as root:
            directory = pathlib.Path(root)
            source = directory / "source.pdf"
            output = directory / "output.pdf"
            source.write_bytes(b"qualified")
            MODULE.atomic_copy_no_replace(source, output)
            self.assertEqual(output.read_bytes(), b"qualified")
            self.assertEqual(os.stat(output).st_mode & 0o777, 0o600)
            with self.assertRaises(FileExistsError):
                MODULE.atomic_copy_no_replace(source, output)
            self.assertEqual(output.read_bytes(), b"qualified")

    def test_private_literals_rejects_empty_or_whitespace_only_files(self):
        with tempfile.TemporaryDirectory() as root:
            private_values = pathlib.Path(root) / "private-values.txt"
            for content in ("", "  \n\t\n"):
                private_values.write_text(content)
                with self.assertRaisesRegex(MODULE.QualificationError, "at least one"):
                    MODULE.private_literals(private_values)
            private_values.write_text("\n PRIVATE_CANARY \n")
            self.assertEqual(MODULE.private_literals(private_values), ["PRIVATE_CANARY"])


if __name__ == "__main__":
    unittest.main()
