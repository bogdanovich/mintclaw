#!/usr/bin/env python3

"""Private fixture and report helpers for browser-capability-smoke.sh."""

from __future__ import annotations

import argparse
import http.server
import json
import os
import pathlib
import sys
import tempfile
import time
from typing import Any


SCHEMA_VERSION = "mintclaw.browser_smoke.v1"
MAX_INPUT_BYTES = 1024 * 1024

SUITE_CHECKS = {
    "core": (
        "initial_blank",
        "navigated_fixture",
        "reversible_action_visible",
        "fresh_observe",
    ),
    "managed-reuse": (
        "first_marker_absent",
        "marker_seeded",
        "marker_reused",
        "marker_cleared",
    ),
    "ephemeral-cleanup": (
        "first_state_clean",
        "cookie_seeded",
        "local_storage_seeded",
        "cache_seeded",
        "service_worker_seeded",
        "cookie_removed",
        "local_storage_removed",
        "cache_removed",
        "service_worker_removed",
    ),
    "driver-conformance": (
        "initial_blank",
        "navigated_fixture",
        "reversible_action_visible",
        "fresh_observe",
    ),
    "playwright-library": (
        "initial_blank",
        "navigated_fixture",
        "reversible_action_visible",
        "fresh_observe",
    ),
    "privileged-execute": (
        "initial_blank",
        "navigated_fixture",
        "structured_extraction",
        "reversible_dom_restored",
        "artifact_retained",
        "sandbox_denial",
        "runtime_timeout",
        "cleanup_after_timeout",
    ),
    "provider-lifecycle": (
        "first_open_ready",
        "first_observe_ready",
        "first_close_clean",
        "second_open_ready",
        "second_observe_ready",
        "second_close_clean",
    ),
    "steel-cloud": (
        "initial_blank",
        "navigated_fixture",
        "reversible_action_visible",
        "fresh_observe",
        "artifact_retained",
    ),
    "steel-profile-reuse": (
        "first_marker_absent",
        "marker_seeded",
        "marker_reused",
        "marker_cleared",
    ),
    "steel-handoff": (
        "initial_blank",
        "navigated_fixture",
        "handoff_started",
        "resumed_same_session",
        "fresh_after_resume",
        "close_clean",
    ),
}

SUITE_STAGES = {
    "core": (
        (
            "core",
            SUITE_CHECKS["core"],
            {"browser_targets": 1, "browser_session": 2, "browser_observe": 3, "browser_act": 2},
        ),
    ),
    "managed-reuse": (
        (
            "managed-seed",
            ("first_marker_absent", "marker_seeded"),
            {"browser_targets": 1, "browser_session": 2, "browser_observe": 3, "browser_act": 2},
        ),
        (
            "managed-verify",
            ("marker_reused", "marker_cleared"),
            {"browser_targets": 1, "browser_session": 2, "browser_observe": 3, "browser_act": 2},
        ),
    ),
    "ephemeral-cleanup": (
        (
            "ephemeral-seed",
            (
                "first_state_clean",
                "cookie_seeded",
                "local_storage_seeded",
                "cache_seeded",
                "service_worker_seeded",
            ),
            {"browser_targets": 1, "browser_session": 2, "browser_observe": 3, "browser_act": 2},
        ),
        (
            "ephemeral-verify",
            (
                "cookie_removed",
                "local_storage_removed",
                "cache_removed",
                "service_worker_removed",
            ),
            {"browser_targets": 1, "browser_session": 2, "browser_observe": 2, "browser_act": 1},
        ),
    ),
    "driver-conformance": (
        (
            "driver-conformance",
            SUITE_CHECKS["driver-conformance"],
            {"browser_targets": 1, "browser_session": 2, "browser_observe": 3, "browser_act": 2},
        ),
    ),
    "playwright-library": (
        (
            "playwright-library",
            SUITE_CHECKS["playwright-library"],
            {"browser_targets": 1, "browser_session": 2, "browser_observe": 3, "browser_act": 2},
        ),
    ),
    "privileged-execute": (
        (
            "privileged-execute",
            SUITE_CHECKS["privileged-execute"],
            {
                "browser_targets": 1,
                "browser_session": 2,
                "browser_observe": 2,
                "browser_act": 1,
                "browser_execute": 3,
            },
        ),
    ),
    "provider-lifecycle": (
        (
            "provider-open-one",
            ("first_open_ready", "first_observe_ready", "first_close_clean"),
            {"browser_targets": 1, "browser_session": 2, "browser_observe": 1},
        ),
        (
            "provider-open-two",
            ("second_open_ready", "second_observe_ready", "second_close_clean"),
            {"browser_targets": 1, "browser_session": 2, "browser_observe": 1},
        ),
    ),
    "steel-cloud": (
        (
            "steel-cloud",
            SUITE_CHECKS["steel-cloud"],
            {
                "browser_targets": 1,
                "browser_session": 2,
                "browser_observe": 3,
                "browser_act": 2,
                "browser_capture": 1,
            },
        ),
    ),
    "steel-profile-reuse": (
        (
            "steel-profile-seed",
            ("first_marker_absent", "marker_seeded"),
            {"browser_targets": 1, "browser_session": 2, "browser_observe": 3, "browser_act": 2},
        ),
        (
            "steel-profile-verify",
            ("marker_reused", "marker_cleared"),
            {"browser_targets": 1, "browser_session": 2, "browser_observe": 3, "browser_act": 2},
        ),
    ),
    "steel-handoff": (
        (
            "steel-handoff",
            SUITE_CHECKS["steel-handoff"],
            {"browser_targets": 1, "browser_session": 4, "browser_observe": 3, "browser_act": 1},
        ),
    ),
}


