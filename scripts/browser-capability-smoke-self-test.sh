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
is_cleanup=false
stage=core
if printf '%s' "$message" | grep -Fq 'cleanup audit'; then
	is_cleanup=true
fi
if ! printf '%s' "$message" | grep -Fq 'delegate as the first and only tool call in this turn, exactly once' ||
	! printf '%s' "$message" | grep -Fq 'Do not call tool_search_tool_bm25, spawn, task_status, stop, or any other tool.' ||
	! printf '%s' "$message" | grep -Fq 'acceptance output_kind=records, min_items=1' ||
	! printf '%s' "$message" | grep -Fq 'using string true or false values only'; then
	echo "browser smoke prompt did not require synchronous delegation" >&2
	exit 1
fi
if [ "$is_cleanup" = false ] &&
	! printf '%s' "$message" | grep -Fq 'Prove observe capability through the required stage workflow; do not open a separate session or run a separate capability probe.'; then
	echo "browser smoke stage prompt allowed a separate capability probe" >&2
	exit 1
fi
is_provider_lifecycle=false
if printf '%s' "$message" | grep -Eq 'stage provider-open-(one|two)'; then
	is_provider_lifecycle=true
fi
if [ "$is_cleanup" = false ] && [ "$is_provider_lifecycle" = true ] && {
	! printf '%s' "$message" | grep -Fq 'Do not call browser_act or navigate in this stage.' ||
	! printf '%s' "$message" | grep -Fq 'The lifecycle probe must remain on about:blank.' ||
	printf '%s' "$message" | grep -Fq 'For every navigate call';
}; then
	echo "provider lifecycle smoke prompt allowed an invented navigation" >&2
	exit 1
fi
if [ "$is_cleanup" = false ] && [ "$is_provider_lifecycle" = false ] && {
	! printf '%s' "$message" | grep -Fq 'For every navigate call, use an action object containing only "kind":"navigate" and "url": the exact fixture URL;' ||
	! printf '%s' "$message" | grep -Fq 'do not include "target" or any unrelated action field.' ||
	! printf '%s' "$message" | grep -Fq 'copy authority fields only from the latest successful' ||
	! printf '%s' "$message" | grep -Fq 'Never invent an ID, generation, reference, token, or placeholder value.' ||
	! printf '%s' "$message" | grep -Fq 'omit both fields unless a fresh browser_contexts list result supplies both.';
}; then
	echo "browser smoke prompt did not require exact navigation shape and fresh non-invented action authority" >&2
	exit 1
fi
if printf '%s' "$message" | grep -Fq 'stage managed-seed'; then
	stage=managed-seed
elif printf '%s' "$message" | grep -Fq 'stage managed-verify'; then
	stage=managed-verify
elif printf '%s' "$message" | grep -Fq 'stage ephemeral-seed'; then
	stage=ephemeral-seed
elif printf '%s' "$message" | grep -Fq 'stage ephemeral-verify'; then
	stage=ephemeral-verify
elif printf '%s' "$message" | grep -Eq 'stage (driver-conformance|playwright-library)'; then
	if printf '%s' "$message" | grep -Fq 'stage playwright-library'; then
		stage=playwright-library
	else
		stage=driver-conformance
	fi
elif printf '%s' "$message" | grep -Fq 'stage provider-open-one'; then
	stage=provider-open-one
elif printf '%s' "$message" | grep -Fq 'stage provider-open-two'; then
	stage=provider-open-two
fi
if [ -n "${MINTCLAW_BROWSER_SMOKE_FAKE_PID_FILE:-}" ]; then
	printf '%s\n' "$$" >>"$MINTCLAW_BROWSER_SMOKE_FAKE_PID_FILE"
fi
if [ "${MINTCLAW_BROWSER_SMOKE_FAKE_HANG:-}" = 1 ] ||
	{ [ "${MINTCLAW_BROWSER_SMOKE_FAKE_HANG_PRIMARY:-}" = 1 ] && [ "$is_cleanup" = false ]; }; then
	if [ "${MINTCLAW_BROWSER_SMOKE_FAKE_IGNORE_TERM:-}" = 1 ]; then
		trap '' HUP INT TERM
	else
		trap 'exit 0' HUP INT TERM
	fi
	while :; do sleep 1; done
