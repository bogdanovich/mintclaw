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


def response_object(outer: dict[str, Any], required_key: str) -> dict[str, Any]:
    if outer.get("outcome") != "success":
        raise ValueError("agent_unavailable")
    response = outer.get("response")
    if not isinstance(response, str) or len(response.encode("utf-8")) > MAX_INPUT_BYTES:
        raise ValueError("invalid_agent_result")
    decoder = json.JSONDecoder()
    candidates: list[dict[str, Any]] = []
    for index, character in enumerate(response):
        if character != "{":
            continue
        try:
            value, _ = decoder.raw_decode(response[index:])
        except json.JSONDecodeError:
            continue
        if isinstance(value, dict) and required_key in value:
            candidates.append(value)
    if not candidates:
        raise ValueError("invalid_agent_result")
    return candidates[-1]


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
    outer: dict[str, Any], suite: str, target: str, profile: str, cleanup: bool
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
        or child.get("tool_failures") != {}
        or child.get("unpaired_calls") != {}
    ):
        raise ValueError("invalid_execution_evidence")
    calls = child.get("tool_calls")
    if not isinstance(calls, dict) or set(calls).difference(
        {"browser_targets", "browser_session", "browser_observe", "browser_act"}
    ):
        raise ValueError("invalid_execution_evidence")
    minimums = {
        "core": {"browser_targets": 1, "browser_session": 2, "browser_observe": 3, "browser_act": 2},
        "managed-reuse": {"browser_targets": 1, "browser_session": 4, "browser_observe": 4, "browser_act": 6},
        "ephemeral-cleanup": {"browser_targets": 1, "browser_session": 4, "browser_observe": 3, "browser_act": 4},
    }
    required = (
        {"browser_targets": 1, "browser_session": 2, "browser_observe": 1}
        if cleanup
        else minimums[suite]
    )
    if any(
        not isinstance(count, int) or isinstance(count, bool) or count < 0
        for count in calls.values()
    ) or any(calls.get(name, 0) < count for name, count in required.items()):
        raise ValueError("invalid_execution_evidence")
    if cleanup and calls.get("browser_act", 0) != 0:
        raise ValueError("invalid_execution_evidence")
    sessions = child.get("browser_sessions")
    expected_sessions = 1 if cleanup or suite == "core" else 2
    if not isinstance(sessions, list) or len(sessions) != expected_sessions * 2:
        raise ValueError("invalid_execution_evidence")
    for index, session in enumerate(sessions):
        if not isinstance(session, dict):
            raise ValueError("invalid_execution_evidence")
        if index % 2 == 0:
            if session != {"operation": "open", "target": target, "profile": profile}:
                raise ValueError("invalid_execution_evidence")
        elif session != {"operation": "close"}:
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
        live_outer = load_json(args.live_json)
        cleanup_outer = load_json(args.cleanup_json)
        result = response_object(live_outer, "checks")
        cleanup = response_object(cleanup_outer, "target_status")
        primary_evidence = verify_execution_evidence(
            live_outer, args.suite, args.target, args.profile, cleanup=False
        )
        cleanup_evidence = verify_execution_evidence(
            cleanup_outer, args.suite, args.target, args.profile, cleanup=True
        )
        report["execution_audit"] = {
            "state": "passed",
            "primary": primary_evidence,
            "cleanup": cleanup_evidence,
        }
        if set(result) != {
            "target_status",
            "capabilities",
            "checks",
            "close_states",
            "safe_error",
        }:
            raise ValueError("invalid_agent_result")
        if set(cleanup) != {
            "target_status",
            "open_state",
            "initial_url",
            "close_state",
            "safe_error",
        }:
            raise ValueError("invalid_agent_result")
        raw_capabilities = result.get("capabilities")
        capability_names = ("navigate", "click", "observe")
        if not isinstance(raw_capabilities, dict) or set(raw_capabilities) != set(
            capability_names
        ):
            raise ValueError("invalid_agent_result")
        if any(not isinstance(raw_capabilities[name], bool) for name in capability_names):
            raise ValueError("invalid_agent_result")
        report["capabilities"] = {
            name: raw_capabilities[name] for name in capability_names
        }
        raw_checks = result.get("checks")
        if not isinstance(raw_checks, dict):
            raise ValueError("invalid_agent_result")
        expected = SUITE_CHECKS[args.suite]
        if set(raw_checks) != set(expected) or any(
            not isinstance(raw_checks[name], bool) for name in expected
        ):
            raise ValueError("invalid_agent_result")
        report["checks"] = [
            {"name": name, "state": "passed" if raw_checks.get(name) is True else "failed"}
            for name in expected
        ]
        close_states = result.get("close_states")
        expected_closes = 1 if args.suite == "core" else 2
        suite_closed = (
            isinstance(close_states, list)
            and len(close_states) == expected_closes
            and all(state == "closed" for state in close_states)
        )
        all_checks_passed = all(item["state"] == "passed" for item in report["checks"])
        audit_clean = (
            cleanup.get("target_status") == "ready"
            and cleanup.get("open_state") == "ready"
            and cleanup.get("initial_url") == "about:blank"
            and cleanup.get("close_state") == "closed"
            and cleanup.get("safe_error") is None
        )
        passed = (
            result.get("target_status") == "ready"
            and result.get("safe_error") is None
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
    report_parser.add_argument("--live-json", required=True)
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