FIXTURE_HTML = b"""<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>MintClaw browser smoke fixture</title></head>
<body>
<main>
  <h1>MintClaw browser smoke fixture</h1>
  <button type="button" aria-label="Run reversible smoke action" id="core">Run reversible smoke action</button>
  <button type="button" aria-label="Set managed smoke marker" id="managed">Set managed smoke marker</button>
  <button type="button" aria-label="Seed ephemeral smoke state" id="ephemeral">Seed ephemeral smoke state</button>
  <button type="button" aria-label="Clear browser smoke state" id="clear">Clear browser smoke state</button>
  <output aria-label="Smoke status" id="status">SMOKE_LOADING</output>
</main>
<script>
const status = document.querySelector('#status');
async function state() {
  const registrations = 'serviceWorker' in navigator
    ? await navigator.serviceWorker.getRegistrations() : [];
  const cache = 'caches' in window ? await caches.has('mintclaw-browser-smoke') : false;
  return {
    cookie: document.cookie.includes('mintclaw_browser_smoke=seeded'),
    local: localStorage.getItem('mintclaw-browser-smoke') === 'seeded',
    cache,
    serviceWorker: registrations.some(registration => registration.scope.includes('/browser-smoke/')),
  };
}
function render(prefix, current) {
  status.textContent = `${prefix} cookie=${current.cookie} local_storage=${current.local} cache=${current.cache} service_worker=${current.serviceWorker}`;
}
async function renderCurrent(prefix = 'SMOKE_STATE') { render(prefix, await state()); }
document.querySelector('#core').addEventListener('click', () => {
  status.textContent = 'CORE_ACTION_OK';
});
document.querySelector('#managed').addEventListener('click', async () => {
  document.cookie = 'mintclaw_browser_smoke=seeded; Path=/browser-smoke/; SameSite=Strict';
  localStorage.setItem('mintclaw-browser-smoke', 'seeded');
  await renderCurrent('MANAGED_MARKER');
});
document.querySelector('#ephemeral').addEventListener('click', async () => {
  document.cookie = 'mintclaw_browser_smoke=seeded; Path=/browser-smoke/; SameSite=Strict';
  localStorage.setItem('mintclaw-browser-smoke', 'seeded');
  const cache = await caches.open('mintclaw-browser-smoke');
  await cache.put(location.origin + '/browser-smoke/cache-entry', new Response('safe smoke fixture'));
  await navigator.serviceWorker.register('/browser-smoke/service-worker.js', {scope: '/browser-smoke/'});
  await navigator.serviceWorker.ready;
  await renderCurrent('EPHEMERAL_SEEDED');
});
document.querySelector('#clear').addEventListener('click', async () => {
  document.cookie = 'mintclaw_browser_smoke=; Path=/browser-smoke/; Max-Age=0; SameSite=Strict';
  localStorage.removeItem('mintclaw-browser-smoke');
  if ('caches' in window) await caches.delete('mintclaw-browser-smoke');
  if ('serviceWorker' in navigator) {
    const registrations = await navigator.serviceWorker.getRegistrations();
    await Promise.all(registrations
      .filter(registration => registration.scope.includes('/browser-smoke/'))
      .map(registration => registration.unregister()));
  }
  await renderCurrent('SMOKE_CLEARED');
});
renderCurrent();
</script>
</body>
</html>
"""

