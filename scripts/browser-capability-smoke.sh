#!/bin/sh

set -eu

usage() {
	cat >&2 <<'EOF'
usage: browser-capability-smoke.sh --target <gateway|companion|cloud> --profile <alias> --suite <core|managed-reuse|ephemeral-cleanup> --json-output <path> [options]

Options:
  --gateway-host <ssh-host>  Run the live client on the gateway over SSH.
                             Use this when the fixture runs on a companion.
  --fixture-origin <origin>  Use an already running safe fixture.
  --fixture-host <host>      Host advertised by the local fixture (default 127.0.0.1).
  --config <path>            Config path on the live-client host. The standard
                             deployed main profile is selected when available.
  --timeout <seconds>        Per-request timeout (default 180).
  --allow-billable           Required for cloud targets.
EOF
}

target=""
profile=""
suite=""
json_output=""
gateway_host=""
fixture_origin=""
fixture_host="127.0.0.1"
config_path="${MINTCLAW_BROWSER_SMOKE_CONFIG:-}"
timeout_seconds=180
allow_billable=false

while [ "$#" -gt 0 ]; do
	case "$1" in
	--target)
		target=${2:?--target requires a value}
		shift 2
		;;
	--profile)
		profile=${2:?--profile requires a value}
		shift 2
		;;
	--suite)
		suite=${2:?--suite requires a value}
		shift 2
		;;
	--json-output)
		json_output=${2:?--json-output requires a value}
		shift 2
		;;
	--gateway-host)
		gateway_host=${2:?--gateway-host requires a value}
		shift 2
		;;
	--fixture-origin)
		fixture_origin=${2:?--fixture-origin requires a value}
		shift 2
		;;
	--fixture-host)
		fixture_host=${2:?--fixture-host requires a value}
		shift 2
		;;
	--config)
		config_path=${2:?--config requires a value}
		shift 2
		;;
	--timeout)
		timeout_seconds=${2:?--timeout requires a value}
		shift 2
		;;
	--allow-billable)
		allow_billable=true
		shift
		;;
	-h|--help)
		usage
		exit 0
		;;
	*)
		usage
		exit 2
		;;
	esac
done

case "$target" in
gateway|companion) ;;
cloud)
	if [ "$allow_billable" != true ]; then
		echo "cloud browser smoke requires --allow-billable" >&2
		exit 2
	fi
	;;
*)
	echo "--target must be gateway, companion, or cloud" >&2
	exit 2
	;;
esac
case "$profile" in
""|*[!a-z0-9_-]*|-*|_*)
	echo "--profile must be a safe lowercase alias" >&2
	exit 2
	;;
esac
if [ "${#profile}" -gt 64 ]; then
	echo "--profile must not exceed 64 characters" >&2
	exit 2
fi
case "$suite" in
core|managed-reuse|ephemeral-cleanup) ;;
*)
	echo "unsupported Phase 0 browser smoke suite" >&2
	exit 2
	;;
esac
if [ -z "$json_output" ]; then
	echo "--json-output is required" >&2
	exit 2
fi
case "$timeout_seconds" in
""|*[!0-9]*)
	echo "--timeout must be a positive integer" >&2
	exit 2
	;;
esac
if [ "$timeout_seconds" -le 0 ] || [ "$timeout_seconds" -gt 900 ]; then
	echo "--timeout must be between 1 and 900 seconds" >&2
	exit 2
fi
if [ -n "$gateway_host" ]; then
	case "$gateway_host" in
	*[!A-Za-z0-9_.@:-]*)
		echo "--gateway-host contains unsupported characters" >&2
		exit 2
		;;
	esac
fi
case "$fixture_host" in
""|*[!A-Za-z0-9._:%-]*)
	echo "--fixture-host must be a hostname or unbracketed IP address" >&2
	exit 2
	;;
esac
if [ "${#fixture_host}" -gt 255 ]; then
	echo "--fixture-host must not exceed 255 characters" >&2
	exit 2
fi
if [ "$target" = cloud ] && [ -z "$fixture_origin" ]; then
	echo "cloud browser smoke requires an explicitly configured public fixture origin" >&2
	exit 2
fi

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
helper="$repo_root/scripts/internal/browser_capability_smoke.py"
python_command=${PYTHON:-python3}
if [ -z "$config_path" ]; then
	if [ -n "$gateway_host" ]; then
		config_path=/home/server/.mintclaw/main/config.json
	elif [ -f /home/server/.mintclaw/main/config.json ]; then
		config_path=/home/server/.mintclaw/main/config.json
	fi