fi
if [ "$is_cleanup" = true ]; then
	record='{"target_ready":"true","open_ready":"true","initial_blank":"true","session_closed":"true","safe_error_absent":"true"}'
elif [ "$stage" = managed-seed ]; then
	if ! printf '%s' "$message" | grep -Fq 'untouched state'; then
		echo "managed smoke prompt did not preserve initial-state ordering" >&2
		exit 1
	fi
	if [ "${MINTCLAW_BROWSER_SMOKE_FAKE_FAIL_FIRST_STAGE:-}" = 1 ]; then
		record='{"target_ready":"true","capability_observe":"true","capability_navigate":"true","capability_click":"true","first_marker_absent":"true","marker_seeded":"false","session_closed":"true","safe_error_absent":"true"}'
	else
		record='{"target_ready":"true","capability_observe":"true","capability_navigate":"true","capability_click":"true","first_marker_absent":"true","marker_seeded":"true","session_closed":"true","safe_error_absent":"true"}'
	fi
elif [ "$stage" = managed-verify ]; then
	record='{"target_ready":"true","capability_observe":"true","capability_navigate":"true","capability_click":"true","marker_reused":"true","marker_cleared":"true","session_closed":"true","safe_error_absent":"true"}'
elif [ "$stage" = ephemeral-seed ]; then
	record='{"target_ready":"true","capability_observe":"true","capability_navigate":"true","capability_click":"true","first_state_clean":"true","cookie_seeded":"true","local_storage_seeded":"true","cache_seeded":"true","service_worker_seeded":"true","session_closed":"true","safe_error_absent":"true"}'
elif [ "$stage" = ephemeral-verify ]; then
	record='{"target_ready":"true","capability_observe":"true","capability_navigate":"true","capability_click":"true","cookie_removed":"true","local_storage_removed":"true","cache_removed":"true","service_worker_removed":"true","session_closed":"true","safe_error_absent":"true"}'
elif [ "$stage" = provider-open-one ]; then
	record='{"target_ready":"true","capability_observe":"true","capability_navigate":"true","capability_click":"true","first_open_ready":"true","first_observe_ready":"true","first_close_clean":"true","session_closed":"true","safe_error_absent":"true"}'
elif [ "$stage" = provider-open-two ]; then
	record='{"target_ready":"true","capability_observe":"true","capability_navigate":"true","capability_click":"true","second_open_ready":"true","second_observe_ready":"true","second_close_clean":"true","session_closed":"true","safe_error_absent":"true"}'
else
	if [ "${MINTCLAW_BROWSER_SMOKE_FAKE_FAIL:-}" = 1 ]; then
		record='{"target_ready":"true","capability_observe":"true","capability_navigate":"true","capability_click":"true","initial_blank":"true","navigated_fixture":"true","reversible_action_visible":"false","fresh_observe":"true","session_closed":"true","safe_error_absent":"true"}'
	elif [ "${MINTCLAW_BROWSER_SMOKE_FAKE_BAD_CAPABILITIES:-}" = 1 ]; then
		record='{"target_ready":"true","capability_observe":"invalid","capability_navigate":"false","capability_click":"false","initial_blank":"true","navigated_fixture":"true","reversible_action_visible":"true","fresh_observe":"true","session_closed":"true","safe_error_absent":"true"}'
	else
		record='{"target_ready":"true","capability_observe":"true","capability_navigate":"true","capability_click":"true","initial_blank":"true","navigated_fixture":"true","reversible_action_visible":"true","fresh_observe":"true","session_closed":"true","safe_error_absent":"true"}'
	fi
fi
python3 - "$record" "$is_cleanup" "$stage" <<'PY'
import json
import os
import sys
record = json.loads(sys.argv[1])
if os.environ.get("MINTCLAW_BROWSER_SMOKE_FAKE_CATEGORICAL_PREDICATE") == "1":
    record["target_ready"] = "ready"
if os.environ.get("MINTCLAW_BROWSER_SMOKE_FAKE_RENAMED_FIELD") == "1" and "fresh_observe" in record:
    record["core_action_ok"] = record.pop("fresh_observe")
result = {"version": 1, "outcome": "success", "response": "validated smoke result"}
if os.environ.get("MINTCLAW_BROWSER_SMOKE_FAKE_NO_RESULT_OUTPUT") != "1":
    result["result_output"] = {"kind": "records", "records": [record]}
