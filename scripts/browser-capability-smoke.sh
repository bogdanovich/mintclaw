#!/bin/sh

set -eu

usage() {
	cat >&2 <<'EOF'
usage: browser-capability-smoke.sh --target <gateway|companion|cloud> --profile <alias> --suite <core|managed-reuse|ephemeral-cleanup|driver-conformance|provider-lifecycle|playwright-library|privileged-execute> --json-output <path> [options]

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
core|managed-reuse|ephemeral-cleanup|driver-conformance|provider-lifecycle|playwright-library|privileged-execute) ;;
*)
	echo "unsupported browser smoke suite" >&2
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
	if [ -z "$pid" ]; then
		return
	fi
	if kill -0 "$pid" >/dev/null 2>&1; then
		kill -TERM "$pid" >/dev/null 2>&1 || true
		stop_wait=0
		while kill -0 "$pid" >/dev/null 2>&1 && [ "$stop_wait" -lt 50 ]; do
			stop_wait=$((stop_wait + 1))
			sleep 0.1
		done
		if kill -0 "$pid" >/dev/null 2>&1; then
			kill -KILL "$pid" >/dev/null 2>&1 || true
		fi
	fi
	wait "$pid" >/dev/null 2>&1 || true
}

wait_pid() {
	pid=$1
	maximum_seconds=$2
	waited_seconds=0
	while kill -0 "$pid" >/dev/null 2>&1; do
		if [ "$waited_seconds" -ge "$maximum_seconds" ]; then
			return 124
		fi
		sleep 1
		waited_seconds=$((waited_seconds + 1))
	done
	wait "$pid"
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

fixture_state=external
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
	fixture_state=stopped
fi

case "$suite" in
core)
	stage_one=core
	stage_one_checks='initial_blank, navigated_fixture, reversible_action_visible, fresh_observe'
	stage_one_workflow="Open one session. Observe about:blank. Navigate to ${fixture_origin}/browser-smoke/. Observe it, click the button named Run reversible smoke action with declared_effect=local_edit, and observe fresh state containing CORE_ACTION_OK. Close the session."
	stage_two=""
	;;
managed-reuse)
	stage_one=managed-seed
	stage_one_checks='first_marker_absent, marker_seeded'
	stage_one_workflow="Open the first session and call browser_observe to verify about:blank. Navigate to ${fixture_origin}/browser-smoke/check and call browser_observe on the untouched state. If the status is still SMOKE_LOADING, observe again, at most twice. Record first_marker_absent before any clear action: it is true only when local_storage=false. If local_storage=true, click Clear browser smoke state with declared_effect=local_edit and call browser_observe until it shows local_storage=false without changing first_marker_absent. Click Set managed smoke marker with declared_effect=local_edit and call browser_observe until it shows local_storage=true. Close this session. Do not open the verification session in this stage."
	stage_two=managed-verify
	stage_two_checks='marker_reused, marker_cleared'
	stage_two_workflow="Open the verification session with the same target and profile and call browser_observe to verify about:blank. Navigate to ${fixture_origin}/browser-smoke/check and call browser_observe until it shows local_storage=true. Click Clear browser smoke state with declared_effect=local_edit, call browser_observe until it shows local_storage=false, and close this session."
	;;
ephemeral-cleanup)
	stage_one=ephemeral-seed
	stage_one_checks='first_state_clean, cookie_seeded, local_storage_seeded, cache_seeded, service_worker_seeded'
	stage_one_workflow="Open the first session and call browser_observe to verify about:blank. Navigate to ${fixture_origin}/browser-smoke/check and call browser_observe until it shows cookie=false, local_storage=false, cache=false, and service_worker=false. If the status is still SMOKE_LOADING, observe again, at most twice. Click Seed ephemeral smoke state with declared_effect=local_edit and call browser_observe until it shows cookie=true, local_storage=true, cache=true, and service_worker=true. Close this session. Do not open the verification session in this stage."
	stage_two=ephemeral-verify
	stage_two_checks='cookie_removed, local_storage_removed, cache_removed, service_worker_removed'
	stage_two_workflow="Open the verification session with the same target and profile and call browser_observe to verify about:blank. Navigate to ${fixture_origin}/browser-smoke/check, call browser_observe until it shows cookie=false, local_storage=false, cache=false, and service_worker=false, and close this session."
	;;
driver-conformance)
	stage_one=driver-conformance
	stage_one_checks='initial_blank, navigated_fixture, reversible_action_visible, fresh_observe'
	stage_one_workflow="Open one session. Observe about:blank. Navigate to ${fixture_origin}/browser-smoke/. Observe it, click the button named Run reversible smoke action with declared_effect=local_edit, and observe fresh state containing CORE_ACTION_OK. Close the session."
	stage_two=""
	;;
playwright-library)
	stage_one=playwright-library
	stage_one_checks='initial_blank, navigated_fixture, reversible_action_visible, fresh_observe'
	stage_one_workflow="Open one session. Observe about:blank. Navigate to ${fixture_origin}/browser-smoke/. Observe it, click the button named Run reversible smoke action with declared_effect=local_edit, and observe fresh state containing CORE_ACTION_OK. Close the session."
	stage_two=""
	;;
