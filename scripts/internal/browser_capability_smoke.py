#!/usr/bin/env python3

"""Private fixture and report helpers for browser-capability-smoke.sh."""

from __future__ import annotations

import argparse
import http.server
import json
import pathlib
import sys
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


def response_object(outer: dict[str, Any]) -> dict[str, Any]:
    if outer.get("outcome") != "success":
        raise ValueError("agent_unavailable")
    response = outer.get("response")
    if not isinstance(response, str) or len(response.encode("utf-8")) > MAX_INPUT_BYTES:
        raise ValueError("invalid_agent_result")
    stripped = response.strip()
    if stripped.startswith("```") and stripped.endswith("```"):
        lines = stripped.splitlines()
        if len(lines) >= 3:
            stripped = "\n".join(lines[1:-1])
    value = json.loads(stripped)
    if not isinstance(value, dict):
        raise ValueError("invalid_agent_result")
    return value


def safe_error(code: str) -> dict[str, str]:
    messages = {
        "agent_unavailable": "The live browser smoke request did not complete.",
        "cleanup_failed": "The browser smoke cleanup audit did not pass.",
        "input_limit": "The browser smoke result exceeded its input limit.",
        "invalid_agent_result": "The browser smoke result was not valid structured JSON.",
        "invalid_json": "The browser smoke input was not valid JSON.",
        "suite_failed": "One or more browser smoke checks did not pass.",
    }
    return {"code": code, "message": messages.get(code, "The browser smoke failed.")}


def build_report(args: argparse.Namespace) -> tuple[dict[str, Any], bool]:
    started_ns = int(args.started_ns)
    report: dict[str, Any] = {
        "schema_version": SCHEMA_VERSION,
        "suite": args.suite,
        "target": args.target,
        "profile": args.profile,
        "capabilities": {},
        "checks": [],
        "cleanup": {"state": "failed", "session_close": "unknown", "fixture": "stopped"},
        "process_audit": {"state": "failed", "immediate_reuse": False},
        "artifacts": [],
        "duration_ms": max(0, (time.time_ns() - started_ns) // 1_000_000),
        "safe_error": None,
    }
    try:
        result = response_object(load_json(args.live_json))
        cleanup = response_object(load_json(args.cleanup_json))
        raw_capabilities = result.get("capabilities")
        if isinstance(raw_capabilities, dict):
            report["capabilities"] = {
                name: bool(raw_capabilities.get(name))
                for name in ("navigate", "click", "observe")
            }
        raw_checks = result.get("checks")
        if not isinstance(raw_checks, dict):
            raise ValueError("invalid_agent_result")
        expected = SUITE_CHECKS[args.suite]
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
            and all(item["state"] == "passed" for item in report["checks"])
            and suite_closed
            and audit_clean
        )
        report["cleanup"] = {
            "state": "clean" if suite_closed and audit_clean else "failed",
            "session_close": "closed" if suite_closed else "failed",
            "fixture": "stopped",
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
    temporary = output.with_name(output.name + ".tmp")
    temporary.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    temporary.replace(output)
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
    report_parser.add_argument("--started-ns", required=True)
    report_parser.add_argument("--output", required=True)
    report_parser.set_defaults(handler=write_report)
    return root


def main() -> int:
    args = parser().parse_args()
    return int(args.handler(args))


if __name__ == "__main__":
    raise SystemExit(main())
