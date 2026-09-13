#!/bin/sh

set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
test_root=$(mktemp -d "${TMPDIR:-/tmp}/mintclaw-browser-smoke-test.XXXXXX")
trap 'rm -rf -- "$test_root"' EXIT HUP INT TERM

fake="$test_root/mintclaw"
cat >"$fake" <<'EOF'
#!/bin/sh
set -eu
message=""
while [ "$#" -gt 0 ]; do
	case "$1" in
	--message)
		message=$2
		shift 2
		;;
	--timeout|--config)
		shift 2
		;;
	*) shift ;;
	esac
done
if [ -n "${MINTCLAW_BROWSER_SMOKE_FAKE_PID_FILE:-}" ]; then
	printf '%s\n' "$$" >"$MINTCLAW_BROWSER_SMOKE_FAKE_PID_FILE"
fi
if [ "${MINTCLAW_BROWSER_SMOKE_FAKE_HANG:-}" = 1 ]; then
	trap 'exit 0' HUP INT TERM
	while :; do sleep 1; done
fi
if printf '%s' "$message" | grep -Fq 'cleanup audit'; then
	response='{"target_status":"ready","open_state":"ready","initial_url":"about:blank","close_state":"closed","safe_error":null}'
elif printf '%s' "$message" | grep -Fq 'managed-reuse'; then
	response='{"target_status":"ready","capabilities":{"observe":true,"navigate":true,"click":true},"checks":{"first_marker_absent":true,"marker_seeded":true,"marker_reused":true,"marker_cleared":true},"close_states":["closed","closed"],"safe_error":null}'
elif printf '%s' "$message" | grep -Fq 'ephemeral-cleanup'; then
	response='{"target_status":"ready","capabilities":{"observe":true,"navigate":true,"click":true},"checks":{"first_state_clean":true,"cookie_seeded":true,"local_storage_seeded":true,"cache_seeded":true,"service_worker_seeded":true,"cookie_removed":true,"local_storage_removed":true,"cache_removed":true,"service_worker_removed":true},"close_states":["closed","closed"],"safe_error":null}'
else
	if [ "${MINTCLAW_BROWSER_SMOKE_FAKE_FAIL:-}" = 1 ]; then
		response='{"target_status":"ready","capabilities":{"observe":true,"navigate":true,"click":true},"checks":{"initial_blank":true,"navigated_fixture":true,"reversible_action_visible":false,"fresh_observe":true},"close_states":["closed"],"safe_error":null}'
	else
		response='Browser smoke completed.\n{"checks":{"initial_blank":true,"navigated_fixture":true,"reversible_action_visible":true,"fresh_observe":true},"close_states":{"core":"closed"},"safe_error":null}'
	fi
fi
python3 - "$response" <<'PY'
import json
import sys
print(json.dumps({"version": 1, "outcome": "success", "response": sys.argv[1]}))
PY
EOF
chmod +x "$fake"

for suite in core managed-reuse ephemeral-cleanup; do
	output="$test_root/$suite.json"
	MINTCLAW_BROWSER_SMOKE_BINARY="$fake" \
		"$repo_root/scripts/browser-capability-smoke.sh" \
		--target gateway --profile managed --suite "$suite" --json-output "$output"
	python3 - "$output" "$suite" <<'PY'
import json
import pathlib
import sys
report = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
assert report["schema_version"] == "mintclaw.browser_smoke.v1"
assert report["suite"] == sys.argv[2]
assert report["cleanup"] == {"fixture": "stopped", "session_close": "closed", "state": "clean"}
assert report["process_audit"] == {"immediate_reuse": True, "state": "passed"}
assert report["capabilities"] == {"click": True, "navigate": True, "observe": True}
assert report["safe_error"] is None
assert report["artifacts"] == []
assert all(check["state"] == "passed" for check in report["checks"])
PY
done

failed_output="$test_root/failed.json"
if MINTCLAW_BROWSER_SMOKE_FAKE_FAIL=1 MINTCLAW_BROWSER_SMOKE_BINARY="$fake" \
	"$repo_root/scripts/browser-capability-smoke.sh" \
	--target gateway --profile managed --suite core --json-output "$failed_output"; then
	echo "browser smoke self-test: forced failure unexpectedly passed" >&2
	exit 1
fi
grep -Fq '"code": "suite_failed"' "$failed_output"
if grep -Fq "$test_root" "$failed_output"; then
	echo "browser smoke self-test: report leaked a private path" >&2
	exit 1
fi

if MINTCLAW_BROWSER_SMOKE_BINARY="$fake" \
	"$repo_root/scripts/browser-capability-smoke.sh" \
	--target cloud --profile managed --suite core --json-output "$test_root/cloud.json"; then
	echo "browser smoke self-test: billable target passed without opt-in" >&2
	exit 1
fi
if MINTCLAW_BROWSER_SMOKE_BINARY="$fake" \
	"$repo_root/scripts/browser-capability-smoke.sh" \
	--target gateway --profile managed --suite core --json-output "$test_root/origin.json" \
	--fixture-origin 'http://127.0.0.1/injected'; then
	echo "browser smoke self-test: non-origin fixture URL was accepted" >&2
	exit 1
fi

pid_file="$test_root/live.pid"
fixture_pid_file="$test_root/fixture.pid"
signal_output="$test_root/signal.json"
MINTCLAW_BROWSER_SMOKE_FAKE_HANG=1 \
MINTCLAW_BROWSER_SMOKE_FAKE_PID_FILE="$pid_file" \
MINTCLAW_BROWSER_SMOKE_FIXTURE_PID_FILE="$fixture_pid_file" \
MINTCLAW_BROWSER_SMOKE_BINARY="$fake" \
	"$repo_root/scripts/browser-capability-smoke.sh" \
	--target gateway --profile managed --suite core --json-output "$signal_output" &
runner_pid=$!
signal_wait=0
while [ ! -s "$pid_file" ]; do
	if [ "$signal_wait" -ge 100 ]; then
		echo "browser smoke self-test: signal fixture did not start" >&2
		kill "$runner_pid" >/dev/null 2>&1 || true
		exit 1
	fi
	signal_wait=$((signal_wait + 1))
	sleep 0.05
done
live_process=$(cat "$pid_file")
fixture_process=$(cat "$fixture_pid_file")
kill -TERM "$runner_pid"
set +e
wait "$runner_pid"
signal_status=$?
set -e
if [ "$signal_status" -ne 143 ]; then
	echo "browser smoke self-test: signal exit $signal_status, expected 143" >&2
	exit 1
fi
if kill -0 "$live_process" >/dev/null 2>&1; then
	echo "browser smoke self-test: live client survived signal cleanup" >&2
	exit 1
fi
if kill -0 "$fixture_process" >/dev/null 2>&1; then
	echo "browser smoke self-test: fixture survived signal cleanup" >&2
	exit 1
fi

echo "browser capability smoke self-test: passed"