privileged-execute)
	stage_one=privileged-execute
	stage_one_checks='initial_blank, navigated_fixture, structured_extraction, reversible_dom_restored, artifact_retained, sandbox_denial, runtime_timeout, cleanup_after_timeout'
	stage_one_workflow=$(cat <<EOF
Open one session and observe about:blank. Navigate with browser_act to ${fixture_origin}/browser-smoke/ and observe the fixture. Then make exactly three browser_execute calls, always copying authority only from the latest fresh result.

First run JavaScript with effect=external_commit. Copy only the text between the source delimiters into the source argument; exclude both delimiter lines:
BEGIN_BROWSER_EXECUTE_SOURCE_1
async ({page, artifacts}) => { const title = await page.title(); const before = await page.locator('#status').innerText(); await page.locator('#status').evaluate("(element) => element.setAttribute('data-mintclaw-execute', 'during')"); const during = await page.locator('#status').getAttribute('data-mintclaw-execute'); await page.locator('#status').evaluate("(element) => element.removeAttribute('data-mintclaw-execute')"); const restored = await page.locator('#status').getAttribute('data-mintclaw-execute'); const screenshot = await artifacts.screenshot({fullPage:false}); return {title, before, during, restored, screenshot}; }
END_BROWSER_EXECUTE_SOURCE_1
Require the returned title to be MintClaw browser smoke fixture, before to be present, during to equal during, restored to be null, one retained PNG artifact to be present, and the returned screenshot reference to correspond to it.

Second run JavaScript with effect=read. Copy only the text between the source delimiters into the source argument; exclude both delimiter lines:
BEGIN_BROWSER_EXECUTE_SOURCE_2
async () => { let denied = false; try { void process.env; } catch { denied = true; } return {denied}; }
END_BROWSER_EXECUTE_SOURCE_2
Require a succeeded result with denied=true.

Third run JavaScript with effect=read. Copy only the text between the source delimiters into the source argument; exclude both delimiter lines:
BEGIN_BROWSER_EXECUTE_SOURCE_3
async () => await new Promise(() => {})
END_BROWSER_EXECUTE_SOURCE_3
Require this call to terminate with the runtime timeout safe failure; count this expected tool failure as runtime_timeout=true, and do not retry it. Close the session after the timeout. The timeout deliberately quarantines the accepted session, so treat either closed or lost as terminal cleanup: set cleanup_after_timeout and session_closed to true only when no live session remains. The independent follow-up audit will also prove immediate reuse.
EOF
	)
	stage_two=""
	;;
provider-lifecycle)
	stage_one=provider-open-one
	stage_one_checks='first_open_ready, first_observe_ready, first_close_clean'
	stage_one_workflow="Open one session, observe about:blank, and close the session. Set first_open_ready, first_observe_ready, and first_close_clean from those exact results."
	stage_two=provider-open-two
	stage_two_checks='second_open_ready, second_observe_ready, second_close_clean'
	stage_two_workflow="Immediately open a new session with the same target and profile, observe about:blank, and close the session. Set second_open_ready, second_observe_ready, and second_close_clean from those exact results."
	;;
esac

stage_action_guidance='For every navigate call, use an action object containing only "kind":"navigate" and "url": the exact fixture URL; do not include "target" or any unrelated action field. For every browser_act call, copy authority fields only from the latest successful browser_observe or browser_contexts result. Never invent an ID, generation, reference, token, or placeholder value. If browser_observe omits context_catalog_id and context_generation, omit both fields unless a fresh browser_contexts list result supplies both.'
stage_execution_guidance='Do not use search, raw MCP, browser code execution, or any other target/profile.'
if [ "$suite" = provider-lifecycle ]; then
	stage_action_guidance='Do not call browser_act or navigate in this stage. Do not use or invent a fixture URL. The lifecycle probe must remain on about:blank. Never invent an ID, generation, reference, token, or placeholder value.'
fi
if [ "$suite" = privileged-execute ]; then
	stage_execution_guidance='Do not use search, raw MCP, or any code-execution mechanism other than the three exact browser_execute calls required by this stage. Do not alter, combine, retry, or add source.'
fi

make_stage_prompt() {
	stage_name=$1
	stage_workflow=$2
	stage_checks=$3
	cat <<EOF
Call the tool named delegate as the first and only tool call in this turn, exactly once, for the browser agent with delivery_mode=user_only, and wait for its terminal result. Do not call tool_search_tool_bm25, spawn, task_status, stop, or any other tool. The delegated browser agent must use only first-party browser tools.

Run stage ${stage_name} of the deterministic ${suite} browser smoke on exact target ${target} and exact profile ${profile}. First call browser_targets and verify that exact target/profile is ready and advertises navigate and click; for privileged-execute it must also advertise privileged_execution. Prove observe capability through the required stage workflow; do not open a separate session or run a separate capability probe. You may call browser_contexts only with operation=list when needed for read-only page introspection. ${stage_action_guidance} ${stage_execution_guidance} Complete every step in this stage before returning. ${stage_workflow}

When calling delegate, set objective_items to exactly one result objective with acceptance output_kind=records, min_items=1, and required_fields exactly: target_ready, capability_observe, capability_navigate, capability_click, ${stage_checks}, session_closed, safe_error_absent. Set the objective item to exactly: Return one record of browser smoke predicates using string true or false values only. Require the child to return exactly one record with exactly those fields and use only the strings true or false for every value. On failure, still close every opened session and set each predicate from the actual terminal state. Do not ask for JSON text and do not add separate workflow or report objectives.
EOF
}