fi
if [ -n "$fixture_origin" ]; then
	if ! fixture_origin=$("$python_command" - "$fixture_origin" <<'PY'
import sys
from urllib.parse import urlsplit

value = sys.argv[1]
parsed = urlsplit(value)
valid = (
    parsed.scheme in {"http", "https"}
    and parsed.hostname is not None
    and parsed.username is None
    and parsed.password is None
    and parsed.path in {"", "/"}
    and not parsed.query
    and not parsed.fragment
    and "\n" not in value
    and "\r" not in value
    and len(value) <= 2048
)
if not valid:
    raise SystemExit(1)
port = f":{parsed.port}" if parsed.port is not None else ""
host = parsed.hostname
if ":" in host:
    host = f"[{host}]"
print(f"{parsed.scheme}://{host}{port}")
PY
	); then
		echo "--fixture-origin must be a bare safe HTTP origin" >&2
		exit 2
	fi
fi
binary=${MINTCLAW_BROWSER_SMOKE_BINARY:-"$repo_root/build/mintclaw"}
remote_binary=${MINTCLAW_BROWSER_SMOKE_REMOTE_BINARY:-/home/server/src/mintclaw/build/mintclaw}
if [ -z "$gateway_host" ] && [ ! -x "$binary" ]; then
	if command -v mintclaw >/dev/null 2>&1; then
		binary=$(command -v mintclaw)
	else
		echo "MintClaw binary is unavailable; build it or set MINTCLAW_BROWSER_SMOKE_BINARY" >&2
		exit 1
	fi
fi

smoke_root=$(mktemp -d "${TMPDIR:-/tmp}/mintclaw-browser-smoke.XXXXXX")
fixture_pid=""
live_pid=""

stop_pid() {
	pid=$1
	if [ -n "$pid" ] && kill -0 "$pid" >/dev/null 2>&1; then
		kill "$pid" >/dev/null 2>&1 || true
		wait "$pid" >/dev/null 2>&1 || true
	fi
}

cleanup() {
	stop_pid "$live_pid"
	live_pid=""
	stop_pid "$fixture_pid"
	fixture_pid=""
	rm -rf -- "$smoke_root"
}

on_signal() {
	trap - EXIT HUP INT TERM
	cleanup
	exit "$1"
}

trap cleanup EXIT
trap 'on_signal 129' HUP
trap 'on_signal 130' INT
trap 'on_signal 143' TERM

if [ -z "$fixture_origin" ]; then
	fixture_ready="$smoke_root/fixture.json"
	"$python_command" "$helper" serve \
		--bind "$fixture_host" \
		--advertise-host "$fixture_host" \
		--ready-file "$fixture_ready" &
	fixture_pid=$!
	if [ -n "${MINTCLAW_BROWSER_SMOKE_FIXTURE_PID_FILE:-}" ]; then
		printf '%s\n' "$fixture_pid" >"$MINTCLAW_BROWSER_SMOKE_FIXTURE_PID_FILE"
	fi
	fixture_wait=0
	while [ ! -s "$fixture_ready" ] && kill -0 "$fixture_pid" >/dev/null 2>&1; do
		if [ "$fixture_wait" -ge 100 ]; then
			echo "browser smoke fixture did not become ready" >&2
			exit 1
		fi
		fixture_wait=$((fixture_wait + 1))
		sleep 0.05
	done
	if [ ! -s "$fixture_ready" ]; then
		echo "browser smoke fixture failed to start" >&2
		exit 1
	fi
	fixture_origin=$("$python_command" -c \
		'import json,sys; print(json.load(open(sys.argv[1], encoding="utf-8"))["origin"])' \
		"$fixture_ready")
fi

case "$suite" in
core)
	checks='initial_blank, navigated_fixture, reversible_action_visible, fresh_observe'
	workflow="Open one session. Observe about:blank. Navigate to ${fixture_origin}/browser-smoke/. Observe it, click the button named Run reversible smoke action with declared_effect=local_edit, and observe fresh state containing CORE_ACTION_OK. Close the session."
	report_template='{"target_status":"ready","capabilities":{"observe":true,"navigate":true,"click":true},"checks":{"initial_blank":true,"navigated_fixture":true,"reversible_action_visible":true,"fresh_observe":true},"close_states":["closed"],"safe_error":null}'
	;;
managed-reuse)
	checks='first_marker_absent, marker_seeded, marker_reused, marker_cleared'
	workflow="Open a first session and navigate to ${fixture_origin}/browser-smoke/check. If the status is still SMOKE_LOADING, observe again, at most twice. Click the button named Clear browser smoke state with declared_effect=local_edit, navigate back to the check URL, and observe local_storage=false. Click the button named Set managed smoke marker with declared_effect=local_edit. Navigate back to the check URL and observe local_storage=true. Close it. Open a second session with the same target and profile, navigate to the same check URL, observe local_storage=true, click Clear browser smoke state with declared_effect=local_edit, navigate back to the check URL, observe local_storage=false, and close it."
	report_template='{"target_status":"ready","capabilities":{"observe":true,"navigate":true,"click":true},"checks":{"first_marker_absent":true,"marker_seeded":true,"marker_reused":true,"marker_cleared":true},"close_states":["closed","closed"],"safe_error":null}'
	;;