SERVICE_WORKER = b"""self.addEventListener('install', event => self.skipWaiting());
self.addEventListener('activate', event => event.waitUntil(self.clients.claim()));
self.addEventListener('fetch', () => {});
"""


class FixtureHandler(http.server.BaseHTTPRequestHandler):
    server_version = "MintClawBrowserSmoke/1"

    def do_GET(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API
        path = self.path.split("?", 1)[0]
        if path in ("/browser-smoke", "/browser-smoke/", "/browser-smoke/check"):
            self._reply("text/html; charset=utf-8", FIXTURE_HTML)
            return
        if path == "/browser-smoke/service-worker.js":
            self._reply("application/javascript; charset=utf-8", SERVICE_WORKER)
            return
        if path == "/browser-smoke/cache-entry":
            self._reply("text/plain; charset=utf-8", b"safe smoke fixture")
            return
        self.send_error(404)

    def log_message(self, _format: str, *_args: object) -> None:
        return

    def _reply(self, content_type: str, body: bytes) -> None:
        self.send_response(200)
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def serve(args: argparse.Namespace) -> int:
    server = http.server.ThreadingHTTPServer((args.bind, 0), FixtureHandler)
    host = args.advertise_host
    if ":" in host and not host.startswith("["):
        host = f"[{host}]"
    report = {"origin": f"http://{host}:{server.server_port}"}
    ready = pathlib.Path(args.ready_file)
    temporary = ready.with_name(ready.name + ".tmp")
    temporary.write_text(json.dumps(report), encoding="utf-8")
    temporary.replace(ready)
    try:
        server.serve_forever(poll_interval=0.1)
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
    return 0


def load_json(path: str) -> dict[str, Any]:
    source = pathlib.Path(path)
    if source.stat().st_size > MAX_INPUT_BYTES:
        raise ValueError("input_limit")
    value = json.loads(source.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise ValueError("invalid_json")
    return value


def result_record(outer: dict[str, Any]) -> dict[str, str]:
    if outer.get("outcome") != "success":
        raise ValueError("agent_unavailable")
    output = outer.get("result_output")
    if not isinstance(output, dict) or set(output) != {"kind", "records"}:
        raise ValueError("invalid_agent_result")
    if output.get("kind") != "records":
        raise ValueError("invalid_agent_result")
    records = output.get("records")
    if not isinstance(records, list) or len(records) != 1:
        raise ValueError("invalid_agent_result")
    record = records[0]
    if (
        not isinstance(record, dict)
        or not record
        or any(
            not isinstance(key, str)
            or not isinstance(value, str)
            or not key
            or not value
            for key, value in record.items()
        )
    ):
        raise ValueError("invalid_agent_result")
    return record


def result_bool(record: dict[str, str], name: str) -> bool:
    value = record[name]
    if value == "true":
        return True
    if value == "false":
        return False
    raise ValueError("invalid_agent_result")


def stage_result(record: dict[str, str], checks: tuple[str, ...]) -> dict[str, Any]:
    required = {
        "target_ready",
        "capability_observe",
        "capability_navigate",
        "capability_click",
        "session_closed",
        "safe_error_absent",
        *checks,
    }
    if set(record) != required:
        raise ValueError("invalid_agent_result")
    return {
        "target_ready": result_bool(record, "target_ready"),
        "capabilities": {
            "observe": result_bool(record, "capability_observe"),
            "navigate": result_bool(record, "capability_navigate"),
            "click": result_bool(record, "capability_click"),
        },
        "checks": {name: result_bool(record, name) for name in checks},
        "session_closed": result_bool(record, "session_closed"),
        "safe_error_absent": result_bool(record, "safe_error_absent"),
    }


def cleanup_result(record: dict[str, str]) -> dict[str, Any]:
    if set(record) != {
        "target_ready",
        "open_ready",
        "initial_blank",
        "session_closed",
        "safe_error_absent",
    }:
        raise ValueError("invalid_agent_result")
    return {
        name: result_bool(record, name)
        for name in (
            "target_ready",
            "open_ready",
            "initial_blank",
            "session_closed",
            "safe_error_absent",
        )
    }


def safe_error(code: str) -> dict[str, str]:
    messages = {
        "agent_unavailable": "The live browser smoke request did not complete.",
        "cleanup_failed": "The browser smoke cleanup audit did not pass.",
        "input_limit": "The browser smoke result exceeded its input limit.",
        "invalid_agent_result": "The browser smoke result was not valid structured JSON.",
        "invalid_execution_evidence": "The browser smoke execution evidence was incomplete or inconsistent.",
        "invalid_json": "The browser smoke input was not valid JSON.",
        "suite_failed": "One or more browser smoke checks did not pass.",
    }
    return {"code": code, "message": messages.get(code, "The browser smoke failed.")}


def verify_execution_evidence(
    outer: dict[str, Any],
    target: str,
    profile: str,
    required_calls: dict[str, int],
    terminal_session_operations: frozenset[str] = frozenset({"close"}),
    exact_session_operations: tuple[str, ...] | None = None,
) -> dict[str, Any]:
    evidence = outer.get("execution_evidence")
    if not isinstance(evidence, dict) or set(evidence) != {
        "schema_version",
        "status",
        "parent",
        "delegation",
        "child",
        "safe_error",
    }:
        raise ValueError("invalid_execution_evidence")
    if (
        evidence.get("schema_version") != "mintclaw.live_execution_evidence.v1"
        or evidence.get("status") != "verified"
        or evidence.get("safe_error") is not None
    ):
        raise ValueError("invalid_execution_evidence")
    parent = evidence.get("parent")
    child = evidence.get("child")
    delegation = evidence.get("delegation")
    trace_keys = {
        "agent_id",
        "outcome",
        "incomplete",
        "tool_calls",
        "tool_failures",
        "unpaired_calls",
        "browser_sessions",
    }
    if (
        not isinstance(parent, dict)
        or set(parent) != trace_keys
        or not isinstance(child, dict)
        or set(child) != trace_keys
        or not isinstance(delegation, dict)
        or set(delegation) != {"agent_id", "admitted"}
    ):
        raise ValueError("invalid_execution_evidence")
    parent_calls = parent.get("tool_calls")
    admitted = delegation.get("admitted")
    if (
        parent.get("outcome") != "completed"
        or parent.get("incomplete") is not False
        or not isinstance(parent_calls, dict)
        or set(parent_calls) != {"delegate"}
        or type(parent_calls.get("delegate")) is not int
        or parent_calls.get("delegate") != 1
        or parent.get("tool_failures") != {}
        or parent.get("unpaired_calls") != {}
        or parent.get("browser_sessions") != []
        or delegation.get("agent_id") != "browser"
        or type(admitted) is not int
        or admitted != 1
        or child.get("agent_id") != "browser"
        or child.get("outcome") != "completed"
        or child.get("incomplete") is not False
        or child.get("unpaired_calls") != {}
    ):
        raise ValueError("invalid_execution_evidence")
    tool_failures = child.get("tool_failures")
    if not isinstance(tool_failures, dict) or tool_failures:
        raise ValueError("invalid_execution_evidence")
    calls = child.get("tool_calls")
    if not isinstance(calls, dict) or set(calls).difference(
        {
            "browser_targets",
            "browser_session",
            "browser_observe",
            "browser_contexts",
            "browser_act",
            "browser_capture",
            "browser_execute",
        }
    ):
        raise ValueError("invalid_execution_evidence")
    if any(
        not isinstance(count, int) or isinstance(count, bool) or count < 0
        for count in calls.values()
    ) or any(calls.get(name, 0) < count for name, count in required_calls.items()):
        raise ValueError("invalid_execution_evidence")
    if "browser_act" not in required_calls and calls.get("browser_act", 0) != 0:
        raise ValueError("invalid_execution_evidence")
    sessions = child.get("browser_sessions")
    if exact_session_operations is None:
        expected_operations = ("open", "terminal")
    else:
        expected_operations = exact_session_operations
    if not isinstance(sessions, list) or len(sessions) != len(expected_operations):
        raise ValueError("invalid_execution_evidence")
    for index, (session, expected_operation) in enumerate(
        zip(sessions, expected_operations, strict=True)
    ):
        if not isinstance(session, dict):
            raise ValueError("invalid_execution_evidence")
        if expected_operation == "open":
            if session != {"operation": "open", "target": target, "profile": profile}:
                raise ValueError("invalid_execution_evidence")
        elif expected_operation == "terminal":
            operation = session.get("operation")
            if operation not in terminal_session_operations:
                raise ValueError("invalid_execution_evidence")
            if operation == "status":
                if session != {"operation": "status", "state": "lost"}:
                    raise ValueError("invalid_execution_evidence")
            elif session != {"operation": "close"}:
                raise ValueError("invalid_execution_evidence")
        elif session != {"operation": expected_operation}:
            raise ValueError("invalid_execution_evidence")
    return {
        "state": "verified",
        "delegations": 1,
        "tool_calls": {name: calls[name] for name in sorted(calls)},
    }


def build_report(args: argparse.Namespace) -> tuple[dict[str, Any], bool]:
    started_ns = int(args.started_ns)
    report: dict[str, Any] = {
        "schema_version": SCHEMA_VERSION,
        "suite": args.suite,
        "target": args.target,
        "profile": args.profile,
        "capabilities": {},
        "checks": [],
        "cleanup": {
            "state": "failed",
            "session_close": "unknown",
            "fixture": args.fixture_state,
        },
        "process_audit": {"state": "failed", "immediate_reuse": False},
        "execution_audit": {
            "state": "failed",
            "primary": {"state": "unverified", "delegations": 0, "tool_calls": {}},
            "cleanup": {"state": "unverified", "delegations": 0, "tool_calls": {}},
        },
        "artifacts": [],
        "duration_ms": max(0, (time.time_ns() - started_ns) // 1_000_000),
        "safe_error": None,
    }
    try:
        stages = SUITE_STAGES[args.suite]
        if len(args.live_json) != len(stages):
            raise ValueError("invalid_agent_result")
        cleanup_outer = load_json(args.cleanup_json)
        cleanup = cleanup_result(result_record(cleanup_outer))
        cleanup_evidence = verify_execution_evidence(
            cleanup_outer,
            args.target,
            args.profile,
            {"browser_targets": 1, "browser_session": 2, "browser_observe": 1},
        )
        stage_results: list[dict[str, Any]] = []
        combined_checks: dict[str, bool] = {}
        primary_calls: dict[str, int] = {}
        for live_path, (stage_name, stage_checks, required_calls) in zip(
            args.live_json, stages, strict=True
        ):
            live_outer = load_json(live_path)
            result = stage_result(result_record(live_outer), stage_checks)
            evidence = verify_execution_evidence(
                live_outer,
                args.target,
                args.profile,
                required_calls,
                frozenset({"close", "status"})
                if stage_name == "privileged-execute"
                else frozenset({"close"}),
                ("open", "handoff", "resume", "close")
                if stage_name == "steel-handoff"
                else None,
            )
            raw_capabilities = result.get("capabilities")
            capability_names = ("navigate", "click", "observe")
            if not isinstance(raw_capabilities, dict) or set(raw_capabilities) != set(
                capability_names
            ):
                raise ValueError("invalid_agent_result")
            if any(
                not isinstance(raw_capabilities[name], bool)
                for name in capability_names
            ):
                raise ValueError("invalid_agent_result")
            raw_checks = result.get("checks")
            if (
                not isinstance(raw_checks, dict)
                or set(raw_checks) != set(stage_checks)
                or any(not isinstance(raw_checks[name], bool) for name in stage_checks)
            ):
                raise ValueError("invalid_agent_result")
            combined_checks.update(raw_checks)
            for name, count in evidence["tool_calls"].items():
                primary_calls[name] = primary_calls.get(name, 0) + count
            stage_results.append(result)
        report["execution_audit"] = {
            "state": "passed",
            "primary": {
                "state": "verified",
                "delegations": len(stages),
                "tool_calls": {name: primary_calls[name] for name in sorted(primary_calls)},
            },
            "cleanup": cleanup_evidence,
        }
        capability_names = ("navigate", "click", "observe")
        report["capabilities"] = {
            name: all(result["capabilities"][name] for result in stage_results)
            for name in capability_names
        }
        expected = SUITE_CHECKS[args.suite]
        if set(combined_checks) != set(expected):
            raise ValueError("invalid_agent_result")
        report["checks"] = [
            {
                "name": name,
                "state": "passed" if combined_checks.get(name) is True else "failed",
            }
            for name in expected
        ]
        suite_closed = all(result.get("session_closed") is True for result in stage_results)
        all_checks_passed = all(item["state"] == "passed" for item in report["checks"])
        audit_clean = all(cleanup.values())
        passed = (
            all(result.get("target_ready") is True for result in stage_results)
            and all(result.get("safe_error_absent") is True for result in stage_results)
            and report["capabilities"] == {
                "navigate": True,
                "click": True,
                "observe": True,
            }
            and all_checks_passed
            and suite_closed
            and audit_clean
        )
        report["cleanup"] = {
            "state": "clean" if suite_closed and audit_clean else "failed",
            "session_close": "closed" if suite_closed else "failed",
            "fixture": args.fixture_state,
        }
        report["process_audit"] = {
            "state": "passed" if audit_clean else "failed",
            "immediate_reuse": audit_clean,
        }
        if args.suite == "steel-cloud" and combined_checks.get("artifact_retained") is True:
            report["artifacts"] = [{"kind": "screenshot", "state": "retained"}]
        if not passed:
            report["safe_error"] = safe_error(
                "cleanup_failed" if not suite_closed or not audit_clean else "suite_failed"
            )
        return report, passed
    except (FileNotFoundError, json.JSONDecodeError, OSError, TypeError, ValueError) as error:
        code = str(error)
        if code not in {
            "agent_unavailable",
            "input_limit",
            "invalid_agent_result",
            "invalid_execution_evidence",
            "invalid_json",
        }:
            code = "invalid_agent_result"
        report["safe_error"] = safe_error(code)
        return report, False


def write_report(args: argparse.Namespace) -> int:
    report, passed = build_report(args)
    output = pathlib.Path(args.output)
    if output.is_symlink():
        print("browser smoke output must not be a symbolic link", file=sys.stderr)
        return 2
    output.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary_name = tempfile.mkstemp(
        prefix=output.name + ".", suffix=".tmp", dir=output.parent
    )
    temporary = pathlib.Path(temporary_name)
    try:
        stream = os.fdopen(descriptor, "w", encoding="utf-8")
        descriptor = -1
        with stream:
            stream.write(json.dumps(report, indent=2, sort_keys=True) + "\n")
        temporary.replace(output)
    except BaseException:
        if descriptor >= 0:
            os.close(descriptor)
        temporary.unlink(missing_ok=True)
        raise
    return 0 if passed else 1


def parser() -> argparse.ArgumentParser:
    root = argparse.ArgumentParser()
    subcommands = root.add_subparsers(dest="command", required=True)
    serve_parser = subcommands.add_parser("serve")
    serve_parser.add_argument("--bind", required=True)
    serve_parser.add_argument("--advertise-host", required=True)
    serve_parser.add_argument("--ready-file", required=True)
    serve_parser.set_defaults(handler=serve)
    report_parser = subcommands.add_parser("report")
    report_parser.add_argument("--suite", choices=tuple(SUITE_CHECKS), required=True)
    report_parser.add_argument("--target", required=True)
    report_parser.add_argument("--profile", required=True)
    report_parser.add_argument("--live-json", action="append", required=True)
    report_parser.add_argument("--cleanup-json", required=True)
    report_parser.add_argument("--fixture-state", choices=("stopped", "external"), required=True)
    report_parser.add_argument("--started-ns", required=True)
    report_parser.add_argument("--output", required=True)
    report_parser.set_defaults(handler=write_report)
    return root


def main() -> int:
    args = parser().parse_args()
    return int(args.handler(args))


if __name__ == "__main__":
    raise SystemExit(main())