prompt_one=$(make_stage_prompt "$stage_one" "$stage_one_workflow" "$stage_one_checks")
prompt_two=""
if [ -n "$stage_two" ]; then
	prompt_two=$(make_stage_prompt "$stage_two" "$stage_two_workflow" "$stage_two_checks")
fi

cleanup_prompt=$(cat <<EOF
Call the tool named delegate as the first and only tool call in this turn, exactly once, for the browser agent with delivery_mode=user_only, and wait for its terminal result. Do not call tool_search_tool_bm25, spawn, task_status, stop, or any other tool. The delegated browser agent must use only first-party browser tools.

Run a cleanup audit on exact target ${target} and exact profile ${profile}. Call browser_targets, open one session, observe the initial page without navigation, and close it. You may call browser_contexts only when needed for read-only page introspection. This probe must not change any page or retained state.

When calling delegate, set objective_items to exactly one result objective with acceptance output_kind=records, min_items=1, and required_fields exactly: target_ready, open_ready, initial_blank, session_closed, safe_error_absent. Set the objective item to exactly: Return one record of browser cleanup predicates using string true or false values only. Require the child to return exactly one record with exactly those fields and use only the strings true or false for every value. On failure, still close every opened session and set each predicate from the actual terminal state. Do not ask for JSON text and do not add separate objectives.
EOF
)

run_live() {
	request=$1
	output=$2
	request_b64=$(printf '%s' "$request" | base64 | tr -d '\n')
	if [ -n "$gateway_host" ]; then
		remote_binary_b64=$(printf '%s' "$remote_binary" | base64 | tr -d '\n')
		remote_config=$config_path
		if [ -z "$remote_config" ]; then
			remote_config_b64=-
		else
			remote_config_b64=$(printf '%s' "$remote_config" | base64 | tr -d '\n')
		fi
		ssh -o BatchMode=yes -o ConnectTimeout=5 "$gateway_host" sh -s -- \
			"$remote_binary_b64" "$timeout_seconds" "$request_b64" "$remote_config_b64" >"$output" 2>"$smoke_root/live.stderr" <<'REMOTE' &
set -eu
binary=$(printf '%s' "$1" | base64 -d)
timeout_seconds=$2
request_b64=$3
request=$(printf '%s' "$request_b64" | base64 -d)
config_b64=$4
if [ "$config_b64" != - ]; then
	config_path=$(printf '%s' "$config_b64" | base64 -d)
		exec "$binary" agent live --json --trace-evidence-agent browser --timeout "${timeout_seconds}s" --config "$config_path" --message "$request"
	fi
	exec "$binary" agent live --json --trace-evidence-agent browser --timeout "${timeout_seconds}s" --message "$request"
REMOTE
	else
		if [ -n "$config_path" ]; then
			"$binary" agent live --json --trace-evidence-agent browser --timeout "${timeout_seconds}s" \
				--config "$config_path" --message "$request" >"$output" 2>"$smoke_root/live.stderr" &
		else
			"$binary" agent live --json --trace-evidence-agent browser --timeout "${timeout_seconds}s" \
				--message "$request" >"$output" 2>"$smoke_root/live.stderr" &
		fi
	fi
	live_pid=$!
	set +e
	run_live_deadline=$((timeout_seconds + 5))
	wait_pid "$live_pid" "$run_live_deadline"
	request_status=$?
	set -e
	if [ "$request_status" -eq 124 ]; then
		stop_pid "$live_pid"
	fi
	live_pid=""
	return "$request_status"
}

started_ns=$("$python_command" -c 'import time; print(time.time_ns())')
live_one_json="$smoke_root/live-one.json"
live_two_json="$smoke_root/live-two.json"
cleanup_json="$smoke_root/cleanup.json"
run_live "$prompt_one" "$live_one_json" || true
if [ -n "$prompt_two" ]; then
	run_live "$prompt_two" "$live_two_json" || true
fi
run_live "$cleanup_prompt" "$cleanup_json" || true

stop_pid "$fixture_pid"
fixture_pid=""

set -- "$helper" report \
	--suite "$suite" \
	--target "$target" \
	--profile "$profile" \
	--live-json "$live_one_json"
if [ -n "$prompt_two" ]; then
	set -- "$@" --live-json "$live_two_json"
fi
set -- "$@" \
	--cleanup-json "$cleanup_json" \
	--fixture-state "$fixture_state" \
	--started-ns "$started_ns" \
	--output "$json_output"
"$python_command" "$@"