ephemeral-cleanup)
	checks='first_state_clean, cookie_seeded, local_storage_seeded, cache_seeded, service_worker_seeded, cookie_removed, local_storage_removed, cache_removed, service_worker_removed'
	workflow="Open a first session, navigate to ${fixture_origin}/browser-smoke/check, and observe all four state flags false. If the status is still SMOKE_LOADING, observe again, at most twice. Click the button named Seed ephemeral smoke state with declared_effect=local_edit. Navigate to ${fixture_origin}/browser-smoke/check and observe cookie=true, local_storage=true, cache=true, and service_worker=true. Close it. Open a second session with the same target and profile, navigate to the same check URL, observe all four flags false, and close it."
	report_template='{"target_status":"ready","capabilities":{"observe":true,"navigate":true,"click":true},"checks":{"first_state_clean":true,"cookie_seeded":true,"local_storage_seeded":true,"cache_seeded":true,"service_worker_seeded":true,"cookie_removed":true,"local_storage_removed":true,"cache_removed":true,"service_worker_removed":true},"close_states":["closed","closed"],"safe_error":null}'
	;;
esac

prompt=$(cat <<EOF
Delegate exactly once to the browser agent with delivery_mode=user_only. Use only first-party browser tools.

Run the deterministic ${suite} browser smoke on exact target ${target} and exact profile ${profile}. First call browser_targets and verify that exact target/profile is ready and advertises navigate and click. Prove observe capability by successfully using browser_observe. This fixture is local, harmless, and reversible; do not use search, raw MCP, browser code execution, or any other target/profile. ${workflow}

Return only one JSON object with exactly these keys:
${report_template}
Use this exact suite-specific shape. The checks object must contain exactly these boolean keys: ${checks}. If anything fails, still close every opened session, change only the relevant values to safe failure values, and return one bounded safe_error object. When calling delegate, use exactly one result objective for this complete JSON report rather than separate workflow and report objectives.
EOF
)

cleanup_prompt=$(cat <<EOF
Delegate exactly once to the browser agent with delivery_mode=user_only. Use only first-party browser tools.

Run a cleanup audit on exact target ${target} and exact profile ${profile}. Call browser_targets, open one session, observe the initial page without navigation, and close it. This probe must not change any page or retained state.

Return only JSON with exactly: {"target_status":"ready-or-safe-status","open_state":"ready-or-safe-state","initial_url":"about:blank-or-null","close_state":"closed-or-safe-state","safe_error":null}
EOF
)

run_live() {
	request=$1
	output=$2
	request_b64=$(printf '%s' "$request" | base64 | tr -d '\n')
	if [ -n "$gateway_host" ]; then
		remote_config=$config_path
		if [ -z "$remote_config" ]; then
			remote_config=-
		fi
		ssh -o BatchMode=yes -o ConnectTimeout=5 "$gateway_host" sh -s -- \
			"$remote_binary" "$timeout_seconds" "$request_b64" "$remote_config" >"$output" 2>"$smoke_root/live.stderr" <<'REMOTE' &
set -eu
binary=$1
timeout_seconds=$2
request_b64=$3
config_path=$4
request=$(printf '%s' "$request_b64" | base64 -d)
if [ "$config_path" != - ]; then
	exec "$binary" agent live --json --timeout "${timeout_seconds}s" --config "$config_path" --message "$request"
fi
exec "$binary" agent live --json --timeout "${timeout_seconds}s" --message "$request"
REMOTE
	else
		if [ -n "$config_path" ]; then
			"$binary" agent live --json --timeout "${timeout_seconds}s" \
				--config "$config_path" --message "$request" >"$output" 2>"$smoke_root/live.stderr" &
		else
			"$binary" agent live --json --timeout "${timeout_seconds}s" \
				--message "$request" >"$output" 2>"$smoke_root/live.stderr" &
		fi
	fi
	live_pid=$!
	set +e
	wait "$live_pid"
	request_status=$?
	set -e
	live_pid=""
	return "$request_status"
}

started_ns=$("$python_command" -c 'import time; print(time.time_ns())')
live_json="$smoke_root/live.json"
cleanup_json="$smoke_root/cleanup.json"
run_live "$prompt" "$live_json" || true
run_live "$cleanup_prompt" "$cleanup_json" || true

stop_pid "$fixture_pid"
fixture_pid=""

"$python_command" "$helper" report \
	--suite "$suite" \
	--target "$target" \
	--profile "$profile" \
	--live-json "$live_json" \
	--cleanup-json "$cleanup_json" \
	--started-ns "$started_ns" \
	--output "$json_output"