if os.environ.get("MINTCLAW_BROWSER_SMOKE_FAKE_NO_EVIDENCE") != "1":
    cleanup = sys.argv[2] == "true"
    stage = sys.argv[3]
    if cleanup:
        calls = {"browser_targets": 1, "browser_session": 2, "browser_observe": 1}
        sessions = [
            {"operation": "open", "target": "gateway", "profile": "managed"},
            {"operation": "close"},
        ]
    else:
        calls = {
            "core": {"browser_targets": 1, "browser_session": 2, "browser_observe": 3, "browser_act": 2},
            "managed-seed": {"browser_targets": 1, "browser_session": 2, "browser_observe": 3, "browser_act": 2},
            "managed-verify": {"browser_targets": 1, "browser_session": 2, "browser_observe": 3, "browser_act": 2},
            "ephemeral-seed": {"browser_targets": 1, "browser_session": 2, "browser_observe": 3, "browser_act": 2},
            "ephemeral-verify": {"browser_targets": 1, "browser_session": 2, "browser_observe": 2, "browser_act": 1},
            "driver-conformance": {"browser_targets": 1, "browser_session": 2, "browser_observe": 3, "browser_act": 2},
            "playwright-library": {"browser_targets": 1, "browser_session": 2, "browser_observe": 3, "browser_act": 2},
            "provider-open-one": {"browser_targets": 1, "browser_session": 2, "browser_observe": 1},
            "provider-open-two": {"browser_targets": 1, "browser_session": 2, "browser_observe": 1},
        }[stage]
        sessions = [
            {"operation": "open", "target": "gateway", "profile": "managed"},
            {"operation": "close"},
        ]
    if os.environ.get("MINTCLAW_BROWSER_SMOKE_FAKE_READONLY_CONTEXTS") == "1":
        calls["browser_contexts"] = 1
    if os.environ.get("MINTCLAW_BROWSER_SMOKE_FAKE_MUTATING_CONTEXTS") == "1":
        calls["other"] = 1
    if os.environ.get("MINTCLAW_BROWSER_SMOKE_FAKE_WRONG_TARGET") == "1":
        sessions[0]["target"] = "companion"
    trace = {
        "agent_id": "browser",
        "outcome": "completed",
        "incomplete": False,
        "tool_calls": calls,
        "tool_failures": {},
        "unpaired_calls": {},
        "browser_sessions": sessions,
    }
    result["execution_evidence"] = {
        "schema_version": "mintclaw.live_execution_evidence.v1",
        "status": "verified",
        "parent": {
            "agent_id": "main",
            "outcome": "completed",
            "incomplete": False,
            "tool_calls": {"delegate": 1},
            "tool_failures": {},
            "unpaired_calls": {},
            "browser_sessions": [],
        },
        "delegation": {"agent_id": "browser", "admitted": 1},
        "child": trace,
        "safe_error": None,
    }
print(json.dumps(result))
PY
EOF
chmod +x "$fake"

for suite in core managed-reuse ephemeral-cleanup driver-conformance provider-lifecycle playwright-library; do
	output="$test_root/$suite.json"
	MINTCLAW_BROWSER_SMOKE_BINARY="$fake" \
		"$repo_root/scripts/browser-capability-smoke.sh" \
		--target gateway --profile managed --suite "$suite" --json-output "$output"
	python3 - "$output" "$suite" <<'PY'
import json
import pathlib
import sys
report = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
expected_primary_calls = {
    "core": {"browser_act": 2, "browser_observe": 3, "browser_session": 2, "browser_targets": 1},
    "managed-reuse": {"browser_act": 4, "browser_observe": 6, "browser_session": 4, "browser_targets": 2},
    "ephemeral-cleanup": {"browser_act": 3, "browser_observe": 5, "browser_session": 4, "browser_targets": 2},
    "driver-conformance": {"browser_act": 2, "browser_observe": 3, "browser_session": 2, "browser_targets": 1},
    "playwright-library": {"browser_act": 2, "browser_observe": 3, "browser_session": 2, "browser_targets": 1},
    "provider-lifecycle": {"browser_observe": 2, "browser_session": 4, "browser_targets": 2},
}[sys.argv[2]]
expected_delegations = 1 if sys.argv[2] in {"core", "driver-conformance", "playwright-library"} else 2
assert report["schema_version"] == "mintclaw.browser_smoke.v1"
assert report["suite"] == sys.argv[2]
assert report["cleanup"] == {"fixture": "stopped", "session_close": "closed", "state": "clean"}
assert report["process_audit"] == {"immediate_reuse": True, "state": "passed"}
assert report["execution_audit"] == {
    "cleanup": {
        "delegations": 1,
        "state": "verified",
        "tool_calls": {"browser_observe": 1, "browser_session": 2, "browser_targets": 1},
    },
    "primary": {
        "delegations": expected_delegations,
        "state": "verified",
        "tool_calls": expected_primary_calls,
    },
    "state": "passed",
}
assert report["capabilities"] == {"click": True, "navigate": True, "observe": True}
assert report["safe_error"] is None
assert report["artifacts"] == []
assert all(check["state"] == "passed" for check in report["checks"])
PY
done

