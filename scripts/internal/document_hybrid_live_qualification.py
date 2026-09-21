#!/usr/bin/env python3
"""Validate one deployed document-only agent-live hybrid-form qualification."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import tempfile
import time
from typing import Any


class QualificationError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise QualificationError(message)


def load_json(path: pathlib.Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    require(isinstance(value, dict), f"{path.name} must contain a JSON object")
    return value


def sha256_file(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def decode_preview(value: Any) -> dict[str, Any]:
    require(isinstance(value, str) and value, "document result preview is absent")
    try:
        decoded, _ = json.JSONDecoder().raw_decode(value.lstrip())
    except json.JSONDecodeError as error:
        raise QualificationError("document result preview is not parseable JSON") from error
    require(isinstance(decoded, dict), "document result preview must contain an object")
    return decoded


def decode_document_preview(value: Any) -> dict[str, Any]:
    try:
        return decode_preview(value)
    except QualificationError:
        require(isinstance(value, str), "document result preview is absent")
        fields_prefix = (
            '"operation":"fields","state":"succeeded"' in value
            and '"form_eligibility":{"state":"eligible","mode":"hybrid_print_ready"}' in value
        )
        require(fields_prefix, "document result preview is truncated before its required state")
        return {
            "operation": "fields",
            "state": "succeeded",
            "form_eligibility": {"state": "eligible", "mode": "hybrid_print_ready"},
        }


def trace_for_session(trace_root: pathlib.Path, session_key: str, wait_seconds: float = 10) -> pathlib.Path:
    session_hash = hashlib.sha256(session_key.encode("utf-8")).hexdigest()
    deadline = time.monotonic() + wait_seconds
    while True:
        matches = []
        for path in trace_root.glob("trace-*.json"):
            try:
                trace = load_json(path)
            except (OSError, ValueError, QualificationError):
                continue
            if trace.get("metadata", {}).get("session_hash") == session_hash:
                matches.append(path)
        if len(matches) == 1:
            return matches[0]
        if len(matches) > 1 or time.monotonic() >= deadline:
            require(False, f"expected one trace for live session, found {len(matches)}")
        time.sleep(0.2)


def document_trace_reports(trace: dict[str, Any]) -> dict[str, dict[str, Any]]:
    require(trace.get("schema_version") == "mintclaw.diagnostic_trace.v1", "unexpected trace schema")
    require(trace.get("outcome", {}).get("status") == "completed", "trace did not complete")
    require(trace.get("truncation") in ({}, None), "trace is truncated")
    records = trace.get("records")
    require(isinstance(records, list), "trace records are absent")

    calls = []
    visible_tools = []
    discovery_call_id: str | None = None
    document_discovered = False
    discovery_results = 0
    expected_actions = ("inspect", "fields", "fill", "verify")
    document_index = 0
    pending_document: tuple[str, str] | None = None
    reports: dict[str, dict[str, Any]] = {}
    for record in records:
        if not isinstance(record, dict):
            continue
        data = record.get("data", {})
        if record.get("kind") == "tool.result":
            result_tool = data.get("tool")
            require(
                result_tool in {"tool_search_tool_bm25", "document"},
                f"prohibited model-visible tool result: {result_tool}",
            )
        if record.get("kind") == "tool.call":
            tool = data.get("tool")
            arguments = data.get("arguments_preview")
            try:
                projected = json.loads(arguments)
            except (TypeError, json.JSONDecodeError) as error:
                raise QualificationError("tool-call projection is malformed") from error
            if tool == "tool_search_tool_bm25":
                query = projected.get("query")
                require(isinstance(query, str) and "document" in query.lower(), "discovery did not target document")
                require(discovery_call_id is None, "document discovery was called more than once")
                call_id = record.get("correlation", {}).get("tool_call_id")
                require(isinstance(call_id, str) and call_id, "document discovery call ID is absent")
                discovery_call_id = call_id
                visible_tools.append(tool)
                continue
            require(tool == "document", f"prohibited model-visible tool used: {tool}")
            require(document_discovered, "document used before successful discovery")
            require(pending_document is None, "document call appeared before the prior result")
            require(document_index < len(expected_actions), "unexpected additional document call")
            action = projected.get("action")
            require(action == expected_actions[document_index], f"unexpected document action: {action}")
            if action == "verify":
                fill_report = reports.get("fill", {})
                fill_artifacts = fill_report.get("artifacts")
                require(
                    projected.get("operation_id") == fill_report.get("operation_id"),
                    "verify did not use the exact fill operation ID",
                )
                require(
                    isinstance(fill_artifacts, list)
                    and len(fill_artifacts) == 1
                    and projected.get("source") == fill_artifacts[0].get("ref"),
                    "verify did not use the exact fill artifact ref",
                )
            call_id = record.get("correlation", {}).get("tool_call_id")
            require(isinstance(call_id, str) and call_id, "document call ID is absent")
            pending_document = (action, call_id)
            visible_tools.append(tool)
            calls.append(action)
        if record.get("kind") == "tool.result" and data.get("tool") == "tool_search_tool_bm25":
            result = data.get("result_preview")
            require(isinstance(result, str), "document discovery result is absent")
            require(discovery_call_id is not None, "document discovery result appeared before its call")
            result_call_id = record.get("correlation", {}).get("tool_call_id")
            require(result_call_id == discovery_call_id, "document discovery result call ID differs")
            discovery_results += 1
            document_discovered = (
                data.get("executed") is True
                and data.get("status") == "completed"
                and re.search(r'"name"\s*:\s*"document"', result) is not None
                and "SUCCESS" in result
            )
        if record.get("kind") == "tool.result" and data.get("tool") == "document":
            require(pending_document is not None, "document result appeared before its call")
            action, call_id = pending_document
            result_call_id = record.get("correlation", {}).get("tool_call_id")
            require(result_call_id == call_id, "document result call ID differs")
            require(
                data.get("executed") is True and data.get("status") == "completed",
                "document result did not complete",
            )
            report = decode_document_preview(data.get("result_preview"))
            operation = report.get("operation")
            require(operation == action, f"document result operation differs from call: {operation}")
            require(operation not in reports, f"duplicate {operation} result")
            reports[operation] = report
            document_index += 1
            pending_document = None

    expected_tools = ["tool_search_tool_bm25", "document", "document", "document", "document"]
    require(discovery_results == 1, f"expected one document discovery result, found {discovery_results}")
    require(document_discovered, "discovery did not unlock the document tool")
    require(visible_tools == expected_tools, f"unexpected model-visible tool sequence: {visible_tools}")
    require(calls == ["inspect", "fields", "fill", "verify"], f"unexpected document calls: {calls}")
    require(
        pending_document is None and document_index == len(expected_actions),
        "document call/result sequence is incomplete",
    )
    require(set(reports) == {"inspect", "fields", "fill", "verify"}, "document result set is incomplete")
    return reports


def parse_pdfinfo(text: str) -> dict[str, str]:
    result = {}
    for line in text.splitlines():
        if ":" in line:
            key, value = line.split(":", 1)
            result[key.strip()] = value.strip()
    return result


def redact_pdfsig_path(text: str, selector: pathlib.Path) -> str:
    redacted = text.replace(str(selector), "[document path omitted]")
    return re.sub(r"(?m)^(File )'[^']*'(.*)$", r"\1'[document path omitted]'\2", redacted)


def run_capture(arguments: list[str], *, check: bool = True, env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
    return subprocess.run(arguments, check=check, text=True, capture_output=True, env=env)


def render_hashes(pdf: pathlib.Path, directory: pathlib.Path, expected_pages: int) -> list[str]:
    directory.mkdir(mode=0o700)
    prefix = directory / "page"
    run_capture(["pdftoppm", "-png", "-r", "150", str(pdf), str(prefix)])
    pages = sorted(directory.glob("page-*.png"))
    require(len(pages) == expected_pages, f"rendered {len(pages)} pages, expected {expected_pages}")
    return [sha256_file(page) for page in pages]


def assert_private_absent(paths: list[pathlib.Path], forbidden: list[str]) -> None:
    for path in paths:
        text = path.read_text(encoding="utf-8", errors="replace")
        for value in forbidden:
            require(value not in text, f"private literal leaked into {path.name}")
        if path.suffix != ".json":
            continue
        try:
            decoded = json.loads(text)
        except json.JSONDecodeError as error:
            raise QualificationError(f"JSON evidence is malformed: {path.name}") from error
        for value in json_strings(decoded):
            for forbidden_value in forbidden:
                require(forbidden_value not in value, f"private literal leaked into {path.name}")


def private_literals(path: pathlib.Path) -> list[str]:
    values = [value.strip() for value in path.read_text(encoding="utf-8").splitlines() if value.strip()]
    require(bool(values), "private-values must contain at least one non-empty literal")
    return values


def json_strings(value: Any):
    if isinstance(value, str):
        yield value
    elif isinstance(value, dict):
        for key, item in value.items():
            yield from json_strings(key)
            yield from json_strings(item)
    elif isinstance(value, list):
        for item in value:
            yield from json_strings(item)


def atomic_copy_no_replace(source: pathlib.Path, destination: pathlib.Path) -> None:
    destination.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{destination.name}.", dir=destination.parent)
    temporary = pathlib.Path(temporary_name)
    try:
        with os.fdopen(descriptor, "wb") as target, source.open("rb") as origin:
            shutil.copyfileobj(origin, target)
            target.flush()
            os.fsync(target.fileno())
        temporary.chmod(0o600)
        os.link(temporary, destination)
    finally:
        temporary.unlink(missing_ok=True)


def validate_write_report(report: dict[str, Any], expected_pages: int, expected_assigned: int) -> None:
    require(report.get("state") == "succeeded", f"{report.get('operation')} did not succeed")
    write = report.get("write", {})
    require(write.get("checked_fields") == expected_assigned, "checked field count differs")
    require(write.get("checked_widgets") == expected_assigned, "checked widget count differs")
    require(write.get("structural_assertions", 0) > 0, "structural verification is absent")
    require(write.get("visual_assertions", 0) > 0, "production visual verification is absent")
    require(write.get("rendered_pages") == expected_pages, "production renderer did not cover every page")
    require(write.get("independent_visual_assertions", 0) > 0, "independent visual verification is absent")
    require(write.get("independent_rendered_pages") == expected_pages, "independent renderer coverage differs")
    output = write.get("output", {})
    require(output.get("mode") == "flattened_print_ready", "output is not print-ready")
    require(output.get("page_count") == expected_pages, "output page count differs")
    for key in ("acroform", "xfa", "content_signatures", "usage_rights", "actions"):
        require(output.get(key) == "absent", f"output {key} was not removed")
    require(output.get("encryption") == "present", "output encryption was not preserved")


def arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--live-json", required=True, type=pathlib.Path)
    parser.add_argument("--live-stderr", required=True, type=pathlib.Path)
    parser.add_argument("--config", required=True, type=pathlib.Path)
    parser.add_argument("--input", required=True, type=pathlib.Path)
    parser.add_argument("--baseline", required=True, type=pathlib.Path)
    parser.add_argument("--private-values", required=True, type=pathlib.Path)
    parser.add_argument("--scratch-before", required=True, type=pathlib.Path)
    parser.add_argument("--staged-output", required=True, type=pathlib.Path)
    parser.add_argument("--evidence-dir", required=True, type=pathlib.Path)
    parser.add_argument("--binary", required=True, type=pathlib.Path)
    parser.add_argument("--expected-sha256", required=True)
    parser.add_argument("--expected-pages", required=True, type=int)
    parser.add_argument("--expected-assigned-fields", required=True, type=int)
    return parser.parse_args()


def main() -> int:
    args = arguments()
    live = load_json(args.live_json)
    require(live.get("outcome") == "success", "agent live did not return success")
    session_key = live.get("session_key")
    require(isinstance(session_key, str) and session_key, "agent live omitted its session key")
    trace_scope = live.get("trace_scope", {})
    workspace_value = trace_scope.get("workspace")
    require(isinstance(workspace_value, str) and workspace_value, "agent live omitted trace workspace")
    workspace = pathlib.Path(workspace_value)
    trace_path = trace_for_session(workspace / "state/diagnostics/traces", session_key)
    trace = load_json(trace_path)
    require(trace.get("metadata", {}).get("root_turn_id") == trace_scope.get("turn_id"), "trace scope mismatch")
    reports = document_trace_reports(trace)

    inspect = reports["inspect"]
    fields = reports["fields"]
    fill = reports["fill"]
    verify = reports["verify"]
    require(inspect.get("state") == "succeeded", "live inspection failed")
    require(inspect.get("source", {}).get("sha256") == args.expected_sha256, "live source digest differs")
    require(inspect.get("page_count") == args.expected_pages, "live source page count differs")
    require(fields.get("state") == "succeeded", "live field discovery failed")
    require(
        fields.get("form_eligibility") == {"state": "eligible", "mode": "hybrid_print_ready"},
        "live field discovery did not admit the hybrid form",
    )
    validate_write_report(fill, args.expected_pages, args.expected_assigned_fields)
    validate_write_report(verify, args.expected_pages, args.expected_assigned_fields)
    require(fill.get("operation_id") == verify.get("operation_id"), "fill and verify operation IDs differ")
    artifacts = fill.get("artifacts")
    require(isinstance(artifacts, list) and len(artifacts) == 1, "fill did not produce exactly one artifact")
    operation_id = fill["operation_id"]
    artifact_ref = artifacts[0].get("ref")
    require(isinstance(artifact_ref, str) and artifact_ref.startswith("media://node-transfer-"), "artifact ref is invalid")

    state_root = args.config.parent / "state/document-writes"
    journal_path = state_root / "journal" / f"{operation_id}.json"
    generation_path = state_root / "generations" / operation_id / "generation.json"
    journal = load_json(journal_path)
    generation = load_json(generation_path)
    require(journal.get("state") == "delivered" and journal.get("revision", 0) >= 8, "write was not durably delivered")
    require(journal.get("artifact_ref") == artifact_ref, "journal artifact ref differs")
    require(journal.get("source_sha256") == args.expected_sha256, "journal source digest differs")
    require(generation.get("facts") == fill.get("write"), "generation facts differ from live fill report")

    outbox_matches: list[pathlib.Path] = []
    session_outboxes: list[pathlib.Path] = []
    for candidate in (workspace / "state/outbox").glob("*.json"):
        value = load_json(candidate)
        if value.get("identity", {}).get("session_key") == session_key:
            session_outboxes.append(candidate)
        recovery = value.get("media", {}).get("recovery", {})
        if recovery.get("operation_id") == operation_id:
            outbox_matches.append(candidate)
    require(len(outbox_matches) == 1, f"expected one operation media outbox, found {len(outbox_matches)}")
    outbox_path = outbox_matches[0]
    outbox = load_json(outbox_path)
    parts = outbox.get("media", {}).get("parts")
    require(outbox.get("status") == "delivered" and outbox.get("attempts") == 1, "artifact delivery was not single-attempt")
    require(isinstance(parts, list) and len(parts) == 1 and parts[0].get("ref") == artifact_ref, "outbox artifact differs")
    session_media_outboxes = [path for path in session_outboxes if load_json(path).get("media") is not None]
    require(
        session_media_outboxes == [outbox_path],
        f"expected one media delivery in the live session, found {len(session_media_outboxes)}",
    )

    media_name = artifact_ref.removeprefix("media://") + ".pdf"
    artifact_path = workspace / "state/media/files" / media_name
    require(artifact_path.is_file(), "delivered media file is unavailable")
    artifact_sha256 = sha256_file(artifact_path)
    require(artifact_sha256 == journal.get("artifact", {}).get("sha256"), "artifact digest differs from journal")
    require(artifact_sha256 == fill.get("write", {}).get("output_sha256"), "artifact digest differs from fill report")
    require(sha256_file(args.input) == args.expected_sha256, "source changed during live qualification")

    info_result = run_capture(["pdfinfo", str(artifact_path)])
    pdfinfo = parse_pdfinfo(info_result.stdout)
    require(int(pdfinfo.get("Pages", "0")) == args.expected_pages, "Poppler page count differs")
    require(pdfinfo.get("Form") == "none", "Poppler still sees a live form")
    require(pdfinfo.get("Encrypted", "").startswith("yes"), "Poppler does not see preserved encryption")
    (args.evidence_dir / "output-pdfinfo.txt").write_text(info_result.stdout, encoding="utf-8")
    pdfsig_result = run_capture(["pdfsig", str(artifact_path)], check=False)
    require("Signature #" not in pdfsig_result.stdout + pdfsig_result.stderr, "Poppler found an output signature")
    (args.evidence_dir / "output-pdfsig.txt").write_text(
        redact_pdfsig_path(pdfsig_result.stdout + pdfsig_result.stderr, artifact_path),
        encoding="utf-8",
    )

    with tempfile.TemporaryDirectory(prefix="mintclaw-live-render-") as render_root:
        render_path = pathlib.Path(render_root)
        baseline_hashes = render_hashes(args.baseline, render_path / "baseline", args.expected_pages)
        live_hashes = render_hashes(artifact_path, render_path / "live", args.expected_pages)
    require(live_hashes == baseline_hashes, "live output pages differ from the deterministic CLI baseline")

    with tempfile.TemporaryDirectory(prefix="mintclaw-live-inspect-") as inspect_home:
        inspect_env = os.environ.copy()
        inspect_env["MINTCLAW_HOME"] = inspect_home
        output_inspect_result = run_capture(
            [str(args.binary), "document", "inspect", "--input", str(artifact_path), "--json"], env=inspect_env
        )
    output_inspection = json.loads(output_inspect_result.stdout)
    output_facts = output_inspection.get("inspection", {})
    require(output_inspection.get("state") == "succeeded", "output inspection failed")
    require(output_facts.get("acroform", {}).get("state") == "absent", "output AcroForm remains")
    require(output_facts.get("xfa", {}).get("state") == "absent", "output XFA remains")
    require(output_facts.get("signatures", {}).get("state") == "absent", "output signature state remains")
    require(output_facts.get("actions", {}).get("state") == "absent", "output actions remain")
    (args.evidence_dir / "output-inspection.json").write_text(
        json.dumps(output_inspection, indent=2) + "\n", encoding="utf-8"
    )

    trace_copy = args.evidence_dir / "trace.json"
    journal_copy = args.evidence_dir / "journal.json"
    generation_copy = args.evidence_dir / "generation.json"
    outbox_copy = args.evidence_dir / "media-outbox.json"
    for source, target in (
        (trace_path, trace_copy),
        (journal_path, journal_copy),
        (generation_path, generation_copy),
        (outbox_path, outbox_copy),
    ):
        shutil.copyfile(source, target)
    for source in session_outboxes:
        target = args.evidence_dir / f"session-{source.name}"
        shutil.copyfile(source, target)

    forbidden = [str(args.input), *private_literals(args.private_values)]
    text_evidence = [path for path in args.evidence_dir.rglob("*") if path.is_file()]
    assert_private_absent(text_evidence, forbidden)
    scratch_root = pathlib.Path(os.environ.get("TMPDIR", "/tmp")) / "mintclaw_document_agent"
    scratch_before = args.scratch_before.read_text(encoding="utf-8").splitlines()
    scratch_after = sorted(str(path) for path in scratch_root.iterdir()) if scratch_root.is_dir() else []
    require(scratch_after == scratch_before, "document scratch state changed after the live turn")
    require(not any(operation_id in path for path in scratch_after), "operation scratch was not removed")

    version = run_capture([str(args.binary), "version", "--no-color"]).stdout
    (args.evidence_dir / "mintclaw-version.txt").write_text(version, encoding="utf-8")
    result = {
        "schema_version": "mintclaw.document_hybrid_live_qualification.v1",
        "state": "flattened_print_ready_live_verified",
        "marker": "MINTCLAW_PDF4H4_LIVE_QUALIFICATION_OK",
        "source_sha256": args.expected_sha256,
        "source_unchanged": True,
        "output_sha256": artifact_sha256,
        "pages": args.expected_pages,
        "assigned_fields": args.expected_assigned_fields,
        "operation_id": operation_id,
        "artifact_ref": artifact_ref,
        "trace_id": trace.get("trace_id"),
        "trace_session_hash": trace.get("metadata", {}).get("session_hash"),
        "journal_revision": journal.get("revision"),
        "delivery_id": journal.get("delivery_id"),
        "outbox_delivery_id": journal.get("outbox_delivery_id"),
        "delivery_attempts": outbox.get("attempts"),
        "document_actions": ["inspect", "fields", "fill", "verify"],
        "prohibited_tools": [],
        "trace_private_literals": 0,
        "render_sha256": live_hashes,
        "cli_baseline_visual_match": True,
        "scratch_restored": True,
        "output_mode": "flattened_print_ready",
    }
    (args.evidence_dir / "result.json").write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    for path in args.evidence_dir.rglob("*"):
        path.chmod(0o700 if path.is_dir() else 0o600)
    args.evidence_dir.chmod(0o700)
    require(not args.staged_output.exists(), "live qualification staged output appeared during validation")
    atomic_copy_no_replace(artifact_path, args.staged_output)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except QualificationError as error:
        raise SystemExit(f"live hybrid qualification failed: {error}") from error