readonly_contexts_output="$test_root/readonly-contexts.json"
MINTCLAW_BROWSER_SMOKE_FAKE_READONLY_CONTEXTS=1 \
	MINTCLAW_BROWSER_SMOKE_BINARY="$fake" \
	"$repo_root/scripts/browser-capability-smoke.sh" \
	--target gateway --profile managed --suite core --json-output "$readonly_contexts_output"
python3 - "$readonly_contexts_output" <<'PY'
import json
import pathlib
import sys
report = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
assert report["safe_error"] is None
assert report["execution_audit"]["primary"]["tool_calls"]["browser_contexts"] == 1
assert report["execution_audit"]["cleanup"]["tool_calls"]["browser_contexts"] == 1
PY

mutating_contexts_output="$test_root/mutating-contexts.json"
if MINTCLAW_BROWSER_SMOKE_FAKE_MUTATING_CONTEXTS=1 \
	MINTCLAW_BROWSER_SMOKE_BINARY="$fake" \
	"$repo_root/scripts/browser-capability-smoke.sh" \
	--target gateway --profile managed --suite core --json-output "$mutating_contexts_output"; then
	echo "browser smoke self-test: mutating context evidence unexpectedly passed" >&2
	exit 1
fi
grep -Fq '"code": "invalid_execution_evidence"' "$mutating_contexts_output"

external_output="$test_root/external.json"
MINTCLAW_BROWSER_SMOKE_BINARY="$fake" \
	"$repo_root/scripts/browser-capability-smoke.sh" \
	--target gateway --profile managed --suite core --json-output "$external_output" \
	--fixture-origin http://127.0.0.1:1
python3 - "$external_output" <<'PY'
import json
import pathlib
import sys
report = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
assert report["cleanup"]["fixture"] == "external"
PY

collision_output="$test_root/collision.json"
collision_victim="$test_root/collision-victim"
printf '%s\n' unchanged >"$collision_victim"
ln -s "$collision_victim" "$collision_output.tmp"
MINTCLAW_BROWSER_SMOKE_BINARY="$fake" \
	"$repo_root/scripts/browser-capability-smoke.sh" \
	--target gateway --profile managed --suite core --json-output "$collision_output"
if [ "$(cat "$collision_victim")" != unchanged ]; then
	echo "browser smoke self-test: predictable temporary symlink was followed" >&2
	exit 1
fi

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

first_stage_failed_output="$test_root/first-stage-failed.json"
if MINTCLAW_BROWSER_SMOKE_FAKE_FAIL_FIRST_STAGE=1 \
	MINTCLAW_BROWSER_SMOKE_BINARY="$fake" \
	"$repo_root/scripts/browser-capability-smoke.sh" \
	--target gateway --profile managed --suite managed-reuse \
	--json-output "$first_stage_failed_output"; then
	echo "browser smoke self-test: failed first stage was masked by second stage" >&2
	exit 1
fi
grep -Fq '"code": "suite_failed"' "$first_stage_failed_output"

bad_capabilities_output="$test_root/bad-capabilities.json"
if MINTCLAW_BROWSER_SMOKE_FAKE_BAD_CAPABILITIES=1 \
	MINTCLAW_BROWSER_SMOKE_BINARY="$fake" \
	"$repo_root/scripts/browser-capability-smoke.sh" \
	--target gateway --profile managed --suite core --json-output "$bad_capabilities_output"; then
	echo "browser smoke self-test: malformed capabilities unexpectedly passed" >&2
	exit 1
fi
grep -Fq '"code": "invalid_agent_result"' "$bad_capabilities_output"

categorical_predicate_output="$test_root/categorical-predicate.json"
if MINTCLAW_BROWSER_SMOKE_FAKE_CATEGORICAL_PREDICATE=1 \
	MINTCLAW_BROWSER_SMOKE_BINARY="$fake" \
	"$repo_root/scripts/browser-capability-smoke.sh" \
	--target gateway --profile managed --suite core --json-output "$categorical_predicate_output"; then
	echo "browser smoke self-test: categorical predicate value unexpectedly passed" >&2
	exit 1
fi
grep -Fq '"code": "invalid_agent_result"' "$categorical_predicate_output"

missing_result_output="$test_root/missing-result-output.json"
if MINTCLAW_BROWSER_SMOKE_FAKE_NO_RESULT_OUTPUT=1 \
	MINTCLAW_BROWSER_SMOKE_BINARY="$fake" \
	"$repo_root/scripts/browser-capability-smoke.sh" \
	--target gateway --profile managed --suite core --json-output "$missing_result_output"; then
	echo "browser smoke self-test: prose passed without validated result output" >&2
	exit 1
fi
grep -Fq '"code": "invalid_agent_result"' "$missing_result_output"

renamed_field_output="$test_root/renamed-field.json"
if MINTCLAW_BROWSER_SMOKE_FAKE_RENAMED_FIELD=1 \
	MINTCLAW_BROWSER_SMOKE_BINARY="$fake" \
	"$repo_root/scripts/browser-capability-smoke.sh" \
	--target gateway --profile managed --suite core --json-output "$renamed_field_output"; then
	echo "browser smoke self-test: renamed required field unexpectedly passed" >&2
	exit 1
fi
grep -Fq '"code": "invalid_agent_result"' "$renamed_field_output"

missing_evidence_output="$test_root/missing-evidence.json"
if MINTCLAW_BROWSER_SMOKE_FAKE_NO_EVIDENCE=1 \
	MINTCLAW_BROWSER_SMOKE_BINARY="$fake" \
	"$repo_root/scripts/browser-capability-smoke.sh" \
	--target gateway --profile managed --suite core --json-output "$missing_evidence_output"; then
	echo "browser smoke self-test: model-only JSON passed without execution evidence" >&2
	exit 1
fi
grep -Fq '"code": "invalid_execution_evidence"' "$missing_evidence_output"

wrong_target_output="$test_root/wrong-target.json"
if MINTCLAW_BROWSER_SMOKE_FAKE_WRONG_TARGET=1 \
	MINTCLAW_BROWSER_SMOKE_BINARY="$fake" \
	"$repo_root/scripts/browser-capability-smoke.sh" \
	--target gateway --profile managed --suite core --json-output "$wrong_target_output"; then
	echo "browser smoke self-test: wrong execution target unexpectedly passed" >&2
	exit 1
fi
grep -Fq '"code": "invalid_execution_evidence"' "$wrong_target_output"

timeout_pid_file="$test_root/timeout-live.pid"
timeout_output="$test_root/timeout.json"
timeout_started=$(date +%s)
if MINTCLAW_BROWSER_SMOKE_FAKE_HANG_PRIMARY=1 \
	MINTCLAW_BROWSER_SMOKE_FAKE_IGNORE_TERM=1 \
	MINTCLAW_BROWSER_SMOKE_FAKE_PID_FILE="$timeout_pid_file" \
	MINTCLAW_BROWSER_SMOKE_BINARY="$fake" \
	"$repo_root/scripts/browser-capability-smoke.sh" \
	--target gateway --profile managed --suite core --json-output "$timeout_output" --timeout 1; then
	echo "browser smoke self-test: forced timeout unexpectedly passed" >&2
	exit 1
fi
timeout_elapsed=$(($(date +%s) - timeout_started))
if [ "$timeout_elapsed" -gt 15 ]; then
	echo "browser smoke self-test: forced timeout was not bounded" >&2
	exit 1
fi
timed_out_process=$(sed -n '1p' "$timeout_pid_file")
if kill -0 "$timed_out_process" >/dev/null 2>&1; then
	echo "browser smoke self-test: timed-out live client survived cleanup" >&2
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
